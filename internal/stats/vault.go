package stats

import (
	"encoding/json"
	"io"
	"net/http"
)

// handleVault stores (PUT) or returns (GET) the account's KeyVault — the
// passphrase-wrapped master key for end-to-end encryption. The server never reads
// the vault's contents; it's opaque wrapped material it can't open. Session-gated.
func handleVault(w http.ResponseWriter, r *http.Request) {
	uid := userFrom(r).ID
	switch r.Method {
	case http.MethodGet:
		v, ok, err := db.GetVault(uid)
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
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !looksLikeVault(body) {
			http.Error(w, "not a vault", http.StatusBadRequest)
			return
		}
		if err := db.SetVault(uid, string(body)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
