package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/store"
)

// askHeardFrom flips true→false once answered, and the answer (or the skip
// sentinel) is persisted.
func TestHeardFromPromptLifecycle(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true})

	ask := func(cookie *http.Cookie) bool {
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.AddCookie(cookie)
		rec := do(mux, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/me: %d (%s)", rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m["askHeardFrom"] == true
	}
	post := func(cookie *http.Cookie, body string) int {
		r := httptest.NewRequest("POST", "/api/heard-from", strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r).Code
	}

	// Answered path.
	_, c1, _ := account(t, sdb, "a@x.com")
	if !ask(c1) {
		t.Fatal("new account should be asked")
	}
	if code := post(c1, `{"source":"Reddit"}`); code != http.StatusOK {
		t.Fatalf("post: %d", code)
	}
	if ask(c1) {
		t.Fatal("answered account should not be asked again")
	}
	if u, _ := sdb.UserByEmail("a@x.com"); u == nil || u.HeardFrom == nil || *u.HeardFrom != "Reddit" {
		t.Fatalf("heard_from not stored: %+v", u)
	}

	// Dismiss (empty source) stores the sentinel and stops asking.
	_, c2, _ := account(t, sdb, "b@x.com")
	if code := post(c2, `{"source":""}`); code != http.StatusOK {
		t.Fatalf("skip post: %d", code)
	}
	if ask(c2) {
		t.Fatal("dismissed account should not be asked again")
	}
	if u, _ := sdb.UserByEmail("b@x.com"); u == nil || u.HeardFrom == nil || *u.HeardFrom != "(skipped)" {
		t.Fatalf("skip sentinel not stored: %+v", u)
	}
}
