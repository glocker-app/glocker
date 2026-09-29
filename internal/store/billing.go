package store

import (
	"time"

	"gorm.io/gorm"
)

// Subscription status values written by the billing webhook. The Merchant-of-
// Record (Polar) is the source of truth; we map its events onto these.
const (
	SubStatusTrialing = "trialing" // in the free trial window; full access, auto-converts to active
	SubStatusActive   = "active"   // paying; access granted
	SubStatusCanceled = "canceled" // cancellation scheduled — access remains until SubCurrentEnd
	SubStatusExpired  = "expired"  // access revoked: cancellation took effect, or payment lapsed
)

// HasAccess reports whether the account is entitled to the paid product right now
// — writing (ingest + dashboard feature edits). Access holds during the trial,
// while active, and through a scheduled cancellation until the period ends. An
// expired or never-subscribed account has no access (read-only). Reads are never
// gated on this; enforcement (ingest/write refusal) is elsewhere.
func (u *User) HasAccess() bool {
	switch u.SubStatus {
	case SubStatusTrialing, SubStatusActive, SubStatusCanceled:
		// A set period-end bounds access. Without this, a lost `revoke`/`canceled`
		// webhook leaves the status granting access forever; with it, access still
		// lapses when the paid/trial period actually ends. (A missed renewal can
		// deny a paying account early — acceptable vs. leaking access indefinitely,
		// and re-syncing the subscription restores it.)
		if !u.SubCurrentEnd.IsZero() && time.Now().After(u.SubCurrentEnd) {
			return false
		}
		return true
	default:
		return false
	}
}

// SetSubscription records a user's subscription state from a billing webhook and
// sets DeviceLimit — the actual enforcement lever — accordingly: deviceLimit is
// the paid cap while access is granted, or 0 (back to the free tier) once
// revoked. It is idempotent: the same event delivered twice yields the same row,
// so the MoR re-delivering webhooks is safe. Returns gorm.ErrRecordNotFound if
// userID matches no account.
func (db *DB) SetSubscription(userID uint, status, subID string, currentEnd time.Time, deviceLimit int, eventAt time.Time) error {
	// A map (not a struct) so zero values are written too — revoking must set
	// device_limit back to 0, which struct-based Updates would skip.
	res := db.Model(&User{}).Where("id = ?", userID).Updates(map[string]any{
		"sub_status":      status,
		"sub_id":          subID,
		"sub_current_end": currentEnd,
		"device_limit":    deviceLimit,
		"sub_event_at":    eventAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// StampSubSynced records that the account's subscription was just reconciled
// against the provider, so the on-load reconcile stays throttled. Best-effort:
// a no-op on an unknown id.
func (db *DB) StampSubSynced(userID uint) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("sub_synced_at", time.Now()).Error
}

// ClearSubscription resets an account to the free tier: no subscription status,
// no stored subscription id, and the free device cap. Used by the admin "cancel
// subscription" action after revoking at the provider. Returns
// gorm.ErrRecordNotFound if userID matches no account.
func (db *DB) ClearSubscription(userID uint) error {
	// Stamp the event high-water mark to now so an in-flight provider webhook from
	// the revoke we just triggered (its modified_at is earlier) is dropped by the
	// ordering guard instead of re-expiring the account we just reset. A genuinely
	// new subscription later carries a later modified_at and still applies.
	return db.SetSubscription(userID, "", "", time.Time{}, 0, time.Now())
}
