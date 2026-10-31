package store

import "gorm.io/gorm/clause"

// CurrentSchemaVersion is the schema version this build migrates to. It's a
// monotonic, hand-bumped marker (not a count of historical migrations): bump it
// whenever a schema change ships, so a deployed DB can report what it's on via
// /healthz and drift from the binary is visible. AutoMigrate still does the
// actual DDL; this number only records that it ran.
const CurrentSchemaVersion = 1

// stampSchemaVersion records CurrentSchemaVersion in the single-row schema_meta
// table. Called at the end of Migrate (after the DDL succeeds), so the stamp
// only advances once the schema is actually in place. Upsert on the fixed id so
// it's idempotent across both engines.
func (db *DB) stampSchemaVersion() error {
	return db.Clauses(clause.OnConflict{UpdateAll: true}).
		Create(&SchemaMeta{ID: 1, Version: CurrentSchemaVersion}).Error
}

// SchemaVersion returns the version the DB was last migrated to, or 0 if the
// marker is missing (never migrated, or a node that booted with SkipMigrate
// against a pre-marker DB). Read-only; safe on the /healthz path.
func (db *DB) SchemaVersion() int {
	var m SchemaMeta
	if err := db.First(&m, 1).Error; err != nil {
		return 0
	}
	return m.Version
}
