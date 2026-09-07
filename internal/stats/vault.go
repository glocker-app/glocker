package stats

import (
	"encoding/json"
	"io"
	"net/http"
)

// handleVault dispatches the account's KeyVault — the passphrase-wrapped master
// key for end-to-end encryption. The server stores it opaquely; it can't open it.
// GET is readable by the browser (session) OR the agent (ingest token), since the
// agent fetches its vault during `glocker -connect`. PUT is browser-only — only
// the setup page mints a vault.
func handleVault(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		requireSessionOrToken(handleVaultGet)(w, r)
	case http.MethodPut:
		requireUser(handleVaultPut)(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleVaultGet(w http.ResponseWriter, r *http.Request) {
	v, ok, err := db.GetVault(userFrom(r).ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no vault", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, v)
}

func handleVaultPut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !looksLikeVault(body) {
		http.Error(w, "not a vault", http.StatusBadRequest)
		return
	}
	if err := db.SetVault(userFrom(r).ID, string(body)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// looksLikeVault is a cheap structural guard against junk PUTs. It does NOT (and
// cannot) validate the crypto — it only checks the expected wrapped-material shape
// so the server never reads or trusts the contents.
func looksLikeVault(b []byte) bool {
	var v struct {
		KDF       string          `json:"kdf"`
		Salt      string          `json:"salt"`
		WrappedMK json.RawMessage `json:"wrappedMK"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return false
	}
	return v.KDF != "" && v.Salt != "" && len(v.WrappedMK) > 0
}
