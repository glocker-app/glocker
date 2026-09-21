# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Glocker is a Linux-based distraction blocking system written in Go. It blocks distracting websites and applications using multiple enforcement mechanisms to make bypassing difficult. The tool runs as a privileged setuid systemd service and uses Unix sockets for IPC.

**Architecture:** Modular Go application with packages in `internal/` directory (see File Map below)

## Working conventions

- **Keep `PLAN.md` current.** When an item is closed, update `PLAN.md` to reflect
  it (flip the checkbox, add the commit ref / one-line result). If unsure whether
  an item is actually closed, ask first; update it once given the go-ahead.
- **Cleartext residuals are a settled decision, not an open risk.** The metadata
  that stays clear on glockpeek — `recordId` (HMAC), `idle_ms`, `window_count`,
  `violation.type`, `lifecycle.type`/`reason`, and the structural `ts` /
  `heartbeat.alive` / `restore_ts` — is consciously accepted and documented in
  `docs/habits.md`. Content is sealed; this metadata is not identity/content.
  Don't keep re-raising it as a concern.

## Build and Test Commands

```bash
# Build all binaries (glocker, glockpeek, glocklock, glockdoc, ...)
make build-all
# or just the agent:
go build ./cmd/glocker

# Check current status (blocking state, active domains, violations)
glocker -status

# Show configuration info (domains, programs, keywords)
glocker -info

# Install as systemd service with setuid privileges
glocker -install

# Uninstall and cleanup all protections (a reason is required)
glocker -uninstall "reason"
```

## File Map - Where to Find What

### Entry Point
- **`cmd/glocker/main.go`** - CLI entry point
  - Command-line flag definitions (`-status`, `-watch`, `-info`, `-version`, `-connect`,
    `-extend`, `-purge`, `-security-check`, `-install`, `-uninstall`, `-note`, `-daemon`,
    `-block`, `-block-app`, `-unblock`, `-add-keyword`, `-panic`, `-lock`, `-force`,
    `-resync` [+`-resync-since`])
  - Flag handlers with socket communication
  - Default behavior: show status or help
  - Daemon startup logic
- **`cmd/glocker/watch.go`** - `-watch` live-refreshing status view (TTY only)

### Configuration (`internal/config/`)
- **`types.go`** - Configuration structs and constants
  - `Config` struct with all settings
  - `Domain`, `TimeWindow`, `SudoersConfig`, `ForbiddenProgram`, `LifecycleConfig` structs
- **`loader.go`** - YAML loading
  - `LoadConfig()` - Loads from `/etc/glocker/config.yaml`
  - `SetupLogging()` - Configures log output
- **`persist.go`** - Input validators for names accepted over the socket
  - `ValidateDomainName()` / `ValidateProgramName()` - used by `-block` / `-block-app`
  - Note: runtime commands (`-block`, `-block-app`, `-add-keyword`) apply **in memory only**; they do NOT write the config file. `conf/conf.yaml` (plus the includes under `conf/conf.d/`) is the source of truth, reinstalled onto `/etc/glocker/config.yaml` by `make install` / `make full-install`.
- **`include.go`** - Include mechanism: `conf/conf.yaml` pulls in blocklist files from `conf/conf.d/*.yaml`
- **`glockpeek.go`** - Loads glockpeek's separate server config (`GlockpeekConfig`)
- **`validator.go`** - `ValidateConfig()` - validates time windows and required fields

### CLI Commands (`internal/cli/`)
- **`commands.go`** - Command processors for socket requests
  - `GetStatusResponse()` - Generates formatted runtime status output
  - `GetInfoResponse()` - Formatted configuration info (domains, programs, keywords)
  - `ProcessUnblockRequest()` - Temporary domain unblocking
  - `ProcessBlockRequest()` - In-memory domain blocking (not persisted)
  - `ProcessBlockAppRequest()` - In-memory forbidden-program add
  - `ProcessExtendRequest()` - One-hour runtime grant for an extendible program
  - `ProcessPanicRequest()` - Panic mode activation
  - `renderUpcoming()` - the `upcoming · next 15m` section (see `internal/schedule`)
  - `formatTimeWindows()` - Helper for time window display
- **`commands_test.go`** - Unit tests for CLI commands

### Enforcement (`internal/enforcement/`)
- **`enforcement.go`** - `RunOnce()` main enforcement cycle
- **`hosts.go`** - `UpdateHosts()` modifies `/etc/hosts`, immutable file protection (chattr)
- **`sudoers.go`** - `UpdateSudoers()` controls sudo access
- **`domains.go`** - Domain block-window evaluation and runtime domain merging
- **`state.go`** - Enforcement-local runtime state (in-memory `-block` / `-block-app` additions)

### IPC / Socket Communication (`internal/ipc/`)
- **`server.go`** - Unix socket server for daemon communication
  - `SetupCommunication()` - Creates socket at `/var/glocker/glocker.sock`
  - `HandleConnection()` - Processes socket commands
  - Socket command handlers (the `case` list in `HandleConnection`):
    - `ping` - Liveness check
    - `status` - Live runtime status query
    - `info` - Configuration info
    - `unblock` - Temporary unblock
    - `block` - Domain block, in memory only
    - `block-app` - Add program to forbidden-programs list (killed on sight, 24/7)
    - `panic` - Panic mode
    - `extend` - One-hour grant for an extendible forbidden program
    - `lock` - Force sudoers lock
    - `add-keyword` - Add monitoring keywords
    - `resync` - Rewind the syncer's cursors and backfill (re-push local records to glockpeek, overwriting its copies); optional `:sinceMS` payload
    - `uninstall` - Uninstall glocker
  - `processLockRequest()` - Lock processor
  - `processAddKeywordRequest()` - Keyword processor
  - `processUninstallRequest()` - Uninstall processor
- **`server_test.go`** - Socket server tests

### Installation (`internal/install/`)
- **`install.go`** - Installation and uninstallation logic
  - `InstallGlocker()` - Copies binary, config, systemd service
  - `RestoreSystemChanges()` - Reverts all system modifications
  - `RunningAsRoot()` - Privilege checks
  - Setuid permission setup
  - File immutability management

### Monitoring (`internal/monitoring/`)
- **`forbidden.go`** - Forbidden program monitoring
  - `MonitorForbiddenPrograms()` - Background goroutine
  - Process killing during time windows
  - Program detection via process name
- **`violations.go`** - Violation tracking
  - `MonitorViolations()` - Counts access attempts
  - Threshold-based action triggering
- **`panic.go`** - Panic mode monitoring
  - System suspend/resume detection
  - Re-suspension on early wake

(There is no tamper-detection module — the `tamper_detection` config key was removed and is now rejected by the loader. The heartbeat gap seen on glockpeek is the real tamper signal.)

### Web Server (`internal/web/`)
- **`server.go`** - HTTP/HTTPS server for browser extension
  - `StartWebTrackingServer()` - Starts on ports 80, 443
- **`handlers.go`** - HTTP endpoint handlers
  - `GET /keywords` - Returns monitoring keywords
  - `POST /report` - Content violation reports
  - `GET /keywords-stream` - Server-sent events (`HandleSSERequest`) for real-time keyword updates
  - `GET /blocked` - Blocked page display

### Notifications
- The agent sends **no notifications of its own** — no desktop toasts (the
  `internal/notify` package was removed) and no email. Local feedback on events
  comes from per-feature commands instead (`domains.blocked_access_command`, the
  violation-tracking lock command, etc.).
- **Accountability email is glockpeek's job** (only it can send when the agent is
  dead). It's configured in the agent's `conf.yaml` `accountability:` stanza
  (enabled, `buddy_emails`, `digest_every_hours`, `digest_when_quiet`,
  `alerts.went_dark`/`high_violations`) but **authored-there, sent-by-glockpeek**:
  the stanza rides the config sync as a **cleartext** `accountability` field (outside the
  encrypted snapshot blob, so glockpeek can read whom/how-often to email) and is
  stored account-level in `store.NotifyConfig` (latest sync wins).
  `internal/notifier` is the server-side scheduler (a goroutine in
  `cmd/glockpeek`): it sends a periodic digest (`internal/report` counts —
  `BuildWindow` over the cadence) and an immediate **went-dark** alert (newest
  heartbeat older than 15m — the dead-man's switch). Emails are **content-free**:
  under E2E glockpeek holds only ciphertext, so they carry counts + liveness,
  never *what* was accessed. Buddy-readable content (multi-recipient encryption +
  a buddy onboarding flow) is a separate, future feature.

### State Management (`internal/state/`)
- **`state.go`** - Thread-safe global state
  - Panic mode state (`panicUntil`)
  - Temporary unblocks tracking
  - `sync.RWMutex` for concurrency safety

### Near-future preview (`internal/schedule/`)
- **`upcoming.go`** - `Upcoming(cfg, now, horizon)` - which restrictions START
  inside the next `DefaultHorizon` (15m), used by the `upcoming` section of
  `-status`. It scans minute by minute and asks each mechanism the same question
  it answers during enforcement, just with a future timestamp
  (`enforcement.IsDomainBlockedAt`, `monitoring.ShouldKill`,
  `enforcement.IsSudoAllowed`), so the preview can't drift from reality.
  Loosening is **not** reported — when something frees up is what `-info` is for.
  It looks for RISING edges rather than the first flip of any kind, so a domain
  that unblocks in 3 minutes and re-blocks in 10 still reports the re-block.

### Utilities (`internal/utils/`)
- **`time.go`** - Time window evaluation
  - `IsInTimeWindow()` - HH:MM containment, handles overnight wraparound
  - `InAnyWindow(windows, t)` - the shared day-of-week + wraparound check used by
    domain, program and sudo window evaluation
- **`fs.go`** / **`network.go`** - file and network helpers

### Browser Extension (`extensions/firefox/`)
- **`manifest.json`** - Firefox extension manifest
- **`background.js`** - URL monitoring, keyword fetching
- **`content.js`** - Page content scanning
- **`blocked.html`** - Blocked page template

### Configuration & Resources
- **`conf/conf.yaml`** - Working configuration (small; pulls in blocklists via includes)
- **`conf/conf.yaml.sample`** - Sample config committed alongside `conf.yaml`; "the sample" means BOTH
- **`conf/conf.d/*.yaml`** - Included blocklist files (~16MB of domain lists: hagezi, stevenblack, shadowwhisperer, etc.)
- **`conf/glockpeek.yaml.sample`** / **`conf/usage-rules.sample.json`** - Sample glockpeek + usage-rule configs
- **`extras/glocker.service`** / **`extras/glockpeek.service`** - Systemd service definitions

### Documentation
- **`CLAUDE.md`** - This file - developer guidance
- **`docs/`** - `architecture.md`, `installation.md`, `bypasses.md`, `e2e-design.md`, and
  **`docs/openapi.yaml`** (the OpenAPI spec for the HTTP APIs)

## Quick Reference: Common Tasks

| Task | Primary File | Key Function |
|------|-------------|--------------|
| Add new CLI flag | `cmd/glocker/main.go` | Add flag definition + handler |
| Modify status output | `internal/cli/commands.go` | `GetStatusResponse()` |
| Change blocking logic | `internal/enforcement/hosts.go` | `UpdateHosts()` |
| Add socket command | `internal/ipc/server.go` | Add case in `HandleConnection()` |
| Modify installation | `internal/install/install.go` | `InstallGlocker()` |
| Add web endpoint | `internal/web/handlers.go` | Add handler function |
| Change time windows | `internal/config/types.go` | `TimeWindow` struct |
| Add monitoring feature | `internal/monitoring/` | Create new file or extend existing |

## Common Development Workflows

### Running Commands
```bash
# Temporarily unblock domains with reason
sudo glocker -unblock "reddit.com,youtube.com:work"

# Add domains to the block list (in memory only, until restart/reinstall)
sudo glocker -block "example.com,another.com"

# Add keywords to monitoring lists
sudo glocker -add-keyword "keyword1,keyword2"

```
There is no runtime config-reload command; config changes take effect on
reinstall (`make install` / `make full-install`, which restarts the daemon).

### To test the program, 
#### Install new version
1. `sudo glocker -uninstall "testing"`
2. `make build-all` (or `go build ./cmd/glocker`)
3. `sudo ./glocker -install`
4. Run `glocker` to get status.

#### Things to verify
1. Killing of forbidden programs
2. Violation trackin
3. Making sure that blocked domains are in `/etc/hosts` properly
4. APIs for browser extension integration (confirm with curl)


#### Testing Browser Extension
The Firefox extension lives in `extensions/firefox/`. After making changes:
1. Navigate to `about:debugging#/runtime/this-firefox`
2. Load temporary extension from `extensions/firefox/manifest.json`
3. Check browser console for extension logs
4. Monitor `/var/log/glocker/reports.log` for content monitoring events

## Architecture

### Core Components

**Main Service (`cmd/glocker/main.go`)**
- Single Go binary that handles all blocking logic
- Runs as systemd service with setuid root permissions
- Config loaded from `/etc/glocker/config.yaml` (sample in `conf/conf.yaml`)
- Uses Unix socket `/var/glocker/glocker.sock` for runtime commands

**Enforcement Mechanisms** (configured independently via YAML)
1. **Hosts file blocking** - Modifies `/etc/hosts` with immutable flag
2. **Forbidden programs** - Kills processes during configured time windows
3. **Sudoers modification** - Restricts sudo access during blocking periods
4. **Browser extension** - Content/keyword monitoring via Firefox extension

**Firefox Extension** (`extensions/firefox/`)
- `background.js` - Monitors URL navigation, fetches keywords from glocker service
- `content.js` - Scans page content for problematic keywords
- Reports violations to glocker via HTTP POST to `http://127.0.0.1/report`

### Key Data Structures

All configuration is in YAML with these main structs (internal/config/types.go):

The real `Config` is **nested** (blocking mechanisms grouped into sub-structs),
not flat:

```go
type Config struct {
    LogLevel string
    User     string // machine-wide account: whose sudo to restrict, whose desktop to track

    // Blocking mechanisms.
    Domains  DomainsConfig  // .Enabled, .HostsPath, .KillOnBlock, .BlockedAccessCommand, .Unblocking, .List []Domain
    Programs ProgramsConfig // .Enabled, .CheckInterval, .List []ForbiddenProgram
    Content  ContentConfig  // .Monitoring (ContentMonitoringConfig), .Keywords (ExtensionKeywordsConfig)
    Sudoers  SudoersConfig

    // Uninstall friction & accountability.
    MindfulUninstall MindfulUninstallConfig
    Lifecycle        LifecycleConfig // install/uninstall logging + valid -uninstall reasons

    // Accountability & telemetry.
    ViolationTracking ViolationTrackingConfig
    UsageMonitor      UsageMonitorConfig
    Heartbeat         HeartbeatConfig
    Sync              SyncConfig // ship local /var records to a glockpeek instance

    // Panic mode.
    Panic PanicConfig
}

type Domain struct {
    Name         string
    BlockWindows []TimeWindow // If empty, domain is always blocked (default); else blocked only during these windows
    LogBlocking  bool         // Log each block hit for this domain
    Unblockable  bool         // Set to true to allow temporary unblocking (default: false = permanent)
}

type SudoersConfig struct {
    Enabled            bool
    AllowedSudoersLine string
    BlockedSudoersLine string
    AllowWindows       []TimeWindow // sudo ALLOWED during these windows, blocked outside (empty = always blocked)
    // Note: the sudo user is Config.User (top-level), not a field here.
}

type ForbiddenProgram struct {
    Name         string
    KillWindows  []TimeWindow // killed DURING these windows
    AllowWindows []TimeWindow // killed OUTSIDE these windows (only consulted when KillWindows is empty)
    Extendible   bool         // allows a one-hour runtime grant via `glocker -extend`, max once per rolling 24h
}

// Domain blocking logic (internal/enforcement/domains.go):
// - No block windows → Always blocked (permanent by default)
// - Block windows specified → Blocked only during those windows
// - Unblockable flag → Allows temporary unblocking (default: permanent)

// Forbidden program kill logic (internal/monitoring/forbidden.go):
// - In any KillWindow → killed
// - Else AllowWindows non-empty → killed unless inside an AllowWindow
// - Else both empty → always killed (legacy default)
```

### Control Flow

**Installation** (`glocker -install`):
1. Copies binary to `/usr/local/bin/glocker` with setuid permissions (internal/install/install.go)
2. Copies `conf/conf.yaml` to `/etc/glocker/config.yaml`
3. Installs systemd service from `extras/glocker.service`

**Enforcement Loop** (cmd/glocker/main.go, daemon path):
1. Loads config from `/etc/glocker/config.yaml` (internal/config/loader.go)
2. Starts Unix socket server for IPC (internal/ipc/server.go)
3. Launches background goroutines:
   - Forbidden programs (internal/monitoring/forbidden.go)
   - Violation tracking (internal/monitoring/violations.go)
   - Panic mode monitoring (internal/monitoring/panic.go)
   - Web tracking HTTP server on ports 80 and 443 (internal/web/server.go)
   - Syncer to glockpeek when `cfg.Sync.Enabled` (internal/syncer/)
4. Main loop applies enforcement periodically:
   - Updates hosts file with blocked domains (internal/enforcement/hosts.go)
   - Updates sudoers restrictions (internal/enforcement/sudoers.go)
   - Applies time window logic

**Socket Commands** (internal/ipc/server.go, `HandleConnection`):
- Client sends command via Unix socket at `/var/glocker/glocker.sock`
- Format: `"action:payload\n"` (e.g., `"block:example.com\n"`)
- Server processes command and returns response
- Commands: `ping`, `status`, `info`, `unblock`, `block`, `block-app`, `panic`, `extend`, `lock`, `add-keyword`, `resync`, `uninstall`
- Multi-line responses end with `"END"`

### Important Files and Paths

**Production paths:**
- `/etc/glocker/config.yaml` - Main configuration
- `/var/glocker/glocker.sock` - Unix socket for IPC
- `/var/log/glocker/reports.log` - Content monitoring logs
- `/var/log/glocker/unblocks.log` - Unblock request logs
- `/etc/hosts` - Modified with GLOCKER markers and immutable flag
- `/etc/sudoers` - Modified during blocking periods
- `/usr/local/bin/glocker` - Installed binary (setuid root)
- `/etc/systemd/system/glocker.service` - Systemd service file

**Development files:**
- `cmd/glocker/main.go` - Entry point (other binaries under `cmd/`: glockpeek, glocklock, glockdoc, configcheck, usageprobe, e2ekit)
- `internal/` - Application packages (see File Map above)
- `conf/conf.yaml` + `conf/conf.d/*.yaml` - Working config plus included blocklists
- `extras/glocker.service` - Systemd service template
- `extensions/firefox/` - Browser extension


### Security Features

**Tamper Resistance**:
- Setuid root binary with immutable flag (`chattr +i`)
- Immutable /etc/hosts during blocking
- Sudoers restrictions prevent privilege escalation
- Mindful-uninstall friction (`mindful_uninstall`, internal/mindful) before uninstall completes
- No active tamper-detection/self-healing module exists; a heartbeat gap on glockpeek is the tamper signal instead

**Accountability**:
- Logs all unblock attempts with reasons; syncs violations/unblocks/lifecycle to glockpeek
- Email notifications and the daily report are handled by **glockpeek**, per-account —
  the agent sends no email and holds no Mailgun key

### Time Window Logic

All time windows share the same `TimeWindow` shape (HH:MM + day-of-week array);
the YAML key name carries the semantics:
```yaml
block_windows:        # or allow_windows / kill_windows depending on the field
  - start: "09:00"
    end: "17:00"
    days: ["Mon", "Tue", "Wed", "Thu", "Fri"]
```

Applied to (all in internal/config/types.go):
- Domain blocking — `Domain.BlockWindows` (yaml: `block_windows`), blocked during
- Sudoers restrictions — `SudoersConfig.AllowWindows` (yaml: `allow_windows`), sudo allowed during
- Forbidden programs — `ForbiddenProgram.KillWindows` (killed during) / `ForbiddenProgram.AllowWindows` (killed outside)
- Evaluation logic in internal/enforcement/enforcement.go

### HTTP(s) Servers (port 80, 443)

Endpoints for browser extension communication (internal/web/handlers.go):
- `POST /report` - Content monitoring reports from extension
- `GET /keywords` - Returns current URL/content keyword lists
- `GET /keywords-stream` - Server-sent events for real-time keyword updates
- `GET /blocked` - Blocked page display (served when a hosts-blocked domain is requested)

Server started by internal/web/server.go:StartWebTrackingServer()

**Stats dashboard (`internal/stats/` + `internal/store/`, served by the standalone
`glockpeek` process):**
- Served by **`cmd/glockpeek`** as its own binary + systemd service
  (`extras/glockpeek.service`), NOT by the glocker daemon. Reads
  `/etc/glocker/config.yaml` for `glockpeek_listen` (default `127.0.0.1:4317`),
  `database` (driver+DSN), and `glockpeek_secure_cookies`.
- **DB-backed, multi-tenant.** `internal/store` is a GORM layer (sqlite via
  pure-Go `glebarez/sqlite`, or postgres) — the dashboard no longer parses log
  files. Every stats row carries a `UserID`; all reads/ingest are scoped to the
  authenticated account. Records are populated by the glocker syncer (see below)
  through the ingest API.
- **Two modes** (`glockpeek_mode`, default **local**). `cmd/glockpeek` maps mode →
  `stats.Options.Auth` (local→false, hosted→true).
  - **local**: `main.forceLoopback` pins the bind to `127.0.0.1` regardless of
    `glockpeek_listen`; `requireUser`/`requireToken` inject an implicit account
    (`EnsureDefaultUser`, username `local`), so no login/token and ingest is open
    to same-machine clients.
  - **hosted**: real logins + tokens + isolation (below); bind honored as configured.
- **Hosted auth** (`internal/stats/auth.go`): humans log in (`POST /api/login`) → argon2id
  verify (`alexedwards/argon2id`) → httpOnly session cookie (`glockpeek_session`,
  opaque token in the `sessions` table). The syncer uses a bearer **API token**
  (hashed in the `api_tokens` table) on `/api/ingest`. Middleware: `requireUser`
  (cookie) / `requireToken` (bearer). Admin CLI: `glockpeek -adduser/-passwd/-addtoken`
  (needs access to the DB, so run as the service user). `glockpeek -import-rules
  tags.json` loads a curated Glocker starter-rule set into the `default_rules`
  template table; `SeedAccountRules` seeds a new account from that table when it
  has rows, else from the built-in `glockerStarterRules`. The curated `tags.json`
  is a **hosted-only** asset (gitignored), so the open-source build ships only the
  minimal built-in defaults. `/api/me` returns `{auth}`
  so the frontend hides sign-out in single-user mode.
- `stats.Register(mux, db, Options{SecureCookies})` mounts:
  - `GET /` (and the clean view URLs `/overview`, `/analytics`, `/analytics/<tab>`,
    `/insights`, `/config`, `/devices`, `/users`) - the server-rendered SSR shell
    (`internal/stats/templates/dashboard.html.tmpl`); app.js's client router shows
    the right view from the path, so a deep link / refresh works — **public**
  - `GET /settings/encryption` - server-rendered E2E setup page (SSR replacement
    for the old standalone `setup-encryption.html`); gated (redirects to `/` if not
    signed in), crypto stays client-side (`assets/encryption-setup.js`)
  - `POST /api/login` — **public**; `POST /api/logout`, `GET /api/me` — session
  - `GET /api/data` - full history (session-gated), unmanaged/downtime derived from
    lifecycle/heartbeat rows; ignored overlay filtered in `buildData`
  - `GET /api/health` - `{ok, counts}` for the account (session)
  - `GET|PUT /api/rules` - the account's usage rules, each attaching registry
    tags by id (`{rules:[{id,position,program,title,tagIds}]}`); replace-all,
    one tag per namespace (session)
  - `GET|POST|PATCH|DELETE /api/tags` - the tag registry: shared `Glocker:*`
    system tags (with per-account distraction overrides) + the account's own
    tags (session). See `docs/tags-and-rules.md`.
  - `GET|POST|PATCH|DELETE /api/tags` - tag registry: shared system `Glocker:*`
    tags (with per-account distraction overrides) + the account's own tags
    (session; see docs/tags-and-rules.md)
  - `GET|PUT /api/ignored` - false-positive overlay in the DB (session)
  - `POST /api/ingest` - batched idempotent upsert from the syncer (**bearer token**);
    also stamps the `sync_status` row (last-ingest time + last-batch counts).
    `GET /api/ingest` returns `{cursors}` — per-source max stored TS
    (`store.HighWaterMarks`) — so the syncer seeds cursors from what glockpeek
    already has (glockpeek is the source of truth for sync progress).
  - `GET /api/sync` - `{lastSyncAt, last{counts}, totals}` for the dashboard's
    Data-sync panel (session)
  - `GET /api/config` - per-device config snapshots (uploaded by the syncer on connect)
  - Hosted-mode auth/account routes also exist (`/api/register`, `/api/me`, `/api/vault`,
    `/api/tokens`, `/api/admin/users`, `/verify`, billing) — this list is not exhaustive.
  - Legacy `/stats`, `/stats/` 301-redirect to `/`. (The old
    `glocker.localhost/stats` alias in hosts.go is vestigial.)
- **Boot path (first load).** Everything sensitive arrives sealed, so the browser
  must open every field before a single chart can be drawn. `app.js` collects one
  "job" per sealed field up front (`collectDecryptJobs`), which both gives the
  total and lets it run `DECRYPT_BATCH`-sized `Promise.all` batches instead of
  awaiting each field — WebCrypto runs off the main thread, so the batches really
  overlap, and `e2e-crypto.js` memoises the imported `CryptoKey` per key-bytes
  object so a big load pays `importKey` once, not once per field. The loading
  screen is a progress dial (`.boot` in `dashboard.html.tmpl` / styles.css, driven
  by `bootPhase` / `bootProgress`): an indeterminate sweep for phases with no
  countable work, a real fraction for the decrypt.
- The frontend is a **hybrid** (SSR/HTMX migration, in progress): the chrome is
  server-rendered from `internal/stats/templates/*.tmpl` (Go `html/template`, via
  `internal/stats/pages.go`), while the encrypted-analytics view stays a
  client-side JS island (`internal/stats/assets/app.js` + Chart.js), because the
  server can't see record content. Both are embedded via `go:embed`; no bundler,
  no `node_modules` (the old `glockpeek-web/` node app was removed). Routing is
  clean-URL/path-based (History API) with `app.js` still owning view-switching and,
  for now, the embedded client login (it gates on `GET /api/me`). **Remaining
  (stage 4, not yet done):** extract the login/register and config/devices/admin
  flows into SSR templates. See `PLAN.md` (SSR/HTMX migration) for stage status.

**Syncer (`internal/syncer/`, a goroutine in the glocker daemon):**
- Wired in cmd/glocker/main.go under `cfg.Sync.Enabled`: `go syncer.New(cfg).Run()`.
- Local-first: reads the `/var` logs (via `reports` parsers + its own usage-JSONL
  parser), builds a **plain-JSON** batch (deliberately NOT importing `store`, so
  the agent binary stays free of GORM), and POSTs to `<glockpeek_url>/api/ingest`.
  At startup `seedCursors` GETs glockpeek's high-water marks so a restart only
  sends the gap (not a full re-backfill); on any error cursors stay 0 and it
  backfills — always safe (idempotent). Then incremental every `interval_seconds`
  with the in-memory per-source max-TS cursor. Errors are logged and retried next
  tick — enforcement never depends on it. After each push it calls
  `state.RecordSync`, which `glocker -status` surfaces (last push + session totals).
- The syncer's payload JSON tags must match glockpeek's ingest decoder
  (store-model JSON). `TestSyncIntoRealGlockpeek` guards that across the boundary.
- Config `sync.{enabled, glockpeek_url, token, interval_seconds}`; token only for a
  hosted glockpeek. Cursor is in-memory and, on startup, **seeded from glockpeek's
  high-water marks** (`seedCursors` GETs `/api/ingest`), so a plain restart only
  sends the gap — it does NOT re-push. To force a full re-push (e.g. to repair rows
  after a field that used to ship cleartext is now encrypted), `glocker -resync`
  (socket cmd → `syncer.Resync`) rewinds the cursors and backfills; because ingest
  upserts on the natural key, the re-push overwrites glockpeek's copies in place
  (no server-side delete, no data loss). `-resync-since <date>` limits how far back.
  `state.IsResyncing()` surfaces an in-flight backfill in `glocker -status`.

### Extension Communication Flow

1. Extension loads and fetches keywords: `GET http://127.0.0.1/keywords`
2. Extension monitors URLs/content using cached keyword regexes (extensions/firefox/background.js, content.js)
3. On violation: `POST http://127.0.0.1/report` with JSON payload
4. Glocker logs to file and increments violation counter (internal/web/handlers.go)
5. If violations exceed threshold: executes lock command (internal/monitoring/violations.go)

## Configuration Notes

`conf/conf.yaml` is small; the extensive blocking lists live in `conf/conf.d/*.yaml`
(~16MB total) and are pulled in via the include mechanism. When making changes:
- There is no runtime reload; reinstall (`make install` / `make full-install`) to apply
- Check logs with `journalctl -u glocker.service -f`

Violation tracking can trigger automatic actions (e.g., screen lock):
```yaml
violation_tracking:
  enabled: true
  max_violations: 5
  time_window_minutes: 60
  command: "sudo -u yourusername DISPLAY=:0 /usr/local/bin/glocklock"
```

## Code Organization

The codebase is organized into modular packages under `internal/`:

```
glocker/
├── cmd/                         # Binary entry points (glocker, glockpeek, glocklock,
│                                #   glockdoc, configcheck, usageprobe, e2ekit)
├── internal/                    # Application packages (partial list)
│   ├── cli/                    # Command processors
│   ├── config/                 # Configuration structs and loading
│   ├── enforcement/            # Core blocking logic
│   ├── install/                # Installation and uninstallation
│   ├── ipc/                    # Unix socket server
│   ├── monitoring/             # Background monitoring tasks
│   ├── schedule/               # Near-future preview of window flips (-status)
│   ├── state/                  # Thread-safe global state
│   ├── syncer/                 # Ships local records to glockpeek
│   ├── stats/ + store/         # glockpeek dashboard + DB layer
│   ├── utils/                  # Shared utilities
│   └── web/                    # HTTP server for extension
├── extensions/firefox/          # Browser extension
├── conf/conf.yaml + conf/conf.d/ # Working config + included blocklists
└── extras/glocker.service      # Systemd service
```

**Package Dependencies:**
- `main` imports all packages for orchestration
- `cli` imports `config`, `enforcement`, `monitoring`, `schedule`, `state`
- `schedule` imports `config`, `enforcement`, `monitoring`
- `ipc` imports `cli`, `config`, `enforcement`, `install`
- `enforcement` imports `config`, `state`, `utils`
- `monitoring` imports `config`, `state`, `utils`
- `utils` imports `config` (for `TimeWindow`) and nothing else
- `web` imports `config`, `monitoring`, `state`

When searching for functionality:
- CLI flags: `cmd/glocker/main.go`
- Enforcement logic: `internal/enforcement/` (`hosts.go`, `sudoers.go`, `domains.go`)
- Socket commands: `internal/ipc/server.go`
- Status output: `internal/cli/commands.go`

## Dependencies

Go modules (go.mod):
- `gopkg.in/yaml.v3` - Configuration parsing
- `github.com/mailgun/mailgun-go/v4` - Email, used only by glockpeek (`internal/mailer`); the agent sends no email
- `github.com/alexedwards/argon2id` - Password hashing for glockpeek hosted auth
- `gorm.io/gorm` + `gorm.io/driver/postgres` + `github.com/glebarez/sqlite` - glockpeek DB layer (`internal/store`)

System dependencies:
- `systemd` - Service management
- `chattr` / `lsattr` - File immutability
- Firefox with extension support
