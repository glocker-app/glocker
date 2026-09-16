package syncer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	TS        int64  `json:"ts"`
	Encrypted bool   `json:"encrypted"`
	Hash      string `json:"hash"`
	Config    string `json:"config"`
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
// they differ (or glockpeek has none). Called once on connect — config changes
// land on the next daemon start/reload, not on every tick. Any error is logged
// and left for the next connect; enforcement never depends on this.
func (s *Syncer) syncConfig() {
	if s.cfg == nil {
		return
	}
	snap := buildConfigSnapshot(s.cfg)
	// Marshal is deterministic here (no maps), so the hash is stable across runs
	// for an unchanged config — that's what lets glockpeek's stored hash short-
	// circuit a re-upload.
	plaintext, err := json.Marshal(snap)
	if err != nil {
		slog.Error("syncer: marshal config snapshot failed", "err", err)
		return
	}
	sum := sha256.Sum256(plaintext)
	hash := hex.EncodeToString(sum[:])

	if stored, ok := s.storedConfigHash(); ok && stored == hash {
		slog.Debug("syncer: config unchanged, skipping upload", "hash", hash)
		return
	}

	ts := time.Now().UnixMilli()
	up := configUpload{TS: ts, Hash: hash}
	up.Config, up.Encrypted = s.encConfig(plaintext, ts)
	if err := s.postConfig(up); err != nil {
		slog.Warn("syncer: config upload failed; will retry next connect", "err", err)
		return
	}
	slog.Info("syncer: config snapshot synced", "hash", hash, "encrypted", up.Encrypted)
}

// encConfig turns the plaintext subset into the stored blob. Under E2E it's the
// whole JSON sealed as one AES-GCM box (aad binds it to this record identity),
// base64 of nonce||ciphertext; otherwise the cleartext JSON, matching how the
// record fields degrade in local/plaintext mode.
func (s *Syncer) encConfig(plaintext []byte, ts int64) (string, bool) {
	if !s.e2e {
		return string(plaintext), false
	}
	aad := fmt.Appendf(nil, "%d|deviceconfig|config", ts)
	box, err := e2e.EncryptRecord(s.mk, plaintext, aad)
	if err != nil {
		// mk is validated at load; still, never fall back to plaintext — drop it.
		slog.Error("syncer: config encryption failed; skipping upload", "err", err)
		return "", true
	}
	buf := make([]byte, 0, len(box.Nonce)+len(box.Ct))
	buf = append(buf, box.Nonce...)
	buf = append(buf, box.Ct...)
	return base64.StdEncoding.EncodeToString(buf), true
}

// storedConfigHash asks glockpeek for the config hash it currently holds for this
// device. ok is false on any error (glockpeek down, no snapshot yet), which makes
// syncConfig upload — safe, since the upload is an idempotent upsert.
func (s *Syncer) storedConfigHash() (string, bool) {
	req, err := http.NewRequest(http.MethodGet, s.url+"/api/config?mine=1", nil)
	if err != nil {
		return "", false
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var body struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false
	}
	return body.Hash, true
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
