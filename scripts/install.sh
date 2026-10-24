#!/bin/sh
# glocker installer — bundled at the root of the release tarball. Run it from the
# unpacked directory:  ./install.sh
# (The curl|sh bootstrap at https://glockerapp.com/install.sh fetches and verifies
# the tarball, then runs this.) It installs nothing until the sudo step below.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$here"

for bin in glocker glocklock glockdoc; do
	[ -x "./$bin" ] || { echo "error: ./$bin missing from the tarball" >&2; exit 1; }
done

echo "Glocker installs to this machine:"
echo "  binaries -> /usr/local/bin   config -> /etc/glocker   a systemd service"
echo

# 1. Config. -init writes conf/conf.yaml from the bundled sample (keeps its
#    comments, won't overwrite an existing one). The wizard is interactive, so
#    give it the terminal even when this script arrived over a pipe.
if [ -f conf/conf.yaml ]; then
	echo "conf/conf.yaml already present — keeping it."
elif [ -r /dev/tty ]; then
	./glocker -init -conf conf/conf.yaml < /dev/tty
else
	./glocker -init -conf conf/conf.yaml
	echo "(no terminal attached — wrote defaults; review conf/conf.yaml.)"
fi

# 2. System install (root). sudo prompts on the terminal itself. Blocklists are
#    bundled in conf/conf.d/, so -install wires them in — no download needed.
echo
echo "Installing (sudo)…"
sudo ./glocker -install -conf conf/conf.yaml

cat <<'EOF'

Done. glocker is installed and running.
  glocker -status      what's blocked right now
  sudo glocker -check   confirm it's actually enforceable

The browser content-blocker is force-installed by policy — restart Firefox/Chrome
to pick it up. Guide: https://glockerapp.com/habits.html

Usage tracking syncs to the hosted dashboard at https://my.glockerapp.com. To run
the tracker (glockpeek) on your own machine, build from source:
https://github.com/glocker-app/glocker
EOF
