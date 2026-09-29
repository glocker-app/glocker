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
	if c := put("/api/ignored", `{"ignored":[]}`); c != http.StatusPaymentRequired {
		t.Errorf("lapsed ignored PUT: got %d, want 402", c)
	}
	// Config upload is token-authed (the syncer's path), so present the bearer token.
	postToken := func(path, body string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		return do(mux, r).Code
	}
	if c := postToken("/api/config", `{"device":"laptop","config":{}}`); c != http.StatusPaymentRequired {
		t.Errorf("lapsed config POST: got %d, want 402", c)
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

// TestBillingCancel: the native cancel endpoint PATCHes Polar (cancel_at_period_end)
// and records the canceled state from the response, and rejects a non-cancelable
// account.
func TestBillingCancel(t *testing.T) {
	var patched string
	polar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/subscriptions/") {
			patched = strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"cancel_at_period_end":true,"current_period_end":"2027-01-01T00:00:00Z","modified_at":"2026-06-01T00:00:00Z"}`))
			return
		}
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer polar.Close()

	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{
		Auth:    true,
		Billing: BillingOptions{Enabled: true, APIBase: polar.URL, APIToken: "tok", ProductID: "p", PaidDeviceLimit: -1},
	})

	uid, cookie, _ := account(t, sdb, "cancel@x.com")
	if err := sdb.SetSubscription(uid, store.SubStatusActive, "sub_abc", time.Now().Add(720*time.Hour), -1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/billing/cancel", nil)
	r.AddCookie(cookie)
	if rec := do(mux, r); rec.Code != http.StatusOK {
		t.Fatalf("cancel: code %d (%s)", rec.Code, rec.Body.String())
	}
	if patched != "sub_abc" {
		t.Errorf("Polar PATCH hit subscription %q, want sub_abc", patched)
	}
	if u, _ := sdb.UserByID(uid); u.SubStatus != store.SubStatusCanceled {
		t.Errorf("after cancel: status %q, want canceled", u.SubStatus)
	}

	// A free account has nothing to cancel → 409, and Polar is not called.
	_, cookie2, _ := account(t, sdb, "free@x.com")
	r2 := httptest.NewRequest("POST", "/api/billing/cancel", nil)
	r2.AddCookie(cookie2)
	if c := do(mux, r2).Code; c != http.StatusConflict {
		t.Errorf("free-account cancel: got %d, want 409", c)
	}
}

// TestBillingCancelAlreadyCanceling: if Polar says the sub is already scheduled to
// cancel (403 AlreadyCanceledSubscription), the endpoint self-heals — it reads the
// current state and records canceled, instead of 502ing. This is the case that bit
// in prod when the webhook that should have marked it canceled never arrived.
func TestBillingCancelAlreadyCanceling(t *testing.T) {
	polar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"AlreadyCanceledSubscription","detail":"already"}`))
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"trialing","cancel_at_period_end":true,"current_period_end":"2027-01-01T00:00:00Z","modified_at":"2026-06-01T00:00:00Z"}`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer polar.Close()

	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, Billing: BillingOptions{Enabled: true, APIBase: polar.URL, APIToken: "tok", ProductID: "p", PaidDeviceLimit: -1}})
	uid, cookie, _ := account(t, sdb, "already@x.com")
	if err := sdb.SetSubscription(uid, store.SubStatusTrialing, "sub_x", time.Now().Add(240*time.Hour), -1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/billing/cancel", nil)
	r.AddCookie(cookie)
	if rec := do(mux, r); rec.Code != http.StatusOK {
		t.Fatalf("already-canceling: code %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if u, _ := sdb.UserByID(uid); u.SubStatus != store.SubStatusCanceled {
		t.Errorf("status %q, want canceled", u.SubStatus)
	}
}

// TestBillingReconcileOnLoad proves /api/me self-heals a drifted account: the DB
// says trialing, but Polar (the source of truth) shows the subscription is
// scheduled to cancel. Loading the dashboard reconciles it to canceled without a
// webhook.
func TestBillingReconcileOnLoad(t *testing.T) {
	var gets int
	polar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		gets++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"trialing","cancel_at_period_end":true,"current_period_end":"2027-01-01T00:00:00Z","ends_at":"2027-01-01T00:00:00Z","modified_at":"2026-06-01T00:00:00Z"}`))
	}))
	defer polar.Close()

	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, Billing: BillingOptions{Enabled: true, APIBase: polar.URL, APIToken: "tok", ProductID: "p", PaidDeviceLimit: -1}})
	uid, cookie, _ := account(t, sdb, "drift@x.com")
	if err := sdb.SetSubscription(uid, store.SubStatusTrialing, "sub_d", time.Now().Add(240*time.Hour), -1, time.Time{}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(cookie)
	rec := do(mux, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/me code %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"subStatus":"canceled"`) {
		t.Errorf("/api/me body %s, want subStatus canceled", rec.Body.String())
	}
	if u, _ := sdb.UserByID(uid); u.SubStatus != store.SubStatusCanceled {
		t.Errorf("DB status %q, want canceled", u.SubStatus)
	}

	// A second immediate load is throttled — no extra Polar call.
	r2 := httptest.NewRequest("GET", "/api/me", nil)
	r2.AddCookie(cookie)
	do(mux, r2)
	if gets != 1 {
		t.Errorf("Polar GETs = %d, want 1 (second load should be throttled)", gets)
	}
}

// TestMapPolarStatus locks the on-load reconcile's status mapping — the same
// transitions the webhook applies, but derived from a fetched subscription.
func TestMapPolarStatus(t *testing.T) {
	cases := []struct {
		name string
		sub  polarSubscription
		want string
	}{
		{"ended-canceled", polarSubscription{Status: "canceled"}, store.SubStatusExpired},
		{"ended-unpaid", polarSubscription{Status: "unpaid"}, store.SubStatusExpired},
		{"scheduled-cancel", polarSubscription{Status: "active", CancelAtPeriodEnd: true}, store.SubStatusCanceled},
		{"trialing", polarSubscription{Status: "trialing"}, store.SubStatusTrialing},
		{"active", polarSubscription{Status: "active"}, store.SubStatusActive},
		{"past_due-grace", polarSubscription{Status: "past_due"}, ""}, // leave stored state
	}
	for _, c := range cases {
		if got, _, _ := mapPolarStatus(&c.sub); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
