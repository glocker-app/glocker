package syncer

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/stats"
	"glocker/internal/store"
)

// captureServer stands in for glockpeek's ingest endpoint, recording every
// posted payload.
type captureServer struct {
	mu      sync.Mutex
	got     []Payload
	tokens  []string
	cursors map[string]int64 // returned on GET /api/ingest
	srv     *httptest.Server
}

func newCapture(t *testing.T) *captureServer {
	c := &captureServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ingest" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			c.mu.Lock()
			cur := c.cursors
			c.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"cursors": cur})
			return
		}
		body, _ := io.ReadAll(r.Body)
		var p Payload
		json.Unmarshal(body, &p)
		c.mu.Lock()
		c.got = append(c.got, p)
		c.tokens = append(c.tokens, r.Header.Get("Authorization"))
		c.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureServer) posts() []Payload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Payload(nil), c.got...)
}

// testSyncer builds a Syncer pointed at the capture server with log paths under
// dir. Only the files that exist are read (missing ones are skipped).
func testSyncer(url, dir, token string, client *http.Client) *Syncer {
	s := &Syncer{url: url, token: token, interval: time.Hour, client: client, cursors: map[string]int64{}}
	s.paths.reports = filepath.Join(dir, "reports.log")
	s.paths.unblocks = filepath.Join(dir, "unblocks.log")
	s.paths.lifecycle = filepath.Join(dir, "lifecycle.log")
	s.paths.heartbeat = filepath.Join(dir, "heartbeat.jsonl")
	s.paths.usage = filepath.Join(dir, "usage.jsonl")
	return s
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncBackfillAndIncrement(t *testing.T) {
	dir := t.TempDir()
	c := newCapture(t)
	s := testSyncer(c.srv.URL, dir, "", c.srv.Client())

	write(t, s.paths.reports, "[2025-11-17 15:35:46] | url-keyword:porn | https://www.google.com/search?q=porn\n")
	write(t, s.paths.usage,
		`{"ts":"2026-07-07T15:36:11+05:30","idle_ms":5,"windows":[{"active":true,"class":"kitty","title":"zsh"}]}`+"\n")

	// First cycle = one-shot backfill: sends everything.
	s.syncOnce()
	posts := c.posts()
	if len(posts) != 1 {
		t.Fatalf("want 1 post on backfill, got %d", len(posts))
	}
	if len(posts[0].Violations) != 1 || posts[0].Violations[0].Keyword != "porn" {
		t.Errorf("backfill violations = %+v", posts[0].Violations)
	}
	if len(posts[0].Usage) != 1 || posts[0].Usage[0].ActiveClass != "kitty" {
		t.Errorf("backfill usage = %+v", posts[0].Usage)
	}

	before := len(c.posts())
	// Append a new violation -> next cycle delivers it.
	write(t, s.paths.reports,
		"[2025-11-17 15:35:46] | url-keyword:porn | https://www.google.com/search?q=porn\n"+
			"[2025-11-18 09:00:00] | url-keyword:casino | https://example.com/casino\n")
	s.syncOnce()

	found := false
	for _, p := range c.posts()[before:] {
		for _, v := range p.Violations {
			if v.Keyword == "casino" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("incremental sync did not deliver the new 'casino' violation")
	}
}

// TestSeedCursorsFromGlockpeek: after seeding from glockpeek's high-water mark,
// the syncer only sends records at/after that mark — not the whole history.
func TestSeedCursorsFromGlockpeek(t *testing.T) {
	dir := t.TempDir()
	c := newCapture(t)
	write(t, filepath.Join(dir, "reports.log"),
		"[2025-11-17 15:00:00] | url-keyword:a | https://x/a\n"+
			"[2025-11-17 15:00:01] | url-keyword:b | https://x/b\n"+
			"[2025-11-17 15:00:02] | url-keyword:c | https://x/c\n")

	// Discover the actual epoch-ms (zone-dependent) from a full build.
	all, _ := testSyncer(c.srv.URL, dir, "", c.srv.Client()).build()
	if len(all.Violations) != 3 {
		t.Fatalf("setup: want 3 violations, got %d", len(all.Violations))
	}
	mid := all.Violations[1].TS

	// glockpeek already holds up to the middle record.
	c.cursors = map[string]int64{"reports": mid}
	s := testSyncer(c.srv.URL, dir, "", c.srv.Client())
	s.seedCursors()
	if s.cursors["reports"] != mid {
		t.Fatalf("cursor not seeded: %v", s.cursors)
	}

	// build() keeps only ts > mid: glockpeek already holds up to (and including)
	// the middle record, so we send just the later one (1 of 3). Re-sending the
	// boundary record glockpeek already has was the bug that made the "session"
	// push tally climb forever while pending stayed 0.
	p, _ := s.build()
	if len(p.Violations) != 1 {
		t.Errorf("seeding should trim to 1 (> cursor), got %d", len(p.Violations))
	}
}

func TestSyncSendsToken(t *testing.T) {
	dir := t.TempDir()
	c := newCapture(t)
	s := testSyncer(c.srv.URL, dir, "secret-token", c.srv.Client())
	write(t, s.paths.heartbeat, `{"timestamp":"2026-07-07T15:36:11+05:30","alive":true}`+"\n")

	s.syncOnce()
	if len(c.tokens) != 1 || c.tokens[0] != "Bearer secret-token" {
		t.Errorf("Authorization header = %v, want 'Bearer secret-token'", c.tokens)
	}
}

func TestSyncEmptyNoPost(t *testing.T) {
	dir := t.TempDir() // no log files at all
	c := newCapture(t)
	s := testSyncer(c.srv.URL, dir, "", c.srv.Client())
	s.syncOnce()
	if n := len(c.posts()); n != 0 {
		t.Errorf("empty sources should not post, got %d posts", n)
	}
}

// TestSyncIntoRealGlockpeek is the true end-to-end: the syncer posts to a real
// glockpeek mux (local mode) and the records land in the DB with the right shape.
// This guarantees the syncer's JSON tags match glockpeek's ingest decoder.
func TestSyncIntoRealGlockpeek(t *testing.T) {
	db, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "gp.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	du, err := db.EnsureDefaultUser()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	stats.Register(mux, db, stats.Options{Auth: false, DefaultUserID: du.ID})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	s := testSyncer(srv.URL, dir, "", srv.Client())
	write(t, s.paths.reports, "[2025-11-17 15:35:46] | url-keyword:porn | https://www.google.com/search?q=porn\n")
	write(t, s.paths.usage,
		`{"ts":"2026-07-07T15:36:11+05:30","idle_ms":5,"windows":[{"active":true,"class":"firefox-esr","title":"YouTube"}]}`+"\n")
	write(t, s.paths.heartbeat, `{"timestamp":"2026-07-07T15:36:00+05:30","alive":true}`+"\n")

	s.syncOnce()

	vs, _ := db.Violations(du.ID)
	if len(vs) != 1 || vs[0].Keyword != "porn" || vs[0].Type != "url-keyword" {
		t.Errorf("violations in DB = %+v", vs)
	}
	us, _ := db.UsageSamples(du.ID)
	if len(us) != 1 || us[0].ActiveClass != "firefox-esr" || us[0].ActiveTitle != "YouTube" {
		t.Errorf("usage in DB = %+v", us)
	}
	hs, _ := db.Heartbeats(du.ID)
	if len(hs) != 1 || !hs[0].Alive {
		t.Errorf("heartbeat in DB = %+v", hs)
	}

	// The sync also stamped the last-sync marker the dashboard panel reads.
	if st, ok, _ := db.SyncStatusFor(du.ID); !ok || st.LastViolations != 1 {
		t.Errorf("sync status not recorded: ok=%v %+v", ok, st)
	}
}

// TestPendingCounts checks that PendingCounts counts only records strictly newer
// than the cursor. It asserts the usage source (whose path is config-overridable
// to a temp file); the other sources read /var/log defaults and are not asserted
// here, so the test is stable on a machine that has real glocker logs.
func TestPendingCounts(t *testing.T) {
	dir := t.TempDir()
	usagePath := filepath.Join(dir, "usage.jsonl")
	write(t, usagePath, strings.Join([]string{
		`{"ts":"2026-07-07T10:00:00Z","idle_ms":1,"windows":[]}`,
		`{"ts":"2026-07-07T10:05:00Z","idle_ms":1,"windows":[]}`,
		`{"ts":"2026-07-07T10:10:00Z","idle_ms":1,"windows":[]}`,
	}, "\n")+"\n")

	cfg := &config.Config{}
	cfg.UsageMonitor.LogFile = usagePath
	// Point the config-overridable lifecycle path at an empty temp file so the
	// test never reads a real /var/log lifecycle log.
	cfg.Lifecycle.LogFile = filepath.Join(dir, "lifecycle.log")

	firstMS := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC).UnixMilli()
	if got := PendingCounts(cfg, map[string]int64{"usage": firstMS})["usage"]; got != 2 {
		t.Errorf("pending usage with cursor at first sample = %d, want 2 (strictly newer)", got)
	}
	if got := PendingCounts(cfg, map[string]int64{"usage": 0})["usage"]; got != 3 {
		t.Errorf("pending usage with cursor 0 = %d, want 3", got)
	}
}

// TestResyncSignal covers the -resync plumbing: the package-level Resync signal
// reaches the registered syncer's channel (and is a no-op with none running),
// and resetCursors rewinds every source.
func TestResyncSignal(t *testing.T) {
	activeMu.Lock()
	active = nil
	activeMu.Unlock()
	if Resync(0) {
		t.Error("Resync should return false when no syncer is running")
	}

	s := &Syncer{resyncCh: make(chan int64, 4), cursors: map[string]int64{"usage": 999, "reports": 42}}
	activeMu.Lock()
	active = s
	activeMu.Unlock()
	defer func() { activeMu.Lock(); active = nil; activeMu.Unlock() }()

	if !Resync(1234) {
		t.Fatal("Resync should return true with a running syncer")
	}
	select {
	case v := <-s.resyncCh:
		if v != 1234 {
			t.Errorf("queued since = %d, want 1234", v)
		}
	default:
		t.Fatal("no resync signal was queued")
	}

	s.resetCursors(0)
	for _, src := range []string{"usage", "reports", "unblocks", "lifecycle", "heartbeat"} {
		if s.cursors[src] != 0 {
			t.Errorf("resetCursors(0): %s = %d, want 0", src, s.cursors[src])
		}
	}
}

// TestBlockedAccessReachesSyncPayload closes the loop the accountability hole left
// open: a blocked-domain hit written to reports.log must appear in the sync payload
// as a web_access violation. Before the parser learned the type, it was dropped.
func TestBlockedAccessReachesSyncPayload(t *testing.T) {
	dir := t.TempDir()
	c := newCapture(t)
	write(t, filepath.Join(dir, "reports.log"),
		"[2025-11-17 15:00:00] | web_access:reddit.com | https://reddit.com/x | reddit.com\n")

	all, _ := testSyncer(c.srv.URL, dir, "", c.srv.Client()).build()
	if len(all.Violations) != 1 {
		t.Fatalf("want 1 violation from a blocked-domain hit, got %d", len(all.Violations))
	}
	if all.Violations[0].Type != "web_access" {
		t.Errorf("violation type = %q, want web_access", all.Violations[0].Type)
	}
	if all.Violations[0].Domain != "reddit.com" {
		t.Errorf("violation domain = %q, want reddit.com", all.Violations[0].Domain)
	}
}
