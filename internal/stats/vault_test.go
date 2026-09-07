package stats

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A well-formed vault payload (structurally — the crypto is never checked server-side).
const sampleVault = `{"version":1,"kdf":"pbkdf2-sha256","pbkdf2":{"iterations":600000},` +
	`"salt":"AAAAAAAAAAAAAAAAAAAAAA==","wrappedMK":{"nonce":"AAAAAAAAAAAAAAAA","ct":"AAAA"},` +
	`"wrappedMKRecovery":{"nonce":"AAAAAAAAAAAAAAAA","ct":"AAAA"}}`

func TestVaultRoundTripAndIsolation(t *testing.T) {
	mux, sdb := newMux(t)
	_, aliceCookie, aliceToken := account(t, sdb, "alice@x.com")
	_, bobCookie, _ := account(t, sdb, "bob@x.com")

	put := func(cookie *http.Cookie, body string) int {
		r := httptest.NewRequest("PUT", "/api/vault", strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r).Code
	}
	get := func(cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/vault", nil)
		r.AddCookie(cookie)
		return do(mux, r)
	}

	// No vault yet -> 404.
	if rec := get(aliceCookie); rec.Code != http.StatusNotFound {
		t.Fatalf("GET before setup: got %d, want 404", rec.Code)
	}

	// Alice stores a vault.
	if code := put(aliceCookie, sampleVault); code != http.StatusNoContent {
		t.Fatalf("PUT vault: got %d, want 204", code)
	}

	// Alice reads it back verbatim.
	rec := get(aliceCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after setup: got %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != sampleVault {
		t.Fatalf("GET returned altered vault:\n got %s\nwant %s", got, sampleVault)
	}

	// Bob has his own (empty) vault — no leakage across accounts.
	if rec := get(bobCookie); rec.Code != http.StatusNotFound {
		t.Fatalf("bob GET should be isolated: got %d, want 404", rec.Code)
	}

	// Junk that isn't a vault is rejected.
	if code := put(aliceCookie, `{"hello":"world"}`); code != http.StatusBadRequest {
		t.Fatalf("PUT junk: got %d, want 400", code)
	}

	// The agent fetches its vault with its ingest token (no browser session).
	rtok := httptest.NewRequest("GET", "/api/vault", nil)
	rtok.Header.Set("Authorization", "Bearer "+aliceToken)
	if rec := do(mux, rtok); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != sampleVault {
		t.Fatalf("token GET should return the vault: code=%d", rec.Code)
	}
	// But a token cannot PUT a vault (browser-only).
	ptok := httptest.NewRequest("PUT", "/api/vault", strings.NewReader(sampleVault))
	ptok.Header.Set("Authorization", "Bearer "+aliceToken)
	if rec := do(mux, ptok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token PUT should be rejected: got %d, want 401", rec.Code)
	}

	// /api/me reflects the vault state.
	meE2E := func(cookie *http.Cookie) bool {
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.AddCookie(cookie)
		return strings.Contains(do(mux, r).Body.String(), `"e2e":true`)
	}
	if !meE2E(aliceCookie) {
		t.Error("/api/me should report e2e:true for alice")
	}
	if meE2E(bobCookie) {
		t.Error("/api/me should report e2e:false for bob")
	}
}
