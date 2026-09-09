package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/store"
)

// newMux registers the stats routes on a fresh mux backed by a temp-file sqlite
// store, and returns both.
func newMux(t *testing.T) (*http.ServeMux, *store.DB) {
	t.Helper()
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true}) // most tests exercise the auth-on path
	return mux, sdb
}

// account creates a user and returns its id, a session cookie, and an API token.
func account(t *testing.T, sdb *store.DB, name string) (uint, *http.Cookie, string) {
	t.Helper()
	u, err := sdb.CreateUser(name, "pw-"+name)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, err := sdb.CreateSession(u.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	api, err := sdb.CreateAPIToken(u.ID, "test")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return u.ID, &http.Cookie{Name: sessionCookie, Value: tok}, api
}

func do(mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ── auth gating ─────────────────────────────────────────
func TestAuthRequired(t *testing.T) {
	mux, _ := newMux(t)
	// No session cookie -> 401 on gated routes.
	for _, p := range []string{"/api/data", "/api/rules", "/api/ignored", "/api/me", "/api/health"} {
		if rec := do(mux, httptest.NewRequest("GET", p, nil)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without session: got %d, want 401", p, rec.Code)
		}
	}
	// No bearer token -> 401 on ingest.
	if rec := do(mux, httptest.NewRequest("POST", "/api/ingest", strings.NewReader("{}"))); rec.Code != http.StatusUnauthorized {
		t.Errorf("ingest without token: got %d, want 401", rec.Code)
	}
	// Static assets are public.
	if rec := do(mux, httptest.NewRequest("GET", "/", nil)); rec.Code != http.StatusOK {
		t.Errorf("index should be public: got %d", rec.Code)
	}
}

// TestAuthDisabled is the self-hosted default: no login, no token — every route
// works and resolves to the single implicit account.
func TestAuthDisabled(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	du, err := sdb.EnsureDefaultUser()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: false, DefaultUserID: du.ID})

	// Ingest with no token works and attributes to the default account.
	ing := httptest.NewRequest("POST", "/api/ingest",
		strings.NewReader(`{"violations":[{"ts":1,"keyword":"k","url":"u","domain":"d"}]}`))
	if rec := do(mux, ing); rec.Code != http.StatusOK {
		t.Fatalf("ingest without token (auth off): %d %s", rec.Code, rec.Body.String())
	}

	// Data with no cookie works.
	rec := do(mux, httptest.NewRequest("GET", "/api/data", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("data without cookie (auth off): %d", rec.Code)
	}
	var d dataResponse
	json.Unmarshal(rec.Body.Bytes(), &d)
	if len(d.Violations) != 1 {
		t.Errorf("want 1 violation for the implicit account, got %+v", d.Violations)
	}

	// /api/me reports auth:false so the frontend hides sign-out.
	me := do(mux, httptest.NewRequest("GET", "/api/me", nil))
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"auth":false`) {
		t.Errorf("/api/me (auth off): %d %s", me.Code, me.Body.String())
	}
}

// fakeMailer records what would have been sent instead of hitting Mailgun.
type fakeMailer struct {
	to, text []string
}

func (f *fakeMailer) Enabled() bool { return true }
func (f *fakeMailer) Send(_ context.Context, to, _, text, _ string) error {
	f.to = append(f.to, to)
	f.text = append(f.text, text)
	return nil
}

func TestRegisterVerifyLogin(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	fm := &fakeMailer{}
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, Mailer: fm, AppURL: "https://glockerapp.com"})

	// Each POST comes from a distinct client IP so the /api/register rate limiter
	// (covered by its own test) doesn't conflate these independent requests.
	reqN := 0
	post := func(path, body string) *httptest.ResponseRecorder {
		reqN++
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", reqN))
		return do(mux, req)
	}

	// Register → 200, and an email with a verify link was "sent".
	if rec := post("/api/register", `{"email":"New@User.com","password":"longenough1"}`); rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	if len(fm.to) != 1 || fm.to[0] != "new@user.com" {
		t.Fatalf("verification email recipients = %v (want [new@user.com], normalized)", fm.to)
	}
	if !strings.Contains(fm.text[0], "https://glockerapp.com/verify?token=") {
		t.Errorf("email missing verify link: %q", fm.text[0])
	}

	// Login before verifying → 403.
	if rec := post("/api/login", `{"email":"new@user.com","password":"longenough1"}`); rec.Code != http.StatusForbidden {
		t.Errorf("login before verify: got %d, want 403", rec.Code)
	}

	// Pull the token from the store and hit the verify link.
	var vt store.VerificationToken
	if err := sdb.First(&vt).Error; err != nil {
		t.Fatalf("no verification token stored: %v", err)
	}
	if rec := do(mux, httptest.NewRequest("GET", "/verify?token="+vt.Token, nil)); rec.Code != http.StatusOK {
		t.Fatalf("verify: %d", rec.Code)
	}

	// Login now succeeds; the token was consumed.
	if rec := post("/api/login", `{"email":"new@user.com","password":"longenough1"}`); rec.Code != http.StatusOK {
		t.Errorf("login after verify: got %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(mux, httptest.NewRequest("GET", "/verify?token="+vt.Token, nil)); rec.Code != http.StatusBadRequest {
		t.Errorf("re-using a consumed token: got %d, want 400", rec.Code)
	}

	// Duplicate email → 409; weak inputs → 400.
	if rec := post("/api/register", `{"email":"new@user.com","password":"longenough1"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate register: got %d, want 409", rec.Code)
	}
	if rec := post("/api/register", `{"email":"bad","password":"longenough1"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad email: got %d, want 400", rec.Code)
	}
	if rec := post("/api/register", `{"email":"ok@x.com","password":"short"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("short password: got %d, want 400", rec.Code)
	}
}

func TestRegisterUnavailableWithoutMailer(t *testing.T) {
	mux, _ := newMux(t) // Auth:true but no Mailer
	rec := do(mux, httptest.NewRequest("POST", "/api/register", strings.NewReader(`{"email":"a@b.com","password":"longenough1"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("register without mailer: got %d, want 503", rec.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	mux, sdb := newMux(t)
	if _, err := sdb.CreateUser("alice@example.com", "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}

	// Bad password -> 401.
	bad := do(mux, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"email":"alice@example.com","password":"nope"}`)))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: got %d", bad.Code)
	}

	// Good password -> 200 + session cookie.
	ok := do(mux, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"email":"alice@example.com","password":"hunter2hunter2"}`)))
	if ok.Code != http.StatusOK {
		t.Fatalf("good login: got %d %s", ok.Code, ok.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("login did not set a session cookie")
	}

	// The cookie authenticates /api/me.
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(cookie)
	if rec := do(mux, req); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "alice") {
		t.Errorf("/api/me with cookie: %d %s", rec.Code, rec.Body.String())
	}
}

func TestServesIndexAndAssets(t *testing.T) {
	mux, _ := newMux(t)
	for _, p := range []string{"/stats", "/stats/"} {
		rec := do(mux, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/" {
			t.Errorf("%s redirect: code=%d loc=%q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, tc := range []struct{ path, needle string }{
		{"/", "glock"},
		{"/app.js", "renderUsage"},
	} {
		rec := do(mux, httptest.NewRequest("GET", tc.path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.needle) {
			t.Errorf("%s: got %d, missing %q", tc.path, rec.Code, tc.needle)
		}
	}
}

// TestIngestThenData drives the full authed path: POST a batch with a bearer
// token, then GET /api/data with a session cookie.
func TestIngestThenData(t *testing.T) {
	mux, sdb := newMux(t)
	_, cookie, api := account(t, sdb, "alice")

	payload := `{
      "violations":[{"ts":1000,"type":"url-keyword","keyword":"porn","url":"https://x/?q=porn","domain":"x"}],
      "lifecycle":[{"ts":100000,"type":"uninstall","reason":"temp"},{"ts":100000000,"type":"install"}],
      "usage":[{"ts":500,"idleMs":5,"activeClass":"kitty","windowCount":1}],
      "heartbeat":[{"ts":600,"alive":true}]
    }`
	ingest := func() {
		req := httptest.NewRequest("POST", "/api/ingest", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+api)
		if rec := do(mux, req); rec.Code != http.StatusOK {
			t.Fatalf("ingest: %d %s", rec.Code, rec.Body.String())
		}
	}
	ingest()
	ingest() // idempotent

	req := httptest.NewRequest("GET", "/api/data", nil)
	req.AddCookie(cookie)
	rec := do(mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("data: %d", rec.Code)
	}
	var d dataResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Violations) != 1 || d.Violations[0].Keyword != "porn" {
		t.Errorf("violations = %+v", d.Violations)
	}
	if len(d.Usage) != 1 || d.Usage[0].Active == nil || d.Usage[0].Active.Class != "kitty" {
		t.Errorf("usage = %+v", d.Usage)
	}
	if len(d.Unmanaged) != 1 || d.Unmanaged[0].Reason != "temp" {
		t.Errorf("unmanaged = %+v", d.Unmanaged)
	}
	if !strings.Contains(rec.Body.String(), `"unblocks":[]`) {
		t.Errorf("empty unblocks should be [] not null")
	}
}

// TestTenantSeparationOverHTTP proves account B's token can't surface account A's
// data through the API.
func TestTenantSeparationOverHTTP(t *testing.T) {
	mux, sdb := newMux(t)
	_, _, aliceTok := account(t, sdb, "alice")
	_, bobCookie, _ := account(t, sdb, "bob")

	req := httptest.NewRequest("POST", "/api/ingest",
		strings.NewReader(`{"violations":[{"ts":1,"keyword":"k","url":"u","domain":"d"}]}`))
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	if rec := do(mux, req); rec.Code != http.StatusOK {
		t.Fatalf("alice ingest: %d", rec.Code)
	}

	// Bob reads his own data — must be empty.
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.AddCookie(bobCookie)
	rec := do(mux, req)
	var d dataResponse
	json.Unmarshal(rec.Body.Bytes(), &d)
	if len(d.Violations) != 0 {
		t.Errorf("bob should not see alice's data, got %+v", d.Violations)
	}
}

// The dashboard data endpoint is content-blind: it returns every violation,
// even ignored ones. Hiding the false-positives happens in the browser from the
// separately-served /api/ignored overlay — the prerequisite for serving E2E
// ciphertext blobs here later (the server can't read violation content).
func TestDataUnfilteredIgnoreOverlayStaysServed(t *testing.T) {
	mux, sdb := newMux(t)
	_, cookie, api := account(t, sdb, "alice")

	ing := httptest.NewRequest("POST", "/api/ingest",
		strings.NewReader(`{"violations":[{"ts":1000,"recordId":"rid-1","keyword":"porn","url":"https://x/?q=porn","domain":"x"}]}`))
	ing.Header.Set("Authorization", "Bearer "+api)
	do(mux, ing)

	// The overlay carries only the opaque recordId (content-blind) — no keyword/url.
	put := httptest.NewRequest("PUT", "/api/ignored",
		strings.NewReader(`{"ignored":[{"recordId":"rid-1","ts":1000}]}`))
	put.AddCookie(cookie)
	if rec := do(mux, put); rec.Code != http.StatusOK {
		t.Fatalf("put ignored: %d %s", rec.Code, rec.Body.String())
	}

	// /api/data returns the violation unfiltered (server no longer hides it).
	get := httptest.NewRequest("GET", "/api/data", nil)
	get.AddCookie(cookie)
	var d dataResponse
	json.Unmarshal(do(mux, get).Body.Bytes(), &d)
	if len(d.Violations) != 1 {
		t.Fatalf("server should return the violation unfiltered, got %+v", d.Violations)
	}

	// The overlay is still stored and readable, so the client can hide it.
	gi := httptest.NewRequest("GET", "/api/ignored", nil)
	gi.AddCookie(cookie)
	var ig struct {
		Ignored []struct {
			RecordID string `json:"recordId"`
			TS       int64  `json:"ts"`
		} `json:"ignored"`
	}
	json.Unmarshal(do(mux, gi).Body.Bytes(), &ig)
	if len(ig.Ignored) != 1 || ig.Ignored[0].RecordID != "rid-1" {
		t.Errorf("ignore overlay should persist for the client, got %+v", ig.Ignored)
	}
}

// TestSyncStatus checks the /api/sync panel data: null before any ingest, then
// last-batch counts + totals after.
func TestSyncStatus(t *testing.T) {
	mux, sdb := newMux(t)
	_, cookie, api := account(t, sdb, "alice")

	// Before any ingest: lastSyncAt is null.
	req := httptest.NewRequest("GET", "/api/sync", nil)
	req.AddCookie(cookie)
	rec := do(mux, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"lastSyncAt":null`) {
		t.Fatalf("pre-ingest sync = %d %s", rec.Code, rec.Body.String())
	}

	ing := httptest.NewRequest("POST", "/api/ingest",
		strings.NewReader(`{"violations":[{"ts":1,"keyword":"k","url":"u"}],"heartbeat":[{"ts":2,"alive":true}]}`))
	ing.Header.Set("Authorization", "Bearer "+api)
	do(mux, ing)

	req = httptest.NewRequest("GET", "/api/sync", nil)
	req.AddCookie(cookie)
	var s struct {
		LastSyncAt *int64         `json:"lastSyncAt"`
		Last       map[string]int `json:"last"`
		Totals     map[string]int `json:"totals"`
	}
	json.Unmarshal(do(mux, req).Body.Bytes(), &s)
	if s.LastSyncAt == nil || *s.LastSyncAt == 0 {
		t.Error("lastSyncAt should be set after ingest")
	}
	if s.Last["violations"] != 1 || s.Last["heartbeat"] != 1 {
		t.Errorf("last batch counts = %+v", s.Last)
	}
	if s.Totals["violations"] != 1 {
		t.Errorf("totals = %+v", s.Totals)
	}
}

func TestRulesGetPut(t *testing.T) {
	mux, sdb := newMux(t)
	_, cookie, _ := account(t, sdb, "alice")

	get := httptest.NewRequest("GET", "/api/rules", nil)
	get.AddCookie(cookie)
	if rec := do(mux, get); !strings.Contains(rec.Body.String(), `"rules":[]`) {
		t.Fatalf("empty GET = %s", rec.Body.String())
	}

	body := `{"rules":[{"program":"^Emacs$","title":"","tag":"Activity:work"},{"nope":true}],"colors":{"Activity:work":"#3aa0ff","x":"bad"}}`
	put := httptest.NewRequest("PUT", "/api/rules", strings.NewReader(body))
	put.AddCookie(cookie)
	if rec := do(mux, put); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}

	get2 := httptest.NewRequest("GET", "/api/rules", nil)
	get2.AddCookie(cookie)
	var cfg rulesConfig
	json.Unmarshal(do(mux, get2).Body.Bytes(), &cfg)
	if len(cfg.Rules) != 1 || cfg.Rules[0].Tag != "Activity:work" || cfg.Colors["Activity:work"] != "#3aa0ff" {
		t.Fatalf("persisted rules = %+v", cfg)
	}
	if len(cfg.Colors) != 1 {
		t.Errorf("bad colour should have been dropped: %+v", cfg.Colors)
	}
}

// TestSanitizeRulesAndColors covers the pure sanitizers.
func TestSanitizeRulesAndColors(t *testing.T) {
	rules := sanitizeRules([]Rule{
		{Program: " ^Emacs$ ", Title: "glocker", Tag: " Project:glocker "},
		{Tag: ""},
	})
	if len(rules) != 1 || rules[0].Program != "^Emacs$" || rules[0].Tag != "Project:glocker" {
		t.Fatalf("sanitizeRules = %+v", rules)
	}
	colors := sanitizeColors(map[string]string{"Activity:work": "#F0A020", "Bad:short": "#fff"})
	if len(colors) != 1 || colors["Activity:work"] != "#f0a020" {
		t.Fatalf("sanitizeColors = %+v", colors)
	}
}
