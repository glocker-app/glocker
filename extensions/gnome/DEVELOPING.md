# Glocker GNOME Shell extension

On GNOME/Wayland, Mutter blocks apps from reading the window list
(`org.gnome.Shell.Introspect` and `Eval` are both access-denied). This tiny
extension runs inside the shell — where it *can* see the windows — and re-exposes
them over a private session-bus name that glocker's usage tracker reads.

## D-Bus contract (stable across all builds)

    name:    app.glocker.Usage
    path:    /app/glocker/Usage
    method:  GetWindows() -> s     # JSON: [{class, instance, title, active}, ...]

Idle time is read separately from `org.gnome.Mutter.IdleMonitor` (no extension
needed). The Go usage source depends only on this contract, never on the GNOME
version — so supporting an older shell means a different `extension.js` exposing
the *same* method, with no Go changes.

## Builds by GNOME version

- `glocker-usage@glocker.app/` — GNOME **45+** (ES modules). Covers all current
  distros. Bumping to a newer GNOME is a one-line `shell-version` change.
- Pre-45 (CommonJS) would be a separate build under the same `uuid`; the agent's
  installer picks the build matching `gnome-shell --version`.

## Installation

`glocker -install` (via `make full-install`) installs and enables this
automatically for the desktop user — it copies the build matching
`gnome-shell --version` into `~/.local/share/gnome-shell/extensions/`, enables it
in the user's dconf, and `glocker -uninstall` removes it again. It self-skips on
non-GNOME systems and on GNOME older than 45. The daemon (running as root) reaches
the session bus through the top-level `user` (`unix:path=/run/user/<uid>/bus`);
`usage_monitor.dbus_address` overrides it. **On
Wayland you must log out and back in once after install** — the shell can't
hot-load a new extension.

## Install / test manually (e.g. in a nested GNOME session)

    uuid=glocker-usage@glocker.app
    dst=~/.local/share/gnome-shell/extensions/$uuid
    mkdir -p "$dst"
    cp extensions/gnome/$uuid/* "$dst/"
    gnome-extensions enable "$uuid"
    # On Wayland the shell can't reload in place — restart the (nested) session,
    # then confirm the bridge is up:
    gdbus call --session --dest app.glocker.Usage \
      --object-path /app/glocker/Usage \
      --method app.glocker.Usage.GetWindows
