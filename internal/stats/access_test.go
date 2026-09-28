package stats

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"glocker/internal/store"
)

// billingMuxEnabled builds a mux with billing turned on (so the access gate
// applies) plus a store, and returns them.
func billingMuxEnabled(t *testing.T) (*http.ServeMux, *store.DB) {
	t.Helper()
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{
		Auth:    true,
		Billing: BillingOptions{Enabled: true, ProductID: "p", PaidDeviceLimit: -1},
	})
	return mux, sdb
}

// A lapsed/never-subscribed account is read-only: ingest and feature writes are
// refused with 402, while a trialing account may write.
func TestAccessGate_ReadOnlyWhenLapsed(t *testing.T) {
	mux, sdb := billingMuxEnabled(t)
	uid, cookie, token := account(t, sdb, "lapsed@x.com") // subStatus "" → no access

	ingest := func() int {
		r := httptest.NewRequest("POST", "/api/ingest",
			strings.NewReader(`{"encrypted":false,"violations":[{"ts":1,"recordId":"r1","keyword":"k","url":"u"}]}`))
		r.Header.Set("Authorization", "Bearer "+token)
		return do(mux, r).Code
	}
	put := func(path, body string) int {
		r := httptest.NewRequest("PUT", path, strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r).Code
	}
	post := func(path, body string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r).Code
	}

	// No subscription → every write is 402.
	if c := ingest(); c != http.StatusPaymentRequired {
		t.Errorf("lapsed ingest: got %d, want 402", c)
	}
	if c := put("/api/rules", `{"rules":[]}`); c != http.StatusPaymentRequired {
		t.Errorf("lapsed rules PUT: got %d, want 402", c)
	}
	if c := post("/api/tags", `{"namespace":"me","name":"x"}`); c != http.StatusPaymentRequired {
		t.Errorf("lapsed tags POST: got %d, want 402", c)
	}
	if c := post("/api/tokens", `{"name":"dev"}`); c != http.StatusPaymentRequired {
		t.Errorf("lapsed token mint: got %d, want 402", c)
	}
	// Reads stay open.
	rg := httptest.NewRequest("GET", "/api/data?from=0", nil)
	rg.AddCookie(cookie)
	if c := do(mux, rg).Code; c != http.StatusOK {
		t.Errorf("lapsed read /api/data: got %d, want 200", c)
	}

	// Grant a trial → writes now allowed.
	if err := sdb.SetSubscription(uid, store.SubStatusTrialing, "sub_1", time.Now().Add(14*24*time.Hour), -1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if c := ingest(); c != http.StatusOK {
		t.Errorf("trialing ingest: got %d, want 200", c)
	}
	if c := put("/api/rules", `{"rules":[]}`); c != http.StatusOK {
		t.Errorf("trialing rules PUT: got %d, want 200", c)
	}
	if c := post("/api/tokens", `{"name":"dev"}`); c != http.StatusOK {
		t.Errorf("trialing token mint: got %d, want 200", c)
	}
}

// A card-up-front trial arrives as subscription.created with status "trialing";
// glockpeek must grant access (not wait for subscription.active).
func TestBillingWebhook_GrantsDuringTrial(t *testing.T) {
	mux, sdb, wh := billingMux(t)
	u, err := sdb.CreateUser("trial@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"type":"subscription.created","data":{"id":"sub_t","status":"trialing","current_period_end":"2027-01-01T00:00:00Z","customer":{"external_id":"` +
		strconv.FormatUint(uint64(u.ID), 10) + `"}}}`
	if rec := do(mux, signedWebhook(t, wh, payload)); rec.Code != http.StatusOK {
		t.Fatalf("trial created webhook: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := sdb.UserByEmail("trial@x.com")
	if after.SubStatus != store.SubStatusTrialing {
		t.Errorf("sub status = %q, want trialing", after.SubStatus)
	}
	if !after.HasAccess() {
		t.Error("trialing account should have access")
	}
}
