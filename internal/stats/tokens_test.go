package stats

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"glocker/internal/store"

	"gorm.io/gorm"
)

func TestStoreDeviceLimitAndTokenOps(t *testing.T) {
	_, sdb := newMux(t)
	u, err := sdb.CreateUser("dev@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}

	// Fresh accounts are free tier: effective limit is DefaultFreeDevices.
	if got := store.EffectiveDeviceLimit(u); got != store.DefaultFreeDevices {
		t.Errorf("default device limit = %d, want %d", got, store.DefaultFreeDevices)
	}
	if n, _ := sdb.CountAPITokens(u.ID); n != 0 {
		t.Errorf("new account token count = %d, want 0", n)
	}

	plainA, err := sdb.CreateAPIToken(u.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.CreateAPIToken(u.ID, "b"); err != nil {
		t.Fatal(err)
	}
	toks, err := sdb.ListAPITokens(u.ID)
	if err != nil || len(toks) != 2 {
		t.Fatalf("list = %v (len %d, err %v), want 2", toks, len(toks), err)
	}

	// Token "a" authenticates at ingest before revocation.
	if _, _, err := sdb.UserAndDeviceByAPIToken(plainA); err != nil {
		t.Fatalf("token A should authenticate before revoke: %v", err)
	}

	// Revoke it (scoped to the user); the active count drops...
	if err := sdb.RevokeAPIToken(u.ID, toks[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if n, _ := sdb.CountAPITokens(u.ID); n != 1 {
		t.Errorf("active count after revoke = %d, want 1", n)
	}
	// ...but the row is kept (soft-revoke), now marked revoked, so the device's
	// history stays named.
	after, _ := sdb.ListAPITokens(u.ID)
	if len(after) != 2 {
		t.Errorf("list after revoke = %d rows, want 2 (revoked kept)", len(after))
	}
	if after[0].RevokedAt == nil {
		t.Error("revoked token should have RevokedAt set")
	}
	// A revoked token no longer authenticates at ingest.
	if _, _, err := sdb.UserAndDeviceByAPIToken(plainA); err == nil {
		t.Error("revoked token must be rejected at ingest")
	}
	// Re-revoking an already-revoked token is a not-found (no active row matched).
	if err := sdb.RevokeAPIToken(u.ID, toks[0].ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("re-revoke: got %v, want ErrRecordNotFound", err)
	}
	// Revoking an unknown / other-user token is a not-found, not a silent no-op.
	if err := sdb.RevokeAPIToken(u.ID, 999999); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("revoke unknown: got %v, want ErrRecordNotFound", err)
	}

	// Raise the limit (paid upgrade), then unlimited.
	if err := sdb.SetDeviceLimit("dev@x.com", 5); err != nil {
		t.Fatal(err)
	}
	u2, _ := sdb.UserByEmail("dev@x.com")
	if got := store.EffectiveDeviceLimit(u2); got != 5 {
		t.Errorf("raised limit = %d, want 5", got)
	}
	if err := sdb.SetDeviceLimit("dev@x.com", -1); err != nil {
		t.Fatal(err)
	}
	u3, _ := sdb.UserByEmail("dev@x.com")
	if got := store.EffectiveDeviceLimit(u3); got != -1 {
		t.Errorf("unlimited limit = %d, want -1", got)
	}
	if err := sdb.SetDeviceLimit("nobody@x.com", 3); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("set limit unknown email: got %v, want ErrRecordNotFound", err)
	}
}

type tokenListResp struct {
	Tokens []struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	} `json:"tokens"`
	Limit  int  `json:"limit"`
	Used   int  `json:"used"`
	CanAdd bool `json:"canAdd"`
}

func TestTokensEndpoint_LimitMintRevoke(t *testing.T) {
	mux, sdb := newMux(t)
	u, err := sdb.CreateUser("owner@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	sessTok, err := sdb.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: sessTok}

	req := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r)
	}
	list := func() tokenListResp {
		rec := req("GET", "/api/tokens", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET tokens: %d %s", rec.Code, rec.Body.String())
		}
		var lr tokenListResp
		if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return lr
	}

	// Empty to start, free-tier limit of 1, add allowed.
	if lr := list(); lr.Used != 0 || lr.Limit != store.DefaultFreeDevices || !lr.CanAdd {
		t.Fatalf("initial list = %+v", lr)
	}

	// Mint one → plaintext returned once.
	rec := req("POST", "/api/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	var minted struct {
		Token, Name string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &minted)
	if minted.Token == "" || minted.Name != "laptop" {
		t.Fatalf("mint response = %s", rec.Body.String())
	}

	// Now at the cap: list reflects it, second mint is refused with 402.
	if lr := list(); lr.Used != 1 || lr.CanAdd {
		t.Fatalf("after mint list = %+v", lr)
	}
	if rec := req("POST", "/api/tokens", `{"name":"phone"}`); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("over-limit mint: got %d, want 402", rec.Code)
	}

	// Raise the limit (paid), second mint now succeeds.
	if err := sdb.SetDeviceLimit("owner@x.com", 2); err != nil {
		t.Fatal(err)
	}
	if rec := req("POST", "/api/tokens", `{"name":"phone"}`); rec.Code != http.StatusOK {
		t.Fatalf("mint after upgrade: %d %s", rec.Code, rec.Body.String())
	}

	// Revoke the first token; count drops. Unknown id → 404.
	lr := list()
	if lr.Used != 2 {
		t.Fatalf("pre-revoke used = %d, want 2", lr.Used)
	}
	id := lr.Tokens[0].ID
	if rec := req("DELETE", "/api/tokens?id="+strconv.FormatUint(uint64(id), 10), ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if lr := list(); lr.Used != 1 {
		t.Errorf("post-revoke used = %d, want 1", lr.Used)
	}
	if rec := req("DELETE", "/api/tokens?id=999999", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown: got %d, want 404", rec.Code)
	}
}

func TestTokensEndpoint_Rename(t *testing.T) {
	mux, sdb := newMux(t)
	u, err := sdb.CreateUser("owner@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _ := sdb.CreateSession(u.ID)
	cookie := &http.Cookie{Name: sessionCookie, Value: sessTok}
	req := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r)
	}

	if rec := req("POST", "/api/tokens", `{"name":"laptop"}`); rec.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	toks, _ := sdb.ListAPITokens(u.ID)
	id := strconv.FormatUint(uint64(toks[0].ID), 10)

	// Rename succeeds and takes effect.
	if rec := req("PATCH", "/api/tokens?id="+id, `{"name":"work laptop"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if after, _ := sdb.ListAPITokens(u.ID); after[0].Name != "work laptop" {
		t.Errorf("name after rename = %q, want %q", after[0].Name, "work laptop")
	}
	// Empty name → 400; unknown id → 404.
	if rec := req("PATCH", "/api/tokens?id="+id, `{"name":"  "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty rename: got %d, want 400", rec.Code)
	}
	if rec := req("PATCH", "/api/tokens?id=999999", `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("rename unknown: got %d, want 404", rec.Code)
	}
	// A revoked device is not renameable (frozen history) → 404.
	if err := sdb.RevokeAPIToken(u.ID, toks[0].ID); err != nil {
		t.Fatal(err)
	}
	if rec := req("PATCH", "/api/tokens?id="+id, `{"name":"nope"}`); rec.Code != http.StatusNotFound {
		t.Errorf("rename revoked: got %d, want 404", rec.Code)
	}
}

func TestTokensEndpoint_RequiresAuth(t *testing.T) {
	mux, _ := newMux(t)
	if rec := do(mux, httptest.NewRequest("GET", "/api/tokens", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /api/tokens: got %d, want 401", rec.Code)
	}
}
