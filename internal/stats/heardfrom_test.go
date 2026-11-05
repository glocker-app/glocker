package stats

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/store"
)

// The sign-up form's optional answer is stored, trimmed and capped; leaving it
// out stores nothing.
func TestRegisterStoresHeardFrom(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, Mailer: &fakeMailer{}, AppURL: "https://my.example.com"})

	register := func(ip, email, heard string) *store.User {
		t.Helper()
		body := `{"email":"` + email + `","password":"longenough1","heardFrom":"` + heard + `"}`
		req := httptest.NewRequest("POST", "/api/register", strings.NewReader(body))
		req.Header.Set("X-Forwarded-For", ip)
		if rec := do(mux, req); rec.Code != http.StatusOK {
			t.Fatalf("register %s: %d %s", email, rec.Code, rec.Body.String())
		}
		u, err := sdb.UserByEmail(email)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	if u := register("203.0.113.1", "a@x.com", "  Reddit "); u.HeardFrom == nil || *u.HeardFrom != "Reddit" {
		t.Errorf("answer not stored: %v", u.HeardFrom)
	}
	if u := register("203.0.113.2", "b@x.com", ""); u.HeardFrom != nil {
		t.Errorf("blank answer stored: %q", *u.HeardFrom)
	}
	if u := register("203.0.113.3", "c@x.com", strings.Repeat("x", 500)); u.HeardFrom == nil || len(*u.HeardFrom) != 120 {
		t.Errorf("long answer not capped: %v", u.HeardFrom)
	}
}
