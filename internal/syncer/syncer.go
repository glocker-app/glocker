// Package syncer ships glocker's local /var records to a glockpeek instance
// (local or remote) via its ingest API. It is the agent side of the local-first
// design: glocker keeps recording to files as the source of truth and enforcing
// offline; the syncer just mirrors new records up on a timer (a one-shot
// backfill at startup, then incremental). It is deliberately decoupled from the
// DB layer — it posts plain JSON, so the agent binary carries no GORM/driver
// weight.
//
// Idempotency: glockpeek upserts on each record's natural key, so re-sending
// overlapping batches (after a restart or a failed POST) never loses or
// double-counts. Failures are logged and retried on the next tick; nothing here
// blocks enforcement.
package syncer

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"glocker/internal/config"
	"glocker/internal/e2e"
	"glocker/internal/reports"
	"glocker/internal/state"
	"glocker/internal/usage"
)

// Payload mirrors glockpeek's ingest shape (store models' JSON tags). Defined
// here rather than imported so the agent stays free of the DB layer.
type Payload struct {
	// Encrypted marks whether this batch's sensitive fields are ciphertext (true
	// when the agent's sync.encrypt is on). glockpeek uses it to stamp the account
	// and to enforce require_encryption.
	Encrypted  bool        `json:"encrypted"`
	Violations []violation `json:"violations,omitempty"`
	Unblocks   []unblock   `json:"unblocks,omitempty"`
	Lifecycle  []lifecycle `json:"lifecycle,omitempty"`
	Usage      []usageRow  `json:"usage,omitempty"`
	Heartbeat  []heartbeat `json:"heartbeat,omitempty"`
}

type violation struct {
	TS int64 `json:"ts"`
	// RecordID is the stable dedup key over the plaintext (see e2e.RecordID);
	// keeps re-syncs idempotent even though the content fields re-encrypt each time.
	RecordID string `json:"recordId"`
	Type     string `json:"type"`
	Keyword  string `json:"keyword"`
	URL      string `json:"url"`
	Domain   string `json:"domain"`
}
type unblock struct {
	TS        int64  `json:"ts"`
	RecordID  string `json:"recordId"`
	Domain    string `json:"domain"`
	RestoreTS *int64 `json:"restoreTs"`
	Reason    string `json:"reason"`
}
type lifecycle struct {
	TS     int64  `json:"ts"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}
type usageRow struct {
	TS             int64  `json:"ts"`
	IdleMS         int64  `json:"idleMs"`
	ActiveClass    string `json:"activeClass"`
	ActiveInstance string `json:"activeInstance"`
	ActiveTitle    string `json:"activeTitle"`
	WindowCount    int    `json:"windowCount"`
}
type heartbeat struct {
	TS    int64 `json:"ts"`
	Alive bool  `json:"alive"`
}

// Syncer holds resolved config for one agent->glockpeek link.
type Syncer struct {
	url      string // base, e.g. http://127.0.0.1:4317
	token    string
	interval time.Duration
	paths    logPaths
	client   *http.Client

	// cfg is the loaded config, kept so the config-snapshot sync (see config.go)
	// can build its subset. nil in the unit-test constructor, which never syncs
	// config.
	cfg *config.Config

	// cursors[source] is the max TS already accepted for that source; each cycle
	// sends records with TS >= cursor (the boundary re-send is idempotent).
	cursors map[string]int64

	// resyncCh carries `glocker -resync` requests: the value is the "since" TS in
	// ms (0 = full history). A request resets the cursors and backfills on the
	// next loop turn, overwriting server-side rows in place (ingest is an
	// idempotent upsert) — used to repair records after a fix like encrypting a
	// field that used to ship in cleartext.
	resyncCh chan int64

	// e2e end-to-end encryption: when on, sensitive fields are encrypted under mk
	// before upload (see enc). e2eBroken is set when E2E was requested but the key
	// couldn't be loaded — the syncer then refuses to run, so it never falls back
	// to uploading plaintext to a hosted server.
	e2e       bool
	mk        []byte
	e2eBroken bool
}

// New builds a Syncer from config, applying defaults.
func New(cfg *config.Config) *Syncer {
	s := &Syncer{
		url:      strings.TrimRight(orDefault(cfg.Sync.GlockpeekURL, config.DefaultGlockpeekURL), "/"),
		token:    cfg.Sync.Token,
		interval: time.Duration(orInt(cfg.Sync.IntervalSeconds, config.DefaultSyncIntervalSeconds)) * time.Second,
		client:   &http.Client{Timeout: 30 * time.Second},
		cursors:  map[string]int64{},
		resyncCh: make(chan int64, 4),
		cfg:      cfg,
	}
	s.paths = resolveLogPaths(cfg)

	if cfg.Sync.Encryption() {
		keyPath := cfg.Sync.KeyFile()
		mk, err := loadMasterKey(keyPath)
		if err != nil {
			// Fail closed: do NOT sync at all rather than upload plaintext to a
			// server the user asked us to hide it from.
			slog.Error("syncer: e2e enabled but master key unusable — refusing to sync (would leak plaintext)", "path", keyPath, "err", err)
			s.e2eBroken = true
		} else {
			s.mk = mk
			s.e2e = true
		}
	}
	return s
}

// loadMasterKey reads a base64-encoded 32-byte master key from path.
func loadMasterKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	mk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	if len(mk) != e2e.MasterKeyLen {
		return nil, fmt.Errorf("master key is %d bytes, want %d", len(mk), e2e.MasterKeyLen)
	}
	return mk, nil
}

// enc returns v unchanged when e2e is off (local mode → plaintext, identical to
// before). When on, it encrypts v under the master key and returns base64 of
// nonce||ciphertext. The aad binds the field to its record identity (ts|kind|
// field) so a blob can't be moved to another field/record. Empty strings pass
// through so an absent field stays absent.
func (s *Syncer) enc(v string, ts int64, kind, field string) string {
	if !s.e2e || v == "" {
		return v
	}
	aad := fmt.Appendf(nil, "%d|%s|%s", ts, kind, field)
	box, err := e2e.EncryptRecord(s.mk, []byte(v), aad)
	if err != nil {
		// mk is validated at load, so this is effectively impossible; still fail
		// closed (drop the field) rather than ever emit plaintext.
		slog.Error("syncer: field encryption failed; dropping field", "field", field, "err", err)
		return ""
	}
	buf := make([]byte, 0, len(box.Nonce)+len(box.Ct))
	buf = append(buf, box.Nonce...)
	buf = append(buf, box.Ct...)
	return base64.StdEncoding.EncodeToString(buf)
}

// recordID computes the stable dedup key for a record over its PLAINTEXT parts.
// Under E2E it's keyed with the master key (HMAC); in plaintext mode s.mk is nil
// and e2e.RecordID falls back to a plain SHA-256. It must be computed from the
// cleartext, before enc() encrypts the same fields.
func (s *Syncer) recordID(kind string, ts int64, parts ...string) string {
	all := append([]string{kind, strconv.FormatInt(ts, 10)}, parts...)
	return e2e.RecordID(s.mk, all...)
}

// logPaths are the local logs the syncer reads (all under /var/log by default).
type logPaths struct{ reports, unblocks, lifecycle, heartbeat, usage string }

// resolveLogPaths derives the log paths from config, applying defaults. Shared by
// New and PendingCounts so both read exactly the same files.
func resolveLogPaths(cfg *config.Config) logPaths {
	return logPaths{
		reports:   config.DefaultReportsLogFile,
		unblocks:  config.DefaultUnblocksLogFile,
		lifecycle: orDefault(cfg.Lifecycle.LogFile, config.DefaultLifecycleLogFile),
		heartbeat: config.DefaultHeartbeatLogFile,
		usage:     orDefault(cfg.UsageMonitor.LogFile, config.DefaultUsageLogFile),
	}
}

// PendingCounts reports how many not-yet-synced records sit in the local logs,
// keyed for display (violations/unblocks/lifecycle/usage/heartbeat) — i.e. what
// the next push will carry. cursors are the per-source high-water TS (ms) from
// the last push; a record is pending when its TS is strictly greater than the
// cursor (TS == cursor was already sent). It reads the same logs as build() but
// only counts, and takes cursors by value, so it is safe to call from another
// goroutine (e.g. the status handler) while the syncer runs.
func PendingCounts(cfg *config.Config, cursors map[string]int64) map[string]int {
	paths := resolveLogPaths(cfg)
	out := map[string]int{}

	if rows, err := reports.ParseReportsLog(paths.reports); err == nil {
		cur := cursors["reports"]
		for _, e := range rows {
			if e.Timestamp.UnixMilli() > cur {
				out["violations"]++
			}
		}
	}
	if rows, err := reports.ParseUnblocksLog(paths.unblocks); err == nil {
		cur := cursors["unblocks"]
		for _, e := range rows {
			if !e.UnblockTime.IsZero() && e.UnblockTime.UnixMilli() > cur {
				out["unblocks"]++
			}
		}
	}
	if rows, err := reports.ParseLifecycleLog(paths.lifecycle); err == nil {
		cur := cursors["lifecycle"]
		for _, e := range rows {
			if e.Timestamp.UnixMilli() > cur {
				out["lifecycle"]++
			}
		}
	}
	if rows, err := reports.ParseHeartbeatLog(paths.heartbeat); err == nil {
		cur := cursors["heartbeat"]
		for _, e := range rows {
			if e.Timestamp.UnixMilli() > cur {
				out["heartbeat"]++
			}
		}
	}
	if rows, err := parseUsageLog(paths.usage); err == nil {
		cur := cursors["usage"]
		for _, r := range rows {
			if r.TS > cur {
				out["usage"]++
			}
		}
	}
	return out
}

// Run seeds cursors from glockpeek's high-water marks, reconciles the config
// snapshot, does the initial sync, then syncs on the interval. Blocks; run in
// a goroutine. Every error is logged and retried next tick — enforcement is
// never affected.
// active is the running syncer, registered while Run loops so `glocker -resync`
// (via the IPC server) can signal it. Only one syncer runs per daemon.
var (
	activeMu sync.Mutex
	active   *Syncer
)

// Resync signals the running syncer to reset its cursors to `since` (ms; 0 = full
// history) and backfill immediately. Returns false if no syncer is running
// (sync disabled, or e2e enabled but the key is unusable). Non-blocking: a
// request already queued is enough, so extra calls collapse into one.
func Resync(since int64) bool {
	activeMu.Lock()
	a := active
	activeMu.Unlock()
	if a == nil {
		return false
	}
	select {
	case a.resyncCh <- since:
	default: // a request is already queued; one backfill covers it
	}
	return true
}

// resetCursors rewinds every source to `since` so the next syncOnce re-sends
// records with TS >= since (0 = everything).
func (s *Syncer) resetCursors(since int64) {
	for _, src := range []string{"reports", "unblocks", "lifecycle", "heartbeat", "usage"} {
		s.cursors[src] = since
	}
}

func (s *Syncer) Run() {
	if s.e2eBroken {
		slog.Error("syncer: not starting — e2e was enabled but the master key is unusable")
		return
	}
	slog.Info("syncer starting", "target", s.url, "interval", s.interval, "e2e", s.e2e)
	activeMu.Lock()
	active = s
	activeMu.Unlock()
	defer func() { activeMu.Lock(); active = nil; activeMu.Unlock() }()

	s.seedCursors() // ask glockpeek what it already has, so we only send the gap
	// Reconcile this device's config snapshot. Retried each tick until it
	// lands: if glockpeek is down at daemon start, a one-shot attempt would
	// leave the Config tab empty until the next restart, which could be days.
	// Once it lands we stop — the config can't change without a restart.
	configSynced := s.syncConfig()
	s.syncOnce()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if !configSynced {
				configSynced = s.syncConfig()
			}
			s.syncOnce()
		case since := <-s.resyncCh:
			slog.Info("syncer: resync requested — rewinding cursors and backfilling", "since", since)
			state.SetResyncing(true)
			s.resetCursors(since)
			s.syncOnce()
			state.SetResyncing(false)
		}
	}
}

// seedCursors asks glockpeek for the max TS it holds per source and seeds the
// cursors from that, so a restart doesn't re-send everything. On any error
// (glockpeek down, etc.) cursors stay at 0 and the next sync does a full
// backfill — always safe because ingest is idempotent.
func (s *Syncer) seedCursors() {
	req, err := http.NewRequest(http.MethodGet, s.url+"/api/ingest", nil)
	if err != nil {
		return
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		slog.Warn("syncer: couldn't read glockpeek cursors; will backfill", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("syncer: cursor request rejected; will backfill", "status", resp.Status)
		return
	}
	var body struct {
		Cursors map[string]int64 `json:"cursors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}
	for src, ts := range body.Cursors {
		s.cursors[src] = ts
	}
	slog.Info("syncer: seeded cursors from glockpeek", "cursors", s.cursors)
}

// syncOnce builds a payload of records newer than the cursors and posts it.
// Cursors only advance on a successful post, so a failed cycle is retried whole.
func (s *Syncer) syncOnce() {
	p, maxTS := s.build()
	if p.empty() {
		return
	}
	if err := s.post(p); err != nil {
		slog.Warn("syncer post failed; will retry", "error", err)
		state.RecordSyncError(err.Error()) // surfaced by `glocker -status`
		return
	}
	for src, ts := range maxTS {
		if ts > s.cursors[src] {
			s.cursors[src] = ts
		}
	}
	counts := map[string]int{
		"violations": len(p.Violations), "unblocks": len(p.Unblocks),
		"lifecycle": len(p.Lifecycle), "usage": len(p.Usage), "heartbeat": len(p.Heartbeat),
	}
	state.RecordSync(counts, s.cursors) // surfaced by `glocker -status`
	slog.Debug("syncer pushed batch", "counts", counts)
}

// build assembles the payload from the logs, filtered to TS >= each cursor, and
// returns the max TS seen per source (for advancing cursors after a good post).
func (s *Syncer) build() (Payload, map[string]int64) {
	var p Payload
	p.Encrypted = s.e2e // batch-level marker: true iff we're encrypting sensitive fields
	// Each source skips records at-or-before its cursor (strict `> cur`), matching
	// PendingCounts. Using `>= cur` re-sent the record sitting exactly at the
	// cursor's max TS every tick — harmless to glockpeek (RecordID dedups) but it
	// inflated the "session" push tally forever even when pending was 0.
	max := map[string]int64{}

	if rows, err := reports.ParseReportsLog(s.paths.reports); err == nil {
		cur := s.cursors["reports"]
		for _, e := range rows {
			ts := e.Timestamp.UnixMilli()
			if ts <= cur {
				continue
			}
			p.Violations = append(p.Violations, violation{
				TS:       ts,
				RecordID: s.recordID("violation", ts, e.Keyword, e.URL), // over plaintext
				Type:     string(e.Type),                                // metadata — stays clear
				Keyword:  s.enc(e.Keyword, ts, "violation", "keyword"),
				URL:      s.enc(e.URL, ts, "violation", "url"),
				Domain:   s.enc(e.Domain, ts, "violation", "domain"),
			})
			max["reports"] = maxi(max["reports"], ts)
		}
	}

	if rows, err := reports.ParseUnblocksLog(s.paths.unblocks); err == nil {
		cur := s.cursors["unblocks"]
		for _, e := range rows {
			if e.UnblockTime.IsZero() {
				continue
			}
			ts := e.UnblockTime.UnixMilli()
			if ts <= cur {
				continue
			}
			u := unblock{
				TS:       ts,
				RecordID: s.recordID("unblock", ts, e.Domain), // over plaintext
				Domain:   s.enc(e.Domain, ts, "unblock", "domain"),
				Reason:   s.enc(e.Reason, ts, "unblock", "reason"),
			}
			if !e.RestoreTime.IsZero() {
				r := e.RestoreTime.UnixMilli()
				u.RestoreTS = &r
			}
			p.Unblocks = append(p.Unblocks, u)
			max["unblocks"] = maxi(max["unblocks"], ts)
		}
	}

	if rows, err := reports.ParseLifecycleLog(s.paths.lifecycle); err == nil {
		cur := s.cursors["lifecycle"]
		for _, e := range rows {
			ts := e.Timestamp.UnixMilli()
			if ts <= cur {
				continue
			}
			p.Lifecycle = append(p.Lifecycle, lifecycle{
				TS:     ts,
				Type:   e.Type,   // metadata — drives the UNMANAGED overlay, stays clear
				Reason: e.Reason, // gated enum (maintenance/work/…), low sensitivity, stays clear
				Note:   s.enc(e.Note, ts, "lifecycle", "note"),
			})
			max["lifecycle"] = maxi(max["lifecycle"], ts)
		}
	}

	if rows, err := reports.ParseHeartbeatLog(s.paths.heartbeat); err == nil {
		cur := s.cursors["heartbeat"]
		for _, e := range rows {
			ts := e.Timestamp.UnixMilli()
			if ts <= cur {
				continue
			}
			p.Heartbeat = append(p.Heartbeat, heartbeat{TS: ts, Alive: e.Alive})
			max["heartbeat"] = maxi(max["heartbeat"], ts)
		}
	}

	if rows, err := parseUsageLog(s.paths.usage); err == nil {
		cur := s.cursors["usage"]
		for _, r := range rows {
			if r.TS <= cur {
				continue
			}
			// Seal the whole active window: the title (document/URL/message text)
			// and the app identity (class/instance, e.g. "com.anthropic.Claude") —
			// the latter reveals which apps you run. Classification runs client-side
			// after decryption now, so the server no longer needs the class in clear.
			r.ActiveTitle = s.enc(r.ActiveTitle, r.TS, "usage", "activeTitle")
			r.ActiveClass = s.enc(r.ActiveClass, r.TS, "usage", "activeClass")
			r.ActiveInstance = s.enc(r.ActiveInstance, r.TS, "usage", "activeInstance")
			p.Usage = append(p.Usage, r)
			max["usage"] = maxi(max["usage"], r.TS)
		}
	}

	return p, max
}

func (s *Syncer) post(p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, s.url+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Surface the server's error body (capped) so the failure is diagnosable
		// from the agent's own log — no need to read glockpeek's logs to see why.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if trimmed := strings.TrimSpace(string(msg)); trimmed != "" {
			return fmt.Errorf("ingest returned %s: %s", resp.Status, trimmed)
		}
		return fmt.Errorf("ingest returned %s", resp.Status)
	}
	return nil
}

// parseUsageLog reads the usage JSONL log into ingest rows (flattening the active
// window), tolerating missing files and malformed lines.
func parseUsageLog(path string) ([]usageRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []usageRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var smp usage.Sample
		if err := json.Unmarshal([]byte(line), &smp); err != nil {
			continue
		}
		row := usageRow{TS: smp.Timestamp.UnixMilli(), IdleMS: smp.IdleMS, WindowCount: len(smp.Windows)}
		for _, w := range smp.Windows {
			if w.Active {
				row.ActiveClass, row.ActiveInstance, row.ActiveTitle = w.Class, w.Instance, w.Title
				break
			}
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

func (p Payload) empty() bool {
	return len(p.Violations)+len(p.Unblocks)+len(p.Lifecycle)+len(p.Usage)+len(p.Heartbeat) == 0
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
func maxi(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
