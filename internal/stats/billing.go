package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"glocker/internal/store"
	"gorm.io/gorm"
)

// Billing: the hosted paid tier via Polar (a Merchant-of-Record). Two endpoints:
//
//	POST /api/billing/checkout  (session) — creates a Polar checkout session for
//	    the logged-in account and returns the hosted checkout URL. We attach the
//	    account's numeric id as external_customer_id (never the email), so the
//	    webhook can map the resulting subscription back without us holding payer
//	    PII — Polar, as MoR, holds the name/email/card and handles tax.
//	POST /api/billing/webhook   (public) — Polar posts subscription lifecycle
//	    events here. Verified with Standard Webhooks (HMAC), then mapped onto
//	    store.SetSubscription, which raises/lowers DeviceLimit (the enforcement
//	    lever). Grant on active/uncanceled, revoke on revoked, mark on canceled.
//
// Enforcement itself lives in the token endpoint (over the cap -> 402); billing
// only moves the cap.

// BillingOptions is the resolved billing configuration handed to Register.
type BillingOptions struct {
	Enabled         bool
	APIToken        string // Polar org access token (server-side)
	WebhookSecret   string // Standard Webhooks secret (whsec_...)
	ProductID       string // Polar subscription product id
	APIBase         string // resolved Polar API base (production if unset)
	PaidDeviceLimit int    // device cap while active (-1 unlimited)
	SuccessURL      string // post-checkout redirect (falls back to AppURL upstream)
}

var (
	billingEnabled bool
	billing        BillingOptions
	// billingWH verifies incoming webhook signatures. Nil when billing is off or
	// the secret was invalid — the webhook handler then fails closed.
	billingWH   *standardwebhooks.Webhook
	billingHTTP = &http.Client{Timeout: 15 * time.Second}
)

// accountHasAccess reports whether the account may write — ingest new records and
// edit dashboard features (rules, tags, ignore list, devices). Access holds while
// the account is trialing, active, or canceling (until period end); an expired or
// never-subscribed account is read-only. When billing is disabled (local mode, or
// a self-hosted instance with no Merchant-of-Record), access is always granted so
// those deployments are never gated. Reads are never gated on this.
func accountHasAccess(u *store.User) bool {
	return !billingEnabled || (u != nil && u.HasAccess())
}

// errReadOnly is the message returned when a lapsed account attempts a write.
const errReadOnly = "subscription required — this account is read-only (trial ended or subscription lapsed). Subscribe to resume."

// initBilling wires the billing configuration into the package. A bad webhook
// secret is logged and leaves billingWH nil so the webhook endpoint refuses
// everything (fail closed) rather than accepting unverified events.
func initBilling(o BillingOptions) {
	billing = o
	billingEnabled = o.Enabled
	if !o.Enabled {
		return
	}
	if o.WebhookSecret == "" {
		log.Printf("billing: enabled but no webhook_secret set; webhook endpoint will reject all events")
		return
	}
	wh, err := standardwebhooks.NewWebhook(o.WebhookSecret)
	if err != nil {
		log.Printf("billing: invalid webhook_secret, webhook endpoint disabled: %v", err)
		return
	}
	billingWH = wh
}

// handleBillingCheckout creates a Polar checkout session for the logged-in
// account and returns {"url": ...} for the browser to redirect to.
func handleBillingCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u := userFrom(r)
	url, err := createPolarCheckout(r.Context(), u.ID, u.Email)
	if err != nil {
		log.Printf("billing: checkout create failed for user %d: %v", u.ID, err)
		http.Error(w, "could not start checkout", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"url": url})
}

// createPolarCheckout POSTs to Polar's checkout API (raw HTTP — Polar's Go SDK
// is archived) and returns the hosted checkout URL.
func createPolarCheckout(ctx context.Context, userID uint, email string) (string, error) {
	ext := strconv.FormatUint(uint64(userID), 10)
	reqBody := map[string]any{
		"products":             []string{billing.ProductID},
		"external_customer_id": ext,
		"metadata":             map[string]string{"user_id": ext},
	}
	// Prefill the payer's email on Polar's page so the account holder doesn't retype
	// it. Polar (the MoR) receives it either way; this only saves the keystrokes, and
	// the numeric external_customer_id stays the mapping key — we still never key on email.
	if email != "" {
		reqBody["customer_email"] = email
	}
	// Mark the return so the dashboard can poll for the just-granted subscription
	// (the webhook can land a beat after the redirect) instead of needing a manual reload.
	if billing.SuccessURL != "" {
		sep := "?"
		if strings.Contains(billing.SuccessURL, "?") {
			sep = "&"
		}
		reqBody["success_url"] = billing.SuccessURL + sep + "checkout=success"
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, billing.APIBase+"/v1/checkouts/", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+billing.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := billingHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("polar checkout status %d: %s", resp.StatusCode, string(rb))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("polar checkout returned no url")
	}
	return out.URL, nil
}

// handleBillingPortal creates a Polar customer-portal session for the logged-in
// account and returns {"url": ...} so the browser can redirect there to manage or
// cancel the subscription (update card, download invoices). Polar, as MoR, owns
// that surface — we never handle payment details.
func handleBillingPortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u := userFrom(r)
	url, err := createPolarPortal(r.Context(), u.ID)
	if errors.Is(err, errNoCustomer) {
		http.Error(w, "no billing account yet", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("billing: portal create failed for user %d: %v", u.ID, err)
		http.Error(w, "could not open billing portal", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"url": url})
}

// createPolarPortal POSTs to Polar's customer-sessions API keyed by the account's
// external id (the numeric user id we set at checkout) and returns the hosted
// customer-portal URL. No Polar customer for the account (it never checked out —
// e.g. a comped subscription granted by an admin) surfaces as errNoCustomer so
// the caller can 404.
func createPolarPortal(ctx context.Context, userID uint) (string, error) {
	ext := strconv.FormatUint(uint64(userID), 10)
	// return_url is where Polar's hosted portal sends the user "back" to, so
	// managing/cancelling there doesn't dead-end on Polar's page. Prefer the
	// dashboard's Devices view; fall back to the checkout success URL.
	reqBody := map[string]any{"customer_external_id": ext}
	if base := strings.TrimRight(appURL, "/"); base != "" {
		reqBody["return_url"] = base + "/devices"
	} else if billing.SuccessURL != "" {
		reqBody["return_url"] = billing.SuccessURL
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, billing.APIBase+"/v1/customer-sessions/", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+billing.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := billingHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// No Polar customer for this account: Polar answers 404, or 422 with a
	// "Customer does not exist" validation error on external_customer_id (what a
	// comped/never-checked-out account hits). Both mean the same — no portal.
	if resp.StatusCode == http.StatusNotFound ||
		(resp.StatusCode == http.StatusUnprocessableEntity && bytes.Contains(rb, []byte("Customer does not exist"))) {
		return "", errNoCustomer
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("polar customer-session status %d: %s", resp.StatusCode, string(rb))
	}
	var out struct {
		CustomerPortalURL string `json:"customer_portal_url"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", err
	}
	if out.CustomerPortalURL == "" {
		return "", fmt.Errorf("polar customer-session returned no portal url")
	}
	return out.CustomerPortalURL, nil
}

var errNoCustomer = errors.New("billing: account has no Polar customer")

// handleBillingCancel cancels the logged-in account's subscription at the end of
// the current period (access kept until then) via the Polar API directly — no
// hosted-portal round trip. It records the canceled state immediately from the API
// response so the dashboard updates even if the webhook is delayed; the
// subscription.updated webhook reconciles idempotently (same modified_at).
func handleBillingCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u := userFrom(r)
	if u.SubID == "" || (u.SubStatus != store.SubStatusTrialing && u.SubStatus != store.SubStatusActive) {
		http.Error(w, "no cancelable subscription", http.StatusConflict)
		return
	}
	sub, err := cancelPolarSubscription(r.Context(), u.SubID)
	if err != nil {
		log.Printf("billing: cancel failed for user %d: %v", u.ID, err)
		http.Error(w, "could not cancel subscription", http.StatusBadGateway)
		return
	}
	end := sub.CurrentPeriodEnd
	if sub.EndsAt != nil {
		end = *sub.EndsAt
	}
	eventAt := time.Time{}
	if sub.ModifiedAt != nil {
		eventAt = *sub.ModifiedAt
	}
	if err := db.SetSubscription(u.ID, store.SubStatusCanceled, u.SubID, end, billing.PaidDeviceLimit, eventAt); err != nil {
		log.Printf("billing: recording cancel for user %d: %v", u.ID, err)
		http.Error(w, "could not record cancellation", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": store.SubStatusCanceled, "subCurrentEnd": end})
}

// polarSubscription is the subset of the PATCH/GET /v1/subscriptions/{id}
// response we read back (after a cancel, and during the on-load reconcile).
type polarSubscription struct {
	Status            string     `json:"status"` // trialing|active|canceled|past_due|...
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	CurrentPeriodEnd  time.Time  `json:"current_period_end"`
	EndsAt            *time.Time `json:"ends_at"`
	ModifiedAt        *time.Time `json:"modified_at"`
}

// cancelPolarSubscription PATCHes the subscription to cancel at period end.
func cancelPolarSubscription(ctx context.Context, subID string) (*polarSubscription, error) {
	body, err := json.Marshal(map[string]any{"cancel_at_period_end": true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, billing.APIBase+"/v1/subscriptions/"+subID, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+billing.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := billingHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 == 2 {
		var sub polarSubscription
		if err := json.Unmarshal(rb, &sub); err != nil {
			return nil, err
		}
		return &sub, nil
	}
	// Already scheduled to cancel (a prior cancel, or via the Polar portal) — not
	// an error from our side: fetch the current state and report it as canceled so
	// the dashboard catches up (this is also the self-heal when the webhook that
	// should have marked it canceled never reached us).
	if bytes.Contains(rb, []byte("AlreadyCanceledSubscription")) {
		return getPolarSubscription(ctx, subID)
	}
	return nil, fmt.Errorf("polar cancel status %d: %s", resp.StatusCode, string(rb))
}

// revokePolarSubscription immediately revokes a subscription at Polar — the admin
// "get this user off their plan now" action, not the user's schedule-at-period-end
// cancel. An already-canceled or already-gone (404) subscription is treated as
// success (that's the end state we want), so an admin can always land the account
// on expired without touching the Polar dashboard.
func revokePolarSubscription(ctx context.Context, subID string) (*polarSubscription, error) {
	body, err := json.Marshal(map[string]any{"revoke": true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, billing.APIBase+"/v1/subscriptions/"+subID, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+billing.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := billingHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 == 2 {
		var sub polarSubscription
		if err := json.Unmarshal(rb, &sub); err != nil {
			return nil, err
		}
		return &sub, nil
	}
	// Already revoked/canceled, or gone at Polar — the end state we want anyway.
	if resp.StatusCode == http.StatusNotFound {
		return &polarSubscription{Status: "canceled"}, nil
	}
	if bytes.Contains(rb, []byte("AlreadyCanceledSubscription")) {
		if sub, err := getPolarSubscription(ctx, subID); err == nil {
			return sub, nil
		}
		return &polarSubscription{Status: "canceled"}, nil
	}
	return nil, fmt.Errorf("polar revoke status %d: %s", resp.StatusCode, string(rb))
}

// getPolarSubscription reads a subscription's current state.
func getPolarSubscription(ctx context.Context, subID string) (*polarSubscription, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, billing.APIBase+"/v1/subscriptions/"+subID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+billing.APIToken)
	resp, err := billingHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("polar get subscription status %d: %s", resp.StatusCode, string(rb))
	}
	var sub polarSubscription
	if err := json.Unmarshal(rb, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// pastDueGrace is how long access is held after a failed renewal (Polar dunning)
// before revoked→expired takes over. Polar emails the customer to update their card
// during this window; keeping the dashboard usable means a transient card problem
// doesn't lock a paying customer out.
const pastDueGrace = 14 * 24 * time.Hour

// subReconcileEvery throttles the on-load reconcile so /api/me doesn't call Polar
// on every poll. A few minutes is fine: the webhook is the fast path, this is the
// safety net that self-heals a dropped one.
const subReconcileEvery = 3 * time.Minute

// maybeReconcileSubscription refreshes the account's subscription from Polar (the
// source of truth) at most once per subReconcileEvery, so the dashboard self-heals
// from a missed or reordered webhook — a cancel that didn't stick, a revoke that
// didn't lock. Best-effort: a Polar error leaves the stored state untouched.
// Returns the (possibly refreshed) user so the caller renders the current state.
func maybeReconcileSubscription(ctx context.Context, u *store.User) *store.User {
	if !billingEnabled || u == nil || u.SubID == "" {
		return u
	}
	if !u.SubSyncedAt.IsZero() && time.Since(u.SubSyncedAt) < subReconcileEvery {
		return u
	}
	sub, err := getPolarSubscription(ctx, u.SubID)
	if err != nil {
		log.Printf("billing: reconcile fetch failed for user %d: %v", u.ID, err)
		// Stamp anyway so a persistent Polar error doesn't retry on every /api/me.
		_ = db.StampSubSynced(u.ID)
		return refreshUser(u)
	}
	if status, end, cap := mapPolarStatus(sub); status != "" && (status != u.SubStatus || !end.Equal(u.SubCurrentEnd)) {
		eventAt := time.Time{}
		if sub.ModifiedAt != nil {
			eventAt = *sub.ModifiedAt
		}
		if err := db.SetSubscription(u.ID, status, u.SubID, end, cap, eventAt); err != nil {
			log.Printf("billing: reconcile write failed for user %d: %v", u.ID, err)
		}
	}
	_ = db.StampSubSynced(u.ID)
	return refreshUser(u)
}

// refreshUser re-reads the account so callers see the reconciled row; falls back
// to the original on error.
func refreshUser(u *store.User) *store.User {
	if fresh, err := db.UserByID(u.ID); err == nil {
		return fresh
	}
	return u
}

// pastDueEnd is the grace deadline for a failed renewal: the period end plus the
// grace window (or now + grace if the provider sent no period end).
func pastDueEnd(periodEnd time.Time) time.Time {
	if periodEnd.IsZero() {
		periodEnd = time.Now()
	}
	return periodEnd.Add(pastDueGrace)
}

// mapPolarStatus maps a live Polar subscription onto our local status/end/cap,
// mirroring applyBillingEvent. An empty status means "leave the stored state
// alone" (e.g. past_due dunning — access is held through Polar's grace).
func mapPolarStatus(sub *polarSubscription) (status string, end time.Time, deviceLimit int) {
	switch {
	case sub.Status == "canceled" || sub.Status == "unpaid" || sub.Status == "incomplete_expired":
		// Ended at Polar: revoke to the free tier.
		return store.SubStatusExpired, sub.CurrentPeriodEnd, 0
	case sub.CancelAtPeriodEnd:
		// Scheduled to cancel: keep the paid cap until the period actually ends.
		end = sub.CurrentPeriodEnd
		if sub.EndsAt != nil {
			end = *sub.EndsAt
		}
		return store.SubStatusCanceled, end, billing.PaidDeviceLimit
	case sub.Status == store.SubStatusTrialing:
		return store.SubStatusTrialing, sub.CurrentPeriodEnd, billing.PaidDeviceLimit
	case sub.Status == store.SubStatusActive:
		return store.SubStatusActive, sub.CurrentPeriodEnd, billing.PaidDeviceLimit
	case sub.Status == store.SubStatusPastDue:
		// Failed renewal: keep access through the grace window, then revoke takes over.
		return store.SubStatusPastDue, pastDueEnd(sub.CurrentPeriodEnd), billing.PaidDeviceLimit
	default:
		return "", time.Time{}, 0
	}
}

// polarSubscriptionEvent is the subset of a Polar webhook we act on.
type polarSubscriptionEvent struct {
	Type string `json:"type"`
	Data struct {
		ID                string     `json:"id"`
		Status            string     `json:"status"`
		CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
		CurrentPeriodEnd  time.Time  `json:"current_period_end"`
		EndsAt            *time.Time `json:"ends_at"`
		ModifiedAt        *time.Time `json:"modified_at"` // for the ordering guard
		Customer          struct {
			ExternalID string `json:"external_id"`
		} `json:"customer"`
	} `json:"data"`
}

// handleBillingWebhook verifies and applies a Polar subscription webhook.
// Responses: 401 bad signature, 400 unreadable, 503 not configured, 502 on a
// transient DB error (so Polar retries), 200 once applied or safely ignored.
func handleBillingWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if billingWH == nil {
		http.Error(w, "billing webhook not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	// Verify the HMAC signature (webhook-id/-timestamp/-signature headers) before
	// trusting a single byte — otherwise anyone could grant themselves access.
	if err := billingWH.Verify(body, r.Header); err != nil {
		log.Printf("billing: webhook signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var evt polarSubscriptionEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	if err := applyBillingEvent(evt); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, errIgnoredEvent) || errors.Is(err, errUnmappable) {
			// Nothing actionable / no such account — ack so Polar stops retrying.
			w.WriteHeader(http.StatusOK)
			return
		}
		// Transient (e.g. DB) error — 502 so Polar retries later.
		log.Printf("billing: applying %s failed: %v", evt.Type, err)
		http.Error(w, "processing error", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

var (
	errIgnoredEvent = errors.New("billing: event type ignored")
	errUnmappable   = errors.New("billing: event has no mappable external_customer_id")
)

// applyBillingEvent maps a verified Polar event onto store.SetSubscription.
func applyBillingEvent(evt polarSubscriptionEvent) error {
	uid, err := strconv.ParseUint(evt.Data.Customer.ExternalID, 10, 64)
	if err != nil {
		// We always set external_customer_id at checkout, so a missing/garbage one
		// is a subscription we can't own. Log and treat as unmappable (ack).
		log.Printf("billing: %s has unmappable external_customer_id %q", evt.Type, evt.Data.Customer.ExternalID)
		return errUnmappable
	}
	userID := uint(uid)
	subID := evt.Data.ID

	// Ordering guard: webhooks can arrive out of order or more than once. Drop any
	// event older than the last one we applied, so a stale subscription.updated
	// (status still "active") can't undo a newer cancel/revoke. Equal timestamps
	// re-apply idempotently; events with no modified_at skip the guard.
	eventAt := time.Time{}
	if evt.Data.ModifiedAt != nil {
		eventAt = *evt.Data.ModifiedAt
	}
	if !eventAt.IsZero() {
		if u, err := db.UserByID(userID); err == nil && !u.SubEventAt.IsZero() && eventAt.Before(u.SubEventAt) {
			return errIgnoredEvent
		}
	}

	// periodEnd prefers ends_at (the concrete access-until on a cancel) over the
	// rolling current_period_end.
	periodEnd := func() time.Time {
		if evt.Data.EndsAt != nil {
			return *evt.Data.EndsAt
		}
		return evt.Data.CurrentPeriodEnd
	}

	switch evt.Type {
	case "subscription.created", "subscription.active", "subscription.uncanceled", "subscription.updated", "subscription.past_due":
		// A scheduled cancellation arrives as an updated/active event with
		// cancel_at_period_end set — treat that as canceled, not a fresh grant, so a
		// re-delivered or reordered updated can't flip a cancel back to active. The
		// subscriber keeps the paid cap until the period ends (revoked drops it).
		if evt.Data.CancelAtPeriodEnd {
			return db.SetSubscription(userID, store.SubStatusCanceled, subID, periodEnd(), billing.PaidDeviceLimit, eventAt)
		}
		// Otherwise grant by status: a card-up-front trial arrives as "trialing"
		// (subscription.created) and auto-converts to "active".
		switch evt.Data.Status {
		case store.SubStatusTrialing:
			return db.SetSubscription(userID, store.SubStatusTrialing, subID, evt.Data.CurrentPeriodEnd, billing.PaidDeviceLimit, eventAt)
		case store.SubStatusActive:
			return db.SetSubscription(userID, store.SubStatusActive, subID, evt.Data.CurrentPeriodEnd, billing.PaidDeviceLimit, eventAt)
		case store.SubStatusPastDue:
			// Renewal failed: hold access through a grace window (Polar's dunning keeps
			// retrying the card + emailing the customer), then revoked→expired takes over.
			return db.SetSubscription(userID, store.SubStatusPastDue, subID, pastDueEnd(evt.Data.CurrentPeriodEnd), billing.PaidDeviceLimit, eventAt)
		default:
			return errIgnoredEvent
		}
	case "subscription.canceled":
		// Cancellation scheduled: keep access + the paid cap until the period ends
		// (Polar sends subscription.revoked then); record the end for "cancels on X".
		return db.SetSubscription(userID, store.SubStatusCanceled, subID, periodEnd(), billing.PaidDeviceLimit, eventAt)
	case "subscription.revoked":
		// Access lost now (cancellation took effect or payment lapsed) — drop the
		// cap back to the free tier.
		return db.SetSubscription(userID, store.SubStatusExpired, subID, evt.Data.CurrentPeriodEnd, 0, eventAt)
	default:
		return errIgnoredEvent
	}
}
