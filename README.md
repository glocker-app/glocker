# Glocker

Glocker helps you break digital habits on Linux. It blocks distracting sites and
apps on a schedule, makes them *annoying* to reach when you slip, and logs your
usage so you can see the patterns and tighten the rules over time.

It's opinionated — built by one person (on Debian + Firefox) to break their own
habits — but if your setup is similar it may help you too.

Website: **[glockerapp.com](https://glockerapp.com)**. The
**[guide](https://glockerapp.com/habits.html)** explains both the thinking and the
features, and is the source of truth for how glocker works.

## The programs

Glocker is a suite of four:

- **glocker** — the main daemon: blocking, usage logging, and the CLI.
- **glocklock** — a timed screen lock you can't dismiss until it expires.
- **glockpeek** — the web dashboard: usage and behaviour insights.
- **glockdoc** — a small watchdog that records whether glocker is alive (left in
  place by an uninstall, so a silent teardown is still logged).

## How it blocks

Each mechanism is independently configurable, usually with time windows:

- **Hosts-file blocking** of domains (made immutable with `chattr`), always or
  only during set windows; unblockable domains can be lifted briefly with a
  logged reason (`glocker -unblock`).
- **Forbidden programs** killed on sight during kill windows (e.g. media players
  at night); mark one `extendible` for a one-hour `glocker -extend` reprieve.
- A **sudo window** (Debian, Ubuntu, Fedora, Omarchy, Arch: install takes over your sudo
  access and uninstall gives it back) and a paced **mindful-uninstall** challenge, to raise
  the cost of turning glocker off.
- **Content monitoring** — a Firefox/Chrome extension flags keywords on any page;
  repeated hits can trip a screen lock (**violation tracking** → `glocklock`).
- **Panic mode** (suspend, re-suspend on early wake) and **lifecycle logging**
  that makes `-uninstall` cite a reason.

Config lives in `conf/conf.yaml`. The big blocklists are generated into
`conf/conf.d/` by `scripts/update_domains.py` and pulled in with `!include`
lines; `conf/conf.yaml.sample` documents every option inline.

## Quick start

On amd64, the installer downloads the latest signed release, verifies it, and
installs (asking for sudo):

```bash
curl -fsSL https://glockerapp.com/glocker-installer.sh | sh
glocker -status
```

Prefer to read it first? Download
[glocker-installer.sh](https://glockerapp.com/glocker-installer.sh) and run it
yourself, or take the tarball from the
[releases page](https://github.com/glocker-app/glocker/releases) and run the
`install.sh` inside it. The config lands in `/etc/glocker/config.yaml`.

To build from source instead (any architecture, and needed to run glockpeek
locally), you'll need the Go toolchain (1.26+):

```bash
make init                 # write conf/conf.yaml from the sample
$EDITOR conf/conf.yaml     # edit to taste
make full-install         # build, fetch blocklists, install the systemd service (sudo)
```

Install refuses up front if a capability an enabled feature needs is missing
(e.g. no `systemd`, or no `visudo` with sudoers management on). Uninstall takes a
required reason:

```bash
sudo glocker -uninstall "reason"
```

## Verifying a release

Release binaries come with a `SHA256SUMS` signed by
[minisign](https://jedisct1.github.io/minisign/). The public key is
[`minisign.pub`](minisign.pub), also published at
[glockerapp.com](https://glockerapp.com):

```
RWT2ioF8xC/sE5SAAyubQQGPAJWWZxougVjndvUzoRH1D1/C8mOoFK5i
```

Download the binaries, `SHA256SUMS` and `SHA256SUMS.minisig` into one directory,
then:

```bash
minisign -Vm SHA256SUMS -P RWT2ioF8xC/sE5SAAyubQQGPAJWWZxougVjndvUzoRH1D1/C8mOoFK5i
sha256sum -c SHA256SUMS
```

## Dashboard (glockpeek)

`glockpeek` is a separate systemd service serving your analytics — violations,
clean streaks, usage, exposure. It's DB-backed (SQLite locally, Postgres hosted)
and runs two ways:

- **local** (default): binds `127.0.0.1:4317`, no login, zero config.
- **hosted**: real accounts + per-device sync tokens. I run one at
  [my.glockerapp.com](https://my.glockerapp.com), end-to-end encrypted — your
  window titles and tags are sealed in your browser, so the server stores only
  ciphertext. $99/yr for unlimited machines, 14-day trial; self-hosting stays
  free.

To sync to the hosted dashboard: register, set an encryption passphrase, mint a
device token on the **Devices** page, put it in the `sync:` stanza of
`conf/conf.yaml`, reinstall, then `sudo glocker -connect`. The
[guide](docs/habits.md) has the full walkthrough.

## Requirements

Linux with systemd (amd64 for the release; Go 1.26+ to build from source) ·
Firefox or Chrome/Chromium for the
extension · root to install.

Tested on Debian, Ubuntu, Fedora, Omarchy and Arch.

## Docs

- **[The guide](https://glockerapp.com/habits.html)** — features, config, the
  dashboard, and the habit psychology behind them. Start here. (Source in this
  repo: [docs/habits.md](docs/habits.md).)
- [conf/conf.yaml.sample](conf/conf.yaml.sample) documents every config option inline.

## License

Free software under the **GNU AGPLv3** — see [LICENSE](LICENSE).

## Contributing

This is a personal tool that solves my specific problem. If it helps you, fork
and adapt it. I'm happy to help add support for other distros.
