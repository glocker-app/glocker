# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Glocker is a Linux-based distraction blocking system written in Go. It blocks distracting websites and applications using multiple enforcement mechanisms to make bypassing difficult. The daemon runs as root under systemd (the installed binary is plain 0755, not setuid) and the CLI talks to it over a Unix socket owned `root:glocker`.

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

# Generate conf/conf.yaml from the sample (once), then install as a systemd service
make init
sudo ./glocker -install   # or: make install

# Uninstall and cleanup all protections (a reason is required)
glocker -uninstall "reason"
```

## File Map - Where to Find What

### Entry Point
- **`cmd/glocker/main.go`** - CLI entry point
  - Command-line flag definitions (`-status`, `-info`, `-version`, `-connect`,
    `-extend`, `-purge`, `-check`, `-install`, `-uninstall`, `-note`, `-daemon`,
    `-block`, `-block-app`, `-unblock`, `-add-keyword`, `-panic`, `-lock`, `-force`,
    `-resync`, `-init`, `-conf`, `-edit`)
  - Flag handlers with socket communication
  - Default behavior: show status or help
  - Daemon startup logic
- **`cmd/glocker/status.go`** - shared `-status`/`-info` socket + on-disk-fallback helpers
- **`cmd/glocker/edit.go`** - `-edit`: opens `/etc/glocker/config.yaml` in `internal/editor`;
  read-only without root, and as root a save restarts `glocker.service`

### Configuration (`internal/config/`)
- **`types.go`** - Configuration structs and constants
  - `Config` struct with all settings
  - `Domain`, `TimeWindow`, `SudoersConfig`, `ForbiddenProgram`, `LifecycleConfig` structs
- **`loader.go`** - YAML loading
  - `LoadConfig()` - Loads from `/etc/glocker/config.yaml`
  - `SetupLogging()` - Configures log output
- **`persist.go`** - Input validators for names accepted over the socket
  - `ValidateDomainName()` / `ValidateProgramName()` - used by `-block` / `-block-app`
  - Note: runtime commands (`-block`, `-block-app`, `-add-keyword`) apply **in memory only**; they do NOT write the config file. The installed `/etc/glocker/config.yaml` (plus its editable includes) is the user's config; see "Config versioning and upgrades" for when `-install` replaces it.
- **`include.go`** - Include mechanism: `conf/conf.yaml` pulls in blocklist files from `conf/conf.d/*.yaml`
- **`glockpeek.go`** - Loads glockpeek's separate server config (`GlockpeekConfig`)
- **`validator.go`** - `ValidateConfig()` - validates time windows and required fields
- **`types.go`** also holds `CurrentConfigVersion` (see "Config versioning and upgrades")

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
- **`state.go`** - The real enforcement cycle: `InitialEnforcement()` (daemon start),
  `EnforcementCheck()` (every 60s: change detection on time windows / temp unblocks /
  sudo state, plus hosts-file checksum check that re-applies on tampering) and
  `ForceEnforcement()` (after `-block`/`-lock` etc.). Also holds enforcement-local
  runtime state (in-memory `-block` / `-block-app` additions)
- **`enforcement.go`** - `RunOnce()` — dead code, not called by the daemon
- **`hosts.go`** - `UpdateHosts()` rewrites the glocker block in `/etc/hosts`
- **`sudoers.go`** - `UpdateSudoers()` writes the allowed/blocked line to `/etc/sudoers.d/glocker` (only once a takeover is recorded; see `internal/sudoplan`); `IsSudoAllowed()` window check
- **`domains.go`** - Domain block-window evaluation (`IsDomainBlockedAt`), temp unblocks

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
    - `resync` - Rewind the syncer's cursors and backfill (re-push local records to glockpeek, overwriting its copies)
    - `uninstall` - Uninstall glocker
  - `processLockRequest()` - Lock processor
  - `processAddKeywordRequest()` - Keyword processor
  - `processUninstallRequest()` - Uninstall processor
- **`server_test.go`** - Socket server tests

### Installation (`internal/install/`)
- **`install.go`** - Installation logic (upgrades in place)
  - `InstallGlocker()` - Copies binaries (0755, not setuid), config, systemd services;
    creates the `glocker` group and adds `cfg.User` to it; glockdoc watchdog timer/cron
  - `RunningAsRoot()` - Privilege checks
  - Clears the `chattr +i` flag older versions set (`legacyImmutablePaths`); glocker no longer sets it
- **`configplan.go`** - What `-install` does with the config: keep, replace or convert
  the installed one, backups and the install checksum (see "Config versioning and upgrades")
- **`uninstall.go`** - `RestoreSystemChanges()` reverts all system modifications;
  `PurgeWatchdog()` for `-purge`
- **`gnome.go`** - Installs/enables the GNOME Shell window-tracking extension for the user
- **`firefox.go`** - Firefox xpi fetch + policy merged into `/etc/firefox/policies/policies.json` (prior values restored on uninstall)
- **`chrome.go`** - Chrome/Chromium/Brave/Edge managed policy (`glocker.json`): force-installs the extension from `https://glockerapp.com/chrome/update.xml`, disables incognito/guest/secure DNS

### Monitoring (`internal/monitoring/`)
- **`forbidden.go`** - Forbidden program monitoring
  - `MonitorForbiddenPrograms()` - Background goroutine
  - Process killing during time windows
  - Program detection via process name
- **`violations.go`** - Violation tracking
  - `RecordViolation()` / `checkViolationThreshold()` - count violations, run the
    configured command past the threshold
  - `MonitorViolations()` - only clears the counter at midnight
- **`panic.go`** - Panic mode monitoring
  - `MonitorPanicMode()` polls every second and re-runs `panic.command` while the
    panic deadline is in the future (5s grace after each suspend); no resume detection

(There is no tamper-detection module — the `tamper_detection` config key was removed and is now rejected by the loader. The heartbeat gap seen on glockpeek is the real tamper signal.)

### Web Server (`internal/web/`)
- **`server.go`** - HTTP/HTTPS server for browser extension
  - `StartWebTrackingServer()` - Starts on ports 80, 443
- **`handlers.go`** - HTTP endpoint handlers
  - `GET /keywords` - Returns monitoring keywords
  - `POST /report` - Content violation reports
  - `GET /keywords-stream` - Server-sent events (`HandleSSERequest`): sends an
    initial keyword snapshot, then keepalives (nothing broadcasts later updates)
- **`blocked.go`** - `GET /blocked` - Blocked page display (`HandleBlockedPageRequest`)

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
  stored account-level in `store.AccountabilityConfig` (latest sync wins).
  `internal/accountability` is the server-side scheduler (a goroutine in
  `cmd/glockpeek`): it sends a periodic digest (`internal/report` counts —
  `BuildWindow` over the cadence) and an immediate **went-dark** alert (newest
  heartbeat older than 2× the heartbeat interval + 5m — the dead-man's switch). Emails are **content-free**:
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

### Other packages
- **`internal/editor/`** - the `-edit` terminal UI (bubbletea + huh). Edits the YAML node
  tree (comments, key order and `!include` lines survive) and replays edits onto the
  original text so saves are minimal diffs (`preserve.go`). `schema.go` declares every
  stanza's fields; schedules are painted on a 7×48 half-hour grid (`week.go`,
  `schedule.go`) and converted back to windows on save. Saves validate with
  `config.ParseConfig` + `ValidateConfig`, show a diff, write atomically and restore
  the file's owner. Non-root opens read-only with a banner computed from the sudo
  windows (`gate.go`). `cmd/configtui` (`make edit-dev`) runs it on any file without root.
- **`internal/usage/`** - arbtt-style usage tracker (focused window, titles, idle) for
  X11, Hyprland and GNOME; writes `usage.jsonl`
- **`internal/connect/`** - `glocker -connect`: fetch the key vault, unwrap the master
  key with the passphrase, install it for the syncer
- **`internal/e2e/`** - E2E encryption primitives (master key, vault wrap/unwrap, record seal)
- **`internal/envcheck/`** - Capability checks (systemd, visudo, watchdog scheduler)
- **`internal/precheck/`** - Bypass-route audit behind `glocker -check`
- **`internal/sudoplan/`** - Per-distro sudo takeover (`plans.go`: Debian, Ubuntu, Fedora, Omarchy, Arch; anything else gets no sudo window). Install removes the user's blanket admin-group membership and moves per-user blanket `sudoers.d` files aside, recording it; uninstall restores it (or, if the user had no sudo of their own and root is locked, leaves a password-sudo `glocker-restored-<user>` drop-in)
- **`internal/heartbeat/`** - glockdoc's liveness probe (pings the socket, appends `heartbeat.jsonl`)
- **`internal/lock/`** - glocklock's timed locker: X11, Wayland (`ext-session-lock`) and GNOME backends
- **`internal/reports/`** - Parsers for the agent's `/var` log files (used by syncer, cli, web)
- **`internal/report/`** - glockpeek's 24h per-account summary from stored records
- **`internal/accountability/`** - glockpeek's accountability-email scheduler
- **`internal/mindful/`** - Typing-challenge gate for `-uninstall`
- **`internal/mailer/`** - Mailgun sender (glockpeek only)

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

### Chrome Extension (`extensions/chrome/`)
- MV3 port of the Firefox extension: `declarativeNetRequest` redirect rules do the
  URL-keyword blocking, a streaming fetch of `/keywords-stream` replaces EventSource
- Signed with `extensions/chrome-signing.pem` (gitignored; fixes the extension ID)
  by `make sign-chrome-extension`; `make deploy-chrome-extension` publishes the crx
  + `update.xml` to the site (`make release-extensions` does both browsers)

### GNOME Shell Extension (`extensions/gnome/`)
- `glocker-usage@glocker.app` - window bridge so the usage monitor can see windows on
  GNOME/Wayland; installed to the user's `~/.local/share/gnome-shell/extensions/` by
  `install/gnome.go`

### Configuration & Resources
- **`conf/conf.yaml`** - Working configuration (gitignored; generated by `make init` / `glocker -init` from the sample; pulls in blocklists via includes)
- **`conf/conf.yaml.sample`** - The committed sample config
- **`conf/conf.d/*.yaml`** - Included blocklist files (~16MB of domain lists: hagezi, stevenblack, shadowwhisperer, etc.)
- **`conf/glockpeek.yaml.sample`** / **`conf/usage-rules.sample.json`** - Sample glockpeek + usage-rule configs
- **`extras/glocker.service`** / **`extras/glockpeek.service`** - Systemd service definitions

### Documentation
- **`CLAUDE.md`** - This file - developer guidance
- **`docs/`** - `habits.md` (the user guide / source of truth), `bypasses.md`, `e2e-design.md`, and
  **`docs/openapi.yaml`** (the OpenAPI spec for glockpeek's HTTP API) and
  **`docs/agent-openapi.yaml`** (the agent's loopback API used by the extensions)

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
There is no runtime config-reload command. `sudo glocker -edit` restarts the daemon
after a save; a source checkout applies `conf/conf.yaml` edits with a reinstall
(`make install` / `make full-install`, which restarts the daemon). If the installed
config was edited since the last install, the reinstall asks before replacing it.

### To test the program, 
#### Install new version
Install upgrades in place; no uninstall needed.
1. `make init` once, if `conf/conf.yaml` doesn't exist yet
2. `make install` (builds, then stops/swaps/restarts the daemon)
3. Run `glocker` to get status.

#### Things to verify
1. Killing of forbidden programs
2. Violation trackin
3. Making sure that blocked domains are in `/etc/hosts` properly
4. APIs for browser extension integration (confirm with curl)


#### Testing Browser Extension
The extensions live in `extensions/firefox/` and `extensions/chrome/`. After making changes:
1. Firefox: `about:debugging#/runtime/this-firefox` → load `extensions/firefox/manifest.json`;
   Chrome: `chrome://extensions` → Developer mode → Load unpacked → `extensions/chrome/`
2. Check the extension's console (Firefox: Inspect; Chrome: the "service worker" link)
3. Monitor `/var/log/glocker/reports.log` for content monitoring events

## Architecture

### Core Components

**Main Service (`cmd/glocker/main.go`)**
- Single Go binary that handles all blocking logic
- Runs as a root systemd service (binary is 0755, not setuid)
- Config loaded from `/etc/glocker/config.yaml` (sample in `conf/conf.yaml.sample`)
- Uses Unix socket `/var/glocker/glocker.sock` for runtime commands

**Enforcement Mechanisms** (configured independently via YAML)
1. **Hosts file blocking** - Modifies `/etc/hosts`
2. **Forbidden programs** - Kills processes during configured time windows
3. **Sudoers modification** - Restricts sudo access during blocking periods
4. **Browser extension** - Content/keyword monitoring via the Firefox and Chrome extensions

**Browser Extensions** (`extensions/firefox/`, `extensions/chrome/` — same behaviour)
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
    Accountability    AccountabilityConfig // glockpeek's buddy emails; synced in cleartext

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
1. Copies binary to `/usr/local/bin/glocker`, root-owned 0755 (internal/install/install.go)
2. Installs the config per `configplan.go` (fresh: `conf/conf.yaml` or `-conf <path>`;
   upgrade: keep/convert/replace), plus the generated `conf.d/` blocklists
3. Creates the `glocker` group and adds `cfg.User` to it (takes effect on next login)
4. Installs systemd services from `extras/glocker.service` and `extras/glockpeek.service`

**Enforcement Loop** (cmd/glocker/main.go, daemon path):
1. Loads config from `/etc/glocker/config.yaml` (internal/config/loader.go)
2. Starts Unix socket server for IPC (internal/ipc/server.go)
3. Launches background goroutines, each gated on its config:
   - Forbidden programs when `programs.enabled` (internal/monitoring/forbidden.go)
   - Violation tracking when enabled (internal/monitoring/violations.go)
   - Panic mode monitoring when `panic.enabled` (internal/monitoring/panic.go)
   - Web tracking HTTP server on ports 80 and 443, only if domains or content
     monitoring is enabled (internal/web/server.go)
   - Usage monitor when `usage_monitor.enabled` (internal/usage/)
   - Syncer to glockpeek when `cfg.Sync.Enabled` (internal/syncer/)
4. `enforcement.InitialEnforcement()`, then `EnforcementCheck()` every 60s
   (internal/enforcement/state.go): rewrites hosts/sudoers only when time windows,
   temp unblocks or sudo state changed, or the hosts checksum no longer matches

**Socket Commands** (internal/ipc/server.go, `HandleConnection`):
- Client sends command via Unix socket at `/var/glocker/glocker.sock`
  (`root:glocker` 0660, in a 0750 dir; group members can connect)
- One command per connection. Format: `"action:payload\n"` (e.g., `"block:example.com\n"`)
- Commands: `ping`, `status`, `info`, `unblock`, `block`, `block-app`, `panic`, `extend`, `lock`, `add-keyword`, `resync`, `uninstall`
- `uninstall` and `resync` require the peer's real uid to be 0 (SO_PEERCRED), i.e. sudo
- `status`/`info` return multi-line output ending with `"END"`; the others reply with
  a single `OK: …` / `ERROR: …` line (`ping` → `pong`)

### Important Files and Paths

**Production paths:**
- `/etc/glocker/config.yaml` - Main configuration
- `/var/glocker/glocker.sock` - Unix socket for IPC
- `/var/log/glocker/reports.log` - Content monitoring logs
- `/var/log/glocker/unblocks.log` - Unblock request logs
- `/etc/hosts` - Modified with GLOCKER markers
- `/etc/sudoers.d/glocker` - glocker's sudo-window drop-in (`/etc/sudoers` itself is never edited)
- `/var/lib/glocker/sudo/takeover.json` (+ `moved/`) - what install took over from the user's sudo, replayed by uninstall
- `/usr/local/bin/glocker` - Installed binary (root:root 0755; not setuid)
- `/etc/systemd/system/glocker.service` - Systemd service file

**Development files:**
- `cmd/glocker/main.go` - Entry point (other binaries under `cmd/`: glockpeek, glocklock, glockdoc, configcheck, usageprobe, e2ekit, configtui)
- `internal/` - Application packages (see File Map above)
- `conf/conf.yaml` + `conf/conf.d/*.yaml` - Working config (gitignored, from `make init`) plus included blocklists
- `extras/glocker.service` - Systemd service template
- `extensions/firefox/`, `extensions/chrome/` - Browser extensions; `extensions/gnome/` - GNOME Shell extension


### Security Features

**Tamper Resistance**:
- Root-owned 0755 binary; no setuid
- Socket `root:glocker` 0660; `uninstall`/`resync` need real root (SO_PEERCRED)
- /etc/hosts block rewritten every enforcement cycle
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
- Evaluation: `utils.InAnyWindow` (internal/utils/time.go), used by
  internal/enforcement/domains.go, state.go, sudoers.go and monitoring/forbidden.go

### HTTP(s) Servers (port 80, 443)

Endpoints for browser extension communication (internal/web/handlers.go):
- `POST /report` - Content monitoring reports from extension
- `GET /keywords` - Returns current URL/content keyword lists
- `GET /keywords-stream` - Server-sent events: initial keyword snapshot + keepalives
- `GET /blocked` - Blocked page display (internal/web/blocked.go; served when a hosts-blocked domain is requested)

Server started by internal/web/server.go:StartWebTrackingServer()

**Stats dashboard (`internal/stats/` + `internal/store/`, served by the standalone
`glockpeek` process):**
- Served by **`cmd/glockpeek`** as its own binary + systemd service
  (`extras/glockpeek.service`), NOT by the glocker daemon. Reads its **own**
  config `/etc/glocker/glockpeek.yaml` (`config.GlockpeekConfigFile`, nested under
  a single `glockpeek:` stanza) for `listen` (default `127.0.0.1:4317`),
  `database` (driver+DSN), `secure_cookies`, `terms_url`, `mail`, `billing`, etc. This file is
  **hand-managed, not Ansible-templated**: it holds DB/mail/billing secrets and is
  the gitignored `conf/glockpeek.yaml` (sample: `conf/glockpeek.yaml.sample`),
  shipped to the box by `make deploy` (binary + config). Ansible (`deploy/ansible/`) only provisions the box — postgres,
  caddy, the service user, systemd — never this config.
- **DB-backed, multi-tenant.** `internal/store` is a GORM layer (sqlite via
  pure-Go `glebarez/sqlite`, or postgres) — the dashboard no longer parses log
  files. Every stats row carries a `UserID`; all reads/ingest are scoped to the
  authenticated account. Records are populated by the glocker syncer (see below)
  through the ingest API.
- **Two modes** (`glockpeek.mode`, default **local**). `cmd/glockpeek` maps mode →
  `stats.Options.Auth` (local→false, hosted→true).
  - **local**: `main.forceLoopback` pins the bind to `127.0.0.1` regardless of
    `glockpeek.listen`; `requireUser`/`requireToken` inject an implicit account
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
  - **User tag/rule content is client-sealed (E2E).** A user tag's
    `namespace`/`name` and a rule's `program`/`title` are sealed in the browser
    (`enc:v1:` via `sealReg`, `internal/stats/assets/app.js`) before they reach
    `/api/tags` / `/api/rules`, with a one-time idempotent migration that seals
    any still-cleartext rows. The DB columns are plain `string` but hold
    ciphertext; matching runs in the browser, so glockpeek stays content-blind.
    Sealing only applies once E2E is set up — without a master key the values are
    stored cleartext (system `Glocker:*` tags stay cleartext; they're not user
    content).
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
- Config `sync.{enabled, glockpeek_url, token, interval_seconds, encrypt,
  encrypt_key_file}`; token only for a hosted glockpeek; `encrypt` must be set
  explicitly when sync is enabled; `encrypt_key_file` defaults to `/etc/glocker/e2e.key`. Cursor is in-memory and, on startup, **seeded from glockpeek's
  high-water marks** (`seedCursors` GETs `/api/ingest`), so a plain restart only
  sends the gap — it does NOT re-push. To force a full re-push (e.g. to repair rows
  after a field that used to ship cleartext is now encrypted), `glocker -resync`
  (socket cmd → `syncer.Resync`) rewinds the cursors and backfills; because ingest
  upserts on the natural key, the re-push overwrites glockpeek's copies in place
  (no server-side delete, no data loss). `state.IsResyncing()` surfaces an
  in-flight backfill in `glocker -status`.

### Extension Communication Flow

1. Extension loads and fetches keywords: `GET http://127.0.0.1/keywords`
2. Extension monitors URLs/content using cached keyword regexes (extensions/{firefox,chrome}/background.js, content.js)
3. On violation: `POST http://127.0.0.1/report` with JSON payload
4. Glocker logs to file and increments violation counter (internal/web/handlers.go)
5. If violations exceed threshold: executes lock command (internal/monitoring/violations.go)

## Config versioning and upgrades

- `config_version` (top of the main config; absent = 0) is the config **format**
  version, covering the whole config including editable includes.
  `config.CurrentConfigVersion` is what this build reads.
- **New keys are always optional with a default** (like nullable columns) and do
  NOT bump the version. Bump it only for a rename, removal or change of meaning,
  and add the step to `migrations` in `internal/editor/migrate.go` (step i turns
  version i into i+1 on the node tree, so comments survive). A test checks there's
  one step per version.
- `-install` (`internal/install/configplan.go`) decides before touching anything:
  - nothing installed → install the source config (converted if older);
  - installed config newer than the build → abort (no downgrades); no conversion
    path or invalid after converting → abort; both say how to go ahead;
  - otherwise keep the installed config, converting it in place, unless a source
    config is given and either `-replace-config` is set, it matches the installed
    one, or the installed one is unchanged since the last install
    (`/var/lib/glocker/config.sha256`) — else it asks on /dev/tty (no tty = keep).
- Anything that changes the installed config is backed up first to
  `/var/lib/glocker/config-backups/<date>-<n>/` (newest 6 kept).
- Generated blocklists (`update_domains.py` marker) are always replaced; editable
  includes like `personal-domains.yaml` only travel with the main config.
- The tarball `install.sh`: `-init` asks keep/fresh when a config is installed
  (keep writes nothing), then `-install -replace-config`.
- `make dev-tarball` builds an unsigned tarball into `dist/dev/` for VM tests.

## Configuration Notes

`conf/conf.yaml` (gitignored; `make init`) is small; the extensive blocking lists live in `conf/conf.d/*.yaml`
(~16MB total) and are pulled in via the include mechanism. When making changes:
- There is no runtime reload; reinstall (`make install` / `make full-install`) to apply, or
  `sudo glocker -edit` on an installed config (restarts the daemon on save)
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
│                                #   glockdoc, configcheck, usageprobe, e2ekit, configtui)
├── internal/                    # Application packages (partial list)
│   ├── accountability/         # glockpeek accountability emails
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
├── extensions/                  # firefox/, chrome/ (browser), gnome/ (GNOME Shell)
├── conf/conf.yaml + conf/conf.d/ # Working config (gitignored) + included blocklists
└── extras/glocker.service      # Systemd service
```

**Package Dependencies:**
(internal imports, non-test)
- `cmd/glocker` imports `cli`, `config`, `connect`, `editor`, `enforcement`, `envcheck`, `install`, `ipc`, `mindful`, `monitoring`, `precheck`, `reports`, `state`, `syncer`, `usage`, `web`
- `cli` imports `config`, `enforcement`, `monitoring`, `reports`, `schedule`, `state`, `sudoplan`, `syncer`, `web`
- `ipc` imports `cli`, `config`, `enforcement`, `install`, `syncer`, `web`
- `install` imports `config`, `envcheck`, `precheck`, `sudoplan`, `utils`, `web`
- `schedule` imports `config`, `enforcement`, `monitoring`
- `syncer` imports `config`, `e2e`, `reports`, `state`, `usage`
- `web` imports `config`, `monitoring`, `reports`, `state`, `utils`
- `monitoring` imports `config`, `reports`, `state`, `utils`
- `enforcement` imports `config`, `state`, `sudoplan`, `utils`; `sudoplan` imports `config`, `precheck`, `utils`
- `usage` imports `config`, `utils`; `lock` imports `utils`; `connect` imports `config`, `e2e`
- `stats` imports `reports`, `store`; `report` imports `store`; `accountability` imports `config`, `report`, `store`; `mailer`, `reports` import `config`
- `editor` imports `config`, `utils`
- `utils` imports `config` (for `TimeWindow`) and nothing else
- `config`, `state`, `store`, `e2e`, `envcheck`, `precheck`, `heartbeat`, `mindful` import no internal packages

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
- `github.com/standard-webhooks/standard-webhooks/libraries` - billing webhook verification (glockpeek)
- `github.com/charmbracelet/bubbletea` + `lipgloss` - mindful-uninstall typing gate (`internal/mindful`) and the `-edit` UI (`internal/editor`)
- `github.com/charmbracelet/bubbles` + `huh` - viewport and text forms in `internal/editor`
- `github.com/pmezard/go-difflib` - line diffs for `-edit`'s minimal saves and review screen
- `golang.org/x/term` - passphrase/password prompts (glocker, glockpeek, e2ekit)
- `github.com/godbus/dbus/v5` - D-Bus (GNOME usage source, GNOME lock backend)
- `github.com/jezek/xgb` - X11 (usage monitor, glocklock X11 backend)
- `github.com/neurlang/wayland` + `github.com/tuxx/wayland-ext-session-lock-go` - Wayland (glocklock, idle)
- `golang.org/x/image` - glocklock rendering
- `github.com/google/renameio/v2` - atomic writes of the Firefox policy file (`install/firefox.go`) and `-edit` saves

System dependencies:
- `systemd` - Service management
- Firefox and/or Chrome/Chromium with extension support
