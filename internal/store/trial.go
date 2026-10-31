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

// SetTrialEnd sets an account's no-card trial deadline. The normal path sets it
// at verification; this is for admin adjustment and tests.
func (db *DB) SetTrialEnd(userID uint, at time.Time) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("trial_ends_at", at).Error
}
