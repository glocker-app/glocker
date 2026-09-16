package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

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
