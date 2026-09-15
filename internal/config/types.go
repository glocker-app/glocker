package config

// Constants used throughout the glocker application
const (
	InstallPath          = "/usr/local/bin/glocker"
	GlocklockInstallPath = "/usr/local/bin/glocklock"
	GlockpeekInstallPath = "/usr/local/bin/glockpeek"
	GlockdocInstallPath  = "/usr/local/bin/glockdoc"
	HeartbeatCronPath    = "/etc/cron.d/glocker-doc"
	GlockerConfigFile    = "/etc/glocker/config.yaml"
	// GlockerConfDDir holds the generated blocklist files that config.yaml pulls
	// in via `!include conf.d/<file>`. Installed alongside the config.
	GlockerConfDDir = "/etc/glocker/conf.d"

	// DefaultGlockpeekURL is where the syncer ships records when Sync.GlockpeekURL
	// is unset — the local glockpeek on its default port.
	DefaultGlockpeekURL = "http://127.0.0.1:4317"
	// DefaultEncryptKeyFile is where the agent reads the master key (base64 of 32
	// bytes) when sync.encrypt is on and sync.encrypt_key_file is unset. Root-only;
	// see docs/e2e-design.md.
	DefaultEncryptKeyFile = "/etc/glocker/e2e.key"
	// DefaultSyncIntervalSeconds is the incremental sync cadence default.
	DefaultSyncIntervalSeconds = 300
	HostsMarkerStart           = "### GLOCKER START ###"
	SudoersPath                = "/etc/sudoers"
	SudoersBackup              = "/etc/sudoers.glocker.backup"
	SudoersMarker              = "# GLOCKER-MANAGED"
	SystemdFile                = "./extras/glocker.service"
	GlockerSock                = "/var/glocker/glocker.sock" // root-owned 0700 dir gates access
	EmailCooldownMinutes       = 15                          // Minimum time between emails for the same event type
)

// TimeWindow represents a time-based blocking window with specific days.
type TimeWindow struct {
	Start string   `yaml:"start"` // HH:MM format
	End   string   `yaml:"end"`   // HH:MM format
	Days  []string `yaml:"days"`  // Mon, Tue, Wed, Thu, Fri, Sat, Sun
}

// Domain represents a domain to be blocked with its blocking rules.
// BlockWindows names the semantic explicitly: the domain is BLOCKED during
// these windows (and accessible outside them). An empty list means always
// blocked.
type Domain struct {
	Name         string       `yaml:"name"`
	BlockWindows []TimeWindow `yaml:"block_windows,omitempty"`
	LogBlocking  bool         `yaml:"log_blocking,omitempty"`
	Unblockable  bool         `yaml:"unblockable,omitempty"` // Set to true to allow temporary unblocking (default: false = permanent)
}

// SudoersConfig controls sudo access restrictions.
// AllowWindows names the semantic explicitly: sudo is ALLOWED during these
// windows (and blocked outside them). An empty list means sudo is always
// blocked while sudoers enforcement is enabled.
type SudoersConfig struct {
	Enabled            bool         `yaml:"enabled"`
	User               string       `yaml:"user"`
	AllowedSudoersLine string       `yaml:"allowed_sudoers_line"`
	BlockedSudoersLine string       `yaml:"blocked_sudoers_line"`
	AllowWindows       []TimeWindow `yaml:"allow_windows"`
}

// MailConfig configures outbound transactional email (Mailgun-backed) — used by
// glockpeek for account-verification mail. Disabled unless Enabled with a domain
// + api key; From defaults to noreply@<domain>. Unlike the daemon's legacy
// accountability mail, the domain here is configurable (not hardcoded).
type MailConfig struct {
	Enabled bool   `yaml:"enabled"`
	Domain  string `yaml:"domain"` // Mailgun sending domain, e.g. mg.glockerapp.com
	APIKey  string `yaml:"api_key"`
	From    string `yaml:"from"`   // e.g. noreply@mg.glockerapp.com
	Region  string `yaml:"region"` // "us" (default) or "eu"
}

// DomainsConfig groups everything about domain blocking: the on/off switch, the
// hosts file, the browsers to close on a block, the nudge run when a blocked
// domain is loaded, the temporary-unblock rules, and the block list itself.
type DomainsConfig struct {
	Enabled     bool     `yaml:"enabled"` // route blocked domains to 127.0.0.1 in /etc/hosts
	HostsPath   string   `yaml:"hosts_path"`
	KillOnBlock []string `yaml:"kill_on_block,omitempty"` // browsers killed on `glocker -block` to flush their DNS cache
	// BlockedAccessCommand runs when you actually load a blocked domain (an
	// audible/visual nudge). Empty = none. Was the old top-level web_tracking.command.
	BlockedAccessCommand string           `yaml:"blocked_access_command"`
	Unblocking           UnblockingConfig `yaml:"unblocking"`
	List                 []Domain         `yaml:"list"`
}

// ContentConfig groups browser-extension content monitoring: logging the
// extension's violation reports, and the keyword lists it watches for.
type ContentConfig struct {
	Monitoring ContentMonitoringConfig `yaml:"monitoring"`
	Keywords   ExtensionKeywordsConfig `yaml:"keywords"`
}

// PanicConfig is panic mode (`glocker -panic`): an emergency self-imposed offline
// lockdown that suspends the machine for Duration minutes, re-suspending on an
// early wake. When disabled, `glocker -panic` is refused.
type PanicConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Command  string `yaml:"command"`
	Duration int    `yaml:"duration"` // minutes to stay suspended (0 → 30)
}

// LogDir holds all of glocker's runtime log files (mutable state → /var).
const LogDir = "/var/log/glocker"

// Default log-file paths, used when the matching config field is unset. Defined
// once here; reports / web / syncer / install all reference these.
const (
	DefaultUnblocksLogFile  = LogDir + "/unblocks.log"
	DefaultReportsLogFile   = LogDir + "/reports.log"
	DefaultLifecycleLogFile = LogDir + "/lifecycle.log"
	DefaultUsageLogFile     = LogDir + "/usage.jsonl"
	DefaultHeartbeatLogFile = LogDir + "/heartbeat.jsonl"
)

// DefaultUsageInterval is the usage-monitor sampling cadence (seconds).
const DefaultUsageInterval = 60

// UsageMonitorConfig controls the arbtt-style desktop usage tracker: it samples
// the focused window (class + title) and idle time to a JSONL log, which the
// /stats dashboard reads and categorizes. Runs only when Enabled.
type UsageMonitorConfig struct {
	Enabled bool `yaml:"enabled"`
	// LogFile is the JSONL sample log (default DefaultUsageLogFile).
	LogFile string `yaml:"log_file"`
	// IntervalSeconds between samples (default DefaultUsageInterval).
	IntervalSeconds int `yaml:"interval_seconds"`
	// Display is the X display to sample, e.g. ":0"; empty uses $DISPLAY. The
	// daemon runs as root, so this (and XAuthority) usually must be set to reach
	// the user's session.
	Display string `yaml:"display"`
	// XAuthority is the path to the user's X authority cookie (optional).
	XAuthority string `yaml:"xauthority"`
	// User is the desktop user whose session to track. On Wayland the daemon
	// (root) derives the D-Bus address from it (unix:path=/run/user/<uid>/bus).
	User string `yaml:"user"`
	// DBusAddress overrides the Wayland session-bus address. Empty derives it
	// from User; if User is also empty, the environment is used.
	DBusAddress string `yaml:"dbus_address"`
}

// ContentMonitoringConfig controls content/keyword monitoring via browser extension.
type ContentMonitoringConfig struct {
	Enabled bool   `yaml:"enabled"`
	LogFile string `yaml:"log_file"`
}

// ExtensionKeywordsConfig defines keywords for browser extension monitoring.
type ExtensionKeywordsConfig struct {
	URLKeywords     []string `yaml:"url_keywords"`
	ContentKeywords []string `yaml:"content_keywords"`
	Whitelist       []string `yaml:"whitelist"`
}

// ViolationTrackingConfig controls violation threshold tracking and enforcement.
type ViolationTrackingConfig struct {
	Enabled           bool   `yaml:"enabled"`
	MaxViolations     int    `yaml:"max_violations"`
	TimeWindowMinutes int    `yaml:"time_window_minutes"`
	Command           string `yaml:"command"`
	LockDuration      string `yaml:"lock_duration"` // Duration for screen lock (e.g., "1m", "5m")
	Background        string `yaml:"background"`    // Path to PNG/JPG background image (X11 backend only)
}

// UnblockingConfig controls temporary unblocking behavior.
type UnblockingConfig struct {
	Reasons         []string `yaml:"reasons"`
	LogFile         string   `yaml:"log_file"`
	TempUnblockTime int      `yaml:"temp_unblock_time"` // Minutes
	// MaxPerDay caps how many domain unblocks are granted per local calendar day.
	// 0 (the default) means unlimited. Enforcement counts entries recorded in
	// LogFile today, so LogFile must be set for the cap to survive a daemon restart.
	MaxPerDay int `yaml:"max_per_day"`
}

// LifecycleConfig controls install/uninstall logging behavior.
type LifecycleConfig struct {
	LogFile string   `yaml:"log_file"`
	Reasons []string `yaml:"reasons"`
}

// MindfulUninstallConfig gates `glocker -uninstall` behind a metronome-paced
// typing challenge (see internal/mindful): characters of a random sentence are
// revealed one at a time and must be typed as they appear, which forces
// presence and breaks the automatic "reach for uninstall" habit path. Enabled
// is the top-level switch; the remaining fields tune the challenge. Any field
// left at zero falls back to the internal/mindful defaults (interval 1s,
// deadline 2s, grace 1.2s, 1 line).
type MindfulUninstallConfig struct {
	Enabled    bool     `yaml:"enabled"`
	IntervalMs int      `yaml:"interval_ms"`         // per-character reveal cadence
	DeadlineMs int      `yaml:"deadline_ms"`         // grace after a char is revealed before a miss resets
	GraceMs    int      `yaml:"grace_ms"`            // pause before the first character is revealed
	Lines      int      `yaml:"lines"`               // base sentences chained into one target (friction tier)
	Sentences  []string `yaml:"sentences,omitempty"` // optional custom sentence pool (empty = built-in pool)

	// Recency escalation: when there is a prior uninstall within RecencyHours in
	// the lifecycle ledger, the current uninstall is treated as a repeat and the
	// challenge is raised to RecentLines. This is what makes "once is fine, twice
	// is hard" work.
	RecencyHours int `yaml:"recency_hours"` // lookback window; 0 disables escalation
	RecentLines  int `yaml:"recent_lines"`  // sentences to chain on a repeat (0 = no escalation)
}

// HeartbeatConfig controls the glockdoc liveness watchdog. It is consumed at
// install time to write the root cron job (internal/install). The glockdoc
// binary itself takes its parameters from CLI flags baked into that cron line,
// so it never reads the config file — which matters because the config is
// removed on uninstall, exactly when we still want the watchdog recording.
type HeartbeatConfig struct {
	Enabled         bool   `yaml:"enabled"`
	IntervalMinutes int    `yaml:"interval_minutes"` // cron cadence (0 → 30)
	LogFile         string `yaml:"log_file"`         // heartbeat JSONL path (empty → DefaultHeartbeatLogFile)
	TimeoutSeconds  int    `yaml:"timeout_seconds"`  // socket probe timeout (0 → 3)
}

// ForbiddenProgram represents a program to be killed during blocking periods.
// KillWindows lists times when the program is KILLED; AllowWindows lists
// times when it is permitted. Precedence at evaluation time:
//   - In any KillWindow → killed.
//   - Else AllowWindows non-empty → killed iff not in any AllowWindow.
//   - Else both empty → always killed (legacy default).
//   - Else (only KillWindows, no match) → allowed.
type ForbiddenProgram struct {
	Name         string       `yaml:"name"`
	KillWindows  []TimeWindow `yaml:"kill_windows"`
	AllowWindows []TimeWindow `yaml:"allow_windows"`
	// Extendible permits a one-hour runtime grant via `glocker -extend`,
	// limited to one grant per rolling 24 hours per program.
	Extendible bool `yaml:"extendible,omitempty"`
}

// ProgramsConfig controls forbidden-program killing.
type ProgramsConfig struct {
	Enabled       bool               `yaml:"enabled"`
	CheckInterval int                `yaml:"check_interval_seconds"`
	List          []ForbiddenProgram `yaml:"list"`
}

// Config is the main configuration structure for glocker. It groups the three
// blocking features into their own stanzas (domains, programs, content) and
// keeps the self-contained ones flat.
type Config struct {
	LogLevel string `yaml:"log_level"`

	// Blocking mechanisms.
	Domains  DomainsConfig  `yaml:"domains"`
	Programs ProgramsConfig `yaml:"programs"`
	Content  ContentConfig  `yaml:"content"`
	Sudoers  SudoersConfig  `yaml:"sudoers"`

	// Uninstall friction & accountability.
	MindfulUninstall MindfulUninstallConfig `yaml:"mindful_uninstall"`
	Lifecycle        LifecycleConfig        `yaml:"lifecycle"`

	// Accountability & telemetry.
	ViolationTracking ViolationTrackingConfig `yaml:"violation_tracking"`
	UsageMonitor      UsageMonitorConfig      `yaml:"usage_monitor"`
	Heartbeat         HeartbeatConfig         `yaml:"heartbeat"`
	// Sync configures how the agent ships its local /var records to a glockpeek
	// instance. Local-first: recording and enforcement never depend on it, so a
	// glockpeek that's down or unreachable only delays the dashboard, nothing else.
	// glockpeek's own server settings (mode, listen, database, mail, …) live in a
	// separate file (see GlockpeekConfig / GlockpeekConfigFile).
	Sync SyncConfig `yaml:"sync"`

	// Panic mode.
	Panic PanicConfig `yaml:"panic"`
}

// SyncConfig controls the glocker->glockpeek syncer. The agent keeps recording
// to /var files as the source of truth; the syncer periodically pushes new
// records to glockpeek's ingest API (one-shot backfill at startup, then
// incremental). Idempotent, so it never loses or double-counts across retries.
type SyncConfig struct {
	Enabled bool `yaml:"enabled"`
	// GlockpeekURL is the base URL of the glockpeek instance
	// (default DefaultGlockpeekURL). Point it at a remote host to sync off-box.
	GlockpeekURL string `yaml:"glockpeek_url"`
	// Token is the ingest bearer token; required only when the target glockpeek
	// is in hosted mode. Empty for a local instance (open ingest).
	Token string `yaml:"token"`
	// IntervalSeconds is the incremental sync cadence (default
	// DefaultSyncIntervalSeconds).
	IntervalSeconds int `yaml:"interval_seconds"`
	// Encrypt is COMPULSORY when sync is enabled: you must state it explicitly —
	// true (encrypt each record's sensitive fields before upload, so a hosted
	// glockpeek only ever stores ciphertext; required for a hosted target) or false
	// (plaintext, only sensible for a local/loopback glockpeek with no untrusted
	// operator). It's a pointer so an omitted key is detectable and rejected by the
	// validator, rather than silently defaulting to plaintext and leaking content
	// to a hosted server. Use Encryption() to read it safely.
	Encrypt *bool `yaml:"encrypt"`
	// EncryptKeyFile is the master-key path; optional, defaults to
	// DefaultEncryptKeyFile when unset. Read only when Encrypt is true. Use
	// KeyFile() to read it with the default applied.
	EncryptKeyFile string `yaml:"encrypt_key_file"`
}

// Encryption reports whether the syncer should encrypt records before upload. It
// is nil-safe: an unset Encrypt reads as false (the validator rejects an unset
// Encrypt when sync is enabled, so in practice it's always explicit here).
func (s SyncConfig) Encryption() bool { return s.Encrypt != nil && *s.Encrypt }

// KeyFile resolves the master-key path, applying DefaultEncryptKeyFile when the
// config leaves EncryptKeyFile unset.
func (s SyncConfig) KeyFile() string {
	if s.EncryptKeyFile != "" {
		return s.EncryptKeyFile
	}
	return DefaultEncryptKeyFile
}

// DatabaseConfig selects glockpeek's DB backend. The abstraction is GORM, so
// switching from sqlite to postgres is just a driver + DSN change here — no
// query rewrites. Consumed by the standalone glockpeek process, not the daemon.
type DatabaseConfig struct {
	// Driver is "sqlite" (default) or "postgres".
	Driver string `yaml:"driver"`
	// DSN is the data source. For sqlite it's a file path
	// (default DefaultDatabaseDSN); for postgres a libpq/pgx connection string.
	DSN string `yaml:"dsn"`
}
