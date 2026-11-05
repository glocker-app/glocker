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

// SetupReminderCandidates returns accounts on a running no-card trial that have
// never had a batch ingested and haven't had the setup reminder yet.
func (db *DB) SetupReminderCandidates(now time.Time) ([]User, error) {
	var users []User
	err := db.Where("verified = ? AND sub_status = ? AND trial_ends_at > ? AND setup_remind_at IS NULL AND id NOT IN (?)",
		true, "", now, db.Model(&SyncStatus{}).Select("user_id")).
		Find(&users).Error
	return users, err
}

// MarkSetupRemind records that the setup reminder was sent.
func (db *DB) MarkSetupRemind(userID uint, at time.Time) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("setup_remind_at", at).Error
}

// HasSyncedData reports whether a batch has ever been ingested for the account.
// APIToken.LastUsedAt can't answer this: any token use stamps it, including
// `glocker -connect` fetching the vault, with no data sent.
func (db *DB) HasSyncedData(userID uint) bool {
	var n int64
	db.Model(&SyncStatus{}).Where("user_id = ?", userID).Count(&n)
	return n > 0
}
