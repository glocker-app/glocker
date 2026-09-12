# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Glocker is a Linux-based distraction blocking system written in Go. It blocks distracting websites and applications using multiple enforcement mechanisms to make bypassing difficult. The tool runs as a privileged setuid systemd service and uses Unix sockets for IPC.

**Architecture:** Modular Go application with packages in `internal/` directory (see File Map below)

## Build and Test Commands

```bash
# Build the binary
go build .

# Run a single enforcement cycle (requires sudo)
glocker -once

# Check current status (blocking state, active domains, violations)
glocker -status

# Install as systemd service with setuid privileges
glocker -install

# Uninstall and cleanup all protections
glocker -uninstall
```

## File Map - Where to Find What

### Entry Point
- **`main.go`** (377 lines) - CLI entry point
  - All 12 command-line flag definitions (lines 26-37)
  - Flag handlers with socket communication (lines 48-234)
  - Default behavior: show status or help (lines 236-266)
  - Daemon startup logic (lines 322-376)

### Configuration (`internal/config/`)
- **`types.go`** - Configuration structs and constants
  - `Config` struct with all settings
  - `Domain`, `TimeWindow`, `SudoersConfig`, `ForbiddenProgram`, `LifecycleConfig` structs
- **`loader.go`** - YAML loading
  - `LoadConfig()` - Loads from `/etc/glocker/config.yaml`
  - `SetupLogging()` - Configures log output
- **`persist.go`** - Input validators for names accepted over the socket
  - `ValidateDomainName()` / `ValidateProgramName()` - used by `-block` / `-block-app`
  - Note: runtime commands (`-block`, `-block-app`, `-add-keyword`) apply **in memory only**; they do NOT write the config file. `conf/conf.yaml` is the single source of truth (regenerated onto `/etc/glocker/config.yaml` by `make full-install`).
- **`validator.go`** - `ValidateConfig()` - validates time windows and required fields

### CLI Commands (`internal/cli/`)
- **`commands.go`** - Command processors for socket requests
  - `GetStatusResponse()` - Generates formatted status output (lines 15-145)
  - `ProcessReloadRequest()` - Handles config reload (lines 148-173)
  - `ProcessUnblockRequest()` - Temporary domain unblocking (lines 76-100)
  - `ProcessBlockRequest()` - In-memory domain blocking (not persisted; via enforcement.AddRuntimeDomains)
  - `ProcessPanicRequest()` - Panic mode activation (lines 127-140)
  - `formatTimeWindows()` - Helper for time window display
- **`commands_test.go`** - Unit tests for CLI commands

### Enforcement (`internal/enforcement/`)
- **`enforcement.go`** - Core blocking logic
  - `RunOnce()` - Main enforcement cycle
  - `UpdateHosts()` - Modifies `/etc/hosts` file
  - `UpdateSudoers()` - Controls sudo access
  - Time window evaluation logic
  - Immutable file protection (chattr)

### IPC / Socket Communication (`internal/ipc/`)
- **`server.go`** - Unix socket server for daemon communication
  - `SetupCommunication()` - Creates socket at `/var/glocker/glocker.sock`
  - `HandleConnection()` - Processes socket commands (lines 33-146)
  - Socket command handlers:
    - `status` - Live status query (lines 75-77)
    - `reload` - Config reload (lines 78-80)
    - `unblock` - Temporary unblock (lines 81-99)
    - `block` - Domain block, in memory only (lines 100-107)
    - `block-app` - Add program to forbidden-programs list (killed on sight, 24/7)
    - `panic` - Panic mode (lines 111-123)
    - `lock` - Force sudoers lock (lines 124-126)
    - `add-keyword` - Add monitoring keywords (lines 127-134)
    - `uninstall` - Uninstall glocker (lines 135-146)
  - `processLockRequest()` - Lock processor (lines 189-200)
  - `processAddKeywordRequest()` - Keyword processor (lines 202-221)
  - `processUninstallRequest()` - Uninstall processor (lines 223-244)
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
- **`tampering.go`** - Tamper detection
  - File checksum monitoring
  - Self-healing mechanisms
  - Alarm command execution
- **`panic.go`** - Panic mode monitoring
  - System suspend/resume detection
  - Re-suspension on early wake

### Web Server (`internal/web/`)
- **`server.go`** - HTTP/HTTPS server for browser extension
  - `StartWebTrackingServer()` - Starts on ports 80, 443
- **`handlers.go`** - HTTP endpoint handlers
  - `GET /keywords` - Returns monitoring keywords
  - `POST /report` - Content violation reports
  - `GET /sse` - Server-sent events for real-time updates
  - `GET /blocked` - Blocked page display

### Notifications (`internal/notify/`)
- **`desktop.go`** - Desktop notifications (local toasts via `notification_command`)
  - `SendNotification()` - fills `{title}/{message}/{urgency}/{icon}` into the command
  - The agent sends **no email** — accountability notifications are glockpeek's job
    (`internal/mailer`, configured server-side). See `internal/report` for the
    DB-backed daily-report builder that lives on the glockpeek side.

### State Management (`internal/state/`)
- **`state.go`** - Thread-safe global state
  - Panic mode state (`panicUntil`)
  - Temporary unblocks tracking
  - `sync.RWMutex` for concurrency safety

### Utilities (`internal/utils/`)
- **`utils.go`** - Shared utility functions
  - Time window evaluation
  - String processing helpers
  - File operations

### Browser Extension (`extensions/firefox/`)
- **`manifest.json`** - Firefox extension manifest
- **`background.js`** - URL monitoring, keyword fetching
- **`content.js`** - Page content scanning
- **`blocked.html`** - Blocked page template

### Configuration & Resources
- **`conf/conf.yaml`** - Sample configuration (~60MB with domain lists)
- **`extras/glocker.service`** - Systemd service definition

### Documentation
- **`CLAUDE.md`** - This file - developer guidance
- **`GAP_ANALYSIS.md`** - Detailed gap analysis from refactoring
- **`IMPLEMENTATION_STATUS.md`** - Implementation completion report

## Quick Reference: Common Tasks

| Task | Primary File | Key Function |
|------|-------------|--------------|
| Add new CLI flag | `main.go` | Add flag definition + handler |
| Modify status output | `internal/cli/commands.go` | `GetStatusResponse()` |
| Change blocking logic | `internal/enforcement/enforcement.go` | `UpdateHosts()` |
| Add socket command | `internal/ipc/server.go` | Add case in `HandleConnection()` |
| Modify installation | `internal/install/install.go` | `InstallGlocker()` |
| Add web endpoint | `internal/web/handlers.go` | Add handler function |
| Change time windows | `internal/config/types.go` | `TimeWindow` struct |
| Add monitoring feature | `internal/monitoring/` | Create new file or extend existing |

## Common Development Workflows

### Running Commands
```bash
# Reload configuration from /etc/glocker/config.yaml
sudo glocker -reload

# Temporarily unblock domains with reason
sudo glocker -unblock "reddit.com,youtube.com:work"

# Add domains to the block list (in memory only, until restart/reinstall)
sudo glocker -block "example.com,another.com"

# Add keywords to monitoring lists
sudo glocker -add-keyword "keyword1,keyword2"

```
### To test the program, 
#### Install new version
1. `sudo glocker -uninstall "testing"`
2. `go build .`
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
4. Monitor `/var/log/glocker-reports.log` for content monitoring events

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

```go
type Config struct {
    EnableHosts             bool
    EnableForbiddenPrograms bool
    Domains                 []Domain
    KillOnBlock             []string                // process names killed after `glocker -block` (DNS-cache flush)
    Sudoers                 SudoersConfig
    TamperDetection         TamperConfig
    ContentMonitoring       ContentMonitoringConfig
    ForbiddenPrograms       ForbiddenProgramsConfig
    ExtensionKeywords       ExtensionKeywordsConfig
    ViolationTracking       ViolationTrackingConfig
    Unblocking              UnblockingConfig
    Lifecycle               LifecycleConfig         // install/uninstall logging + valid -uninstall reasons
    // ... more fields
}

type Domain struct {
    Name         string
    BlockWindows []TimeWindow // If empty, domain is always blocked (default); else blocked only during these windows
    LogBlocking  bool         // Log each block hit for this domain
    Unblockable  bool         // Set to true to allow temporary unblocking (default: false = permanent)
}

type SudoersConfig struct {
    Enabled            bool
    User               string
    AllowedSudoersLine string
    BlockedSudoersLine string
    AllowWindows       []TimeWindow // sudo ALLOWED during these windows, blocked outside (empty = always blocked)
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

**Enforcement Loop** (main.go:322-376):
1. Loads config from `/etc/glocker/config.yaml` (internal/config/loader.go)
2. Starts Unix socket server for IPC (internal/ipc/server.go)
3. Launches monitoring goroutines:
   - Tamper detection (internal/monitoring/tampering.go)
   - Forbidden programs (internal/monitoring/forbidden.go)
   - Violation tracking (internal/monitoring/violations.go)
   - Panic mode monitoring (internal/monitoring/panic.go)
   - Web tracking HTTP server on ports 80 and 443 (internal/web/server.go)
4. Main loop applies enforcement every 60s:
   - Updates hosts file with blocked domains (internal/enforcement/enforcement.go)
   - Updates sudoers restrictions
   - Applies time window logic

**Socket Commands** (internal/ipc/server.go:33-146):
- Client sends command via Unix socket at `/var/glocker/glocker.sock`
- Format: `"action:payload\n"` (e.g., `"block:example.com\n"`)
- Server processes command and returns response
- Commands: `status`, `reload`, `unblock`, `block`, `block-app`, `panic`, `lock`, `add-keyword`, `uninstall`
- Multi-line responses end with `"END"`

### Important Files and Paths

**Production paths:**
- `/etc/glocker/config.yaml` - Main configuration
- `/var/glocker/glocker.sock` - Unix socket for IPC
- `/var/log/glocker-reports.log` - Content monitoring logs
- `/var/log/glocker-unblocks.log` - Unblock request logs
- `/etc/hosts` - Modified with GLOCKER markers and immutable flag
- `/etc/sudoers` - Modified during blocking periods
- `/usr/local/bin/glocker` - Installed binary (setuid root)
- `/etc/systemd/system/glocker.service` - Systemd service file

**Development files:**
- `main.go` - Entry point
- `internal/` - Application packages (see File Map above)
- `conf/conf.yaml` - Sample configuration (~60MB with domain lists)
- `extras/glocker.service` - Systemd service template
- `extensions/firefox/` - Browser extension


### Security Features

**Self-Healing** (when enabled):
- Monitors critical file checksums (binary, /etc/hosts, systemd service)
- Re-applies protections if tampering detected
- Triggers alarm command on tampering

**Tamper Resistance**:
- Setuid root binary with immutable flag (`chattr +i`)
- Immutable /etc/hosts during blocking
- Sudoers restrictions prevent privilege escalation
- Mindful delay (configurable, default 60s) before uninstall completes

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
- `GET /sse` - Server-sent events for real-time updates
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
  (needs access to the DB, so run as the service user). `/api/me` returns `{auth}`
  so the frontend hides sign-out in single-user mode.
- `stats.Register(mux, db, Options{SecureCookies})` mounts:
  - `GET /` - dashboard (embedded frontend, copy of `glockpeek-web/public`) — **public**
  - `POST /api/login` — **public**; `POST /api/logout`, `GET /api/me` — session
  - `GET /api/data` - full history (session-gated), unmanaged/downtime derived from
    lifecycle/heartbeat rows; ignored overlay filtered in `buildData`
  - `GET /api/health` - `{ok, counts}` for the account (session)
  - `GET|PUT /api/rules` - `{rules, colors}` in the DB (session)
  - `GET|PUT /api/ignored` - false-positive overlay in the DB (session)
  - `POST /api/ingest` - batched idempotent upsert from the syncer (**bearer token**);
    also stamps the `sync_status` row (last-ingest time + last-batch counts).
    `GET /api/ingest` returns `{cursors}` — per-source max stored TS
    (`store.HighWaterMarks`) — so the syncer seeds cursors from what glockpeek
    already has (glockpeek is the source of truth for sync progress).
  - `GET /api/sync` - `{lastSyncAt, last{counts}, totals}` for the dashboard's
    Data-sync panel (session)
  - Legacy `/stats`, `/stats/` 301-redirect to `/`. (The old
    `glocker.localhost/stats` alias in hosts.go is vestigial.)
- Frontend must be kept in sync between `glockpeek-web/public/` and
  `internal/stats/assets/` (the embedded copy). `app.js` gates on `GET /api/me`
  and shows a login screen when unauthenticated.

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
  hosted glockpeek. Cursor is in-memory (restart re-backfills; idempotent).

### Extension Communication Flow

1. Extension loads and fetches keywords: `GET http://127.0.0.1/keywords`
2. Extension monitors URLs/content using cached keyword regexes (extensions/firefox/background.js, content.js)
3. On violation: `POST http://127.0.0.1/report` with JSON payload
4. Glocker logs to file and increments violation counter (internal/web/handlers.go)
5. If violations exceed threshold: executes lock command (internal/monitoring/violations.go)

## Configuration Notes

The `conf/conf.yaml` file contains extensive blocking lists and is ~60MB due to comprehensive domain lists. When making changes:
- Use `glocker -reload` to reload config without restart
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
├── main.go                      # Entry point, CLI flags, daemon startup
├── internal/                    # Application packages
│   ├── cli/                    # Command processors
│   ├── config/                 # Configuration structs and loading
│   ├── enforcement/            # Core blocking logic
│   ├── install/                # Installation and uninstallation
│   ├── ipc/                    # Unix socket server
│   ├── monitoring/             # Background monitoring tasks
│   ├── notify/                 # Desktop notifications (no email)
│   ├── state/                  # Thread-safe global state
│   ├── utils/                  # Shared utilities
│   └── web/                    # HTTP server for extension
├── extensions/firefox/          # Browser extension
├── conf/conf.yaml              # Sample configuration
└── extras/glocker.service      # Systemd service
```

**Package Dependencies:**
- `main` imports all packages for orchestration
- `cli` imports `config`, `enforcement`, `state`
- `ipc` imports `cli`, `config`, `enforcement`, `install`
- `enforcement` imports `config`, `state`, `utils`
- `monitoring` imports `config`, `notify`, `state`
- `web` imports `config`, `monitoring`, `state`

When searching for functionality:
- CLI flags: `main.go`
- Enforcement logic: `internal/enforcement/enforcement.go`
- Socket commands: `internal/ipc/server.go`
- Status output: `internal/cli/commands.go`

## Dependencies

Go modules (go.mod):
- `github.com/mailgun/mailgun-go/v4` - Email, used only by glockpeek (`internal/mailer`); the agent sends no email
- `gopkg.in/yaml.v3` - Configuration parsing

System dependencies:
- `systemd` - Service management
- `chattr` / `lsattr` - File immutability
- Firefox with extension support
