# Glocker

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
- **Usage tracking** of the focused window (X11, GNOME, Hyprland), viewable in
  the `glockpeek` dashboard.

## Install

On amd64:

```bash
curl -fsSL https://glockerapp.com/glocker-installer.sh | sh
glocker -status
```

To change the config afterwards (inside your sudo window):

```bash
sudoedit /etc/glocker/config.yaml
sudo systemctl restart glocker
```

To build from source (Go 1.26+):

```bash
make init            # write conf/conf.yaml from the sample
$EDITOR conf/conf.yaml
make full-install
```

Uninstall with `sudo glocker -uninstall "reason"`.

Requires Linux with systemd and root to install.

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

## License

GNU AGPLv3. See [LICENSE](LICENSE).
