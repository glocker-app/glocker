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
	url, err := createPolarCheckout(r.Context(), u.ID)
	if err != nil {
		log.Printf("billing: checkout create failed for user %d: %v", u.ID, err)
		http.Error(w, "could not start checkout", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"url": url})
}

// createPolarCheckout POSTs to Polar's checkout API (raw HTTP — Polar's Go SDK
// is archived) and returns the hosted checkout URL.
func createPolarCheckout(ctx context.Context, userID uint) (string, error) {
	ext := strconv.FormatUint(uint64(userID), 10)
	reqBody := map[string]any{
		"products":             []string{billing.ProductID},
		"external_customer_id": ext,
		"metadata":             map[string]string{"user_id": ext},
	}
	if billing.SuccessURL != "" {
		reqBody["success_url"] = billing.SuccessURL
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
// customer-portal URL. A 404 means the account never checked out (no Polar
// customer yet) — surfaced as errNoCustomer so the caller can 404.
func createPolarPortal(ctx context.Context, userID uint) (string, error) {
	ext := strconv.FormatUint(uint64(userID), 10)
	body, err := json.Marshal(map[string]any{"customer_external_id": ext})
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
	if resp.StatusCode == http.StatusNotFound {
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

// polarSubscriptionEvent is the subset of a Polar webhook we act on.
type polarSubscriptionEvent struct {
	Type string `json:"type"`
	Data struct {
		ID               string     `json:"id"`
		Status           string     `json:"status"`
		CurrentPeriodEnd time.Time  `json:"current_period_end"`
		EndsAt           *time.Time `json:"ends_at"`
		Customer         struct {
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

	switch evt.Type {
	case "subscription.created", "subscription.active", "subscription.uncanceled", "subscription.updated":
		// Grant by the subscription's own status: a card-up-front trial arrives as
		// "trialing" (subscription.created) and auto-converts to "active" later.
		// Both grant full access + the paid device cap; any other status here (e.g.
		// past_due) we leave to the explicit canceled/revoked events below.
		switch evt.Data.Status {
		case store.SubStatusTrialing:
			return db.SetSubscription(userID, store.SubStatusTrialing, subID, evt.Data.CurrentPeriodEnd, billing.PaidDeviceLimit)
		case store.SubStatusActive:
			return db.SetSubscription(userID, store.SubStatusActive, subID, evt.Data.CurrentPeriodEnd, billing.PaidDeviceLimit)
		default:
			return errIgnoredEvent
		}
	case "subscription.canceled":
		// Cancellation scheduled: the subscriber keeps access until the period
		// ends (Polar sends subscription.revoked then). Keep the paid cap, just
		// record the pending end so the UI can say "cancels on <date>".
		end := evt.Data.CurrentPeriodEnd
		if evt.Data.EndsAt != nil {
			end = *evt.Data.EndsAt
		}
		return db.SetSubscription(userID, store.SubStatusCanceled, subID, end, billing.PaidDeviceLimit)
	case "subscription.revoked":
		// Access lost now (cancellation took effect or payment lapsed) — drop the
		// cap back to the free tier.
		return db.SetSubscription(userID, store.SubStatusExpired, subID, evt.Data.CurrentPeriodEnd, 0)
	default:
		return errIgnoredEvent
	}
}
