package store

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Coordination primitives that make the app tier stateless: per-IP rate-limit
// counters, the captcha replay set, and a scheduler lease all live in the DB so
// every node shares them instead of keeping the state in process memory.

// RateHit is one fixed-window rate-limit counter, shared across the fleet. The
// key is e.g. "register:1.2.3.4"; window_bucket is unix-time / window-seconds.
type RateHit struct {
	Key    string `gorm:"column:rate_key;primaryKey"`
	Window int64  `gorm:"column:window_bucket;primaryKey"`
	Count  int64
}

func (RateHit) TableName() string { return "rate_hits" }

// RateAllow atomically increments the counter for key in the current window and
// reports whether it is still within limit. Fixed-window; a brief over/under at a
// window edge is fine for abuse-prevention. Fail-OPEN on a DB error (a DB blip
// must not lock out real users). Old buckets are swept opportunistically.
func (db *DB) RateAllow(key string, window time.Duration, limit int, now time.Time) (bool, error) {
	bucket := now.Unix() / int64(window.Seconds())
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "rate_key"}, {Name: "window_bucket"}},
		DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr("count + 1")}),
	}).Create(&RateHit{Key: key, Window: bucket, Count: 1}).Error; err != nil {
		return true, err // fail-open
	}
	var cur RateHit
	if err := db.Where("rate_key = ? AND window_bucket = ?", key, bucket).First(&cur).Error; err != nil {
		return true, err // fail-open
	}
	db.Where("window_bucket < ?", bucket-2).Delete(&RateHit{}) // best-effort sweep
	return cur.Count <= int64(limit), nil
}

// CaptchaUse records a spent challenge salt so a solved captcha can't be replayed.
type CaptchaUse struct {
	Salt      string    `gorm:"primaryKey"`
	ExpiresAt time.Time `gorm:"index"`
}

func (CaptchaUse) TableName() string { return "captcha_uses" }

// CaptchaUseOnce records salt as spent and reports whether this was its FIRST use
// (true) or a replay (false). Shared across nodes, so a solution can't be replayed
// on a second node. Expired rows are swept. On a DB error it returns (false, err)
// — fail CLOSED, since a captcha bypass is a security hole, not a UX blip.
func (db *DB) CaptchaUseOnce(salt string, expires time.Time) (bool, error) {
	res := db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&CaptchaUse{Salt: salt, ExpiresAt: expires})
	if res.Error != nil {
		return false, res.Error
	}
	db.Where("expires_at < ?", time.Now()).Delete(&CaptchaUse{}) // best-effort sweep
	return res.RowsAffected == 1, nil                            // 1 = inserted (first use); 0 = replay
}

// SchedulerLease is a single-row (id=1) fleet-wide lease electing exactly one node
// to run the accountability scheduler, so N nodes don't each fire the digests.
type SchedulerLease struct {
	ID        uint `gorm:"primaryKey"`
	Holder    string
	ExpiresAt time.Time
}

// TryAcquireSchedulerLease claims or renews the scheduler lease for `me` for the
// given duration, returning true if `me` holds it now (so this node runs the
// tick). A dead leader's lease expires and another node takes over on its next
// attempt. Portable (no advisory locks / pinned connections), so it works on both
// sqlite (single process → always leader) and postgres.
func (db *DB) TryAcquireSchedulerLease(me string, lease time.Duration) (bool, error) {
	now := time.Now()
	exp := now.Add(lease)
	// Ensure the row exists; the inserter becomes the initial holder.
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&SchedulerLease{ID: 1, Holder: me, ExpiresAt: exp}).Error; err != nil {
		return false, err
	}
	// Take it if we already hold it, or the current holder's lease has expired.
	res := db.Model(&SchedulerLease{}).
		Where("id = 1 AND (holder = ? OR expires_at < ?)", me, now).
		Updates(map[string]any{"holder": me, "expires_at": exp})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
