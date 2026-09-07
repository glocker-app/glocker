// Package store is glockpeek's persistence layer. It is dialect-agnostic via
// GORM: sqlite locally (pure-Go glebarez driver, cgo-free) and postgres for a
// hosted instance — switching is a driver + DSN change, no query rewrites.
//
// The models mirror the JSON shapes the dashboard already consumes
// (internal/stats data.go), so the sync/ingest path (glocker -> glockpeek) and
// the dashboard read path map onto them with minimal translation. Timestamps are
// epoch milliseconds, matching the rest of the stack.
//
// Multi-tenant: every stats row carries a UserID and all reads/writes are scoped
// to the authenticated account (the hosted model), so one glockpeek instance can
// serve many users without their data mixing. Natural-key unique indexes are
// therefore prefixed with user_id.
//
// Records that the dashboard derives (unmanaged spans from lifecycle events,
// downtime spans from heartbeats) are NOT stored — they stay computed.
package store

import "time"

// ── Accounts & credentials ──────────────────────────────

// User is a dashboard account. Email is the login identity. Passwords are stored
// as an argon2id-encoded hash (never plaintext); see auth.go.
//
// Username is a legacy column kept populated (= Email for real accounts, "local"
// for the implicit single-user account) so the original NOT NULL UNIQUE
// constraint on existing databases is satisfied without a destructive migration.
// It is not part of the API surface.
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"uniqueIndex" json:"email"`
	Username     string    `gorm:"uniqueIndex;not null" json:"-"`
	PasswordHash string    `gorm:"not null" json:"-"`
	Verified     bool      `json:"verified"` // email confirmed; false until the verify link is clicked
	// DeviceLimit caps how many ingest API tokens (connected devices) this account
	// may hold. 0 (the zero value / free tier) means DefaultFreeDevices; a negative
	// value means unlimited. Raised for paid accounts.
	DeviceLimit int       `json:"deviceLimit"`
	CreatedAt   time.Time `json:"createdAt"`
}

// VerificationToken is a single-use, expiring token emailed to a new account so
// it can confirm its address. Consumed (deleted) on successful verification.
type VerificationToken struct {
	Token     string    `gorm:"primaryKey" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"-"`
	ExpiresAt time.Time `gorm:"index" json:"-"`
	CreatedAt time.Time `json:"-"`
}

// Session is a browser login session (opaque random token in an httpOnly
// cookie). Expired rows are ignored and pruned lazily.
type Session struct {
	Token     string    `gorm:"primaryKey" json:"-"` // random, opaque
	UserID    uint      `gorm:"index;not null" json:"-"`
	ExpiresAt time.Time `gorm:"index" json:"-"`
	CreatedAt time.Time `json:"-"`
}

// APIToken is a long-lived bearer credential the glocker syncer uses to ingest.
// Only the hash is stored; the plaintext is shown once at creation. The token
// identifies which account the pushed data belongs to.
type APIToken struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `json:"name"`
	TokenHash  string     `gorm:"uniqueIndex;not null" json:"-"`
	UserID     uint       `gorm:"index;not null" json:"userId"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// ── Per-account stats (all scoped by UserID) ─────────────

// Violation is one content/URL-keyword report. Dedup is by RecordID, not by the
// content columns: under E2E, Keyword/URL/Domain are ciphertext that re-encrypts
// to fresh bytes on every sync, so they can't be the idempotency key. RecordID is
// a stable per-record fingerprint the agent computes over the plaintext (see
// e2e.RecordID); (UserID, RecordID) is the natural key.
type Violation struct {
	ID     uint  `gorm:"primaryKey" json:"-"`
	UserID uint  `gorm:"index;uniqueIndex:ux_violation,priority:1" json:"-"`
	TS     int64 `gorm:"index" json:"ts"`
	// RecordID is HMAC(masterKey, ts|keyword|url) under E2E, or sha256 of the same
	// in plaintext mode. The dedup key; set by the agent, opaque to the server.
	RecordID string `gorm:"uniqueIndex:ux_violation,priority:2" json:"recordId"`
	Keyword  string `json:"keyword"`
	// URLHash is sha256(URL). Vestigial for dedup now that RecordID is the key, but
	// still populated so plaintext-mode ignored-matching keeps working.
	URLHash string `json:"-"`
	URL     string `json:"url"`
	Type    string `json:"type"`
	Domain  string `json:"domain"`
}

// Unblock is one temporary-unblock event; RestoreTS is nil while still open.
// Dedup is by RecordID (see Violation) since Domain is encrypted under E2E.
type Unblock struct {
	ID        uint   `gorm:"primaryKey" json:"-"`
	UserID    uint   `gorm:"index;uniqueIndex:ux_unblock,priority:1" json:"-"`
	TS        int64  `gorm:"index" json:"ts"`
	RecordID  string `gorm:"uniqueIndex:ux_unblock,priority:2" json:"recordId"`
	Domain    string `json:"domain"`
	RestoreTS *int64 `json:"restoreTs"`
	Reason    string `json:"reason"`
}

// LifecycleEvent is an install/uninstall record (drives the UNMANAGED overlay).
type LifecycleEvent struct {
	ID     uint   `gorm:"primaryKey" json:"-"`
	UserID uint   `gorm:"index;uniqueIndex:ux_lifecycle,priority:1" json:"-"`
	TS     int64  `gorm:"index;uniqueIndex:ux_lifecycle,priority:2" json:"ts"`
	Type   string `gorm:"uniqueIndex:ux_lifecycle,priority:3" json:"type"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// UsageSample is one focused-window sample from the usage tracker. One row per
// (user, timestamp); the active window is flattened in and the full window list
// is dropped, same as the file-based reader.
type UsageSample struct {
	ID             uint   `gorm:"primaryKey" json:"-"`
	UserID         uint   `gorm:"index;uniqueIndex:ux_usage,priority:1" json:"-"`
	TS             int64  `gorm:"uniqueIndex:ux_usage,priority:2" json:"ts"`
	IdleMS         int64  `json:"idleMs"`
	ActiveClass    string `json:"activeClass"`
	ActiveInstance string `json:"activeInstance"`
	ActiveTitle    string `json:"activeTitle"`
	WindowCount    int    `json:"windowCount"`
}

// Heartbeat is one liveness sample recorded by glockdoc (drives downtime spans).
type Heartbeat struct {
	ID     uint  `gorm:"primaryKey" json:"-"`
	UserID uint  `gorm:"index;uniqueIndex:ux_heartbeat,priority:1" json:"-"`
	TS     int64 `gorm:"uniqueIndex:ux_heartbeat,priority:2" json:"ts"`
	Alive  bool  `json:"alive"`
}

// Rule is one usage-categorization rule (optional program/title regex -> tag).
// Dashboard-local state (created on glockpeek, not synced from glocker).
type Rule struct {
	ID      uint   `gorm:"primaryKey" json:"-"`
	UserID  uint   `gorm:"index" json:"-"`
	Program string `json:"program"`
	Title   string `json:"title"`
	Tag     string `json:"tag"`
}

// TagColor maps a tag to its display colour (#rrggbb), per account.
type TagColor struct {
	ID     uint   `gorm:"primaryKey" json:"-"`
	UserID uint   `gorm:"index;uniqueIndex:ux_tagcolor,priority:1" json:"-"`
	Tag    string `gorm:"uniqueIndex:ux_tagcolor,priority:2" json:"tag"`
	Color  string `json:"color"`
}

// SyncStatus records when the account last received an ingest batch (the syncer
// push from the glocker daemon) and how many records that batch carried. One row
// per account; the dashboard shows it as a "last sync" panel.
type SyncStatus struct {
	UserID         uint      `gorm:"primaryKey" json:"-"`
	LastIngestAt   time.Time `json:"-"`
	LastViolations int       `json:"violations"`
	LastUnblocks   int       `json:"unblocks"`
	LastLifecycle  int       `json:"lifecycle"`
	LastUsage      int       `json:"usage"`
	LastHeartbeat  int       `json:"heartbeat"`
}

// IgnoredViolation marks a report line as a false positive so the dashboard
// stops counting it. Matched on (UserID, TS, Keyword, URL).
type IgnoredViolation struct {
	ID      uint   `gorm:"primaryKey" json:"-"`
	UserID  uint   `gorm:"index;uniqueIndex:ux_ignored,priority:1" json:"-"`
	TS      int64  `gorm:"uniqueIndex:ux_ignored,priority:2" json:"ts"`
	Keyword string `gorm:"uniqueIndex:ux_ignored,priority:3" json:"keyword"`
	// URLHash is sha256(URL) — see Violation.URLHash for why the hash is indexed
	// rather than the raw URL.
	URLHash string `gorm:"uniqueIndex:ux_ignored,priority:4" json:"-"`
	URL     string `json:"url"`
	Domain  string `json:"domain"`
}

// KeyVault holds the account's passphrase-wrapped master key for end-to-end
// encryption (see internal/e2e / docs/e2e-design.md). The server stores it
// opaquely — it's wrapped material (never a key), and the server can't open it.
// One row per account; Vault is the vault JSON verbatim from the client.
type KeyVault struct {
	UserID uint   `gorm:"primaryKey" json:"-"`
	Vault  string `json:"-"`
}

// AllModels is the migration set. Kept in one place so Migrate and tests stay in
// sync.
func AllModels() []any {
	return []any{
		&User{}, &Session{}, &APIToken{}, &VerificationToken{},
		&Violation{}, &Unblock{}, &LifecycleEvent{}, &UsageSample{},
		&Heartbeat{}, &Rule{}, &TagColor{}, &IgnoredViolation{},
		&SyncStatus{}, &KeyVault{},
	}
}
