package store

import (
	"encoding/json"
	"time"

	"gorm.io/gorm/clause"
)

// NotifyConfig is an account's accountability-notification settings, synced from
// the agent's conf.yaml (cleartext — glockpeek is the sender and must read whom
// and how often to email). Account-level: authored per-device, but the digest is
// an account roll-up, so the latest sync wins (SaveNotifyConfig upserts the config
// columns, leaving the scheduler's Last* bookkeeping untouched).
type NotifyConfig struct {
	UserID uint `gorm:"primaryKey" json:"-"`

	// Synced from conf.yaml.
	Enabled          bool   `json:"enabled"`
	BuddyEmails      string `json:"-"` // JSON-encoded []string
	DigestEveryHours int    `json:"digestEveryHours"`
	DigestWhenQuiet  bool   `json:"digestWhenQuiet"`
	WentDark         bool   `json:"wentDark"`
	HighViolations   int    `json:"highViolations"`
	UpdatedAt        time.Time

	// Scheduler bookkeeping (throttle / dedupe) — written by glockpeek, never by
	// the config sync. Epoch ms; 0 = never sent.
	LastDigestAt   int64 `json:"-"`
	LastWentDarkAt int64 `json:"-"`
	LastHighViolAt int64 `json:"-"`
}

// Buddies decodes the stored buddy-email list.
func (n NotifyConfig) Buddies() []string {
	var out []string
	if n.BuddyEmails != "" {
		_ = json.Unmarshal([]byte(n.BuddyEmails), &out)
	}
	return out
}

// SaveNotifyConfig upserts the account's notification settings from a config sync.
// It writes only the synced columns — the scheduler's Last* bookkeeping is left
// alone so re-syncing the same config never resets the throttle.
func (db *DB) SaveNotifyConfig(userID uint, n NotifyConfig) error {
	n.UserID = userID
	n.UpdatedAt = time.Now()
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"enabled", "buddy_emails", "digest_every_hours",
			"digest_when_quiet", "went_dark", "high_violations", "updated_at",
		}),
	}).Create(&n).Error
}

// EnabledNotifyConfigs returns every account's notification settings that are
// enabled with at least one recipient — the scheduler's work list.
func (db *DB) EnabledNotifyConfigs() ([]NotifyConfig, error) {
	var out []NotifyConfig
	err := db.Where("enabled = ? AND buddy_emails <> '' AND buddy_emails <> '[]'", true).Find(&out).Error
	return out, err
}

// LatestHeartbeatTS returns the newest heartbeat timestamp (epoch ms) across all
// of an account's devices, and whether the account has any heartbeat at all. Used
// to detect "went dark": no heartbeat for a while means the agent is down.
func (db *DB) LatestHeartbeatTS(userID uint) (int64, bool) {
	var max *int64
	err := db.Model(&Heartbeat{}).Where("user_id = ?", userID).
		Select("MAX(ts)").Scan(&max).Error
	if err != nil || max == nil {
		return 0, false
	}
	return *max, true
}

// MarkDigestSent / MarkWentDarkSent / MarkHighViolSent stamp the throttle fields.
func (db *DB) MarkDigestSent(userID uint, at int64) error {
	return db.Model(&NotifyConfig{}).Where("user_id = ?", userID).Update("last_digest_at", at).Error
}
func (db *DB) MarkWentDarkSent(userID uint, at int64) error {
	return db.Model(&NotifyConfig{}).Where("user_id = ?", userID).Update("last_went_dark_at", at).Error
}
func (db *DB) MarkHighViolSent(userID uint, at int64) error {
	return db.Model(&NotifyConfig{}).Where("user_id = ?", userID).Update("last_high_viol_at", at).Error
}

// encodeBuddies is a helper for callers building a NotifyConfig from a wire list.
func EncodeBuddies(emails []string) string {
	if len(emails) == 0 {
		return "[]"
	}
	b, err := json.Marshal(emails)
	if err != nil {
		return "[]"
	}
	return string(b)
}
