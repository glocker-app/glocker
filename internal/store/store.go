package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite" // pure-Go (modernc) sqlite driver — no cgo
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Options selects the backend. Driver is "sqlite" (default) or "postgres";
// empty fields fall back to the config defaults. For sqlite, DSN is a file path.
type Options struct {
	Driver string
	DSN    string
	// SkipMigrate opens the DB WITHOUT running migrations. Hosted/multi-node uses
	// it so N nodes don't race on DDL at boot — migrations are an explicit
	// `glockpeek -migrate` step run once before a deploy. Local/single-process and
	// tests leave it false (migrate on open, as before).
	SkipMigrate bool
}

// DB wraps the GORM handle. Callers use the embedded *gorm.DB directly; the
// wrapper exists so we can add helpers (ingest, queries) without leaking GORM
// everywhere later.
type DB struct {
	*gorm.DB
}

// Open connects using the given driver/DSN and runs migrations. The dialector is
// the only dialect-specific choice — everything above this is portable GORM, so
// swapping sqlite<->postgres is contained here.
func Open(o Options) (*DB, error) {
	driver := o.Driver
	if driver == "" {
		driver = "sqlite"
	}
	dsn := o.DSN
	if dsn == "" {
		return nil, fmt.Errorf("store: empty DSN for driver %q", driver)
	}

	var dialector gorm.Dialector
	switch driver {
	case "sqlite":
		// Ensure the parent dir exists for a file DSN (skip in-memory).
		if !strings.HasPrefix(dsn, ":memory:") && !strings.Contains(dsn, "mode=memory") {
			if dir := filepath.Dir(dsn); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, fmt.Errorf("store: creating db dir %s: %w", dir, err)
				}
			}
		}
		dialector = sqlite.Open(dsn)
	case "postgres":
		dialector = postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("store: unsupported driver %q (want sqlite or postgres)", driver)
	}

	gdb, err := gorm.Open(dialector, &gorm.Config{
		// Warn-level, but don't treat "record not found" as an error — it's a
		// normal control-flow signal (e.g. the default-user lookup on first run).
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			// Log SQL with $1,$2 placeholders, never the bound values — the values
			// are user content (URLs, window titles, keyword hits) and must not
			// leak into the journal/logs.
			ParameterizedQueries: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", driver, err)
	}

	db := &DB{gdb}
	if !o.SkipMigrate {
		if err := db.Migrate(); err != nil {
			return nil, err
		}
	}
	return db, nil
}

// Migrate creates/updates all tables. Safe to call repeatedly (idempotent).
func (db *DB) Migrate() error {
	// Older schemas indexed the raw URL in the violations/ignored unique index,
	// which overflows Postgres's btree row-size limit for long URLs. Move to a
	// fixed-size url_hash first — non-destructively, so no records are lost and a
	// full re-sync from the local logs stays possible.
	if err := db.migrateURLHash(); err != nil {
		return fmt.Errorf("store: url_hash migration: %w", err)
	}
	if err := db.migrateRecordIDIndexes(); err != nil {
		return fmt.Errorf("store: record_id index migration: %w", err)
	}
	if err := db.migrateDeviceID(); err != nil {
		return fmt.Errorf("store: device_id index migration: %w", err)
	}
	if err := db.AutoMigrate(AllModels()...); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	if err := db.backfillDeviceID(); err != nil {
		return fmt.Errorf("store: device_id backfill: %w", err)
	}
	// Seed/refresh the shared Glocker:* tag taxonomy (idempotent).
	if err := db.SeedSystemTags(); err != nil {
		return fmt.Errorf("store: seed system tags: %w", err)
	}
	// Migrate legacy single-tag rules into the registry model (idempotent).
	if err := db.backfillRulesToTags(); err != nil {
		return fmt.Errorf("store: rules->rule_tags backfill: %w", err)
	}
	return nil
}

// backfillDeviceID sets device_id on rows created before the column existed.
// AutoMigrate adds device_id as NULL (it won't force a default on an existing
// table), but leaving pre-multi-device rows NULL is a data hazard: the live
// agent syncs under its API token's id, its per-device high-water mark for that
// id is 0, so it would re-backfill its whole history and DUPLICATE every
// historical row. Backfill each account's existing rows to its single device —
// its API token (min id if somehow several) — so the agent's cursor matches and
// nothing re-sends. Tokenless/local accounts get 0, which is what the local
// agent stamps. Idempotent (only touches NULLs) and portable (no ALTER; GORM's
// uint field never writes NULL again once these are filled).
func (db *DB) backfillDeviceID() error {
	for _, tbl := range []string{"violations", "unblocks", "lifecycle_events", "usage_samples", "heartbeats"} {
		// No target-table alias (SQLite rejects `UPDATE t AS x`); the correlated
		// subquery qualifies the outer column by table name, valid on both engines.
		// Only NULLs are touched, so this is a no-op on a fresh DB and idempotent.
		sql := "UPDATE " + tbl + " SET device_id = " +
			"COALESCE((SELECT MIN(k.id) FROM api_tokens k WHERE k.user_id = " + tbl + ".user_id), 0) " +
			"WHERE device_id IS NULL"
		if err := db.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateDeviceID drops the per-source unique indexes so AutoMigrate rebuilds
// them to include the new device_id column. Each ingest row is now scoped to a
// device (the authenticating API token), so the natural keys gain device_id:
// usage/heartbeat become (user_id, device_id, ts), lifecycle
// (user_id, device_id, ts, type), violations/unblocks (user_id, device_id,
// record_id). Without this, multiple agents on one account collided on the old
// (user_id, ts) keys and silently dropped each other's time-aligned rows.
//
// AutoMigrate adds device_id (default 0) before recreating the indexes, so
// existing single-device rows keep working: they were unique on (user_id, ts),
// so (user_id, 0, ts) is unique too. Idempotent (drop-if-exists then rebuild).
func (db *DB) migrateDeviceID() error {
	for _, idx := range []string{"ux_violation", "ux_unblock", "ux_lifecycle", "ux_usage", "ux_heartbeat"} {
		// Raw SQL for the same Postgres/SQLite portability reason as the migrations
		// above; names are fixed literals.
		if err := db.Exec("DROP INDEX IF EXISTS " + idx).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateRecordIDIndexes drops the stale natural-key unique indexes for the
// tables whose idempotency key moved to record_id (violations, unblocks) when
// records became content-blind — the old key columns (keyword/url, domain) are
// now ciphertext that re-encrypts every sync. GORM's AutoMigrate won't ALTER an
// existing same-named index, so the new (user_id, record_id) definition only
// takes effect if the old index is dropped first; AutoMigrate then rebuilds it.
//
// Idempotent (drop-if-exists, then AutoMigrate recreates) and safe over existing
// rows: their record_id is NULL, and NULLs don't collide in a unique index. Note
// ux_violation is also dropped by migrateURLHash; dropping again here is harmless.
func (db *DB) migrateRecordIDIndexes() error {
	for _, idx := range []string{"ux_violation", "ux_unblock"} {
		// Raw SQL, not Migrator().DropIndex — the latter emits invalid
		// `DROP INDEX CURRENT_SCHEMA()."name"` on Postgres. `DROP INDEX IF EXISTS`
		// is valid on both Postgres and SQLite; the names are fixed literals.
		if err := db.Exec("DROP INDEX IF EXISTS " + idx).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateURLHash upgrades the violations and ignored tables from a URL-in-the-
// unique-index schema to a hashed one. For each table it adds url_hash if
// missing, backfills it from the url, then drops the old index so AutoMigrate
// rebuilds it on url_hash. It never deletes rows; the backfill is deterministic,
// so it can't create a conflict the old (…, url) index didn't already prevent.
func (db *DB) migrateURLHash() error {
	for _, t := range []struct {
		model any
		index string
	}{
		{&Violation{}, "ux_violation"},
		{&IgnoredViolation{}, "ux_ignored"},
	} {
		if !db.Migrator().HasTable(t.model) {
			continue // fresh DB: AutoMigrate builds the hashed schema directly
		}
		if !db.Migrator().HasColumn(t.model, "URLHash") {
			if err := db.Migrator().AddColumn(t.model, "URLHash"); err != nil {
				return err
			}
		}
		if err := db.backfillURLHash(t.model); err != nil {
			return err
		}
		// Drop the stale (url-based) index so AutoMigrate recreates it on url_hash.
		// Raw SQL, not Migrator().DropIndex — the latter emits invalid
		// `DROP INDEX CURRENT_SCHEMA()."name"` on Postgres. `DROP INDEX IF EXISTS
		// <name>` is valid on both Postgres and SQLite; the name is a fixed literal.
		if err := db.Exec("DROP INDEX IF EXISTS " + t.index).Error; err != nil {
			return err
		}
	}
	return nil
}

// backfillURLHash fills url_hash for any rows that lack it, computing it from the
// stored url. Runs in one transaction.
func (db *DB) backfillURLHash(model any) error {
	type idURL struct {
		ID  uint
		URL string
	}
	var rows []idURL
	if err := db.Model(model).
		Where("url_hash IS NULL OR url_hash = ''").
		Select("id", "url").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return db.DB.Transaction(func(tx *gorm.DB) error {
		for _, r := range rows {
			if err := tx.Model(model).Where("id = ?", r.ID).
				Update("url_hash", hashURL(r.URL)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Close releases the underlying connection pool.
func (db *DB) Close() error {
	sqlDB, err := db.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
