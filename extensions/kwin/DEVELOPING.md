# glocker KWin usage bridge

Window-list source for usage tracking on **KDE Plasma / KWin (Wayland)**. KWin
blocks apps from reading the focused window, so a KWin script does it from inside
the compositor.

Unlike the GNOME Shell extension, a KWin script **cannot export a D-Bus
interface** — it can only call out (`callDBus`). So the roles are flipped: the
**glocker daemon owns the name** and the script pushes to it.

    daemon owns:  app.glocker.KWin   (object /app/glocker/KWin)
                  Push(s)            # JSON: [{class, instance, title, active}, ...]

The script (`glocker-usage/`) pushes the window list on every `windowActivated` /
`windowAdded` / `windowRemoved` / caption change, plus once on load.
`usage.KWinSource.Capture()` returns the last push; idle comes from
`ext-idle-notify-v1` (see `internal/usage/idle_wayland.go`).

Installed by `internal/install/kwin.go` to
`~/.local/share/kwin/scripts/glocker-usage/`, enabled via
`kwinrc [Plugins] glocker-usageEnabled=true`, loaded live with
`org.kde.KWin.reconfigure` (no relogin).

Check it by hand (in a Plasma session):

    # is the script enabled?
    kreadconfig6 --file kwinrc --group Plugins --key glocker-usageEnabled
    # does the daemon own the bridge? (after `glocker` is running)
    gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
      --method org.freedesktop.DBus.NameHasOwner app.glocker.KWin
