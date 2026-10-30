#!/bin/sh
# glocker installer — bundled at the root of the release tarball. Run it from the
# unpacked directory:  ./install.sh
# (The curl|sh bootstrap at https://glockerapp.com/install.sh fetches and verifies
# the tarball, then runs this.) It installs nothing until the sudo step below.
set -eu

# Tell -init to skip its "next, go install" steps: we install right after it and
# print our own review note at the end.
export GLOCKER_INSTALLER=1

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$here"

for bin in glocker glocklock glockdoc; do
	[ -x "./$bin" ] || { echo "error: ./$bin missing from the tarball" >&2; exit 1; }
done

# An upgrade or a first install? Step 1 can write the config, so note it now.
upgrade=
[ -f /etc/glocker/config.yaml ] && upgrade=1

echo "Glocker installs to this machine:"
echo "  binaries -> /usr/local/bin   config -> /etc/glocker   a systemd service"
echo

# 1. Config. -init writes conf/conf.yaml from the bundled sample (keeps its
#    comments, won't overwrite an existing one). On an upgrade it asks whether to
#    keep the installed config and writes nothing if so. The wizard is
#    interactive, so give it the terminal even when this script arrived over a pipe.
if [ -f conf/conf.yaml ]; then
	echo "conf/conf.yaml already present — keeping it."
elif ( : < /dev/tty ) 2>/dev/null; then
	# A controlling terminal is actually usable (interactive curl|sh) — prompt.
	# ( : < /dev/tty ) really opens it; `[ -r /dev/tty ]` is true even with no
	# controlling tty (piped/CI), where the redirect would then fail under set -e.
	./glocker -init -conf conf/conf.yaml < /dev/tty
else
	./glocker -init -conf conf/conf.yaml
	if [ -f conf/conf.yaml ]; then
		echo "(no terminal attached — wrote defaults; review conf/conf.yaml.)"
	fi
fi

# 2. System install (root). sudo prompts on the terminal itself. Blocklists are
#    bundled in conf/conf.d/, so -install wires them in — no download needed.
#    -install checks the installed config's version first and stops, changing
#    nothing, if it can't be converted. A conf/conf.yaml from step 1 replaces the
#    installed config (backed up first); without one the installed config is kept.
echo
echo "Installing (sudo)…"
sudo ./glocker -install -conf conf/conf.yaml -replace-config

if [ -n "$upgrade" ]; then
	cat <<'EOF'

Done. glocker upgraded — run `glocker` to see what's blocked.
Restart Firefox/Chrome to pick up the updated content-blocker.
EOF
	exit 0
fi

cat <<'EOF'

Done. glocker is installed and running — run `glocker` to see what's blocked.

To review or change your sites, schedules and sudo window (inside your sudo window):

  sudo glocker -edit

Without sudo it opens read-only. The config is /etc/glocker/config.yaml, with your
own sites in /etc/glocker/conf.d/personal-domains.yaml.

More: https://glockerapp.com/habits.html

The browser content-blocker is force-installed by policy — restart Firefox/Chrome
to pick it up.

Usage tracking syncs to the hosted dashboard at https://my.glockerapp.com. To run
the tracker (glockpeek) on your own machine, build from source:
https://github.com/glocker-app/glocker
EOF
