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

// handleTags is the tag-registry API (session-gated). GET lists the account's
// registry (system tags with its overrides applied, then its own tags). POST
// creates a user tag; PATCH edits one (or flips a system tag's distraction via an
// override); DELETE removes a user tag and unlinks it from the account's rules.
func handleTags(w http.ResponseWriter, r *http.Request) {
	uid := userFrom(r).ID
	switch r.Method {
	case http.MethodGet:
		tags, err := db.TagsForUser(uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"tags": tags})

	case http.MethodPost:
		in, ok := readTagInput(w, r)
		if !ok {
			return
		}
		if in.Namespace == "" || in.Name == "" {
			http.Error(w, "namespace and name are required", http.StatusBadRequest)
			return
		}
		if strings.EqualFold(in.Namespace, "Glocker") {
			http.Error(w, "Glocker is a system namespace; add tags to your own namespace", http.StatusBadRequest)
			return
		}
		t, err := db.CreateUserTag(uid, in.Namespace, in.Name, in.Color, in.Distraction)
		if err != nil {
			// The unique index makes a duplicate a conflict, not a server error.
			http.Error(w, "a tag with that namespace and name already exists", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, t)

	case http.MethodPatch:
		id, ok := tagID(w, r)
		if !ok {
			return
		}
		in, ok := readTagPatch(w, r)
		if !ok {
			return
		}
		// PATCH is partial: only the fields the client sent are written, so
		// saving a colour never blanks the tag's namespace/name. Build the
		// column map from the fields that were actually present.
		fields := map[string]any{}
		if in.Namespace != nil {
			fields["namespace"] = *in.Namespace
		}
		if in.Name != nil {
			fields["name"] = *in.Name
		}
		if in.Color != nil {
			fields["color"] = *in.Color
		}
		if in.Distraction != nil {
			fields["distraction"] = *in.Distraction
		}
		// A user tag updates in place; a system tag only accepts a distraction
		// change, recorded as this account's override.
		err := db.UpdateUserTagFields(uid, id, fields)
		if errors.Is(err, gorm.ErrRecordNotFound) && in.Distraction != nil {
			err = db.SetTagDistraction(uid, id, *in.Distraction)
		}
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "tag not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		id, ok := tagID(w, r)
		if !ok {
			return
		}
		switch err := db.DeleteUserTag(uid, id); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrSystemTag):
			http.Error(w, "system tags cannot be deleted", http.StatusForbidden)
		case errors.Is(err, gorm.ErrRecordNotFound):
			http.Error(w, "tag not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type tagInput struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Distraction bool   `json:"distraction"`
}

func readTagInput(w http.ResponseWriter, r *http.Request) (tagInput, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return tagInput{}, false
	}
	var in tagInput
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return tagInput{}, false
	}
	return in, true
}

// tagPatch is the PATCH body. Pointers distinguish "field omitted" (leave the
// column alone) from "field set to its zero value", so a partial update — e.g.
// just {"color":"…"} — never blanks the namespace/name it didn't mention.
type tagPatch struct {
	Namespace   *string `json:"namespace"`
	Name        *string `json:"name"`
	Color       *string `json:"color"`
	Distraction *bool   `json:"distraction"`
}

func readTagPatch(w http.ResponseWriter, r *http.Request) (tagPatch, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return tagPatch{}, false
	}
	var in tagPatch
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return tagPatch{}, false
	}
	return in, true
}

func tagID(w http.ResponseWriter, r *http.Request) (uint, bool) {
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "a numeric ?id is required", http.StatusBadRequest)
		return 0, false
	}
	return uint(id), true
}
