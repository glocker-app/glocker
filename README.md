# Glocker

[![Tests](https://img.shields.io/github/actions/workflow/status/glocker-app/glocker/tests.yml?branch=master&label=tests)](https://github.com/glocker-app/glocker/actions/workflows/tests.yml)
[![Latest release](https://img.shields.io/github/v/release/glocker-app/glocker)](https://github.com/glocker-app/glocker/releases/latest)
[![Last commit](https://img.shields.io/github/last-commit/glocker-app/glocker)](https://github.com/glocker-app/glocker/commits/master)
[![License](https://img.shields.io/github/license/glocker-app/glocker)](LICENSE)
[![Discord](https://img.shields.io/badge/Discord-join-5865F2?logo=discord&logoColor=white)](https://discord.gg/4qJp9uYyam)
[![Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-support-FFDD00?logo=buymeacoffee&logoColor=black)](https://buymeacoffee.com/noufalibrahim)

A distraction blocker for Linux. It blocks sites and apps on a schedule, makes
turning it off slow and deliberate, and logs your usage.

## Features

- **Site blocking** through `/etc/hosts`, always or during set windows, with
  logged temporary unblocks.
- **Program blocking**: listed programs are killed during their windows.
- **Sudo window**: sudo only works during the hours you choose (Debian, Ubuntu,
  Fedora, Omarchy, Arch).
- **Browser extension** for Firefox and Chrome that blocks pages by keyword.
- **Screen lock** after repeated violations (`glocklock`).
- **Slow uninstall**: a stated reason and a typing challenge.
- **Usage tracking** of the focused window (X11, GNOME, KDE Plasma, Hyprland),
  viewable in the `glockpeek` dashboard.

## Install

On amd64:

```bash
curl -fsSL https://glockerapp.com/glocker-installer.sh | sh
glocker -status
```

To change the config afterwards, use the built-in editor inside your sudo window
(without sudo it opens read-only):

```bash
sudo glocker -edit
```

Or edit the file by hand and restart:

```bash
sudoedit /etc/glocker/config.yaml
sudo systemctl restart glocker
```

To build from source (Go 1.26+, python3 for the blocklist script):

```bash
make init            # write conf/conf.yaml from the sample
$EDITOR conf/conf.yaml
make full-install
```

Uninstall with `sudo glocker -uninstall "reason"`.

Requires Linux with systemd and root to install. The content-blocking extension
needs Firefox or Chrome/Chromium.

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

## More

- Website: [glockerapp.com](https://glockerapp.com)
- Guide: [docs/habits.md](docs/habits.md) (also at
  [glockerapp.com/habits.html](https://glockerapp.com/habits.html))

## Use of AI

glocker is almost wholly AI-written and human-directed. It started as an
experiment by the author in using AI coding assistants (mainly Claude Code)
to build something useful for personal use, and grew into a full product.
The author decides what gets built and how it should behave; the code is
written by the assistant. Most recent commits carry a
`Co-Authored-By: Claude` line.

It has been tested by hand and run daily on the author's own machines for
over a year, and has a few other users.

## License

GNU AGPLv3. See [LICENSE](LICENSE).
