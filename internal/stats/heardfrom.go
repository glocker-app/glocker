package stats

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// handleHeardFrom records the account's one-time, optional "how did you hear
// about Glocker?" answer (asked post-signup in the dashboard). A dismiss stores
// the sentinel "(skipped)" so the prompt isn't shown again. Session-gated.
func handleHeardFrom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u := userFrom(r)
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var in struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src := strings.TrimSpace(in.Source)
	if src == "" {
		src = "(skipped)"
	}
	if len(src) > 120 {
		src = src[:120]
	}
	if err := db.SetHeardFrom(u.ID, src); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
