# Glocker

A Linux-based distraction blocking system that uses multiple enforcement mechanisms to help you stay focused.

## Why Glocker?

I needed an application that would block distracting sites and apps, but existing solutions didn't work for me:
- **Plucky** wasn't transparent about its strategies and didn't support Firefox
- **Accountable2You** couldn't get it working on Linux

Given the control that Linux offers for `root`, it's hard to make something that *really* blocks everything. However, it is possible to make it very tedious to break out. That's what this application does.

I've often found that there are liminal moments where I make the wrong decision in a fog of distraction. Having someone, or if not possible, something that makes it hard to make the wrong decision, lets me get back to work.

That's what Glocker tries to do.

## Quick Start

```bash
# Configure: copy the sample and edit it to taste
cp conf/conf.yaml.sample conf/conf.yaml
$EDITOR conf/conf.yaml

# Build, fetch the blocklists into conf.d/, and install as a systemd service.
# (Needs sudo. Aborts up front if a required capability is missing — see below.)
make full-install

# Check status
glocker -status

# Uninstall (if this ever can't run, see manual-uninstall.md)
sudo glocker -uninstall "reason for uninstalling"
```

The big blocklists aren't stored in `conf.yaml`. `update_domains.py` (run by
`make full-install`) fetches them from curated sources into `conf/conf.d/`, one
file per source, and `conf.yaml` pulls them in with `!include conf.d/…` lines.
Edit `conf.yaml` for your own settings and hand-picked domains; regenerate the
lists any time with `python3 update_domains.py all`.

Install **refuses up front** if a capability its enabled features need is missing
— no `systemd` (it runs as a service), or no `visudo` while sudoers management is
on. Missing `cron` (heartbeat watchdog) or immutable-flag support only warn;
blocking still works.

## What It Does

Glocker uses **8 independent monitoring systems** that work together:

1. **Hosts File Blocking** - Redirects blocked domains to `127.0.0.1`
2. **Web Tracking** - Intercepts HTTP/HTTPS requests, records violations
3. **Browser Extension** - Monitors page content for forbidden keywords
4. **Forbidden Programs** - Kills specified programs during time windows
5. **Violation Tracking** - Triggers actions when threshold exceeded (e.g., screen lock)
6. **Sudoers Control** - Restricts `sudo` access during blocking periods
7. **Tamper Detection** - Self-healing when critical files are modified
8. **Panic Mode** - Emergency system suspension with re-suspend on early wake

Each system can be independently enabled/disabled and configured with time windows for fine-grained control.

## Architecture

Glocker is three cooperating processes:

- **`glocker`** — the privileged agent. Does the enforcement and data
  collection: usage monitoring, `/etc/hosts` management, sudoers control, killing
  forbidden programs, mindful uninstalls, processing local commands (`-block`,
  `-unblock`, …), and running the local web server the browser extension reports
  violations into. It records everything to log files under `/var/log/`.
- **`glockpeek`** — the stats service and web dashboard, a **separate process**
  (its own systemd service, localhost only). It reads the same
  `/etc/glocker/config.yaml`, reads the logs directly, and serves the dashboard
  at the root (default `http://127.0.0.1:4317/`). The glocker daemon no
  longer serves the dashboard itself.
- **`glockdoc`** — the watchdog. Runs periodically and records whether glocker is
  alive to a heartbeat log, and is **deliberately left in place by an uninstall**
  so a silent teardown is still recorded.

`glocker` and `glockdoc` watch the machine locally; `glockpeek` reads and
displays. The longer-term direction — glocker syncing to a local *or hosted*
glockpeek, mutual glocker/glockdoc monitoring, end-to-end encryption, and an
open-core hosted tier — is captured in [`ROADMAP.md`](ROADMAP.md).

## Documentation

- **[Installation & Usage Guide](docs/installation.md)** - Commands, utilities, development setup
- **[Sample config](conf/conf.yaml.sample)** - Every option documented inline (the config reference)
- **[Architecture](docs/architecture.md)** - System design, monitoring systems, technical details

## Key Features

- **Time-Based Blocking** - Block sites only during work hours
- **Temporary Unblocking** - Unblock domains for short periods with logged reasons
- **Program Extensions** - Mark a forbidden program `extendible: true` to allow `glocker -extend` to grant a one-hour reprieve for legitimate edge cases (e.g. an unplanned evening business call); capped at one grant per rolling 24 hours, persisted across daemon restarts, and logged
- **Allow Windows for Programs** - In addition to `kill_windows`, programs can be configured with `allow_windows` (killed outside the listed times) for inverse semantics
- **Accountability** - Violations, unblocks, extension grants, and lifecycle events are synced to glockpeek; email notifications and the daily report are handled there, per-account, not by the agent (the agent sends no email and needs no Mailgun key)
- **DNS Cache Flushing** - Configured browsers (`kill_on_block`) are killed right after `glocker -block` applies, forcing a fresh lookup so a newly blocked domain isn't still reachable from a browser's internal DNS cache
- **Lifecycle Logging** - Install and uninstall events are recorded with a required reason and optional note; valid reasons are gated by config
- **Content Monitoring** - Firefox extension watches for keywords on any page
- **Screen Locker** - Timed auto-unlock, on X11 or Wayland (GNOME via the shell extension; wlroots/KDE via `ext-session-lock`)
- **Stats Dashboard** - Web dashboard (`glockpeek`) with violations, clean streaks, usage analytics, and exposure patterns
- **Panic Mode** - Nuclear option: suspend system and re-suspend on early wake

## Utilities

- **[glocker](cmd/glocker/)** - Main daemon and CLI (enforcement + data collection)
- **[glockpeek](cmd/glockpeek/)** - Standalone stats web dashboard; serves on localhost (default `:4317`), reading the logs
- **[glockdoc](cmd/glockdoc/)** - Liveness watchdog; records whether glocker is running (survives uninstall)
- **[glocklock](cmd/glocklock/)** - Screen locker (X11 and Wayland); locks for a fixed duration, then auto-unlocks

## Example Configuration

```yaml
domains:
  # Always blocked (permanent - default)
  - {name: "reddit.com"}

  # Always blocked, but can be temporarily unblocked
  - {name: "youtube.com", unblockable: true}

  # Time-based blocking - only blocked during the listed windows
  - name: "twitter.com"
    block_windows:
      - start: "09:00"
        end: "17:00"
        days: ["Mon", "Tue", "Wed", "Thu", "Fri"]

# Kill programs during the listed windows
forbidden_programs:
  programs:
    # Killed during the window (use kill_windows for "blocked during these hours")
    - name: "chromium"
      kill_windows:
        - start: "20:00"
          end: "05:00"
          days: ["Mon", "Tue", "Wed", "Thu", "Fri"]

    # Allowed only during the window (use allow_windows for "permitted during these hours")
    - name: "steam"
      allow_windows:
        - start: "19:00"
          end: "22:00"
          days: ["Fri", "Sat"]

    # Extendible: `glocker -extend firefox "client call"` grants one hour,
    # max once per rolling 24 hours.
    - name: "firefox"
      extendible: true
      kill_windows:
        - start: "22:00"
          end: "05:00"
          days: ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]

# Browsers cache DNS internally, so a domain freshly added via `glocker -block`
# stays reachable until they restart. Kill them on every -block to force a
# fresh lookup against the updated /etc/hosts. Ignores time windows and is not
# counted as a violation.
kill_on_block:
  - firefox
  - chromium
  - brave

# Lifecycle accountability: -uninstall must cite one of these reasons
lifecycle:
  log_file: "/var/log/glocker-lifecycle.log"
  reasons: ["maintenance", "hardware", "testing"]

# Lock screen after 5 violations in 60 minutes
violation_tracking:
  enabled: true
  max_violations: 5
  time_window_minutes: 60
  command: "glocklock -duration 5m"
```

**Domain Blocking Behavior:**
- **No time windows** → Always blocked (permanent by default)
- **Time windows specified** → Only blocked during those time windows
- **`unblockable: true`** → Domain can be temporarily unblocked (otherwise permanent)

See the [sample config](conf/conf.yaml.sample) — every option is documented inline.

## Command Examples

```bash
# Inspection
glocker -status          # Runtime state: blocked count, temp unblocks,
                         # active program extensions, recent violations,
                         # panic mode
glocker -info            # Static configuration: domains, programs,
                         # time windows, keywords

# Domain management
glocker -unblock "youtube.com,reddit.com:work research"
glocker -block "facebook.com,instagram.com"   # persisted to config; also kills
                                              # kill_on_block browsers to flush DNS
glocker -add-keyword "gambling,casino,poker"

# Forbidden-program management
glocker -block-app "steam,chromium"            # kill these on sight, 24/7
glocker -extend "firefox:client call with X"   # one hour, max once per 24h
                                               # (program must be extendible: true)

# Control
glocker -reload          # Reload config
glocker -lock            # Lock sudo immediately
glocker -panic 30        # Suspend for 30 minutes

# Uninstall with accountability
sudo glocker -uninstall "maintenance" -note "kernel upgrade"
```

## Security check (optional)

glocker restricts *your* sudo during blocked windows, but a determined user can
still reach root by other routes. Auditing (and closing) those is a personal
choice, so it's a separate read-only step — **not** part of install:

```bash
make security-check          # builds, then runs the check under sudo
# or directly:
sudo glocker -security-check
```

It reports the ways glocker could still be bypassed or recovered from — a root
password (`su`), an unprotected recovery mode, extra `sudoers` grants, SSH root
login, other sudo users, autologin — and prints the exact command to close each
(`close: …`). It changes nothing.

Hardening is deliberately **manual** — glocker won't touch your system security
config for you. Run the check, then apply the `close:` steps for whichever routes
you want shut. Keep at least one **break-glass**, though: recovery mode is flagged
to keep, because it's how you'd uninstall glocker if it ever broke (see
[manual-uninstall.md](manual-uninstall.md)).

## Stats Dashboard (glockpeek)

`glockpeek` runs as its own systemd service and serves a web dashboard of your
exposure and usage analytics. It is **DB-backed** (SQLite locally, Postgres for a
hosted instance — via GORM, so switching is just a driver + DSN change) and
**multi-tenant** under the hood, which lets the same code run as a zero-config
personal desktop tool or as a shared/hosted instance. The glocker agent doesn't
write to the dashboard directly; a syncer pushes local records into glockpeek's
**ingest API**, so the two can live on different machines (see PLAN, untracked).

glockpeek runs in one of two modes (`glockpeek_mode`, default `local`):

**`local` (default) — personal desktop.** Binds `127.0.0.1` only (unreachable
from other hosts), no login or account to create, and the ingest endpoint is open
to same-machine clients (no token). Just run it and open the page:

```bash
glockpeek                        # uses /etc/glocker/config.yaml
xdg-open http://127.0.0.1:4317/
```

Any local user on that machine can view it — fine for a personal desktop; use
`hosted` for a shared box.

**`hosted` — shared/remote instance.** Per-account logins (httpOnly **session
cookie**) + ingest **tokens** (the syncer sends `Authorization: Bearer <token>`,
which identifies its account) + isolation. Manage accounts with admin subcommands
(they touch the DB, so run them on the DB host as the service user):

```bash
glockpeek -adduser admin     # create a dashboard account (prompts for a password)
glockpeek -passwd  admin     # change a password
glockpeek -addtoken admin    # mint an ingest API token for the syncer (printed once)
```

- **Config** (`conf.yaml`): `glockpeek_mode` (`local`/`hosted`), `glockpeek_listen`
  (address, hosted mode), `database` (driver + DSN), `glockpeek_secure_cookies`
  (set true when served over HTTPS).

**How data gets there.** The glocker daemon keeps recording to its `/var` logs
(the source of truth) and enforcing offline; a **syncer** goroutine
(`sync.enabled`) mirrors new records up to glockpeek's ingest API — a one-shot
backfill at startup, then incremental on a timer. It's local-first: if glockpeek
is down the syncer just retries, and enforcement is never affected. The Data-sync
panel on the dashboard shows when the daemon last pushed. Point `sync.glockpeek_url`
at a remote host to sync off-box.

The dashboard surfaces violation totals and types, clean streaks, a
coverage/health score, per-day activity, and usage analytics (per-program
title-word breakdowns). Periods during which Glocker was uninstalled are shown as
`UNMANAGED` coverage gaps rather than silently absent.

The installed service currently runs as **root** (unprivileged operation is a
planned hardening step before self-hosting ships to others).

## Implementation

Glocker is a **Go application** that runs as a systemd service with setuid root privileges:

- **Daemon:** Runs enforcement loop every 60s, manages protections
- **CLI:** Communicates with daemon via Unix socket (`/tmp/glocker.sock`)
- **Browser Extension:** Firefox extension in [`extensions/firefox/`](extensions/firefox/)
- **Config:** YAML at `/etc/glocker/config.yaml` ([sample](conf/conf.yaml.sample)), with generated blocklists in `conf.d/` pulled in via `!include`

See [architecture documentation](docs/architecture.md) for detailed design.

## Project Structure

```
glocker/
├── cmd/                        # Binaries
│   ├── glocker/                # Main daemon
│   ├── glockpeek/              # Standalone stats web dashboard
│   ├── glockdoc/               # Liveness watchdog
│   └── glocklock/              # Screen locker
├── internal/                   # Application packages
│   ├── cli/                    # Command processors
│   ├── config/                 # Configuration loading
│   ├── enforcement/            # Core blocking logic
│   ├── monitoring/             # Background monitors
│   ├── web/                    # HTTP server for extension
│   └── ...
├── extensions/firefox/         # Browser extension
├── conf/conf.yaml.sample       # Sample config (lean; copy to conf.yaml)
├── conf/conf.d/                # Generated blocklists, pulled in via !include
├── update_domains.py           # Fetches blocklists into conf.d/
├── extras/glocker.service      # Systemd service (daemon)
├── extras/glockpeek.service    # Systemd service (dashboard)
└── docs/                       # Documentation
```

See [CLAUDE.md](CLAUDE.md) for complete file map and developer guide.

## Additional Resources

- **[Domain Blocklist Updater](update_domains.py)** - Automated domain list updates from curated sources
- **[Android Port Architecture](ANDROID.md)** - Design docs for Android version

## System Requirements

- Linux with systemd
- Go 1.25+ (for building)
- Firefox (for browser extension)
- Root access (for installation)

## License

See [LICENSE](LICENSE) file for details.

## Contributing

This is a personal tool that solves my specific problem. If it helps you too, great! Feel free to fork and adapt to your needs.
