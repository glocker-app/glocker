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
//   - POST (agent, ingest token): upsert this device's snapshot.
//   - GET ?mine=1 (agent, ingest token): return this device's stored hash, so the
//     agent only re-uploads when its local config actually changed.
//   - GET (browser, session): return every device's snapshot for the account.
func handleConfig(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost:
		requireToken(handleConfigPost)(w, r)
	case r.Method == http.MethodGet && r.URL.Query().Get("mine") == "1":
		requireToken(handleConfigMine)(w, r)
	case r.Method == http.MethodGet:
		requireUser(handleConfigList)(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// configUpload is the snapshot the agent POSTs. Config is the opaque blob
// (base64 nonce||ciphertext under E2E, or cleartext JSON in plaintext mode);
// Hash fingerprints the plaintext subset.
type configUpload struct {
	TS        int64  `json:"ts"`
	Encrypted bool   `json:"encrypted"`
	Hash      string `json:"hash"`
	Config    string `json:"config"`
}

func handleConfigPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB is ample for a curated subset
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
	w.WriteHeader(http.StatusNoContent)
}

func handleConfigMine(w http.ResponseWriter, r *http.Request) {
	hash, err := db.DeviceConfigHash(userFrom(r).ID, deviceFrom(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"hash": hash})
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
