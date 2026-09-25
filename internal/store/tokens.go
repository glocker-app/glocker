package store

import (
	"time"

	"gorm.io/gorm"
)

// Device/token management for the hosted dashboard. Each ingest API token
// represents one connected device (a glocker agent syncing to glockpeek). The
// number of tokens an account may hold is capped by User.DeviceLimit, with more
// devices offered as a paid upgrade.

// DefaultFreeDevices is the device (API-token) cap for a new, free-tier account.
const DefaultFreeDevices = 1

// EffectiveDeviceLimit resolves a user's device cap: the zero value (free tier)
// maps to DefaultFreeDevices, and a negative value means unlimited (returned as
// -1). Callers treat a negative return as "no limit".
func EffectiveDeviceLimit(u *User) int {
	switch {
	case u.DeviceLimit < 0:
		return -1
	case u.DeviceLimit == 0:
		return DefaultFreeDevices
	default:
		return u.DeviceLimit
	}
}

// ListAPITokens returns a user's ingest tokens (metadata only — the hash column
// is never selected into JSON), oldest first. Revoked tokens are included so the
// dashboard can still name the historical rows of a revoked device; callers tell
// them apart by RevokedAt.
func (db *DB) ListAPITokens(userID uint) ([]APIToken, error) {
	var toks []APIToken
	if err := db.Where("user_id = ?", userID).Order("created_at asc").Find(&toks).Error; err != nil {
		return nil, err
	}
	return toks, nil
}

// CountAPITokens counts a user's ACTIVE (non-revoked) ingest tokens — this is
// what enforces the device cap, so revoking a device frees a slot.
func (db *DB) CountAPITokens(userID uint) (int, error) {
	var n int64
	if err := db.Model(&APIToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// RevokeAPIToken soft-revokes one token by ID, scoped to userID so a user can
// only revoke their own. The row is kept (so the device's history stays named)
// but marked revoked: it stops authenticating at ingest and no longer counts
// against the device cap. Returns gorm.ErrRecordNotFound if the user has no such
// token that is still active (already-revoked or unknown ids both 404).
func (db *DB) RevokeAPIToken(userID, tokenID uint) error {
	now := time.Now()
	res := db.Model(&APIToken{}).
		Where("user_id = ? AND id = ? AND revoked_at IS NULL", userID, tokenID).
		Update("revoked_at", &now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// RenameAPIToken relabels one active token, scoped to userID. Revoked tokens
// keep their historical name (not renameable). Returns gorm.ErrRecordNotFound if
// the user has no such active token.
func (db *DB) RenameAPIToken(userID, tokenID uint, name string) error {
	res := db.Model(&APIToken{}).
		Where("user_id = ? AND id = ? AND revoked_at IS NULL", userID, tokenID).
		Update("name", name)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SetDeviceLimit sets an account's device cap by email (admin action, backing
// the paid-plan upgrade). A negative limit means unlimited. Returns
// ErrRecordNotFound if the email is unknown.
func (db *DB) SetDeviceLimit(email string, limit int) error {
	res := db.Model(&User{}).Where("email = ?", normalizeEmail(email)).Update("device_limit", limit)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
