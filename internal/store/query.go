package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// hashURL returns a fixed-size, index-safe fingerprint of a URL. It backs the
// violations/ignored unique index, which can't hold a raw URL (some exceed
// Postgres's btree row-size limit). Deterministic: equal URLs → equal hash.
func hashURL(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// All reads/writes below are scoped to a single account (userID). Ingest sets
// UserID on every row from the authenticated token; reads filter by it.

// ── Reads (ordered by TS ascending, scoped to userID) ──

func (db *DB) Violations(userID uint) ([]Violation, error) {
	var out []Violation
	err := db.Where("user_id = ?", userID).Order("ts asc").Find(&out).Error
	return out, err
}

func (db *DB) Unblocks(userID uint) ([]Unblock, error) {
	var out []Unblock
	err := db.Where("user_id = ?", userID).Order("ts asc").Find(&out).Error
	return out, err
}

func (db *DB) LifecycleEvents(userID uint) ([]LifecycleEvent, error) {
	var out []LifecycleEvent
	err := db.Where("user_id = ?", userID).Order("ts asc").Find(&out).Error
	return out, err
}

func (db *DB) UsageSamples(userID uint) ([]UsageSample, error) {
	var out []UsageSample
	err := db.Where("user_id = ?", userID).Order("ts asc").Find(&out).Error
	return out, err
}

func (db *DB) Heartbeats(userID uint) ([]Heartbeat, error) {
	var out []Heartbeat
	err := db.Where("user_id = ?", userID).Order("ts asc").Find(&out).Error
	return out, err
}

func (db *DB) Rules(userID uint) ([]Rule, error) {
	var out []Rule
	err := db.Where("user_id = ?", userID).Order("id asc").Find(&out).Error
	return out, err
}

func (db *DB) Colors(userID uint) (map[string]string, error) {
	var rows []TagColor
	if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Tag] = r.Color
	}
	return m, nil
}

// GetVault returns the account's stored vault JSON (ok=false if none). The server
// never parses it — it's opaque wrapped material.
func (db *DB) GetVault(userID uint) (string, bool, error) {
	var v KeyVault
	err := db.Where("user_id = ?", userID).First(&v).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v.Vault, true, nil
}

// SetVault stores (or replaces) the account's vault JSON.
func (db *DB) SetVault(userID uint, vaultJSON string) error {
	return db.Save(&KeyVault{UserID: userID, Vault: vaultJSON}).Error
}

// HasVault reports whether the account has set up encryption.
func (db *DB) HasVault(userID uint) bool {
	var n int64
	db.Model(&KeyVault{}).Where("user_id = ?", userID).Count(&n)
	return n > 0
}

func (db *DB) IgnoredViolations(userID uint) ([]IgnoredViolation, error) {
	var out []IgnoredViolation
	err := db.Where("user_id = ?", userID).Order("ts asc").Find(&out).Error
	return out, err
}

// ── Ingest (idempotent; stamps UserID on every row) ──

// IngestViolations upserts report lines; a re-sent (user, record_id) no-ops.
// RecordID is the agent-computed dedup key (opaque under E2E); URLHash is still
// filled for plaintext-mode ignored-matching but no longer keys the index.
func (db *DB) IngestViolations(userID uint, rows []Violation) error {
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].UserID = userID
		rows[i].URLHash = hashURL(rows[i].URL)
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "record_id"}},
		DoNothing: true,
	}).CreateInBatches(rows, 500).Error
}

// IngestUnblocks upserts unblock events; on conflict updates restore_ts+reason so
// a later sync can close an unblock that was open when first sent.
func (db *DB) IngestUnblocks(userID uint, rows []Unblock) error {
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].UserID = userID
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "record_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"restore_ts", "reason"}),
	}).CreateInBatches(rows, 500).Error
}

func (db *DB) IngestLifecycle(userID uint, rows []LifecycleEvent) error {
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].UserID = userID
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "ts"}, {Name: "type"}},
		DoNothing: true,
	}).CreateInBatches(rows, 500).Error
}

func (db *DB) IngestUsage(userID uint, rows []UsageSample) error {
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].UserID = userID
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "ts"}},
		DoNothing: true,
	}).CreateInBatches(rows, 500).Error
}

func (db *DB) IngestHeartbeats(userID uint, rows []Heartbeat) error {
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].UserID = userID
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "ts"}},
		DoNothing: true,
	}).CreateInBatches(rows, 500).Error
}

// ── Dashboard-local settings (replace-all, scoped to userID) ──

// SetRulesConfig replaces the account's rules + colours in one transaction.
func (db *DB) SetRulesConfig(userID uint, rules []Rule, colors map[string]string) error {
	return db.Transaction(func(tx *DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&Rule{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&TagColor{}).Error; err != nil {
			return err
		}
		if len(rules) > 0 {
			for i := range rules {
				rules[i].UserID = userID
			}
			if err := tx.Create(&rules).Error; err != nil {
				return err
			}
		}
		if len(colors) > 0 {
			tc := make([]TagColor, 0, len(colors))
			for tag, color := range colors {
				tc = append(tc, TagColor{UserID: userID, Tag: tag, Color: color})
			}
			if err := tx.Create(&tc).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SetIgnored replaces the account's false-positive ignore list.
func (db *DB) SetIgnored(userID uint, rows []IgnoredViolation) error {
	return db.Transaction(func(tx *DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&IgnoredViolation{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			for i := range rows {
				rows[i].UserID = userID
			}
			return tx.Create(&rows).Error
		}
		return nil
	})
}

// Transaction runs fn inside a DB transaction, wrapping the *gorm.DB back into
// our *DB so the helper methods are available on the tx handle.
func (db *DB) Transaction(fn func(tx *DB) error) error {
	return db.DB.Transaction(func(g *gorm.DB) error {
		return fn(&DB{g})
	})
}

// RecordIngest stamps the account's last-sync time (now) and the counts from the
// batch just accepted. Upserts the single per-account row.
func (db *DB) RecordIngest(userID uint, s SyncStatus) error {
	s.UserID = userID
	s.LastIngestAt = time.Now()
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"last_ingest_at", "last_violations", "last_unblocks",
			"last_lifecycle", "last_usage", "last_heartbeat", "encrypted",
		}),
	}).Create(&s).Error
}

// AccountEncrypted reports whether the account's most recent ingest was encrypted,
// so the dashboard knows to decrypt records before rendering. False if the account
// has never synced.
func (db *DB) AccountEncrypted(userID uint) bool {
	var s SyncStatus
	if err := db.Where("user_id = ?", userID).First(&s).Error; err != nil {
		return false
	}
	return s.Encrypted
}

// HighWaterMarks returns the max stored TS per source for an account, keyed by
// the syncer's source names. The syncer seeds its cursors from this so it only
// sends records glockpeek doesn't already have (glockpeek is the source of truth
// for what's been synced — no local cursor state, self-healing if the DB is
// reset). Missing/empty tables report 0.
func (db *DB) HighWaterMarks(userID uint) map[string]int64 {
	out := map[string]int64{}
	for src, model := range map[string]any{
		"reports": &Violation{}, "unblocks": &Unblock{},
		"lifecycle": &LifecycleEvent{}, "usage": &UsageSample{}, "heartbeat": &Heartbeat{},
	} {
		var max int64
		db.Model(model).Where("user_id = ?", userID).Select("COALESCE(MAX(ts), 0)").Scan(&max)
		out[src] = max
	}
	return out
}

// SyncStatusFor returns the account's last-sync row; ok is false if it has never
// received an ingest batch.
func (db *DB) SyncStatusFor(userID uint) (SyncStatus, bool, error) {
	var s SyncStatus
	err := db.Where("user_id = ?", userID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s, false, nil
	}
	return s, err == nil, err
}

// Counts returns row counts per stats table for one account (health endpoint).
func (db *DB) Counts(userID uint) map[string]int64 {
	out := map[string]int64{}
	for name, m := range map[string]any{
		"violations": &Violation{}, "unblocks": &Unblock{},
		"lifecycle": &LifecycleEvent{}, "usage": &UsageSample{},
		"heartbeat": &Heartbeat{}, "rules": &Rule{}, "ignored": &IgnoredViolation{},
	} {
		var n int64
		db.Model(m).Where("user_id = ?", userID).Count(&n)
		out[name] = n
	}
	return out
}
