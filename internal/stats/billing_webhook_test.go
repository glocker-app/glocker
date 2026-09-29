package stats

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"glocker/internal/store"
)

// billingMux registers the routes with billing enabled and returns the mux, the
// store, and a signer that produces validly-signed webhook requests.
func billingMux(t *testing.T) (*http.ServeMux, *store.DB, *standardwebhooks.Webhook) {
	t.Helper()
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("billing-test-signing-key"))
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{
		Auth: true,
		Billing: BillingOptions{
			Enabled:         true,
			WebhookSecret:   secret,
			ProductID:       "prod_test",
			PaidDeviceLimit: -1, // unlimited
		},
	})
	wh, err := standardwebhooks.NewWebhook(secret)
	if err != nil {
		t.Fatalf("new webhook: %v", err)
	}
	return mux, sdb, wh
}

// signedWebhook builds a POST /api/billing/webhook request signed with wh.
func signedWebhook(t *testing.T, wh *standardwebhooks.Webhook, payload string) *http.Request {
	t.Helper()
	msgID := "msg_test_1"
	ts := time.Now()
	sig, err := wh.Sign(msgID, ts, []byte(payload))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/billing/webhook", strings.NewReader(payload))
	r.Header.Set(standardwebhooks.HeaderWebhookID, msgID)
	r.Header.Set(standardwebhooks.HeaderWebhookTimestamp, strconv.FormatInt(ts.Unix(), 10))
	r.Header.Set(standardwebhooks.HeaderWebhookSignature, sig)
	return r
}

func subPayload(evtType string, userID uint) string {
	return fmt.Sprintf(`{"type":%q,"data":{"id":"sub_abc","status":"active","current_period_end":"2027-01-01T00:00:00Z","customer":{"external_id":%q}}}`,
		evtType, strconv.FormatUint(uint64(userID), 10))
}

func TestBillingWebhook_GrantAndRevoke(t *testing.T) {
	mux, sdb, wh := billingMux(t)
	u, err := sdb.CreateUser("payer@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}

	// A valid subscription.active grants the unlimited paid cap.
	rec := do(mux, signedWebhook(t, wh, subPayload("subscription.active", u.ID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("active webhook: code %d (%s)", rec.Code, rec.Body.String())
	}
	got, _ := sdb.UserByEmail("payer@x.com")
	if store.EffectiveDeviceLimit(got) != -1 || got.SubStatus != store.SubStatusActive {
		t.Errorf("after active: limit=%d status=%q, want -1/active", store.EffectiveDeviceLimit(got), got.SubStatus)
	}

	// subscription.revoked drops the cap back to the free tier.
	rec = do(mux, signedWebhook(t, wh, subPayload("subscription.revoked", u.ID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoked webhook: code %d", rec.Code)
	}
	got, _ = sdb.UserByEmail("payer@x.com")
	if store.EffectiveDeviceLimit(got) != store.DefaultFreeDevices || got.SubStatus != store.SubStatusExpired {
		t.Errorf("after revoke: limit=%d status=%q, want %d/expired", store.EffectiveDeviceLimit(got), got.SubStatus, store.DefaultFreeDevices)
	}
}

// TestBillingWebhook_PastDueGrace: a failed renewal holds access through a grace
// window (period end + pastDueGrace), then either recovers to active or, if dunning
// fails, revoked → expired.
func TestBillingWebhook_PastDueGrace(t *testing.T) {
	mux, sdb, wh := billingMux(t)
	u, _ := sdb.CreateUser("late@x.com", "longenough1")
	ext := strconv.FormatUint(uint64(u.ID), 10)

	// Renewal failed → past_due: access held, cap unchanged, end pushed out by grace.
	periodEnd := "2027-01-01T00:00:00Z"
	payload := fmt.Sprintf(`{"type":"subscription.past_due","data":{"id":"sub_pd","status":"past_due","current_period_end":%q,"customer":{"external_id":%q}}}`, periodEnd, ext)
	if rec := do(mux, signedWebhook(t, wh, payload)); rec.Code != http.StatusOK {
		t.Fatalf("past_due webhook: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := sdb.UserByEmail("late@x.com")
	if got.SubStatus != store.SubStatusPastDue || !got.HasAccess() || store.EffectiveDeviceLimit(got) != -1 {
		t.Errorf("past_due: status=%q access=%v cap=%d, want past_due/true/-1", got.SubStatus, got.HasAccess(), store.EffectiveDeviceLimit(got))
	}
	base, _ := time.Parse(time.RFC3339, periodEnd)
	if want := base.Add(pastDueGrace); !got.SubCurrentEnd.Equal(want) {
		t.Errorf("grace end = %v, want %v (period end + grace)", got.SubCurrentEnd, want)
	}

	// Recovery: payment goes through → subscription.active → back to active.
	if rec := do(mux, signedWebhook(t, wh, subPayload("subscription.active", u.ID))); rec.Code != http.StatusOK {
		t.Fatalf("recovery webhook: %d", rec.Code)
	}
	if got, _ = sdb.UserByEmail("late@x.com"); got.SubStatus != store.SubStatusActive {
		t.Errorf("after recovery: status %q, want active", got.SubStatus)
	}

	// Dunning ultimately fails → revoked → expired, no access.
	if rec := do(mux, signedWebhook(t, wh, subPayload("subscription.revoked", u.ID))); rec.Code != http.StatusOK {
		t.Fatalf("revoke webhook: %d", rec.Code)
	}
	if got, _ = sdb.UserByEmail("late@x.com"); got.SubStatus != store.SubStatusExpired || got.HasAccess() {
		t.Errorf("after revoke: status %q access %v, want expired/no-access", got.SubStatus, got.HasAccess())
	}

	// past_due past the grace deadline → the period-end bound denies access.
	_ = sdb.SetSubscription(u.ID, store.SubStatusPastDue, "sub_pd", time.Now().Add(-time.Hour), -1, time.Time{})
	if got, _ = sdb.UserByEmail("late@x.com"); got.HasAccess() {
		t.Error("past_due past grace should not have access")
	}
}

func TestBillingWebhook_RejectsBadSignature(t *testing.T) {
	mux, sdb, wh := billingMux(t)
	u, err := sdb.CreateUser("payer2@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	req := signedWebhook(t, wh, subPayload("subscription.active", u.ID))
	// Tamper with the signature — must be rejected, and no access granted.
	req.Header.Set(standardwebhooks.HeaderWebhookSignature, "v1,YWFhYWFhYWFhYWFhYWFhYQ==")
	rec := do(mux, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered signature: code %d, want 401", rec.Code)
	}
	got, _ := sdb.UserByEmail("payer2@x.com")
	if store.EffectiveDeviceLimit(got) != store.DefaultFreeDevices || got.SubStatus != "" {
		t.Errorf("tampered event must not grant access: limit=%d status=%q", store.EffectiveDeviceLimit(got), got.SubStatus)
	}
}

func TestBillingWebhook_UnknownUserAcked(t *testing.T) {
	mux, _, wh := billingMux(t)
	// external_customer_id points at a non-existent account: ack (200) so Polar
	// stops retrying, but obviously nothing to grant.
	rec := do(mux, signedWebhook(t, wh, subPayload("subscription.active", 99999)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown user: code %d, want 200 (ack)", rec.Code)
	}
}

// TestBillingWebhook_CancelStickyAndOrdered guards the two cancel-reliability fixes:
// a scheduled cancel (updated + cancel_at_period_end) is recorded as canceled, and a
// stale/reordered updated (older modified_at, still "active") can't flip it back.
func TestBillingWebhook_CancelStickyAndOrdered(t *testing.T) {
	mux, sdb, wh := billingMux(t)
	u, err := sdb.CreateUser("cx@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	uid := strconv.FormatUint(uint64(u.ID), 10)
	payload := func(evtType, status string, cancel bool, modified string) string {
		return fmt.Sprintf(`{"type":%q,"data":{"id":"sub_abc","status":%q,"cancel_at_period_end":%v,"current_period_end":"2027-01-01T00:00:00Z","ends_at":"2027-01-01T00:00:00Z","modified_at":%q,"customer":{"external_id":%q}}}`,
			evtType, status, cancel, modified, uid)
	}

	if do(mux, signedWebhook(t, wh, payload("subscription.active", "active", false, "2026-01-01T00:00:00Z"))).Code != http.StatusOK {
		t.Fatal("active webhook failed")
	}
	// Cancel scheduled: arrives as updated, status still active, cancel_at_period_end true.
	if do(mux, signedWebhook(t, wh, payload("subscription.updated", "active", true, "2026-01-02T00:00:00Z"))).Code != http.StatusOK {
		t.Fatal("cancel-updated webhook failed")
	}
	if got, _ := sdb.UserByEmail("cx@x.com"); got.SubStatus != store.SubStatusCanceled {
		t.Fatalf("after scheduled cancel: status=%q, want canceled", got.SubStatus)
	}
	// A stale updated (older modified_at, no cancel flag) must be dropped, not revert.
	if do(mux, signedWebhook(t, wh, payload("subscription.updated", "active", false, "2026-01-01T12:00:00Z"))).Code != http.StatusOK {
		t.Fatal("stale webhook should still ack 200")
	}
	if got, _ := sdb.UserByEmail("cx@x.com"); got.SubStatus != store.SubStatusCanceled {
		t.Errorf("stale updated reverted the cancel: status=%q, want canceled", got.SubStatus)
	}
}
