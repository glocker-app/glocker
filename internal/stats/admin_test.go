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

func TestAdminClearSubscription(t *testing.T) {
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
	Register(mux, sdb, Options{Auth: true, AdminEmail: "admin@x.com"})
	req := func(method, target string, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return do(mux, r)
	}

	// Give bob an active paid subscription (unlimited devices).
	if err := sdb.SetSubscription(bob.ID, store.SubStatusActive, "sub_1", time.Now().Add(720*time.Hour), -1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	bobID := strconv.FormatUint(uint64(bob.ID), 10)

	// A non-admin can't clear.
	if rec := req("PATCH", "/api/admin/users?id="+bobID, bobTok); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin clear: got %d, want 403", rec.Code)
	}
	// The admin clears bob's subscription → free tier.
	if rec := req("PATCH", "/api/admin/users?id="+bobID, adminTok); rec.Code != http.StatusOK {
		t.Fatalf("clear sub: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := sdb.UserByEmail("bob@x.com")
	if after.SubStatus != "" || after.SubID != "" {
		t.Errorf("sub not cleared: status=%q id=%q", after.SubStatus, after.SubID)
	}
	if store.EffectiveDeviceLimit(after) != store.DefaultFreeDevices {
		t.Errorf("device limit not reset: %d", store.EffectiveDeviceLimit(after))
	}
	// Unknown id → 404.
	if rec := req("PATCH", "/api/admin/users?id=999999", adminTok); rec.Code != http.StatusNotFound {
		t.Errorf("clear unknown: got %d, want 404", rec.Code)
	}
}

// TestAdminCancelSubscription: the admin revokes a user's LIVE subscription at
// Polar (immediate) and glockpeek records it expired — the in-app replacement for
// canceling from the Polar merchant dashboard.
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
	// Admin revokes at Polar → glockpeek records expired, cap back to free.
	if rec := req("/api/admin/cancel-subscription?id="+bobID, adminTok); rec.Code != http.StatusOK {
		t.Fatalf("admin cancel: %d %s", rec.Code, rec.Body.String())
	}
	if revoked != "sub_live" {
		t.Errorf("Polar revoke hit %q, want sub_live", revoked)
	}
	after, _ := sdb.UserByEmail("bob@x.com")
	if after.SubStatus != store.SubStatusExpired {
		t.Errorf("status %q, want expired", after.SubStatus)
	}
	if store.EffectiveDeviceLimit(after) != store.DefaultFreeDevices {
		t.Errorf("device limit not reset: %d", store.EffectiveDeviceLimit(after))
	}
	// A free account (no live sub) → 409.
	carol, _ := sdb.CreateUser("carol@x.com", "longenough1")
	if rec := req("/api/admin/cancel-subscription?id="+strconv.FormatUint(uint64(carol.ID), 10), adminTok); rec.Code != http.StatusConflict {
		t.Errorf("cancel free account: got %d, want 409", rec.Code)
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
