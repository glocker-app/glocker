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

// ClearSubscription resets an account to the free tier: no subscription status,
// no stored subscription id, and the free device cap. Used by the admin panel to
// undo a subscription when the MoR and glockpeek have drifted (e.g. a sandbox sub
// deleted at Polar whose revoke webhook never reached here). Returns
// gorm.ErrRecordNotFound if userID matches no account.
func (db *DB) ClearSubscription(userID uint) error {
	// Zero eventAt so any later webhook (which carries a real modified_at) applies
	// — an admin clear is a manual reset, not part of the provider event stream.
	return db.SetSubscription(userID, "", "", time.Time{}, 0, time.Time{})
}
