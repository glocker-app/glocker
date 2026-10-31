package store

import "time"

// TrialReminderCandidates returns never-subscribed accounts that are on a no-card
// trial (SubStatus == "", TrialEndsAt set) and still owe at least one reminder
// email. The scheduler sweep decides per account which of the two reminders
// (ending-soon, ended) is due from the timestamps; this just narrows the scan to
// accounts that can still receive one. Verified accounts only — an unverified
// signup has no trial yet.
func (db *DB) TrialReminderCandidates() ([]User, error) {
	var users []User
	err := db.Where("verified = ? AND sub_status = ? AND trial_ends_at IS NOT NULL AND "+
		"(trial_remind_soon_at IS NULL OR trial_remind_end_at IS NULL)", true, "").
		Find(&users).Error
	return users, err
}

// MarkTrialRemindSoon records that the "trial ending soon" email was sent, so the
// sweep sends it at most once. Best-effort: a no-op on an unknown id.
func (db *DB) MarkTrialRemindSoon(userID uint, at time.Time) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("trial_remind_soon_at", at).Error
}

// MarkTrialRemindEnd records that the "trial ended" email was sent.
func (db *DB) MarkTrialRemindEnd(userID uint, at time.Time) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("trial_remind_end_at", at).Error
}

// UsersMissingTrial returns verified, never-subscribed accounts that have no
// trial deadline — the set a one-off backfill grants a trial to when the feature
// ships. Subscribed and already-trialing accounts are excluded, so a re-run is a
// no-op. Scoped on sub_status, not sub_id, so an expired account (which may also
// have an empty sub_id) is left read-only rather than handed a fresh trial.
func (db *DB) UsersMissingTrial() ([]User, error) {
	var users []User
	err := db.Where("verified = ? AND sub_status = ? AND trial_ends_at IS NULL", true, "").
		Order("id").Find(&users).Error
	return users, err
}

// SetTrialEnd sets an account's no-card trial deadline. Used by the backfill and
// by tests; the normal path sets it at verification.
func (db *DB) SetTrialEnd(userID uint, at time.Time) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("trial_ends_at", at).Error
}
