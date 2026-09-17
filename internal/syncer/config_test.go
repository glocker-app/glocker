package syncer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/e2e"
	"glocker/internal/stats"
	"glocker/internal/store"
)

// configServer stands in for glockpeek's /api/config: it stores one hash and
// records every uploaded snapshot.
type configServer struct {
	mu       sync.Mutex
	stored   string // hash it currently "holds"
	uploads  []configUpload
	failPost bool // reject uploads, standing in for a glockpeek that's down
	srv      *httptest.Server
}

func newConfigServer(t *testing.T) *configServer {
	c := &configServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet: // ?mine=1 -> stored hash
			c.mu.Lock()
			h := c.stored
			c.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"hash": h})
		case http.MethodPost:
			c.mu.Lock()
			reject := c.failPost
			c.mu.Unlock()
			if reject {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var up configUpload
			json.Unmarshal(body, &up)
			c.mu.Lock()
			c.uploads = append(c.uploads, up)
			c.stored = up.Hash // now glockpeek holds this hash
			c.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *configServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.uploads)
}

func (c *configServer) fail(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPost = v
}

func (c *configServer) storedHash() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stored
}

func sampleConfig() *config.Config {
	tru := true
	cfg := &config.Config{User: "noufal"}
	cfg.Domains.Enabled = true
	cfg.Domains.List = []config.Domain{
		{Name: "always.com"},
		{Name: "reddit.com", BlockWindows: []config.TimeWindow{{Start: "09:00", End: "17:00", Days: []string{"Mon"}}}},
	}
	cfg.Domains.Unblocking.MaxPerDay = 3
	cfg.Programs.Enabled = true
	cfg.Programs.CheckInterval = 5
	cfg.Programs.List = []config.ForbiddenProgram{
		{Name: "brave"},
		{Name: "chrome", KillWindows: []config.TimeWindow{{Start: "20:00", End: "05:00", Days: []string{"Mon"}}}, Extendible: true},
	}
	cfg.Content.Monitoring.Enabled = true
	cfg.Content.Keywords.URLKeywords = []string{"porn", "goon"}
	cfg.Content.Keywords.ContentKeywords = []string{"gooning"}
	cfg.Content.Keywords.Whitelist = []string{"a.com", "b.com"}
	cfg.ViolationTracking.Enabled = true
	cfg.ViolationTracking.MaxViolations = 5
	cfg.ViolationTracking.TimeWindowMinutes = 60
	cfg.UsageMonitor.Enabled = true
	cfg.Sudoers.Enabled = true
	cfg.Sudoers.AllowWindows = []config.TimeWindow{{Start: "10:00", End: "18:00", Days: []string{"Mon"}}}
	cfg.Panic.Duration = 30
	cfg.Sync.Enabled = true
	cfg.Sync.Encrypt = &tru
	return cfg
}

func TestBuildConfigSnapshot(t *testing.T) {
	snap := buildConfigSnapshot(sampleConfig())

	if !snap.Services.Hosts || !snap.Services.ForbiddenPrograms || !snap.Services.Sync {
		t.Errorf("services not reflected: %+v", snap.Services)
	}
	if got := snap.Domains.Blocked; got != 2 {
		t.Errorf("blocked = %d, want 2", got)
	}
	if got := snap.Domains.AlwaysBlocked; got != 1 {
		t.Errorf("alwaysBlocked = %d, want 1", got)
	}
	if len(snap.Domains.TimeBased) != 1 || snap.Domains.TimeBased[0].Name != "reddit.com" {
		t.Errorf("timeBased = %+v", snap.Domains.TimeBased)
	}
	if len(snap.Programs.AlwaysKilled) != 1 || snap.Programs.AlwaysKilled[0] != "brave" {
		t.Errorf("alwaysKilled = %+v", snap.Programs.AlwaysKilled)
	}
	if len(snap.Programs.Windowed) != 1 || !snap.Programs.Windowed[0].Extendible {
		t.Errorf("windowed = %+v", snap.Programs.Windowed)
	}
	if snap.Keywords.WhitelistCount != 2 || len(snap.Keywords.URL) != 2 {
		t.Errorf("keywords = %+v", snap.Keywords)
	}
	if snap.Guardrails.CheckIntervalSec != 5 || snap.Guardrails.UnblockMaxPerDay != 3 || snap.Guardrails.PanicDurationMin != 30 {
		t.Errorf("guardrails = %+v", snap.Guardrails)
	}
}

// TestSyncConfigReconciles: first connect uploads; an unchanged config skips the
// upload (hash matches); a changed config uploads again.
func TestSyncConfigReconciles(t *testing.T) {
	c := newConfigServer(t)
	cfg := sampleConfig()
	s := &Syncer{url: c.srv.URL, interval: time.Hour, client: c.srv.Client(), cursors: map[string]int64{}, cfg: cfg}

	// First connect: glockpeek holds nothing -> upload.
	s.syncConfig()
	if c.count() != 1 {
		t.Fatalf("first connect uploads = %d, want 1", c.count())
	}
	if up := c.uploads[0]; up.Hash == "" || up.Config == "" || up.Encrypted {
		t.Errorf("upload looks wrong (plaintext expected here): %+v", up)
	}

	// Second connect, unchanged config: hash matches -> no upload.
	s.syncConfig()
	if c.count() != 1 {
		t.Fatalf("unchanged config re-uploaded: count = %d, want 1", c.count())
	}

	// Config changes -> upload again.
	cfg.Content.Keywords.ContentKeywords = append(cfg.Content.Keywords.ContentKeywords, "newword")
	s.syncConfig()
	if c.count() != 2 {
		t.Fatalf("changed config not re-uploaded: count = %d, want 2", c.count())
	}
	if c.uploads[1].Hash == c.uploads[0].Hash {
		t.Errorf("hash should change with the config")
	}
}

// TestSyncConfigIntoRealGlockpeek is the true end-to-end for E2E config: the
// agent seals the snapshot, posts it to a real glockpeek mux, and we read it back
// and decrypt it with the same primitives the browser uses (e2e + the ts-bound
// aad). This guarantees the agent's seal and the dashboard's open agree.
func TestSyncConfigIntoRealGlockpeek(t *testing.T) {
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

	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i + 1)
	}
	s := &Syncer{url: srv.URL, interval: time.Hour, client: srv.Client(), cursors: map[string]int64{}, cfg: sampleConfig(), e2e: true, mk: mk}
	s.syncConfig()

	// Read it back the way the dashboard does.
	r, err := srv.Client().Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out struct {
		Configs []struct {
			TS        int64  `json:"ts"`
			Encrypted bool   `json:"encrypted"`
			Config    string `json:"config"`
		} `json:"configs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Configs) != 1 || !out.Configs[0].Encrypted {
		t.Fatalf("configs = %+v", out.Configs)
	}

	// Decrypt: base64 -> nonce||ct -> open under mk with the ts-bound aad.
	raw, err := base64.StdEncoding.DecodeString(out.Configs[0].Config)
	if err != nil {
		t.Fatalf("blob not base64: %v", err)
	}
	aad := fmt.Appendf(nil, "%d|deviceconfig|config", out.Configs[0].TS)
	plain, err := e2e.DecryptRecord(mk, e2e.Box{Nonce: raw[:12], Ct: raw[12:]}, aad)
	if err != nil {
		t.Fatalf("decrypt failed (agent seal / browser open disagree): %v", err)
	}
	var snap cfgSnapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		t.Fatalf("decrypted blob is not the subset JSON: %v", err)
	}
	if snap.Domains.Blocked != 2 || len(snap.Keywords.URL) != 2 {
		t.Errorf("round-tripped snapshot wrong: %+v", snap)
	}
}

// TestSyncConfigDoesNotUploadUnsealable pins the fail-closed contract: if the
// snapshot can't be sealed, NOTHING is uploaded.
//
// The regression this guards is subtle and permanent. The upload carries a hash
// of the PLAINTEXT, and the agent skips re-uploading whenever glockpeek already
// holds that hash. So posting an empty blob alongside a valid hash doesn't just
// lose one snapshot — it leaves glockpeek holding unreadable content under the
// hash the agent will keep recomputing, so every later connect short-circuits
// and the Config tab stays broken until the config itself changes.
func TestSyncConfigDoesNotUploadUnsealable(t *testing.T) {
	c := newConfigServer(t)
	// e2e on with a deliberately unusable key: AES-GCM rejects the length, so
	// sealing fails the way it would with a corrupt master key on disk.
	s := &Syncer{
		url: c.srv.URL, interval: time.Hour, client: c.srv.Client(),
		cursors: map[string]int64{}, cfg: sampleConfig(),
		e2e: true, mk: []byte("not-a-32-byte-key"),
	}

	s.syncConfig()
	if c.count() != 0 {
		t.Fatalf("uploaded %d snapshot(s) that could not be sealed, want 0: %+v", c.count(), c.uploads)
	}
	// Nothing may be left on the server that would satisfy a later hash check.
	if got := c.storedHash(); got != "" {
		t.Fatalf("server holds hash %q after a failed seal — later connects would short-circuit", got)
	}

	// The failure must not be sticky: a working key uploads on the next connect.
	s.mk = make([]byte, e2e.MasterKeyLen)
	s.syncConfig()
	if c.count() != 1 {
		t.Fatalf("recovered connect uploads = %d, want 1", c.count())
	}
	if up := c.uploads[0]; !up.Encrypted || up.Config == "" {
		t.Errorf("expected a sealed, non-empty snapshot, got %+v", up)
	}
}

// TestSyncConfigRetriesUntilItLands covers the case where glockpeek is down at
// daemon start. syncConfig must report failure so Run keeps retrying, and must
// succeed once glockpeek answers — otherwise the Config tab stays empty until
// someone restarts the daemon, which could be days.
func TestSyncConfigRetriesUntilItLands(t *testing.T) {
	c := newConfigServer(t)
	c.fail(true) // glockpeek is up but rejecting (or, to the agent, may as well be down)
	s := &Syncer{url: c.srv.URL, interval: time.Hour, client: c.srv.Client(), cursors: map[string]int64{}, cfg: sampleConfig()}

	if s.syncConfig() {
		t.Fatal("syncConfig reported success while the server was rejecting uploads")
	}
	if c.storedHash() != "" {
		t.Fatalf("server holds a hash after a rejected upload: %q", c.storedHash())
	}

	c.fail(false)
	if !s.syncConfig() {
		t.Fatal("syncConfig still reporting failure after the server recovered")
	}
	if c.count() != 1 {
		t.Fatalf("uploads after recovery = %d, want 1", c.count())
	}

	// Having landed, an unchanged config reports success without re-uploading,
	// which is what lets Run stop calling it.
	if !s.syncConfig() {
		t.Fatal("unchanged config should report success")
	}
	if c.count() != 1 {
		t.Fatalf("re-uploaded an unchanged config: count = %d, want 1", c.count())
	}
}

// TestRunSyncsConfig guards the wiring, not the logic: every other config test
// calls syncConfig directly, so deleting the call from Run would break config
// sync in production with the whole suite still green.
func TestRunSyncsConfig(t *testing.T) {
	c := newConfigServer(t)
	// A long interval so Run does its start-up work and then parks on the
	// ticker rather than spinning for the rest of the test binary's life.
	s := &Syncer{url: c.srv.URL, interval: time.Hour, client: c.srv.Client(), cursors: map[string]int64{}, cfg: sampleConfig()}
	go s.Run()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && c.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if c.count() == 0 {
		t.Fatal("Run() never uploaded a config snapshot — the syncConfig call is missing from Run")
	}
}
