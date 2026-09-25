package stats

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"glocker/internal/store"

	"gorm.io/gorm"
)

// tokenView is the safe projection of an ingest API token for the dashboard —
// it never carries the token hash or plaintext. Times are epoch ms (or null).
type tokenView struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
	RevokedAt  *int64 `json:"revokedAt"` // set = revoked device (kept for history)
}

func toTokenView(t store.APIToken) tokenView {
	v := tokenView{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.UnixMilli()}
	if t.LastUsedAt != nil {
		ms := t.LastUsedAt.UnixMilli()
		v.LastUsedAt = &ms
	}
	if t.RevokedAt != nil {
		ms := t.RevokedAt.UnixMilli()
		v.RevokedAt = &ms
	}
	return v
}

// handleTokens manages a user's ingest API tokens — one per connected device
// (a glocker agent syncing to glockpeek):
//
//	GET    /api/tokens        list tokens + the account's device limit/usage
//	POST   /api/tokens        mint a token (returns the plaintext once), capped
//	                          by the device limit; over the cap → 402 upsell
//	PATCH  /api/tokens?id=N   rename one device ({"name":...})
//	DELETE /api/tokens?id=N   revoke one token
func handleTokens(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	switch r.Method {
	case http.MethodGet:
		writeTokenList(w, u)
	case http.MethodPost:
		mintToken(w, r, u)
	case http.MethodPatch:
		renameToken(w, r, u)
	case http.MethodDelete:
		revokeToken(w, r, u)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// writeTokenList returns the account's tokens plus its device limit and usage so
// the UI can show "M of N devices" and gate the add button.
func writeTokenList(w http.ResponseWriter, u *store.User) {
	toks, err := db.ListAPITokens(u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]tokenView, 0, len(toks))
	active := 0
	for _, t := range toks {
		if t.RevokedAt == nil {
			active++
		}
		views = append(views, toTokenView(t))
	}
	limit := deviceLimit(u)
	// The cap counts only active devices; revoked ones are kept in the list (for
	// naming history) but don't consume a slot.
	writeJSON(w, map[string]any{
		"tokens": views,
		"limit":  limit, // -1 means unlimited
		"used":   active,
		"canAdd": limit < 0 || active < limit,
	})
}

// mintToken creates a new token for the account, enforcing the device limit.
func mintToken(w http.ResponseWriter, r *http.Request, u *store.User) {
	var in struct {
		Name string `json:"name"`
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024)); err == nil && len(body) > 0 {
		_ = json.Unmarshal(body, &in)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "device"
	}
	if len(name) > 64 {
		name = name[:64]
	}

	// Enforce the device cap (skipped in single-user/local mode, where ingest is
	// tokenless anyway and there is no plan).
	if authEnabled {
		limit := deviceLimit(u)
		if limit >= 0 {
			used, err := db.CountAPITokens(u.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if used >= limit {
				// 402: the account is at its plan's device limit.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "device limit reached", "limit": limit, "used": used,
				})
				return
			}
		}
	}

	tok, err := db.CreateAPIToken(u.ID, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The plaintext is returned exactly once; only its hash is stored.
	writeJSON(w, map[string]any{"token": tok, "name": name})
}

// renameToken relabels one of the account's devices by id (?id=N).
func renameToken(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid token id", http.StatusBadRequest)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024)); err == nil && len(body) > 0 {
		_ = json.Unmarshal(body, &in)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if len(name) > 64 {
		name = name[:64]
	}
	if err := db.RenameAPIToken(u.ID, uint(id), name); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

// revokeToken deletes one of the account's tokens by id (?id=N).
func revokeToken(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid token id", http.StatusBadRequest)
		return
	}
	if err := db.RevokeAPIToken(u.ID, uint(id)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// deviceLimit is the effective device cap for u (-1 = unlimited).
func deviceLimit(u *store.User) int {
	return store.EffectiveDeviceLimit(u)
}
