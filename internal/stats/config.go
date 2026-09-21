package stats

import (
	"encoding/json"
	"io"
	"net/http"

	"glocker/internal/store"
)

// handleConfig dispatches the per-device configuration snapshot the agent uploads
// so the dashboard can render it (the foundation for the Insights tab). Like the
// stats rows, the snapshot is content-blind: Config is an opaque blob the server
// stores and hands back without parsing.
//
//   - POST (agent, ingest token): upsert this device's snapshot, and optionally
//     the (large) blocklist blob when its own hash changed.
//   - GET ?mine=1 (agent, ingest token): return this device's stored snapshot and
//     blocklist hashes, so the agent re-uploads only what actually changed.
//   - GET ?blocklist=1 (browser, session): return every device's blocklist blob
//     (multi-MB) for membership checks + domain search.
//   - GET (browser, session): return every device's snapshot (no blocklist).
func handleConfig(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost:
		requireToken(handleConfigPost)(w, r)
	case r.Method == http.MethodGet && r.URL.Query().Get("mine") == "1":
		requireToken(handleConfigMine)(w, r)
	case r.Method == http.MethodGet && r.URL.Query().Get("blocklist") == "1":
		requireUser(handleConfigBlocklists)(w, r)
	case r.Method == http.MethodGet:
		requireUser(handleConfigList)(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// configUpload is what the agent POSTs. Config is the opaque snapshot blob (base64
// nonce||ciphertext under E2E, or cleartext JSON otherwise); Hash fingerprints the
// plaintext subset. Blocklist (optional, only when it changed) is the gzip'd domain
// list, likewise encrypted+base64; BlocklistHash fingerprints its plaintext.
type configUpload struct {
	TS            int64  `json:"ts"`
	Encrypted     bool   `json:"encrypted"`
	Hash          string `json:"hash"`
	Config        string `json:"config"`
	BlocklistHash string `json:"blocklistHash,omitempty"`
	Blocklist     string `json:"blocklist,omitempty"`
	// Accountability is the accountability-email config, sent cleartext (glockpeek
	// is the sender and must read whom/how-often to email). Absent on older agents.
	Accountability *accountabilityUpload `json:"accountability,omitempty"`
}

// accountabilityUpload mirrors the agent's cleartext accountability wire shape.
type accountabilityUpload struct {
	Enabled          bool     `json:"enabled"`
	BuddyEmails      []string `json:"buddyEmails,omitempty"`
	DigestEveryHours int      `json:"digestEveryHours"`
	DigestWhenQuiet  bool     `json:"digestWhenQuiet"`
	WentDark         bool     `json:"wentDark"`
	HighViolations   int      `json:"highViolations"`
}

func handleConfigPost(w http.ResponseWriter, r *http.Request) {
	// Generous cap: the snapshot is tiny but the optional blocklist blob is a few MB.
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var in configUpload
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Same backstop as ingest: when the operator requires encryption, refuse a
	// snapshot the agent didn't encrypt, so a misconfigured agent can't record
	// cleartext config here.
	if requireEncryption && !in.Encrypted {
		http.Error(w, "this glockpeek requires encrypted records (require_encryption is on) but the config snapshot is not encrypted — set sync.encrypt: true on the agent", http.StatusUnprocessableEntity)
		return
	}
	uid := userFrom(r).ID
	dev := deviceFrom(r) // the authenticating token = this device; 0 in local mode
	if err := db.SaveDeviceConfig(uid, dev, store.DeviceConfig{
		TS: in.TS, Encrypted: in.Encrypted, Hash: in.Hash, Config: in.Config,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The blocklist rides along only when it changed; save it separately so a
	// snapshot-only upload never touches the multi-MB column.
	if in.BlocklistHash != "" && in.Blocklist != "" {
		if err := db.SaveDeviceBlocklist(uid, dev, in.BlocklistHash, in.Blocklist); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// Accountability config (cleartext) is account-level; the latest sync wins.
	if in.Accountability != nil {
		n := in.Accountability
		if err := db.SaveAccountabilityConfig(uid, store.AccountabilityConfig{
			Enabled: n.Enabled, BuddyEmails: store.EncodeBuddies(n.BuddyEmails),
			DigestEveryHours: n.DigestEveryHours, DigestWhenQuiet: n.DigestWhenQuiet,
			WentDark: n.WentDark, HighViolations: n.HighViolations,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleConfigMine(w http.ResponseWriter, r *http.Request) {
	hash, blocklistHash, err := db.DeviceConfigHashes(userFrom(r).ID, deviceFrom(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"hash": hash, "blocklistHash": blocklistHash})
}

// blocklistView is one device's blocklist blob as the dashboard consumes it.
type blocklistView struct {
	DeviceID  uint   `json:"deviceId"`
	Encrypted bool   `json:"encrypted"`
	Hash      string `json:"hash"`
	Blocklist string `json:"blocklist"`
}

func handleConfigBlocklists(w http.ResponseWriter, r *http.Request) {
	rows, err := db.DeviceBlocklists(userFrom(r).ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]blocklistView, 0, len(rows))
	for _, c := range rows {
		out = append(out, blocklistView{DeviceID: c.DeviceID, Encrypted: c.Encrypted, Hash: c.BlocklistHash, Blocklist: c.Blocklist})
	}
	writeJSON(w, map[string]any{"blocklists": out})
}

// configView is one device's snapshot as the dashboard consumes it: the stored
// blob plus a human label (the device's API-token name; empty for the local /
// single-device account).
type configView struct {
	DeviceID  uint   `json:"deviceId"`
	Name      string `json:"name"`
	TS        int64  `json:"ts"`
	Encrypted bool   `json:"encrypted"`
	Config    string `json:"config"`
}

func handleConfigList(w http.ResponseWriter, r *http.Request) {
	uid := userFrom(r).ID
	rows, err := db.DeviceConfigs(uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Map device id -> token name for a friendly label; a snapshot from device 0
	// (local mode) has no token and stays unnamed.
	names := map[uint]string{}
	if toks, err := db.ListAPITokens(uid); err == nil {
		for _, t := range toks {
			names[t.ID] = t.Name
		}
	}
	out := make([]configView, 0, len(rows))
	for _, c := range rows {
		out = append(out, configView{
			DeviceID: c.DeviceID, Name: names[c.DeviceID],
			TS: c.TS, Encrypted: c.Encrypted, Config: c.Config,
		})
	}
	writeJSON(w, map[string]any{"configs": out})
}
