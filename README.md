# Glocker

Glocker helps you break digital habits on Linux. It blocks distracting sites and
apps on a schedule, makes them *annoying* to reach when you slip, and logs your
usage so you can see the patterns and tighten the rules over time.

It's opinionated — built by one person (on Debian + Firefox) to break their own
habits — but if your setup is similar it may help you too. The
**[guide](docs/habits.md)** explains both the thinking and the features, and is
the source of truth for how glocker works.

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
- **Sudoers restriction** and a paced **mindful-uninstall** challenge, to raise
  the cost of turning glocker off.
- **Content monitoring** — a Firefox/Chrome extension flags keywords on any page;
  repeated hits can trip a screen lock (**violation tracking** → `glocklock`).
- **Panic mode** (suspend, re-suspend on early wake) and **lifecycle logging**
  that makes `-uninstall` cite a reason.

Config lives in `conf/conf.yaml`. The big blocklists are generated into
`conf/conf.d/` by `scripts/update_domains.py` and pulled in with `!include`
lines; `conf/conf.yaml.sample` documents every option inline.

## Quick start

```bash
make init                 # write conf/conf.yaml from the sample
$EDITOR conf/conf.yaml     # edit to taste
make full-install         # build, fetch blocklists, install the systemd service (sudo)
glocker -status
```

Install refuses up front if a capability an enabled feature needs is missing
(e.g. no `systemd`, or no `visudo` with sudoers management on). Uninstall takes a
required reason:

```bash
sudo glocker -uninstall "reason"
```

## Dashboard (glockpeek)

`glockpeek` is a separate systemd service serving your analytics — violations,
clean streaks, usage, exposure. It's DB-backed (SQLite locally, Postgres hosted)
and runs two ways:

- **local** (default): binds `127.0.0.1:4317`, no login, zero config.
- **hosted**: real accounts + per-device sync tokens. I run one at
  [my.glockerapp.com](https://my.glockerapp.com), end-to-end encrypted — your
  window titles and tags are sealed in your browser, so the server stores only
  ciphertext. $99/yr for up to three machines, 14-day trial; self-hosting stays
  free.

To sync to the hosted dashboard: register, set an encryption passphrase, mint a
device token on the **Devices** page, put it in the `sync:` stanza of
`conf/conf.yaml`, reinstall, then `sudo glocker -connect`. The
[guide](docs/habits.md) has the full walkthrough.

## Requirements

Linux with systemd · Go 1.26+ to build · Firefox or Chrome/Chromium for the
extension · root to install.

## Docs

- **[docs/habits.md](docs/habits.md)** — the guide (features, config, the
  dashboard, and the habit psychology behind them). Start here.
- [docs/installation.md](docs/installation.md), [docs/architecture.md](docs/architecture.md),
  and [conf/conf.yaml.sample](conf/conf.yaml.sample) for the rest.

## License

Free software under the **GNU AGPLv3** — see [LICENSE](LICENSE).

## Contributing

This is a personal tool that solves my specific problem. If it helps you, fork
and adapt it. I'm happy to help add support for other distros.
