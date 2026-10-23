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
	ID           uint   `gorm:"primaryKey" json:"id"`
	Email        string `gorm:"uniqueIndex" json:"email"`
	Username     string `gorm:"uniqueIndex;not null" json:"-"`
	PasswordHash string `gorm:"not null" json:"-"`
	Verified     bool   `json:"verified"` // email confirmed; false until the verify link is clicked
	// DeviceLimit caps how many ingest API tokens (connected devices) this account
	// may hold. 0 (the zero value / free tier) means DefaultFreeDevices; a negative
	// value means unlimited. Raised for paid accounts.
	DeviceLimit int `json:"deviceLimit"`
	// RulesSeeded is set once the account has had the Glocker starter rules seeded
	// (on activation/creation), so it never re-seeds and deleted starters stay gone.
	RulesSeeded bool      `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`

	// Subscription state for the hosted paid tier. The Merchant-of-Record
	// (Polar) is the source of truth: these fields are written only by the
	// billing webhook and exist for display + idempotency. DeviceLimit above is
	// the actual enforcement lever the webhook raises/lowers. We deliberately
	// store no payer name/email/card here — that PII lives with the MoR, keeping
	// payment identity severed from usage data.
	SubStatus     string    `json:"subStatus"`      // "" free, "active", "canceled" (access until period end), "expired"
	SubID         string    `gorm:"index" json:"-"` // opaque provider subscription id; indexed for webhook lookups
	SubCurrentEnd time.Time `json:"subCurrentEnd"`  // current period end / access-until
	// SubEventAt is the provider's modified_at of the last webhook we applied. The
	// webhook handler drops any event older than this so an out-of-order or
	// re-delivered subscription.updated can't undo a newer cancel/revoke.
	SubEventAt time.Time `json:"-"`
	// SubProductID is the provider product the subscription is currently on, so the
	// dashboard can show the plan (monthly/annual) and offer an in-app plan switch.
	SubProductID string `json:"-"`
	// SubSyncedAt is when we last reconciled this subscription against the provider
	// (on dashboard load). The reconcile is throttled off this so it doesn't fire on
	// every /api/me, and it self-heals a missed or reordered webhook.
	SubSyncedAt time.Time `json:"-"`
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
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `gorm:"uniqueIndex;not null" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
	// LastUsedAt is the last successful ingest with this token (the device's
	// last-seen). RevokedAt, when set, disables the token: it is rejected at
	// ingest and no longer counts against the device cap, but the row is kept so
	// the device's historical stats rows keep a name in the dashboard.
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

// ── Per-account stats (all scoped by UserID) ─────────────

// Violation is one content/URL-keyword report. Dedup is by RecordID, not by the
// content columns: under E2E, Keyword/URL/Domain are ciphertext that re-encrypts
// to fresh bytes on every sync, so they can't be the idempotency key. RecordID is
// a stable per-record fingerprint the agent computes over the plaintext (see
// e2e.RecordID); (UserID, RecordID) is the natural key.
type Violation struct {
	ID uint `gorm:"primaryKey" json:"-"`
	// UserID + DeviceID scope every row to one account's one device. DeviceID is
	// stamped server-side from the authenticating API token (one token = one
	// device); the agent never sends it. It is part of the dedup key so the same
	// slip on two devices is two rows, while one device's resends still dedup.
	UserID   uint  `gorm:"index;uniqueIndex:ux_violation,priority:1" json:"-"`
	DeviceID uint  `gorm:"index;uniqueIndex:ux_violation,priority:2" json:"-"`
	TS       int64 `gorm:"index" json:"ts"`
	// RecordID is HMAC(masterKey, ts|keyword|url) under E2E, or sha256 of the same
	// in plaintext mode. The dedup key; set by the agent, opaque to the server.
	RecordID string `gorm:"uniqueIndex:ux_violation,priority:3" json:"recordId"`
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
	DeviceID  uint   `gorm:"index;uniqueIndex:ux_unblock,priority:2" json:"-"`
	TS        int64  `gorm:"index" json:"ts"`
	RecordID  string `gorm:"uniqueIndex:ux_unblock,priority:3" json:"recordId"`
	Domain    string `json:"domain"`
	RestoreTS *int64 `json:"restoreTs"`
	Reason    string `json:"reason"`
}

// LifecycleEvent is an install/uninstall record (drives the UNMANAGED overlay).
type LifecycleEvent struct {
	ID       uint   `gorm:"primaryKey" json:"-"`
	UserID   uint   `gorm:"index;uniqueIndex:ux_lifecycle,priority:1" json:"-"`
	DeviceID uint   `gorm:"index;uniqueIndex:ux_lifecycle,priority:2" json:"-"`
	TS       int64  `gorm:"index;uniqueIndex:ux_lifecycle,priority:3" json:"ts"`
	Type     string `gorm:"uniqueIndex:ux_lifecycle,priority:4" json:"type"`
	Reason   string `json:"reason"`
	Note     string `json:"note"`
}

// UsageSample is one focused-window sample from the usage tracker. One row per
// (user, timestamp); the active window is flattened in and the full window list
// is dropped, same as the file-based reader.
type UsageSample struct {
	ID             uint   `gorm:"primaryKey" json:"-"`
	UserID         uint   `gorm:"index;uniqueIndex:ux_usage,priority:1" json:"-"`
	DeviceID       uint   `gorm:"index;uniqueIndex:ux_usage,priority:2" json:"-"`
	TS             int64  `gorm:"uniqueIndex:ux_usage,priority:3" json:"ts"`
	IdleMS         int64  `json:"idleMs"`
	ActiveClass    string `json:"activeClass"`
	ActiveInstance string `json:"activeInstance"`
	ActiveTitle    string `json:"activeTitle"`
	WindowCount    int    `json:"windowCount"`
}

// Heartbeat is one liveness sample recorded by glockdoc (drives downtime spans).
type Heartbeat struct {
	ID       uint  `gorm:"primaryKey" json:"-"`
	UserID   uint  `gorm:"index;uniqueIndex:ux_heartbeat,priority:1" json:"-"`
	DeviceID uint  `gorm:"index;uniqueIndex:ux_heartbeat,priority:2" json:"-"`
	TS       int64 `gorm:"uniqueIndex:ux_heartbeat,priority:3" json:"ts"`
	Alive    bool  `json:"alive"`
}

// Rule is one usage-categorization rule (optional program/title regex -> tags).
// Dashboard-local state (created on glockpeek, not synced from glocker). Position
// orders the rules for first-match-wins precedence (lower runs first). Tags are
// attached through the rule_tags registry join, not stored on the rule itself.
type Rule struct {
	ID       uint   `gorm:"primaryKey" json:"-"`
	UserID   uint   `gorm:"index" json:"-"`
	Position int    `json:"position"`
	Program  string `json:"program"`
	Title    string `json:"title"`
}

// Tag is one entry in the tag registry. Two scopes: "system" (the shared,
// vendor-owned Glocker:* taxonomy, UserID 0, the same for every account) and
// "user" (an account's own tags). See docs/tags-and-rules.md. Behavior is "" or
// "hide" (a Glocker:hide tag whose windows are dropped from usage totals).
type Tag struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Scope       string `gorm:"uniqueIndex:ux_tag,priority:1" json:"scope"`
	UserID      uint   `gorm:"index;uniqueIndex:ux_tag,priority:2" json:"-"`
	Namespace   string `gorm:"uniqueIndex:ux_tag,priority:3" json:"namespace"`
	Name        string `gorm:"uniqueIndex:ux_tag,priority:4" json:"name"`
	Color       string `json:"color"`
	Distraction bool   `json:"distraction"`
	Behavior    string `json:"behavior,omitempty"`
}

// TagOverride records an account's distraction choice for a system tag, so a
// user can flip a Glocker tag's distraction flag without forking the shared tag.
type TagOverride struct {
	UserID      uint `gorm:"uniqueIndex:ux_tagoverride,priority:1" json:"-"`
	TagID       uint `gorm:"uniqueIndex:ux_tagoverride,priority:2" json:"tagId"`
	Distraction bool `json:"distraction"`
}

// RuleTag links a rule to the tags it attaches (at most one per namespace,
// enforced app-side). Many-to-many between rules and the registry.
type RuleTag struct {
	RuleID uint `gorm:"uniqueIndex:ux_ruletag,priority:1" json:"ruleId"`
	TagID  uint `gorm:"uniqueIndex:ux_ruletag,priority:2" json:"tagId"`
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
	// Encrypted mirrors the most recent ingest batch's marker: whether the agent is
	// encrypting sensitive fields. The dashboard reads it (via /api/me) to decide
	// whether records need decrypting in the browser before rendering.
	Encrypted bool `json:"-"`
}

// IgnoredViolation marks a report line as a false positive so the dashboard stops
// counting it. Keyed on the opaque RecordID (the same content-blind fingerprint
// the Violation dedups on) so the overlay stores no plaintext. The legacy
// content columns are retained (nullable, no longer written) for old rows.
type IgnoredViolation struct {
	ID       uint   `gorm:"primaryKey" json:"-"`
	UserID   uint   `gorm:"index;uniqueIndex:ux_ignored,priority:1" json:"-"`
	RecordID string `gorm:"uniqueIndex:ux_ignored,priority:2" json:"recordId"`
	TS       int64  `gorm:"index" json:"ts"`
	Keyword  string `json:"-"`
	URLHash  string `json:"-"`
	URL      string `json:"-"`
	Domain   string `json:"-"`
}

// DeviceConfig is the latest configuration snapshot for one device, uploaded by
// the agent so the dashboard can render it (and, later, derive insights). It is
// content-blind like the stats rows: Config is an opaque blob — base64(nonce||
// ciphertext) of a small JSON subset under E2E, or that JSON in cleartext in
// plaintext/local mode — whose shape only the agent (producer) and browser
// (consumer) know; the server never parses it. Hash is a stable fingerprint of
// the plaintext subset so the agent can skip re-uploading an unchanged config: on
// connect it GETs the stored Hash and only POSTs when it differs. One row per
// (user, device), upserted — latest snapshot only, no history.
type DeviceConfig struct {
	UserID    uint   `gorm:"primaryKey" json:"-"`
	DeviceID  uint   `gorm:"primaryKey" json:"deviceId"`
	TS        int64  `json:"ts"`
	Encrypted bool   `json:"encrypted"`
	Hash      string `json:"hash"`
	Config    string `json:"config"`
	// Blocklist is the device's full blocked-domain list — gzip'd, then encrypted
	// (base64 nonce||ciphertext) or plain base64 in plaintext mode — kept opaque
	// like Config. It's large (~MBs), so it is NEVER returned by the default config
	// list; a separate request fetches it for the dashboard's membership checks and
	// domain search. BlocklistHash fingerprints the plaintext domain list so the
	// agent re-uploads it only when the blocklist actually changes (rarely).
	Blocklist     string `json:"-"`
	BlocklistHash string `json:"-"`
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
		&User{}, &Session{}, &APIToken{}, &VerificationToken{}, &PasswordResetToken{},
		&Violation{}, &Unblock{}, &LifecycleEvent{}, &UsageSample{},
		&Heartbeat{}, &Rule{}, &TagColor{}, &IgnoredViolation{},
		&SyncStatus{}, &KeyVault{}, &DeviceConfig{},
		&Tag{}, &TagOverride{}, &RuleTag{}, &DefaultRule{},
		&AccountabilityConfig{},
		&RateHit{}, &CaptchaUse{}, &SchedulerLease{},
	}
}

// DefaultRule is one template Glocker starter rule. When the table has any rows,
// SeedAccountRules seeds new accounts from these instead of the built-in
// glockerStarterRules. It's a hosted-only asset: the rows are loaded from a
// (gitignored) tags.json via `glockpeek -import-rules`, so the open-source build
// ships only the minimal built-in defaults. Tag is a Glocker tag NAME.
type DefaultRule struct {
	ID       uint   `gorm:"primaryKey" json:"-"`
	Position int    `json:"-"`
	Program  string `json:"program"`
	Title    string `json:"title"`
	Tag      string `json:"tag"`
}
