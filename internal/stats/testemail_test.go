package stats

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"glocker/internal/store"
)

// muxWithMailer registers the routes with a working (fake) mailer, so the
// test-email path can actually "send".
func muxWithMailer(t *testing.T, fm Mailer) (*http.ServeMux, *store.DB) {
	t.Helper()
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, Mailer: fm})
	return mux, sdb
}

func postTestEmail(mux *http.ServeMux, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/test-email", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return do(mux, r)
}

// The happy path: an account with synced buddies gets a test email per buddy.
func TestTestEmailSendsToBuddies(t *testing.T) {
	fm := &fakeMailer{}
	mux, sdb := muxWithMailer(t, fm)
	uid, _, token := account(t, sdb, "alice@x.com")
	if err := sdb.SaveAccountabilityConfig(uid, store.AccountabilityConfig{
		Enabled: true, BuddyEmails: store.EncodeBuddies([]string{"buddy1@x.com", "buddy2@x.com"}),
	}); err != nil {
		t.Fatal(err)
	}

	if rec := postTestEmail(mux, token); rec.Code != http.StatusOK {
		t.Fatalf("test-email: got %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if len(fm.to) != 2 {
		t.Fatalf("sent to %v, want both buddies", fm.to)
	}
}

// No token → the requireToken gate rejects it (no relay for anonymous callers).
func TestTestEmailNeedsToken(t *testing.T) {
	fm := &fakeMailer{}
	mux, _ := muxWithMailer(t, fm)
	if rec := postTestEmail(mux, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	if len(fm.to) != 0 {
		t.Fatal("nothing should have been sent")
	}
}

// A valid account with no synced config (or no buddies) is a clear 422, not a
// silent success.
func TestTestEmailNoBuddies(t *testing.T) {
	fm := &fakeMailer{}
	mux, sdb := muxWithMailer(t, fm)
	_, _, token := account(t, sdb, "alice@x.com")

	// No config synced yet.
	if rec := postTestEmail(mux, token); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no config: got %d, want 422", rec.Code)
	}
	// Config synced but empty recipient list.
	uid, _, token2 := account(t, sdb, "bob@x.com")
	_ = sdb.SaveAccountabilityConfig(uid, store.AccountabilityConfig{Enabled: true, BuddyEmails: store.EncodeBuddies(nil)})
	if rec := postTestEmail(mux, token2); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty buddies: got %d, want 422", rec.Code)
	}
	if len(fm.to) != 0 {
		t.Fatalf("nothing should have been sent, got %v", fm.to)
	}
}

// When mail is unconfigured the endpoint says so (503) rather than reporting a
// phantom success (a disabled mailer's Send is a silent no-op).
func TestTestEmailMailUnconfigured(t *testing.T) {
	mux, sdb := newMux(t) // Options without a Mailer → mail is nil
	uid, _, token := account(t, sdb, "alice@x.com")
	_ = sdb.SaveAccountabilityConfig(uid, store.AccountabilityConfig{
		Enabled: true, BuddyEmails: store.EncodeBuddies([]string{"buddy@x.com"}),
	})
	if rec := postTestEmail(mux, token); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("mail unconfigured: got %d, want 503", rec.Code)
	}
}
