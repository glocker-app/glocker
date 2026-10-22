package store

import "gorm.io/gorm"

// Admin operations for the hosted dashboard's user-management panel. These are
// account-wide (not scoped to one user) and are gated to the admin account by
// the stats layer.

// ListUsers returns all accounts, oldest first.
func (db *DB) ListUsers() ([]User, error) {
	var us []User
	if err := db.Order("id asc").Find(&us).Error; err != nil {
		return nil, err
	}
	return us, nil
}

// DeleteUserData removes an account and ALL of its data across every
// user-scoped table, in one transaction. Irreversible.
func (db *DB) DeleteUserData(userID uint) error {
	return db.DB.Transaction(func(tx *gorm.DB) error {
		// RuleTag has no user_id — it links a Rule to a Tag. Remove the rows for this
		// user's rules first (before the rules themselves go).
		var ruleIDs []uint
		if err := tx.Model(&Rule{}).Where("user_id = ?", userID).Pluck("id", &ruleIDs).Error; err != nil {
			return err
		}
		if len(ruleIDs) > 0 {
			if err := tx.Where("rule_id IN ?", ruleIDs).Delete(&RuleTag{}).Error; err != nil {
				return err
			}
		}
		// Every user-scoped table (all keyed by user_id). System-owned rows —
		// Glocker:* tags and DefaultRule templates carry user_id 0, so this never
		// touches them. Non-scoped infra (RateHit/CaptchaUse/SchedulerLease) is left.
		for _, m := range []any{
			&Session{}, &APIToken{}, &VerificationToken{}, &PasswordResetToken{},
			&Violation{}, &Unblock{}, &LifecycleEvent{}, &UsageSample{},
			&Heartbeat{}, &Rule{}, &Tag{}, &TagOverride{}, &TagColor{},
			&IgnoredViolation{}, &SyncStatus{}, &KeyVault{}, &DeviceConfig{},
			&AccountabilityConfig{},
		} {
			if err := tx.Where("user_id = ?", userID).Delete(m).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&User{}, userID).Error
	})
}

// SetDeviceLimitByID sets an account's device cap by id (admin action; the
// self-service SetDeviceLimit keys off email). Negative means unlimited.
func (db *DB) SetDeviceLimitByID(userID uint, limit int) error {
	res := db.Model(&User{}).Where("id = ?", userID).Update("device_limit", limit)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
