package syncer

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"glocker/internal/config"
	"glocker/internal/e2e"
)

// This file is the agent side of config sync: it builds a small, curated subset
// of the loaded config, fingerprints it, and — on connect — uploads it to
// glockpeek only when it differs from what glockpeek already holds for this
// device. The dashboard decrypts and renders it (the foundation for Insights).
//
// The JSON shape below is the contract the dashboard's Config tab parses
// (internal/stats/assets/app.js) — keep the two in step. It deliberately omits
// the full domain lists (huge, low signal) and any host/identity metadata.

const maxTimeBasedDomains = 50 // cap the spelled-out time-based domains in the blob

type cfgSnapshot struct {
	Services   cfgServices   `json:"services"`
	Keywords   cfgKeywords   `json:"keywords"`
	Programs   cfgPrograms   `json:"programs"`
	Domains    cfgDomains    `json:"domains"`
	Guardrails cfgGuardrails `json:"guardrails"`
}

type cfgServices struct {
	Hosts             bool `json:"hosts"`
	ForbiddenPrograms bool `json:"forbiddenPrograms"`
	ContentMonitoring bool `json:"contentMonitoring"`
	ViolationTracking bool `json:"violationTracking"`
	UsageMonitor      bool `json:"usageMonitor"`
	Sudoers           bool `json:"sudoers"`
	Panic             bool `json:"panic"`
	Sync              bool `json:"sync"`
	SyncEncrypted     bool `json:"syncEncrypted"`
}

type cfgKeywords struct {
	URL            []string `json:"url"`
	Content        []string `json:"content"`
	WhitelistCount int      `json:"whitelistCount"`
}

type cfgWindow struct {
	Start string   `json:"start"`
	End   string   `json:"end"`
	Days  []string `json:"days"`
}

type cfgProgram struct {
	Name       string      `json:"name"`
	Kill       []cfgWindow `json:"kill"`
	Allow      []cfgWindow `json:"allow"`
	Extendible bool        `json:"extendible"`
}

type cfgPrograms struct {
	AlwaysKilled []string     `json:"alwaysKilled"`
	Windowed     []cfgProgram `json:"windowed"`
}

type cfgDomain struct {
	Name  string      `json:"name"`
	Block []cfgWindow `json:"block"`
}

type cfgDomains struct {
	Blocked       int         `json:"blocked"`
	AlwaysBlocked int         `json:"alwaysBlocked"`
	TimeBased     []cfgDomain `json:"timeBased"`
}

type cfgGuardrails struct {
	SudoAllow          []cfgWindow `json:"sudoAllow"`
	UnblockMaxPerDay   int         `json:"unblockMaxPerDay"`
	ViolationMax       int         `json:"violationMax"`
	ViolationWindowMin int         `json:"violationWindowMin"`
	PanicDurationMin   int         `json:"panicDurationMin"`
	CheckIntervalSec   int         `json:"checkIntervalSec"`
}

// configUpload is the POST body /api/config expects. Config is the opaque blob
// (base64 nonce||ciphertext under E2E, or the cleartext subset JSON otherwise);
// Hash fingerprints the plaintext subset.
type configUpload struct {
	TS            int64  `json:"ts"`
	Encrypted     bool   `json:"encrypted"`
	Hash          string `json:"hash"`
	Config        string `json:"config"`
	BlocklistHash string `json:"blocklistHash,omitempty"`
	Blocklist     string `json:"blocklist,omitempty"`
}

// buildConfigSnapshot derives the curated subset from the loaded config. It reads
// config.yaml's own view (not runtime enforcement state), so it reflects what the
// user configured, and never includes the full ~60MB domain list — only counts
// plus the handful of time-based domains.
func buildConfigSnapshot(cfg *config.Config) cfgSnapshot {
	toWindows := func(ws []config.TimeWindow) []cfgWindow {
		out := make([]cfgWindow, 0, len(ws))
		for _, w := range ws {
			out = append(out, cfgWindow{Start: w.Start, End: w.End, Days: w.Days})
		}
		return out
	}

	// Domains: total & always-blocked counts, with the (few) time-based ones spelled out.
	blocked := len(cfg.Domains.List)
	timeBasedCount := 0
	var timeBased []cfgDomain
	for _, d := range cfg.Domains.List {
		if len(d.BlockWindows) == 0 {
			continue
		}
		timeBasedCount++
		if len(timeBased) < maxTimeBasedDomains {
			timeBased = append(timeBased, cfgDomain{Name: d.Name, Block: toWindows(d.BlockWindows)})
		}
	}
	always := blocked - timeBasedCount
	if always < 0 {
		always = 0
	}

	// Forbidden programs, bucketed the same way as `glocker -info`.
	var alwaysKilled []string
	var windowed []cfgProgram
	for _, p := range cfg.Programs.List {
		if len(p.KillWindows) == 0 && len(p.AllowWindows) == 0 {
			alwaysKilled = append(alwaysKilled, p.Name)
		} else {
			windowed = append(windowed, cfgProgram{
				Name:       p.Name,
				Kill:       toWindows(p.KillWindows),
				Allow:      toWindows(p.AllowWindows),
				Extendible: p.Extendible,
			})
		}
	}

	return cfgSnapshot{
		Services: cfgServices{
			Hosts:             cfg.Domains.Enabled,
			ForbiddenPrograms: cfg.Programs.Enabled,
			ContentMonitoring: cfg.Content.Monitoring.Enabled,
			ViolationTracking: cfg.ViolationTracking.Enabled,
			UsageMonitor:      cfg.UsageMonitor.Enabled,
			Sudoers:           cfg.Sudoers.Enabled,
			Panic:             cfg.Panic.Enabled,
			Sync:              cfg.Sync.Enabled,
			SyncEncrypted:     cfg.Sync.Encryption(),
		},
		Keywords: cfgKeywords{
			URL:            cfg.Content.Keywords.URLKeywords,
			Content:        cfg.Content.Keywords.ContentKeywords,
			WhitelistCount: len(cfg.Content.Keywords.Whitelist),
		},
		Programs: cfgPrograms{AlwaysKilled: alwaysKilled, Windowed: windowed},
		Domains:  cfgDomains{Blocked: blocked, AlwaysBlocked: always, TimeBased: timeBased},
		Guardrails: cfgGuardrails{
			SudoAllow:          toWindows(cfg.Sudoers.AllowWindows),
			UnblockMaxPerDay:   cfg.Domains.Unblocking.MaxPerDay,
			ViolationMax:       cfg.ViolationTracking.MaxViolations,
			ViolationWindowMin: cfg.ViolationTracking.TimeWindowMinutes,
			PanicDurationMin:   cfg.Panic.Duration,
			CheckIntervalSec:   cfg.Programs.CheckInterval,
		},
	}
}

// syncConfig reconciles this device's config snapshot with glockpeek: it hashes
// the local subset, asks glockpeek for the hash it holds, and uploads only when
// they differ (or glockpeek has none). Enforcement never depends on this.
//
// It reports whether glockpeek is now known to hold the current snapshot —
// either because we just uploaded it, or because its stored hash already
// matched. False means "try again on the next tick": glockpeek down, upload
// rejected, or the snapshot couldn't be sealed. Run retries on that basis until
// it lands, so a glockpeek that was unreachable at daemon start doesn't leave
// the Config tab empty until the next restart. Once it lands Run stops calling
// it — changing the config needs a daemon restart anyway, since there is no
// reload path.
func (s *Syncer) syncConfig() bool {
	if s.cfg == nil {
		return true // nothing to sync, and nothing a retry would change
	}
	snap := buildConfigSnapshot(s.cfg)
	// Marshal is deterministic here (no maps), so the hash is stable across runs
	// for an unchanged config — that's what lets glockpeek's stored hash short-
	// circuit a re-upload.
	plaintext, err := json.Marshal(snap)
	if err != nil {
		slog.Error("syncer: marshal config snapshot failed", "err", err)
		return true // deterministic: retrying the same snapshot can't help
	}
	sum := sha256.Sum256(plaintext)
	hash := hex.EncodeToString(sum[:])

	// The blocklist is reconciled independently — it's multi-MB and changes rarely
	// (only when the blocklists are refreshed), so we only ship it when its own
	// hash differs.
	names := s.sortedDomains()
	blHash := blocklistHashOf(names)

	storedHash, storedBl, ok := s.storedConfigHashes()
	snapshotFresh := ok && storedHash == hash
	blocklistFresh := ok && storedBl == blHash
	if snapshotFresh && blocklistFresh {
		slog.Debug("syncer: config + blocklist unchanged, skipping upload", "hash", hash)
		return true
	}

	ts := time.Now().UnixMilli()
	up := configUpload{TS: ts, Hash: hash}
	blob, encrypted, err := s.encConfig(plaintext, ts)
	if err != nil {
		// Never upload a snapshot we couldn't seal. The whole subset is ONE
		// blob, so an empty one isn't a degraded snapshot the way a dropped
		// field is (see enc) — and it would be stored against a valid hash,
		// after which the hash check below short-circuits every later connect
		// and the dashboard shows "Unreadable snapshot" for good.
		slog.Error("syncer: config encryption failed; not uploading", "err", err)
		return false
	}
	up.Config, up.Encrypted = blob, encrypted

	// Attach the blocklist only when it changed. A seal failure here doesn't sink
	// the snapshot upload — the blocklist just retries next tick.
	blocklistSent := false
	if !blocklistFresh {
		if blBlob, _, berr := s.encBlocklist(names, blHash); berr != nil {
			slog.Error("syncer: blocklist seal failed; sending snapshot only", "err", berr)
		} else {
			up.Blocklist, up.BlocklistHash = blBlob, blHash
			blocklistSent = true
		}
	}

	if err := s.postConfig(up); err != nil {
		slog.Warn("syncer: config upload failed; will retry next tick", "err", err)
		return false
	}
	slog.Info("syncer: config synced", "hash", hash, "blocklistSent", blocklistSent, "encrypted", up.Encrypted)
	// Only "landed" once the blocklist is up too, so Run keeps retrying if only
	// the blocklist is still missing.
	return blocklistFresh || blocklistSent
}

// encConfig turns the plaintext subset into the stored blob. Under E2E it's the
// whole JSON sealed as one AES-GCM box (aad binds it to this record identity),
// base64 of nonce||ciphertext; otherwise the cleartext JSON, matching how the
// record fields degrade in local/plaintext mode. A sealing failure is returned,
// never swallowed — the caller has to abandon the upload entirely.
func (s *Syncer) encConfig(plaintext []byte, ts int64) (string, bool, error) {
	if !s.e2e {
		return string(plaintext), false, nil
	}
	aad := fmt.Appendf(nil, "%d|deviceconfig|config", ts)
	box, err := e2e.EncryptRecord(s.mk, plaintext, aad)
	if err != nil {
		// mk is validated at load, so this is effectively impossible. Report it
		// rather than returning an empty blob: the caller must not upload, and
		// must not leave a valid hash standing for content nobody can read.
		return "", true, err
	}
	buf := make([]byte, 0, len(box.Nonce)+len(box.Ct))
	buf = append(buf, box.Nonce...)
	buf = append(buf, box.Ct...)
	return base64.StdEncoding.EncodeToString(buf), true, nil
}

// storedConfigHashes asks glockpeek for the snapshot + blocklist hashes it holds
// for this device. ok is false on any error (glockpeek down, nothing stored),
// which makes syncConfig upload — safe, since the upload is an idempotent upsert.
func (s *Syncer) storedConfigHashes() (hash, blocklistHash string, ok bool) {
	req, err := http.NewRequest(http.MethodGet, s.url+"/api/config?mine=1", nil)
	if err != nil {
		return "", "", false
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var body struct {
		Hash          string `json:"hash"`
		BlocklistHash string `json:"blocklistHash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", false
	}
	return body.Hash, body.BlocklistHash, true
}

// sortedDomains returns the unique blocked-domain names, sorted — the canonical
// form the blocklist hash and gzip blob are both built from.
func (s *Syncer) sortedDomains() []string {
	set := make(map[string]struct{}, len(s.cfg.Domains.List))
	for _, d := range s.cfg.Domains.List {
		if d.Name != "" {
			set[d.Name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// blocklistHashOf fingerprints the plaintext domain list (newline-joined) without
// building the whole ~8MB string, so an unchanged blocklist is cheap to detect.
func blocklistHashOf(names []string) string {
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// encBlocklist gzips the domain list, then (under E2E) seals it as one AES-GCM box
// bound to its content hash; base64 of nonce||ciphertext, or of the gzip bytes in
// plaintext mode. The browser reverses this to a Set for membership checks.
func (s *Syncer) encBlocklist(names []string, hash string) (string, bool, error) {
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	for _, n := range names {
		gz.Write([]byte(n))
		gz.Write([]byte{'\n'})
	}
	if err := gz.Close(); err != nil {
		return "", s.e2e, err
	}
	raw := buf.Bytes()
	if !s.e2e {
		return base64.StdEncoding.EncodeToString(raw), false, nil
	}
	aad := fmt.Appendf(nil, "deviceblocklist|%s", hash)
	box, err := e2e.EncryptRecord(s.mk, raw, aad)
	if err != nil {
		return "", true, err
	}
	b := make([]byte, 0, len(box.Nonce)+len(box.Ct))
	b = append(append(b, box.Nonce...), box.Ct...)
	return base64.StdEncoding.EncodeToString(b), true, nil
}

// postConfig uploads the snapshot to glockpeek.
func (s *Syncer) postConfig(up configUpload) error {
	body, err := json.Marshal(up)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, s.url+"/api/config", bytes.NewReader(body))
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
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("config upload returned %s", resp.Status)
	}
	return nil
}
