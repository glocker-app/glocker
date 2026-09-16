package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestConfigRoundTripAndIsolation exercises the /api/config triangle: the agent
// POSTs its snapshot with its ingest token, probes the stored hash with ?mine=1,
// and the browser reads every device's snapshot with its session — all scoped to
// one account.
func TestConfigRoundTripAndIsolation(t *testing.T) {
	mux, sdb := newMux(t)
	_, aliceCookie, aliceToken := account(t, sdb, "alice@x.com")
	_, bobCookie, _ := account(t, sdb, "bob@x.com")

	post := func(token, body string) int {
		r := httptest.NewRequest("POST", "/api/config", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		return do(mux, r).Code
	}
	mineHash := func(token string) string {
		r := httptest.NewRequest("GET", "/api/config?mine=1", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		var out struct {
			Hash string `json:"hash"`
		}
		json.Unmarshal(do(mux, r).Body.Bytes(), &out)
		return out.Hash
	}
	list := func(cookie *http.Cookie) []configView {
		r := httptest.NewRequest("GET", "/api/config", nil)
		r.AddCookie(cookie)
		var out struct {
			Configs []configView `json:"configs"`
		}
		json.Unmarshal(do(mux, r).Body.Bytes(), &out)
		return out.Configs
	}

	// Nothing stored yet: hash is empty, list is empty.
	if h := mineHash(aliceToken); h != "" {
		t.Fatalf("hash before upload = %q, want empty", h)
	}
	if cs := list(aliceCookie); len(cs) != 0 {
		t.Fatalf("list before upload = %d, want 0", len(cs))
	}

	// Agent uploads a snapshot.
	const blob = " blob-nonce-ciphertext-base64 "
	if code := post(aliceToken, `{"ts":1000,"encrypted":true,"hash":"h1","config":"`+strings.TrimSpace(blob)+`"}`); code != http.StatusNoContent {
		t.Fatalf("POST config: got %d, want 204", code)
	}

	// The hash probe reflects it, so the agent won't re-upload an unchanged config.
	if h := mineHash(aliceToken); h != "h1" {
		t.Fatalf("mine hash = %q, want h1", h)
	}

	// The browser reads it back verbatim, tagged with a non-local device id.
	cs := list(aliceCookie)
	if len(cs) != 1 {
		t.Fatalf("list = %d, want 1", len(cs))
	}
	if cs[0].Config != strings.TrimSpace(blob) || !cs[0].Encrypted || cs[0].TS != 1000 {
		t.Fatalf("round-trip mismatch: %+v", cs[0])
	}
	if cs[0].DeviceID == 0 {
		t.Errorf("device id should be the token id (non-zero) in hosted mode")
	}

	// A second upload upserts (latest only, no history).
	if code := post(aliceToken, `{"ts":2000,"encrypted":true,"hash":"h2","config":"newblob"}`); code != http.StatusNoContent {
		t.Fatalf("POST config (update): got %d, want 204", code)
	}
	if h := mineHash(aliceToken); h != "h2" {
		t.Fatalf("mine hash after update = %q, want h2", h)
	}
	if cs := list(aliceCookie); len(cs) != 1 || cs[0].Config != "newblob" || cs[0].TS != 2000 {
		t.Fatalf("update should replace, not append: %+v", cs)
	}

	// Isolation: bob sees none of alice's config.
	if cs := list(bobCookie); len(cs) != 0 {
		t.Errorf("bob should see no configs, got %d", len(cs))
	}

	// A browser session cannot POST a snapshot (agent-token only).
	rp := httptest.NewRequest("POST", "/api/config", strings.NewReader(`{"hash":"x"}`))
	rp.AddCookie(aliceCookie)
	if code := do(mux, rp).Code; code != http.StatusUnauthorized {
		t.Errorf("session POST should be rejected: got %d, want 401", code)
	}
}
