# Installing glocker

Glocker is a Linux tool. Installing it means editing one config file and running
one make target — but because it runs as a privileged service and makes parts of
your system immutable, it's worth understanding each step.

## Requirements

- Linux with **systemd**
- **Go 1.26+** (to build), **python3** (for the blocklist script)
- **Root access** (`sudo`) — for the install, the systemd service, and the immutable files
- **Firefox or Chrome/Chromium** — for the content-monitoring extension

## 1. Get the code

```bash
git clone https://github.com/glocker-app/glocker
cd glocker
```

## 2. Edit your config

Everything is driven by **`conf/conf.yaml`** — this is the single source of truth.
`make full-install` regenerates `/etc/glocker/config.yaml` from it. (The runtime
commands `glocker -block`, `-block-app` and `-add-keyword` change things *in memory
only*; for anything permanent, edit `conf/conf.yaml`.) A fully commented reference lives in
`conf/conf.yaml.sample`.

`conf/conf.yaml` isn't in the repo; create a starting one with `make init`.
Then open it and set what applies to you:

| Section | What to set |
|---|---|
| `domains` | Sites to block. Add your own, and/or use the automated blocklists (step 3). |
| `user` | Your login name: whose `sudo` is held back and whose desktop is tracked. |
| `programs` | Programs to close, with `kill_windows` / `allow_windows`. |
| `sudoers` | The `allow_windows` when `sudo` is permitted. |
| `usage_monitor` | `display` (e.g. `:0`) and `xauthority` so the root daemon can see your X session for usage tracking. |
| `sync` | `enabled`, `glockpeek_url`, `token`, `encrypt`, `interval_seconds` — the agent → glockpeek sync. `encrypt` must be set when sync is on. |
| `violation_tracking` | Screen-lock threshold and command. |
| `mindful_uninstall` | The typing-gate friction for uninstalling. |
| `lifecycle` | Allowed `-uninstall` reasons. |

Time windows follow the same `HH:MM` + `days` shape everywhere; the key name
(`block_windows` / `allow_windows` / `kill_windows`) carries the meaning.

> **Heads up:** the sample config ships with placeholder values
> (username, `DISPLAY`, lock command). Replace them with your own before installing.

## 3. (Optional) Pull the blocklists

`make full-install` does this for you, but you can populate curated blocklists
(adult content, ads, malware, proxy-bypass sites) directly:

```bash
./scripts/update_domains.py        # list the available sources
./scripts/update_domains.py all    # pull them all into conf/conf.d/ (included by conf/conf.yaml)
```

## 4. Build and install

```bash
make full-install
```

This builds every binary, pulls the blocklists, and installs and starts the
services. (Under the hood: `make build-all`, then `scripts/update_domains.py all`, then
`sudo ./glocker -install`.) You'll be asked for `sudo`.

## 5. Browser extensions

The installer force-installs the content-monitor extension in Firefox and
Chrome/Chromium through browser policy; restart the browser to pick it up.
`about:policies` (Firefox) and `chrome://policy` (Chrome) show what's applied.

To work on the extension itself, load it unpacked instead:

- Firefox: `about:debugging#/runtime/this-firefox` → **Load Temporary Add-on** →
  `extensions/firefox/manifest.json`
- Chrome: `chrome://extensions` → Developer mode → **Load unpacked** →
  `extensions/chrome/`

## 6. Verify

```bash
glocker -status                    # blocking state, violations, sync status
xdg-open http://127.0.0.1:4317/    # the glockpeek dashboard
```

Check that `/etc/hosts` contains the `GLOCKER` block and that a blocked domain
resolves to `127.0.0.1`.

## Everyday use

```bash
glocker -status                          # runtime state
glocker -info                            # configured domains/programs/windows
glocker -unblock "youtube.com:research"   # temporary unblock, logged
glocker -extend "firefox:client call"     # one-hour program grant, once/24h
make install                             # apply config edits (there's no live reload)
sudo glocker -uninstall "maintenance"    # uninstall; reason required, mindful gate first
```

## Updating

After pulling new changes, re-run:

```bash
make full-install
```
