package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"glocker/internal/store"
)

func TestAdminUsers(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })

	admin, err := sdb.CreateUser("admin@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := sdb.CreateUser("bob@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _ := sdb.CreateSession(admin.ID)
	bobTok, _ := sdb.CreateSession(bob.ID)
	adminCookie := &http.Cookie{Name: sessionCookie, Value: adminTok}
	bobCookie := &http.Cookie{Name: sessionCookie, Value: bobTok}

	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, AdminEmail: "admin@x.com"})

	req := func(method, target string, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.AddCookie(c)
		return do(mux, r)
	}

	// A non-admin session is forbidden from the admin endpoint.
	if rec := req("GET", "/api/admin/users", bobCookie); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin GET: got %d, want 403", rec.Code)
	}
	// No session at all → 401 (requireUser).
	if rec := do(mux, httptest.NewRequest("GET", "/api/admin/users", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: got %d, want 401", rec.Code)
	}
	// Give bob some synced records so the stats are non-trivial.
	if err := sdb.IngestViolations(bob.ID, 0, []store.Violation{
		{TS: 1, RecordID: "r1", Keyword: "k", URL: "http://x"}, {TS: 2, RecordID: "r2", Keyword: "k", URL: "http://y"},
	}); err != nil {
		t.Fatal(err)
	}
	// The admin can list users, with per-account records + totals.
	rec := req("GET", "/api/admin/users", adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Users []struct {
			ID      uint  `json:"id"`
			Records int64 `json:"records"`
		} `json:"users"`
		Totals struct {
			Accounts int   `json:"accounts"`
			Records  int64 `json:"records"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Totals.Accounts != 2 || list.Totals.Records != 2 {
		t.Errorf("totals = %+v, want accounts=2 records=2", list.Totals)
	}
	for _, u := range list.Users {
		if u.ID == bob.ID && u.Records != 2 {
			t.Errorf("bob records = %d, want 2", u.Records)
		}
	}

	// The admin can't delete themselves.
	if rec := req("DELETE", "/api/admin/users?id="+strconv.FormatUint(uint64(admin.ID), 10), adminCookie); rec.Code != http.StatusBadRequest {
		t.Errorf("delete self: got %d, want 400", rec.Code)
	}
	// The admin deletes bob; bob is gone.
	if rec := req("DELETE", "/api/admin/users?id="+strconv.FormatUint(uint64(bob.ID), 10), adminCookie); rec.Code != http.StatusOK {
		t.Fatalf("delete bob: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := sdb.UserByEmail("bob@x.com"); err == nil {
		t.Error("bob should be deleted")
	}
}

// TestAdminCancelSubscription: one admin action revokes a user's live subscription
// at Polar and returns the account to the free tier — the in-app replacement for
// the Polar merchant dashboard, and also the drift-repair path.
func TestAdminCancelSubscription(t *testing.T) {
	var revoked string
	polar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			revoked = strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"canceled","current_period_end":"2027-01-01T00:00:00Z","ended_at":"2026-06-01T00:00:00Z","modified_at":"2026-06-01T00:00:00Z"}`))
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
	admin, _ := sdb.CreateUser("admin@x.com", "longenough1")
	bob, _ := sdb.CreateUser("bob@x.com", "longenough1")
	adminTok, _ := sdb.CreateSession(admin.ID)
	bobTok, _ := sdb.CreateSession(bob.ID)

	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, AdminEmail: "admin@x.com",
		Billing: BillingOptions{Enabled: true, APIBase: polar.URL, APIToken: "tok", ProductID: "p", PaidDeviceLimit: -1}})
	req := func(target, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", target, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return do(mux, r)
	}

	if err := sdb.SetSubscription(bob.ID, store.SubStatusActive, "sub_live", time.Now().Add(720*time.Hour), -1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	bobID := strconv.FormatUint(uint64(bob.ID), 10)

	// Non-admin is forbidden.
	if rec := req("/api/admin/cancel-subscription?id="+bobID, bobTok); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin cancel: got %d, want 403", rec.Code)
	}
	// Admin revokes at Polar → glockpeek resets to the free tier (blank, free cap).
	if rec := req("/api/admin/cancel-subscription?id="+bobID, adminTok); rec.Code != http.StatusOK {
		t.Fatalf("admin cancel: %d %s", rec.Code, rec.Body.String())
	}
	if revoked != "sub_live" {
		t.Errorf("Polar revoke hit %q, want sub_live", revoked)
	}
	after, _ := sdb.UserByEmail("bob@x.com")
	if after.SubStatus != "" || after.SubID != "" {
		t.Errorf("not reset to free: status=%q id=%q", after.SubStatus, after.SubID)
	}
	if store.EffectiveDeviceLimit(after) != store.DefaultFreeDevices {
		t.Errorf("device limit not reset: %d", store.EffectiveDeviceLimit(after))
	}
	// A free account (no live sub) → idempotent no-op reset, no Polar call.
	revoked = ""
	carol, _ := sdb.CreateUser("carol@x.com", "longenough1")
	if rec := req("/api/admin/cancel-subscription?id="+strconv.FormatUint(uint64(carol.ID), 10), adminTok); rec.Code != http.StatusOK {
		t.Errorf("cancel free account: got %d, want 200", rec.Code)
	}
	if revoked != "" {
		t.Errorf("free account should not hit Polar, but revoked %q", revoked)
	}
	// Unknown id → 404.
	if rec := req("/api/admin/cancel-subscription?id=999999", adminTok); rec.Code != http.StatusNotFound {
		t.Errorf("cancel unknown: got %d, want 404", rec.Code)
	}
}

// TestAdminGrantSubscription: the admin comps a free account the paid tier without a
// payment — active status, paid cap, empty provider id (so the reconcile skips it) —
// and refuses to comp an account that already has a provider subscription.
func TestAdminGrantSubscription(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	admin, _ := sdb.CreateUser("admin@x.com", "longenough1")
	bob, _ := sdb.CreateUser("bob@x.com", "longenough1")
	adminTok, _ := sdb.CreateSession(admin.ID)
	bobTok, _ := sdb.CreateSession(bob.ID)

	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, AdminEmail: "admin@x.com",
		Billing: BillingOptions{Enabled: true, APIBase: "http://unused", APIToken: "tok", ProductID: "p", PaidDeviceLimit: 7}})
	req := func(target, tok, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", target, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return do(mux, r)
	}
	bobID := strconv.FormatUint(uint64(bob.ID), 10)

	// Non-admin is forbidden.
	if rec := req("/api/admin/grant-subscription?id="+bobID, bobTok, ""); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin grant: got %d, want 403", rec.Code)
	}
	// Admin comps bob → active, paid cap, empty sub id, permanent (zero end).
	if rec := req("/api/admin/grant-subscription?id="+bobID, adminTok, ""); rec.Code != http.StatusOK {
		t.Fatalf("admin grant: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := sdb.UserByEmail("bob@x.com")
	if after.SubStatus != store.SubStatusActive || after.SubID != "" {
		t.Errorf("comp not written: status=%q id=%q", after.SubStatus, after.SubID)
	}
	if !after.SubCurrentEnd.IsZero() {
		t.Errorf("permanent comp should have zero end, got %v", after.SubCurrentEnd)
	}
	if store.EffectiveDeviceLimit(after) != 7 {
		t.Errorf("paid device cap not applied: got %d, want 7", store.EffectiveDeviceLimit(after))
	}
	if !after.HasAccess() {
		t.Error("comped account should have access")
	}
	// Re-granting an account that now has a subscription is refused (409) — but this
	// one has an empty sub id, so it's still comp-able. Give carol a real provider sub.
	carol, _ := sdb.CreateUser("carol@x.com", "longenough1")
	if err := sdb.SetSubscription(carol.ID, store.SubStatusActive, "sub_live", time.Now().Add(720*time.Hour), 7, time.Time{}); err != nil {
		t.Fatal(err)
	}
	carolID := strconv.FormatUint(uint64(carol.ID), 10)
	if rec := req("/api/admin/grant-subscription?id="+carolID, adminTok, ""); rec.Code != http.StatusConflict {
		t.Errorf("grant over a provider sub: got %d, want 409", rec.Code)
	}
	// Time-boxed comp: an expiry in the past means no access.
	dave, _ := sdb.CreateUser("dave@x.com", "longenough1")
	daveID := strconv.FormatUint(uint64(dave.ID), 10)
	past := time.Now().Add(-time.Hour).UnixMilli()
	if rec := req("/api/admin/grant-subscription?id="+daveID, adminTok,
		`{"untilMillis":`+strconv.FormatInt(past, 10)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("time-boxed grant: %d %s", rec.Code, rec.Body.String())
	}
	afterDave, _ := sdb.UserByEmail("dave@x.com")
	if afterDave.HasAccess() {
		t.Error("comp expired in the past should not grant access")
	}
	// Unknown id → 404.
	if rec := req("/api/admin/grant-subscription?id=999999", adminTok, ""); rec.Code != http.StatusNotFound {
		t.Errorf("grant unknown: got %d, want 404", rec.Code)
	}
}

// TestAdminGrantSubscriptionBillingDisabled: with billing off, the grant is refused —
// every account already has access, so a comp is meaningless.
func TestAdminGrantSubscriptionBillingDisabled(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	admin, _ := sdb.CreateUser("admin@x.com", "longenough1")
	bob, _ := sdb.CreateUser("bob@x.com", "longenough1")
	adminTok, _ := sdb.CreateSession(admin.ID)

	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, AdminEmail: "admin@x.com"}) // Billing not enabled
	r := httptest.NewRequest("POST", "/api/admin/grant-subscription?id="+strconv.FormatUint(uint64(bob.ID), 10), nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: adminTok})
	if rec := do(mux, r); rec.Code != http.StatusBadRequest {
		t.Errorf("grant with billing disabled: got %d, want 400", rec.Code)
	}
}

func TestAdminDisabledWithoutAdminEmail(t *testing.T) {
	mux, sdb := newMux(t) // Options with no AdminEmail
	u, _ := sdb.CreateUser("someone@x.com", "longenough1")
	tok, _ := sdb.CreateSession(u.ID)
	r := httptest.NewRequest("GET", "/api/admin/users", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	// No admin configured → even a valid user is not admin → 403.
	if rec := do(mux, r); rec.Code != http.StatusForbidden {
		t.Errorf("admin endpoint with no admin configured: got %d, want 403", rec.Code)
	}
}
