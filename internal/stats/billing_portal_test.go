package stats

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/store"
)

// TestBillingPortal exercises POST /api/billing/portal against a stub Polar
// customer-sessions API: a known customer gets a portal URL, an account that
// never checked out (Polar 404) surfaces as 404, and the wrong method is 405.
func TestBillingPortal(t *testing.T) {
	var gotAuth, gotBody string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/customer-sessions/" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		// Polar returns 404 when no customer matches the external id.
		if !strings.Contains(gotBody, `"customer_external_id":"1"`) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"customer_portal_url":"https://portal.example/sess"}`))
	}))
	defer stub.Close()

	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{
		Auth: true,
		Billing: BillingOptions{
			Enabled: true, APIToken: "polar_at_x", APIBase: stub.URL,
			ProductID: "prod_test",
		},
	})

	// First account → id 1 (the stub's "known" customer).
	u, err := sdb.CreateUser("owner@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := sdb.CreateSession(u.ID)
	post := func(uid string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/billing/portal", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: uid})
		return do(mux, r)
	}

	rec := post(sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("portal: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "https://portal.example/sess") {
		t.Errorf("portal url missing: %s", rec.Body.String())
	}
	if gotAuth != "Bearer polar_at_x" {
		t.Errorf("auth header = %q, want Bearer polar_at_x", gotAuth)
	}

	// An account with no Polar customer (id 2 → stub 404) → 404.
	u2, _ := sdb.CreateUser("two@x.com", "longenough1")
	sess2, _ := sdb.CreateSession(u2.ID)
	if rec := post(sess2); rec.Code != http.StatusNotFound {
		t.Errorf("no-customer portal: got %d, want 404", rec.Code)
	}

	// Wrong method → 405.
	r := httptest.NewRequest(http.MethodGet, "/api/billing/portal", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess})
	if rec := do(mux, r); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET portal: got %d, want 405", rec.Code)
	}
}
