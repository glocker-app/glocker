# Architecture

## Overview

Glocker is a Linux-based distraction blocking system written in Go. It blocks distracting websites and applications using multiple enforcement mechanisms to make bypassing difficult. The tool runs as a privileged setuid systemd service and uses Unix sockets for IPC.

## System Design

Glocker is a **Go application** organized into modular packages under `internal/`:

```
glocker/
├── cmd/                        # Binaries
│   ├── glocker/                # Main daemon + CLI (entry point, flags)
│   ├── glocklock/              # Screen locker utility
│   └── glockpeek/              # Log analysis tool
├── internal/                   # Application packages
│   ├── cli/                    # Command processors
│   ├── config/                 # Configuration loading (YAML)
│   ├── enforcement/            # Core blocking logic
│   ├── install/                # Installation/uninstallation
│   ├── ipc/                    # Unix socket server
│   ├── monitoring/             # Background monitoring tasks
│   ├── notify/                 # Email notifications
│   ├── reports/                # Log parsing for glockpeek (reports, unblocks, lifecycle)
│   ├── state/                  # Thread-safe global state
│   ├── utils/                  # Shared utilities
│   └── web/                    # HTTP server for extension
├── extensions/firefox/         # Browser extension
├── conf/conf.yaml              # Sample configuration (~60MB)
└── extras/glocker.service      # Systemd service definition
```

## Monitoring Systems

Glocker uses multiple independent monitors that work together to enforce blocking:

### 1. Hosts File Blocking

**What it does:** Modifies `/etc/hosts` to redirect blocked domains to `127.0.0.1`

**How it works:**
- Writes blocked domains below `### GLOCKER START ###`.
- Sets file immutable using `chattr +i` (when enabled)
- Updates every 60 seconds (fixed internal cadence)
- Handles 800,000+ domains efficiently (memory optimization clears list after initial write)

**Time windows:** Can block domains only during specific times/days

```yaml
domains:
  # No windows → always blocked (permanent default)
  - {name: "reddit.com"}
  # Windows → blocked only during them
  - name: "twitter.com"
    block_windows:
      - start: "09:00"
        end: "17:00"
        days: ["Mon", "Tue", "Wed", "Thu", "Fri"]
```

### 2. Firewall Blocking (Optional)

**What it does:** Uses `iptables` to block domains at network level

**How it works:**
- Resolves domains to IPs and adds DROP rules
- More aggressive than hosts file (can't be bypassed by direct IP access)
- Disabled by default due to complexity
- Enable with `enable_firewall: true`

### 3. Web Tracking & Violation Recording

**What it does:** Catches attempts to access blocked sites via HTTP/HTTPS

**How it works:**
- Runs HTTP server on port 80 and HTTPS on port 443
- When browser tries to access blocked domain (redirected by hosts file), server intercepts
- Records violation to database (synced to glockpeek, which handles any alerting)
- Executes configured command (e.g., play alert sound)
- Shows blocking reason (always blocked, time-based, etc.)

**Configuration:**

```yaml
web_tracking:
  enabled: true
  command: "mpg123 /path/to/alert.mp3"  # Optional: command to run on block
```

### 4. Browser Extension Integration

**What it does:** Content monitoring for keywords and URL patterns in browsers

**How it works:**
- Firefox extension (in `extensions/firefox/`) monitors page URLs and content
- Fetches keyword lists from `http://127.0.0.1/keywords` API
- Scans page content for forbidden keywords
- Reports violations to `http://127.0.0.1/report` API
- Works with glocker's violation tracking system

**Configuration:**

```yaml
extension_keywords:
  url_keywords: ["gambling", "casino", "poker"]
  content_keywords: ["bet", "jackpot"]
  whitelist:
    - "stackoverflow.com"
    - "github.com"
```

### 5. Forbidden Programs Monitor

**What it does:** Kills specified programs based on per-program time windows

**How it works:**
- Checks every 5 seconds (configurable) for forbidden process names
- Process matching is a case-insensitive substring of the process name (`comm`)
- Two window modes per program:
  - `kill_windows` → killed **during** the listed windows
  - `allow_windows` → killed **outside** the listed windows (inverse semantics)
  - Neither → killed 24/7 (always forbidden)
- A kill during an active window counts as a violation
- Useful for blocking browsers, chat apps, games during work hours

**Runtime additions:** `glocker -block-app "steam,chromium"` appends programs to
this list at runtime — persisted to the config file and applied to the in-memory
list the monitor reads, so already-running matches are killed on the next tick.

**Extensions:** A program marked `extendible: true` can be granted a one-hour
reprieve via `glocker -extend "firefox:client call"` for legitimate edge cases.
Grants are capped at one per rolling 24 hours, persisted across daemon restarts,
and logged (synced to glockpeek for accountability).

**Configuration:**

```yaml
forbidden_programs:
  enabled: true
  check_interval_seconds: 5
  programs:
    - name: "chromium"            # killed during these hours
      kill_windows:
        - start: "20:00"
          end: "05:00"
          days: ["Mon", "Tue", "Wed", "Thu", "Fri"]
    - name: "steam"               # killed except during these hours
      allow_windows:
        - start: "19:00"
          end: "22:00"
          days: ["Fri", "Sat"]
    - name: "firefox"             # killed 22:00–05:00, one-hour extensions allowed
      extendible: true
      kill_windows:
        - {start: "22:00", end: "05:00", days: ["Mon","Tue","Wed","Thu","Fri","Sat","Sun"]}
    - name: "mullvadbrowser"      # no windows → always killed
```

### 5b. Kill-on-Block (DNS Cache Flush)

**What it does:** Kills configured browsers immediately after `glocker -block`
applies, so a newly blocked domain isn't still reachable from a browser's
internal DNS cache.

**How it works:**
- `cli.ProcessBlockRequest` calls `monitoring.KillOnBlock(cfg)` right after
  enforcement writes the new domain to `/etc/hosts`
- Matching reuses the forbidden-programs core (`terminateMatching`), but unlike
  forbidden programs it **ignores time windows** and is **not counted as a
  violation** — restarting a browser to re-read `/etc/hosts` is expected behavior
- The list is surfaced in `glocker -info`

**Configuration:**

```yaml
kill_on_block:
  - firefox
  - chromium
  - brave
```

### 6. Violation Tracking & Threshold Actions

**What it does:** Counts violations and triggers action when threshold exceeded

**How it works:**
- Counts violations (web access attempts, content keyword matches) in time window
- Executes command when threshold reached (e.g., lock screen)
- Resets counter after time window expires
- Can be used to escalate enforcement

**Configuration:**

```yaml
violation_tracking:
  enabled: true
  max_violations: 5
  time_window_minutes: 60
  command: "sudo -u yourusername DISPLAY=:0 glocklock -duration 5m"
  lock_duration: "5m"  # For glocklock
```

The command runs as root, so a GUI locker needs `sudo -u <your-user>` plus your
session's display env: `DISPLAY=:0` (check `echo $DISPLAY`) on X11, or
`WAYLAND_DISPLAY=… XDG_RUNTIME_DIR=/run/user/$(id -u)` on Wayland.

### 7. Sudoers Control

**What it does:** Restricts `sudo` access during blocking periods

**How it works:**
- Swaps between "allowed" and "blocked" sudoers lines based on time windows
- Prevents user from running `sudo` to bypass protections
- Can whitelist specific commands (e.g., suspend, package management)

**Configuration:**

```yaml
sudoers:
  enabled: true
  user: "yourusername"
  allowed_sudoers_line: "yourusername ALL=(ALL) NOPASSWD:ALL"
  blocked_sudoers_line: "yourusername ALL=(ALL) NOPASSWD: /usr/bin/apt, /sbin/modprobe"
  # sudo is ALLOWED during these windows, blocked outside them
  allow_windows:
    - start: "10:00"
      end: "16:00"
      days: ["Mon", "Tue", "Wed", "Thu", "Fri"]
```

### 8. Tamper Detection & Self-Healing

**What it does:** Monitors critical files for modifications and restores them

**How it works:**
- Calculates checksums of `/etc/hosts`, glocker binary, systemd service
- Checks every 30 seconds (configurable)
- Re-applies protections if tampering detected
- Executes alarm command (e.g., play sound, send notification)

**Configuration:**

```yaml
enable_self_healing: true
tamper_detection:
  enabled: true
  check_interval_seconds: 30
  alarm_command: "mpg123 /path/to/alarm.mp3"
```

### 9. Panic Mode

**What it does:** Emergency enforcement via system suspension

**How it works:**
- Suspends system using `pm-suspend` or `rtcwake`
- Monitors for early wake (via logind D-Bus signals)
- Re-suspends system if woken before timer expires
- Requires accountability partner to disable panic mode
- Nuclear option for moments of extreme distraction

**Usage:**

```bash
# Suspend for 30 minutes (re-suspends on early wake)
glocker -panic 30
```

**Configuration:**

```yaml
panic_command: "sudo pm-suspend"
```

### How Monitors Work Together

```
                     User attempts to access blocked site
                                    |
                                    v
     ┌──────────────────────────────────────────────────────┐
     |              DNS/Hosts Resolution                    |
     |  /etc/hosts redirects blocked.com → 127.0.0.1        |
     └──────────────────────────────────────────────────────┘
                                    |
                                    v
     ┌──────────────────────────────────────────────────────┐
     |         HTTP Request to 127.0.0.1:80/443             |
     |  Web tracking server intercepts request              |
     └──────────────────────────────────────────────────────┘
                                    |
                                    v
     ┌──────────────────────────────────────────────────────┐
     |              Violation Recording                     |
     |  • Increment violation counter                       |
     |  • Log to database with timestamp                    |
     |  • Execute web_tracking.command (alert sound)        |
     |  • Sync to glockpeek (handles any alerting)          |
     └──────────────────────────────────────────────────────┘
                                    |
                                    v
     ┌──────────────────────────────────────────────────────┐
     |           Check Violation Threshold                  |
     |  If count >= max_violations in time_window:          |
     |    Execute violation_tracking.command (lock screen)  |
     └──────────────────────────────────────────────────────┘
                                    |
                                    v
     ┌──────────────────────────────────────────────────────┐
     |              Show Blocking Page                      |
     |  Redirect to: http://127.0.0.1/blocked?domain=...    |
     |  Display blocking reason and time remaining          |
     └──────────────────────────────────────────────────────┘

     Meanwhile, in parallel:

     • Browser Extension monitors page content for keywords
     • Forbidden Programs monitor kills blocked apps
     • Sudoers control restricts sudo during work hours
     • Tamper detection ensures protections stay active
```

## Execution Model

### Daemon Mode (normal operation)

```
systemd launches glocker
         |
         v
  ┌──────────────────┐
  │  main.go         │
  │  -daemon flag    │
  └──────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Load config from YAML               │
  │  (/etc/glocker/config.yaml)          │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Start Unix socket server            │
  │  (/var/glocker/glocker.sock)                 │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Launch background goroutines:       │
  │  • Tamper detection monitor          │
  │  • Forbidden programs monitor        │
  │  • Violation tracking monitor        │
  │  • Panic mode monitor                │
  │  • Web tracking HTTP/HTTPS servers   │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Main enforcement loop (60s cycle):  │
  │  • Update /etc/hosts file            │
  │  • Update firewall rules             │
  │  • Update sudoers restrictions       │
  │  • Evaluate time windows             │
  │  • Apply file immutability           │
  └──────────────────────────────────────┘
```

### Client Mode (CLI commands)

```
User runs: glocker -status
         |
         v
  ┌──────────────────────────────────────┐
  │  Connect to /var/glocker/glocker.sock        │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Send command: "status"              │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Daemon processes request            │
  │  (internal/ipc/server.go)            │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Generate response                   │
  │  (internal/cli/commands.go)          │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Send response back to client        │
  └──────────────────────────────────────┘
         |
         v
  ┌──────────────────────────────────────┐
  │  Client displays formatted output    │
  └──────────────────────────────────────┘
```

## IPC Protocol

Communication between CLI and daemon uses Unix socket with simple text protocol:

**Format:** `"action:payload\n"`

**Examples:**
- `status\n` - Request runtime status
- `reload\n` - Reload configuration
- `unblock:youtube.com,reddit.com:work\n` - Temporarily unblock domains
- `block:facebook.com\n` - Permanently block domain (persists to config, kills `kill_on_block` browsers)
- `block-app:steam,chromium\n` - Add programs to the forbidden list (killed 24/7)
- `extend:firefox:client call\n` - Grant a one-hour run extension for an extendible program
- `add-keyword:gambling,casino\n` - Add monitoring keywords
- `lock\n` - Force sudoers lock immediately
- `panic:30\n` - Enter panic mode for 30 minutes
- `uninstall\n` - Uninstall glocker

**Responses:** Multi-line text ending with `"END\n"`

## Key Technical Details

### 1. Setuid Binary

Glocker is installed as a setuid root binary:
- Allows non-root users to run commands via socket
- Daemon runs with elevated privileges to modify `/etc/hosts`, iptables, sudoers
- Binary made immutable with `chattr +i` to prevent tampering

### 2. Memory Optimization

To handle 800,000+ domains:
- Full domain list loaded during initial enforcement
- List written to `/etc/hosts`
- **Domain list cleared from memory** after initial write to save RAM
- Only time-window domains (typically <10) kept cached
- On config reload, domains are loaded from disk temporarily

### 3. Lazy-Loaded Cache for Web Tracking

Web tracking needs domain info but domains are cleared from memory:
- **First violation:** Loads domain from config file (~3-4s)
- **Subsequent violations:** Uses cached domain info (~0.2s, 15-20x faster)
- Cache cleared on config reload
- Only accessed domains are cached (memory efficient)

### 4. Time Window Evaluation

```go
// Ported from internal/utils/time.go
func IsInTimeWindow(current, start, end string) bool {
    if start <= end {
        // Normal window: 09:00 to 17:00
        return current >= start && current <= end
    } else {
        // Midnight-crossing: 22:00 to 05:00
        return current >= start || current <= end
    }
}
```

### 5. Immutable File Protection

```bash
# Make /etc/hosts immutable (requires root)
chattr +i /etc/hosts

# Remove immutability to update
chattr -i /etc/hosts

# Check immutable status
lsattr /etc/hosts
```

### 6. Hosts File Format

```
# Standard entries
127.0.0.1 localhost
::1 localhost

### GLOCKER START ###
127.0.0.1 blocked-domain1.com
127.0.0.1 www.blocked-domain1.com
::1 blocked-domain1.com
::1 www.blocked-domain1.com
127.0.0.1 blocked-domain2.com
...

# User's custom entries below (preserved)
```

### 7. Browser Extension Communication

```
Firefox Extension (background.js)
         |
         v
  GET http://127.0.0.1/keywords
         |
         v
  {"url_keywords": [...], "content_keywords": [...]}
         |
         v
  Extension monitors page URLs and content
         |
         v
  POST http://127.0.0.1/report
  {"trigger": "url_keyword_match", "url": "...", "domain": "..."}
         |
         v
  Glocker records violation
```

### 8. Violation Tracking Flow

```go
// internal/monitoring/violations.go
func RecordViolation(cfg *config.Config, violationType, domain, url string) {
    // 1. Add to in-memory list
    violations = append(violations, Violation{
        Type: violationType,
        Domain: domain,
        URL: url,
        Timestamp: time.Now(),
    })

    // 2. Log to file
    logViolation(cfg, violation)

    // 3. Check threshold
    recentCount := countRecentViolations(cfg.ViolationTracking.TimeWindowMinutes)
    if recentCount >= cfg.ViolationTracking.MaxViolations {
        // 4. Execute command (e.g., lock screen)
        executeViolationCommand(cfg)
    }
}
```

## Dependencies

### Go Modules

- `github.com/mailgun/mailgun-go/v4` - Email, used only by glockpeek (account
  verification and notifications); the agent sends no email
- `gopkg.in/yaml.v3` - YAML configuration parsing

### System Requirements

- `iptables` / `ip6tables` - Firewall enforcement (optional)
- `systemd` - Service management
- `chattr` / `lsattr` - File immutability
- Firefox with extension support (for content monitoring)

### Runtime Paths

- `/etc/glocker/config.yaml` - Main configuration
- `/var/glocker/glocker.sock` - Unix socket for IPC
- `/var/log/glocker/reports.log` - Content monitoring / violation logs
- `/var/log/glocker/unblocks.log` - Unblock request logs
- `/var/log/glocker/lifecycle.log` - Install/uninstall events (reason + note); read by glockpeek to mark `UNMANAGED` periods
- `/usr/local/bin/glocker` - Installed binary (setuid root)
- `/etc/systemd/system/glocker.service` - Systemd service file

## Security Features

### Self-Healing

When enabled:
- Monitors critical file checksums (binary, /etc/hosts, systemd service)
- Re-applies protections if tampering detected
- Triggers alarm command on tampering

### Tamper Resistance

- Setuid root binary with immutable flag (`chattr +i`)
- Immutable /etc/hosts during blocking
- Sudoers restrictions prevent privilege escalation
- Mindful delay (configurable, default 60s) before uninstall completes

### Accountability

- Logs all unblock attempts with reasons
- Logs install/uninstall lifecycle events with a required reason (gated by the configured list) and optional note
- All accountability records (violations, unblocks, extension grants, lifecycle)
  are synced to glockpeek. Email notifications and the daily report are handled by
  glockpeek, per-account — the agent itself sends no email and needs no Mailgun key
  (glockpeek has its own `mail:` config, separate from the agent).
