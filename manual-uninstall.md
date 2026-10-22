# Manual uninstall (escape hatch)

Normally you remove glocker with:

```bash
sudo glocker -uninstall "<reason>"
```

This page is the **fallback** for when that can't run — the daemon is dead, the
config is gone, there's no terminal for the typing gate, or the binary is broken.
It reverts everything by hand instead of through glocker.

Everything here needs **root**. That's by design: glocker restricts your `sudo`
during blocked windows, and a determined removal shouldn't be a one-keystroke
reflex. Root is still reachable — it's just not the `sudo` shortcut.

## Step 0 — become root

In rough order of least disruption:

1. **Wait for your allowed sudo window**, then `sudo -i`. No reboot. Use this
   for anything that isn't urgent.
2. **Recovery mode.** Reboot, pick the recovery / "Advanced options" entry in
   GRUB, drop to a root shell. Always works unless GRUB itself is
   password-protected — but you lose your session. This is the "it's broken and I
   can't wait" path.
3. **`su -`** — only if the root account has a password you set (on most desktop
   installs it's locked, so this won't work).

`sudo` being blocked mid-window is *not* a lockout: options 2 and 3 don't go
through sudoers.

## Step 1 — stop the daemon

It re-applies everything (immutable flags, hosts, sudoers) every ~60s, so stop it
first or your edits get overwritten:

```bash
systemctl stop glocker glockpeek
systemctl disable glocker glockpeek
```

Only `glocker.service` is immutable (`glockpeek.service` isn't), but stopping and
disabling a service doesn't touch the file, so this still works either way. (`Restart=always` only re-runs a *crashed*
service; an explicit `stop` stays stopped.)

## Step 2 — clear the immutable flags

glocker marks these files immutable (`chattr +i`) so a quick edit won't stick.
Clear them before you can edit or remove them:

```bash
chattr -i /etc/hosts \
          /etc/glocker/config.yaml \
          /etc/glocker/conf.d/*.yaml \
          /usr/local/bin/glocker \
          /usr/local/bin/glocklock \
          /etc/systemd/system/glocker.service
```

(If `/etc/glocker/conf.d/` doesn't exist, ignore that line.)

"No such file" for anything already gone is fine — ignore it.

## Step 3 — restore /etc/hosts

glocker's block is appended at the end of the file, starting at its marker.
Delete from that marker to the end:

```bash
sed -i '/### GLOCKER START ###/,$d' /etc/hosts
```

Then confirm nothing of yours lived below it (it never should):

```bash
grep GLOCKER /etc/hosts || echo "hosts is clean"
```

## Step 4 — restore sudoers

So your normal `sudo` works again. Prefer glocker's backup; always go through
`visudo` so you can't save a broken file (a broken `/etc/sudoers` breaks `sudo`
for everyone):

```bash
# If the backup exists and is valid, use it:
visudo -c -f /etc/sudoers.glocker.backup && cp /etc/sudoers.glocker.backup /etc/sudoers

# Otherwise edit by hand and delete the line ending in "# GLOCKER-MANAGED":
visudo
```

## Step 5 — remove glocker's files

```bash
rm -f /usr/local/bin/glocker /usr/local/bin/glocklock \
      /usr/local/bin/glockpeek /usr/local/bin/glockdoc
rm -f /etc/systemd/system/glocker.service /etc/systemd/system/glockpeek.service
systemctl disable --now glockdoc.timer 2>/dev/null   # the glockdoc heartbeat timer
rm -f /etc/systemd/system/glockdoc.timer /etc/systemd/system/glockdoc.service
rm -f /etc/cron.d/glocker-doc          # the glockdoc heartbeat cron (older installs / no systemd timer)
rm -f /etc/sudoers.glocker.backup
rm -rf /var/glocker                    # the socket dir
rm -rf /etc/glocker                    # includes e2e.key (your encryption key); keep a copy to reinstall without -connect
rm -f /etc/opt/chrome/policies/managed/glocker.json \
      /etc/chromium/policies/managed/glocker.json \
      /etc/brave/policies/managed/glocker.json \
      /etc/opt/edge/policies/managed/glocker.json
groupdel glocker
systemctl daemon-reload
```

`/var/lib/glocker/glockpeek.db` is the local dashboard's database (your synced
history). Delete it only if you don't want that data:

```bash
rm -f /var/lib/glocker/glockpeek.db
```

Optional leftovers (logs, browser bits and small state files, harmless to keep):

```bash
rm -rf /var/log/glocker/ /var/log/glocker-*.log /var/log/glocker-*.jsonl
# Firefox: in /etc/firefox/policies/policies.json delete the glocker@nibrahim.net.in
#          ExtensionSettings entry and DisablePrivateBrowsing (any earlier values
#          are in /var/lib/glocker/firefox-policies.json), then:
rm -f /usr/local/share/glocker/glocker.xpi /var/lib/glocker/firefox-policies.json
rm -rf /usr/local/share/glocker/       # also holds lock-background.png
rm -f /var/lib/glocker/extensions.json # -extend cooldown state
# GNOME (as your user):
#   rm -rf ~/.local/share/gnome-shell/extensions/glocker-usage@glocker.app
#   then drop 'glocker-usage@glocker.app' from:
#   gsettings get org.gnome.shell enabled-extensions
```

## Step 6 — verify

```bash
pgrep -a glocker || echo "no glocker processes"
lsattr /etc/hosts                       # no 'i' in the flags
grep GLOCKER /etc/hosts || echo "hosts clean"
sudo -v && echo "sudo works"            # run outside a blocked window
```

If all four look right, glocker is fully removed.
