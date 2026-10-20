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

### glockpeek - Log Analysis

A read-only command-line tool for analyzing Glocker's violation, unblock, and
lifecycle logs with visual summaries. Needs no daemon or root.

**Summary View** (default)

```bash
# Violations summary (totals, by type, time of day, top keywords/domains)
glockpeek

# Unblocks summary (totals, top domains, reasons, day of week)
glockpeek -unblocks
```

The violations summary also reports **unmanaged time** — periods when Glocker
was uninstalled (from the lifecycle log). Because that is a bypass of the
blocking regime, it is shown in red and counted as a violation, with the total
duration, number of periods, and uninstall reasons. The window respects any
`-from`/`-to` filter.

**Date Filtering** — accepts `YYYY`, `YYYY-MM`, or `YYYY-MM-DD`:

```bash
glockpeek -from 2024
glockpeek -from 2024-06
glockpeek -from 2024-06-15 -to 2024-06-30
glockpeek -unblocks -from 2024-06       # filtering works with -unblocks too
```

**Detailed Views** — `-detail` selects day/week/month/year granularity from the
input and accepts exact dates or natural-language expressions:

```bash
glockpeek -detail 2024-06-15       # hour-by-hour breakdown for a day
glockpeek -detail yesterday        # natural-language day
glockpeek -detail 'last week'      # day-by-day, Mon–Sun
glockpeek -detail 2024-06          # daily calendar for a month
glockpeek -detail 2024             # month-by-month rollup for a year
```

The output includes:
- Colored bar charts (red for above average, green for below)
- Inverse video highlighting for egregious periods (>2 violations)
- Top offenders by frequency
- Time-of-day patterns
- `UNMANAGED` markers (from the lifecycle log) for periods when Glocker was
  uninstalled, annotated with the recorded reason/note

### glocklock - Screen Locker

A standalone X11 screen locker with two modes, designed for mindful breaks.
It will read the `violation_tracking` section from the config file and work
accordingly. However, the settings can be overridden by command line flags
listed below.

**Time-based Mode** - Automatically unlocks after a timeout:

```bash
# Lock using duration from config (default: 1 minute)
glocklock

# Lock for 5 minutes with custom message
glocklock -duration 5m -message "Break time"

# Use custom config file
glocklock -conf /path/to/config.yaml
```

glocklock locks for a fixed duration and then auto-unlocks (X11 and Wayland).

**Configuration** (in `/etc/glocker/config.yaml`):

```yaml
violation_tracking:
  lock_duration: "5m"  # Duration: "30s", "5m", or plain number (seconds)
  background: "/path/to/image.png"  # Optional PNG/JPG background (X11 backend)
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
# Build all binaries (glocker, glocklock, glockpeek)
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

```bash
# Uninstall current version
sudo glocker -uninstall "testing"

# Build new version
make build-all

# Install new version
sudo ./glocker -install

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

- **Binary:** `/usr/local/bin/glocker` (0755, immutable)
- **Config:** `/etc/glocker/config.yaml`
- **Service:** `/etc/systemd/system/glocker.service`
- **Socket:** `/var/glocker/glocker.sock`
- **Logs:**
  - `/var/log/glocker/reports.log` (content monitoring)
  - `/var/log/glocker/unblocks.log` (unblock requests)
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
