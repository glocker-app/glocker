package stats

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/store"
)

// With require_encryption on, the ingest endpoint must reject a batch the agent
// didn't mark encrypted, and accept one that is marked — the server-side backstop
// against silently recording cleartext.
func TestIngestRequireEncryption(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, RequireEncryption: true})
	_, _, token := account(t, sdb, "a@x.com")

	post := func(body string) int {
		r := httptest.NewRequest("POST", "/api/ingest", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		return do(mux, r).Code
	}

	// Unmarked (plaintext) batch -> rejected.
	if code := post(`{"encrypted":false,"violations":[{"ts":1,"recordId":"r1","keyword":"k","url":"u"}]}`); code != http.StatusUnprocessableEntity {
		t.Errorf("plaintext batch with require_encryption on: got %d, want 422", code)
	}
	// Encrypted batch -> accepted.
	if code := post(`{"encrypted":true,"violations":[{"ts":1,"recordId":"r1","keyword":"ct","url":"ct"}]}`); code != http.StatusOK {
		t.Errorf("encrypted batch: got %d, want 200", code)
	}
}

// With require_encryption off (the default), an unmarked plaintext batch is
// accepted — the backstop is strictly opt-in.
func TestIngestPlaintextAllowedByDefault(t *testing.T) {
	mux, sdb := newMux(t) // Options{Auth: true}, RequireEncryption defaults false
	_, _, token := account(t, sdb, "b@x.com")
	r := httptest.NewRequest("POST", "/api/ingest",
		strings.NewReader(`{"encrypted":false,"violations":[{"ts":1,"recordId":"r1","keyword":"k","url":"u"}]}`))
	r.Header.Set("Authorization", "Bearer "+token)
	if code := do(mux, r).Code; code != http.StatusOK {
		t.Errorf("plaintext batch with require_encryption off: got %d, want 200", code)
	}
}
