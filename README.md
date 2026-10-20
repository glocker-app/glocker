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
# Configure: run the setup wizard (writes conf/conf.yaml from the sample and
# fills in your login; won't overwrite an existing file), then edit to taste
make init
$EDITOR conf/conf.yaml

# Build, fetch the blocklists into conf.d/, and install as a systemd service.
# (Needs sudo. Aborts up front if a required capability is missing — see below.)
make full-install

# Check status
glocker -status

# Uninstall (if this ever can't run, see manual-uninstall.md)
sudo glocker -uninstall "reason for uninstalling"
```

The big blocklists aren't stored in `conf.yaml`. `scripts/update_domains.py` (run by
`make full-install`) fetches them from curated sources into `conf/conf.d/`, one
file per source, and `conf.yaml` pulls them in with `!include conf.d/…` lines.
Edit `conf.yaml` for your own settings and hand-picked domains; regenerate the
lists any time with `python3 scripts/update_domains.py all`.

Install **refuses up front** if a capability its enabled features need is missing
— no `systemd` (it runs as a service), or no `visudo` while sudoers management is
on. Missing immutable-flag support only warns; blocking still works. The
heartbeat watchdog (`glockdoc`) is scheduled with a systemd timer
(`glockdoc.timer`), falling back to cron only if the timer can't be set up.

### Connect to the hosted dashboard

The Quick Start above gives you a **local** dashboard on `127.0.0.1:4317`. To
sync to the hosted dashboard at <https://my.glockerapp.com> instead:

1. **Register** an account at <https://my.glockerapp.com> and confirm your email.
2. **Set an encryption passphrase** (Settings → Encryption,
   `/settings/encryption`). This creates your key vault: records are encrypted on
   your machine, and the server only ever stores ciphertext. Losing the
   passphrase means losing access to that data, so keep it somewhere safe.
3. **Mint a device token** on the **Devices** page. It's shown once, so copy it.
4. **Point the agent at it.** Set the `sync:` stanza in `conf/conf.yaml`:

   ```yaml
   sync:
     enabled: true
     glockpeek_url: "https://my.glockerapp.com"
     token: "<device token from step 3>"
     encrypt: true
   ```

5. **Install** so the daemon picks up the config: `make full-install` (or
   `make install` if you've already fetched the blocklists).
6. **Connect** this machine to your vault. It prompts for the passphrase from
   step 2 and installs the key under `/etc/glocker`:

   ```bash
   sudo glocker -connect
   sudo systemctl restart glocker
   ```

`glocker -status` then shows the last push to the dashboard. Accountability
emails to a buddy are configured in the same file (`accountability:`); see the
[sample config](conf/conf.yaml.sample).

## What It Does

Glocker uses **seven independent enforcement mechanisms** that work together:

1. **Hosts-file blocking** - Redirects blocked domains to `127.0.0.1` (the file is made immutable)
2. **Browser extension** - Watches page URLs and content for forbidden keywords (Firefox), reporting hits to a local server
3. **Forbidden programs** - Kills specified programs during time windows
4. **Violation tracking** - Triggers an action when a threshold is crossed (e.g., screen lock)
5. **Sudoers control** - Restricts `sudo` access outside the allowed windows
6. **Mindful uninstall** - A paced typing challenge before `-uninstall` can complete
7. **Panic mode** - Emergency system suspension, re-suspending on early wake

Each mechanism can be independently enabled/disabled and configured with time windows for fine-grained control.

## Architecture

Glocker is three cooperating processes:

- **`glocker`** — the privileged agent. Does the enforcement and data
  collection: usage monitoring, `/etc/hosts` management, sudoers control, killing
  forbidden programs, mindful uninstalls, processing local commands (`-block`,
  `-unblock`, …), and running the local web server the browser extension reports
  violations into. It records everything to log files under `/var/log/`.
- **`glockpeek`** — the stats service and web dashboard, a **separate process**
  (its own systemd service, localhost by default). It's **DB-backed** (SQLite
  locally): rather than parsing the logs, it ingests records the glocker daemon's
  syncer pushes to it. It reads its own config at `/etc/glocker/glockpeek.yaml`
  and serves the dashboard at the root (default `http://127.0.0.1:4317/`). The
  glocker daemon no longer serves the dashboard itself.
- **`glockdoc`** — the watchdog. Runs periodically and records whether glocker is
  alive to a heartbeat log, and is **deliberately left in place by an uninstall**
  so a silent teardown is still recorded.

`glocker` and `glockdoc` watch the machine locally; `glockpeek` reads and
displays. glocker syncs to a local *or* hosted glockpeek, with end-to-end
encryption so a hosted dashboard only ever stores ciphertext.

## Documentation

- **[User guide](docs/habits.md)** - The why and the how: habit psychology plus a walkthrough of every feature and config stanza
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
- **Content Monitoring** - Browser extension (Firefox, Chrome/Chromium) watches for keywords on any page
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
  enabled: true
  hosts_path: "/etc/hosts"
  # Browsers cache DNS internally, so a domain freshly added via `glocker -block`
  # stays reachable until they restart. Killing them on every -block forces a
  # fresh lookup against the updated /etc/hosts.
  kill_on_block: [firefox, chromium, brave]
  unblocking:
    reasons: ["work", "research", "emergency"]
    temp_unblock_time: 20     # minutes a domain stays unblocked
    max_per_day: 3            # 0 = unlimited
  list:
    - name: "reddit.com"                      # always blocked (permanent)
    - name: "youtube.com"
      unblockable: true                       # can be temporarily unblocked
    - name: "twitter.com"                     # blocked only during the windows
      block_windows:
        - start: "09:00"
          end: "17:00"
          days: ["Mon", "Tue", "Wed", "Thu", "Fri"]

programs:
  enabled: true
  check_interval_seconds: 5
  list:
    - name: "chromium"                        # killed DURING the window
      kill_windows:
        - start: "20:00"
          end: "05:00"
          days: ["Mon", "Tue", "Wed", "Thu", "Fri"]
    - name: "steam"                           # allowed ONLY during the window
      allow_windows:
        - start: "19:00"
          end: "22:00"
          days: ["Fri", "Sat"]
    - name: "firefox"                         # `glocker -extend "firefox:reason"`
      extendible: true                        #   grants 1h, max once per 24h
      kill_windows:
        - start: "22:00"
          end: "05:00"
          days: ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]

# Lifecycle accountability: -uninstall must cite one of these reasons
lifecycle:
  log_file: "/var/log/glocker/lifecycle.log"
  reasons: ["maintenance", "hardware", "testing"]

# Lock the screen after 5 violations in 60 minutes
violation_tracking:
  enabled: true
  max_violations: 5
  time_window_minutes: 60
  # Runs as root, so run the locker as you; it finds your X11/Wayland session.
  command: "sudo -u yourusername /usr/local/bin/glocklock -duration 5m"
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
glocker -unblock "youtube.com:research"     # reason must be one of domains.unblocking.reasons
glocker -block "facebook.com,instagram.com"   # in-memory only (until restart);
                                              # also kills kill_on_block browsers to flush DNS
glocker -add-keyword "gambling,casino,poker"

# Forbidden-program management
glocker -block-app "steam,chromium"            # kill these on sight, 24/7
glocker -extend "firefox:client call with X"   # one hour, max once per 24h
                                               # (program must be extendible: true)

# Control
glocker -lock            # Lock sudo immediately
glocker -panic           # Suspend now for panic.duration minutes (set in config)
# (No runtime reload — edit conf/conf.yaml and re-run `make install` to apply.)

# Uninstall with accountability
sudo glocker -uninstall "maintenance" -note "kernel upgrade"
```

## Security check (optional)

glocker restricts *your* sudo during blocked windows, but a determined user can
still reach root by other routes. Auditing (and closing) those is a personal
choice, so it's a separate read-only step — **not** part of install:

```bash
sudo glocker -check
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

## Firefox extension

Content monitoring runs as a Firefox extension that `glocker -install` sets up
**automatically**: it fetches the Mozilla-signed `.xpi` from
`https://glockerapp.com/glocker.xpi` and force-installs it system-wide via a
Firefox enterprise policy (`/etc/firefox/policies/policies.json`) — no store
visit, and it can't be toggled off in `about:addons`. Policies already in that
file are kept; the ones glocker changes are restored on uninstall.

If the installer couldn't fetch it (e.g. no network during install), it prints a
warning and Firefox content monitoring stays **inactive** — everything else
(domain blocking, forbidden programs, sudoers) keeps working. To add it by hand:

```bash
# 1. Download the signed extension
sudo install -d /usr/local/share/glocker
sudo curl -fL -o /usr/local/share/glocker/glocker.xpi https://glockerapp.com/glocker.xpi

# 2. Re-run the installer — it detects the local xpi and installs the policy
sudo glocker -install
```

**Note — snap / Flatpak Firefox:** Firefox installed via snap (the Ubuntu default)
or Flatpak does **not** read `/etc/firefox/policies/`, so the enterprise install
can't reach it. Use the distro `.deb` Firefox for the extension to force-install.

### Chrome / Chromium

`glocker -install` also writes a managed policy (`glocker.json`) into the Chrome,
Chromium, Brave and Edge policy dirs (`/etc/opt/chrome/policies/managed/`,
`/etc/chromium/policies/managed/`, …). It force-installs the same extension from
`https://glockerapp.com/chrome/update.xml` in every profile, where it can't be
disabled, and turns off incognito, guest windows, secure DNS and Brave's Tor
windows. Chrome re-checks that URL every few hours for updates. If the installer
can't reach it, it warns; the policy stays and Chrome installs the extension once
it can. Uninstall removes only glocker's policy file. Tested on Chrome and
Chromium; snap / Flatpak builds ignore `/etc` policies, as with Firefox.

## Stats Dashboard (glockpeek)

`glockpeek` runs as its own systemd service and serves a web dashboard of your
exposure and usage analytics. It is **DB-backed** (SQLite locally, Postgres for a
hosted instance — via GORM, so switching is just a driver + DSN change) and
**multi-tenant** under the hood, which lets the same code run as a zero-config
personal desktop tool or as a shared/hosted instance. The glocker agent doesn't
write to the dashboard directly; a syncer pushes local records into glockpeek's
**ingest API**, so the two can live on different machines.

glockpeek runs in one of two modes (`mode`, default `local`):

**`local` (default) — personal desktop.** Binds `127.0.0.1` only (unreachable
from other hosts), no login or account to create, and the ingest endpoint is open
to same-machine clients (no token). Just run it and open the page:

```bash
glockpeek                        # uses /etc/glocker/glockpeek.yaml
xdg-open http://127.0.0.1:4317/
```

Any local user on that machine can view it — fine for a personal desktop; use
`hosted` for a shared box.

**`hosted` — shared/remote instance.** Per-account logins (httpOnly **session
cookie**) + ingest **tokens** (the syncer sends `Authorization: Bearer <token>`,
which identifies its account) + isolation. Manage accounts with admin subcommands
(they touch the DB, so run them on the DB host as the service user):

```bash
glockpeek -adduser you@example.com     # create a dashboard account (prompts for a password)
glockpeek -passwd  you@example.com     # change a password
glockpeek -addtoken you@example.com    # mint an ingest API token for the syncer (printed once)
```

- **Config** (`/etc/glocker/glockpeek.yaml`, everything under the `glockpeek:`
  stanza): `mode` (`local`/`hosted`), `listen` (address, hosted mode), `database`
  (driver + DSN), `secure_cookies` (set true when served over HTTPS).

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

Glocker is a **Go application** that runs as a root systemd service; the `glocker` command reaches it through a socket owned by the `glocker` group (install adds you to it — log out and in once):

- **Daemon:** Runs enforcement loop every 60s, manages protections
- **CLI:** Communicates with daemon via Unix socket (`/var/glocker/glocker.sock`)
- **Browser Extensions:** Firefox in [`extensions/firefox/`](extensions/firefox/), Chrome/Chromium in [`extensions/chrome/`](extensions/chrome/)
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
├── scripts/update_domains.py           # Fetches blocklists into conf.d/
├── extras/glocker.service      # Systemd service (daemon)
├── extras/glockpeek.service    # Systemd service (dashboard)
└── docs/                       # Documentation
```

See [CLAUDE.md](CLAUDE.md) for complete file map and developer guide.

## Additional Resources

- **[Domain Blocklist Updater](scripts/update_domains.py)** - Automated domain list updates from curated sources
- **[Android Port Architecture](ANDROID.md)** - Design docs for Android version

## Verifying a release

The primary path is building from source (`make full-install`). If you instead
download **prebuilt binaries** from a GitHub release, verify them first — each
release ships a `SHA256SUMS` file signed with the project's
[minisign](https://jedisct1.github.io/minisign/) key. The signature covers the
checksums; the checksums cover the binaries.

Public key (also at `https://glockerapp.com/minisign.pub`):

```
<paste the minisign public key here — the RWQ… line from minisign.pub>
```

```bash
# 1. confirm SHA256SUMS is authentically signed
minisign -Vm SHA256SUMS -P '<public key>'      # or: minisign -Vm SHA256SUMS -p minisign.pub
# 2. confirm the binaries match their checksums
sha256sum -c SHA256SUMS
```

If either step fails, don't run the binary. The signing key is offline, so a
valid signature holds even if GitHub or the website were compromised.

## System Requirements

- Linux with systemd
- Go 1.26+ (for building)
- Firefox or Chrome/Chromium (for the browser extension)
- Root access (for installation)

## License

Glocker is free software under the **GNU AGPLv3** — see [LICENSE](LICENSE).

## Contributing

This is a personal tool that solves my specific problem. If it helps you too, great! Feel free to fork and adapt to your needs.
