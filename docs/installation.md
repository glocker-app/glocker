# Installation and Usage Guide

## Quick Start

```bash
# Build all binaries
make build-all

# Install as systemd service (requires sudo)
sudo ./glocker -install

# Check status
glocker -status

# View configuration info
glocker -info

# Uninstall
sudo glocker -uninstall "reason for uninstalling"
```

## Command Line Usage

### Status and Information

```bash
# Show runtime status (violations, temp unblocks, panic mode)
glocker -status

# Show configuration info (blocked domains, time windows, forbidden programs)
glocker -info

# Show version information
glocker -version
```

### Domain Management

```bash
# Temporarily unblock domains (domains.unblocking.temp_unblock_time minutes,
# 30 if unset); the reason must be one of domains.unblocking.reasons
glocker -unblock "youtube.com:research"

# Block additional domains until glocker restarts (in memory only; also kills
# any kill_on_block browsers so the new block isn't masked by their DNS cache)
glocker -block "facebook.com,instagram.com"

# Add keywords to monitoring lists (URL and content; in memory only)
glocker -add-keyword "gambling,casino,poker"

# Add programs to the forbidden list (killed on sight, 24/7; in memory only)
glocker -block-app "steam,chromium"
```

### Control Commands

```bash
# Config edits apply on reinstall (there's no live reload)
make install

# Immediately lock sudo access (ignores time windows)
glocker -lock

# Enter panic mode: suspend for panic.duration minutes, re-suspending if woken early
glocker -panic
```

### Installation

```bash
# Install as a systemd service (needs sudo)
sudo ./glocker -install

# Uninstall and revert all system changes
sudo glocker -uninstall "testing new features"
```

All commands communicate with the running daemon via Unix socket (`/var/glocker/glocker.sock`). The `-daemon` flag is used internally by systemd and shouldn't be invoked manually.

## Utility Tools

### glockpeek - Dashboard

A database-backed web dashboard. It doesn't read the logs itself: the agent's
syncer (`sync:` in the config) pushes records to it. `glocker -install` installs
it as `glockpeek.service`, running in local mode on `http://127.0.0.1:4317/`
(sqlite at `/var/lib/glocker/glockpeek.db`). Its own config, if any, is
`/etc/glocker/glockpeek.yaml` (sample: `conf/glockpeek.yaml.sample`).

### glocklock - Screen Locker

A standalone timed screen locker (X11 and Wayland), designed for mindful breaks.
It will read the `violation_tracking` section from the config file and work
accordingly. However, the settings can be overridden by command line flags
listed below.

It automatically unlocks after a timeout:

```bash
# Lock using duration from config (default: 1 minute)
glocklock

# Lock for 5 minutes with custom message
glocklock -duration 5m -message "Break time"

# Use custom config file
glocklock -conf /path/to/config.yaml

# Custom background image
glocklock -background /path/to/image.png
```

**Configuration** (in `/etc/glocker/config.yaml`):

```yaml
violation_tracking:
  lock_duration: "5m"  # Duration: "30s", "5m", or plain number (seconds)
  background: "/path/to/image.png"  # Optional PNG/JPG background
```

## Browser Extension Installation

`glocker -install` force-installs the extension in Firefox (`extensions/firefox/`)
and Chrome/Chromium (`extensions/chrome/`) through browser policy, so there's
nothing to load by hand. It monitors URLs and page content based on keywords from
Glocker.

For development, load it unpacked: Firefox via
`about:debugging#/runtime/this-firefox` → "Load Temporary Add-on" →
`extensions/firefox/manifest.json`; Chrome via `chrome://extensions` → Developer
mode → "Load unpacked" → `extensions/chrome/`.

## Development

### Building

```bash
# Build all binaries (glocker, glocklock, glockpeek, glockdoc, ...)
make build-all

# Build single binary
go build -o glocker ./cmd/glocker
```

### Testing

```bash
# Run all tests
go test ./...

# Run specific package tests
go test ./internal/web -v
go test ./internal/enforcement -v
```

### Installing for Development

Install upgrades in place, so there's no need to uninstall first:

```bash
# First time only: generate conf/conf.yaml from the sample
make init

# Rebuild and reinstall (stops, swaps and restarts the daemon)
make install

# Check status
glocker -status
```

### Things to Verify During Testing

1. Killing of forbidden programs
2. Violation tracking
3. Making sure that blocked domains are in `/etc/hosts` properly
4. APIs for browser extension integration (confirm with curl)

### Testing Browser Extension

After making changes:
1. Navigate to `about:debugging#/runtime/this-firefox`
2. Load temporary extension from `extensions/firefox/manifest.json`
3. Check browser console for extension logs
4. Monitor `/var/log/glocker/reports.log` for content monitoring events

## System Requirements

- Go 1.26+ for building
- Linux with systemd
- Firefox or Chrome/Chromium (for the browser extension)
- Root access for installation

## File Locations

- **Binaries:** `/usr/local/bin/glocker` (0755, immutable), `glocklock`,
  `glockpeek`, `glockdoc` (same dir)
- **Config:** `/etc/glocker/config.yaml`, `/etc/glocker/conf.d/` (blocklists),
  `/etc/glocker/glockpeek.yaml` (dashboard), `/etc/glocker/e2e.key` (sync
  encryption key)
- **Services:** `/etc/systemd/system/glocker.service`, `glockpeek.service`,
  `glockdoc.service` + `glockdoc.timer` (heartbeat watchdog; or
  `/etc/cron.d/glocker-doc` without systemd timers)
- **Socket:** `/var/glocker/glocker.sock`
- **Logs:**
  - `/var/log/glocker/reports.log` (content monitoring)
  - `/var/log/glocker/unblocks.log` (unblock requests)
  - `/var/log/glocker/lifecycle.log` (install/uninstall)
  - `/var/log/glocker/usage.jsonl` (usage monitor)
  - `/var/log/glocker/heartbeat.jsonl` (glockdoc)
- **State:** `/var/lib/glocker/extensions.json` (`-extend` grants),
  `/var/lib/glocker/glockpeek.db` (dashboard DB),
  `/var/lib/glocker/firefox-policies.json` (prior Firefox policy values)
- **Shared files:** `/usr/local/share/glocker/` (Firefox xpi, glocklock background)
- **systemd logs:** `journalctl -u glocker.service`

## Troubleshooting

### Check Service Status

```bash
systemctl status glocker.service
```

### View Logs

```bash
# Follow service logs
journalctl -u glocker.service -f

# View violation logs
tail -f /var/log/glocker/reports.log

# View unblock logs
tail -f /var/log/glocker/unblocks.log
```

### Socket Communication Issues

```bash
# Check if socket exists
ls -l /var/glocker/glocker.sock

# Check if daemon is running
ps aux | grep glocker
```

### Hosts File Not Updating

```bash
# Check if hosts file is immutable
lsattr /etc/hosts

# Remove immutability (requires root)
sudo chattr -i /etc/hosts
```

### Permission Issues

The daemon runs as root under systemd; the `glocker` command talks to it over a
socket owned by the `glocker` group. Install adds your user to that group — log
out and back in (or run `newgrp glocker`) before using `glocker` without sudo.
`-install`, `-uninstall`, `-resync`, `-connect` and `-check` still need sudo.

```bash
id -nG | grep -w glocker        # you're in the group
ls -l /var/glocker/glocker.sock # srw-rw---- root glocker
```
