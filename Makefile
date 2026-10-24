.PHONY: test-deps build-all init install full-install update-blocklists test release migrate deploy deploy-full deploy-rules sign-extensions deploy-extensions release-extensions sign-firefox-extension deploy-firefox-extension release-firefox-extension sign-chrome-extension deploy-chrome-extension release-chrome-extension db-backup help

.DEFAULT_GOAL:=help
help: ## display this help message
	@echo "Please use \`make <target>' where <target> is one of"
	@perl -nle'print $& if m{^[a-zA-Z_-]+:.*?## .*$$}' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m  %-25s\033[0m %s\n", $$1, $$2}'




# Build version stamped into binaries via -ldflags -X main.version. git describe
# gives the nearest tag + commits-since + short sha, and -dirty flags an
# uncommitted tree, so `glockpeek -version` / the dashboard footer say exactly
# what's running. Falls back to "dev" outside a git checkout.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOLDFLAGS := -ldflags "-X main.version=$(VERSION)"

# ── Deploy knobs ─────────────────────────────────────────────
# Defaults suit a standalone glocker checkout. Override on the command line or
# from a parent Makefile — the private hosted repo passes these pointing at its
# own out-of-tree config/secrets (e.g. GLOCKPEEK_CONF=$(abspath conf/glockpeek.yaml)).
DEPLOY_USER    ?= root
# The box to ssh to — no default: a public domain may front a load balancer that
# forwards only 80/443, where ssh would just hang. Pass the node's address.
DEPLOY_HOST    ?=
# Fail fast on an unreachable host instead of hanging.
SSH_OPTS       ?= -o ConnectTimeout=10
# (Comments sit on their own lines: a trailing `# …` would leave spaces in the value.)
# Server config with secrets (gitignored).
GLOCKPEEK_CONF ?= conf/glockpeek.yaml
# Curated starter rules (hosted-only, gitignored).
TAGS_FILE      ?= tags.json
# Local pg_dump destination (gitignored).
DB_BACKUP_DIR  ?= deploy/backups
# Set → dump this DB (e.g. managed PG); empty → on-box postgres.
DB_DSN         ?=

# Fail fast if the Go toolchain is missing — every build target needs it (an
# -install checks its own system deps at runtime). Not surfaced in help.
test-deps:
	@command -v go >/dev/null 2>&1 || { echo "error: Go toolchain not found. Install Go 1.26+ (see README) and retry." >&2; exit 1; }

# Internal build step (dependency of install / full-install); not surfaced in help.
build-all: test-deps
	go build $(GOLDFLAGS) -o glocker ./cmd/glocker
	go build $(GOLDFLAGS) -o glocklock ./cmd/glocklock
	go build $(GOLDFLAGS) -o glockpeek ./cmd/glockpeek
	go build $(GOLDFLAGS) -o glockdoc ./cmd/glockdoc


init: test-deps ## Generate conf/conf.yaml from the sample (fills in your login; keeps comments; won't overwrite)
	go run ./cmd/glocker -init

install: build-all ## Rebuild and reinstall (in-place; stops+swaps+restarts the daemon)
	sudo ./glocker -install


full-install: build-all update-blocklists ## Rebuild, update blocklists, and reinstall
	sudo ./glocker -install


# update-blocklists is a step of full-install; not surfaced in `make help`.
update-blocklists:
	python3 scripts/update_domains.py all


test: ## Run tests
	go test ./...

# ── Deploy: push updated artifacts to the already-provisioned VPS over SSH. ──
# Mirrors the install targets: `deploy-full` is the whole thing (like
# `full-install`); the rest ship one piece each (like `install`). One-time host
# provisioning — caddy, the service user, the landing page — is managed in a private ops
# repo (this repo is consumed there as a git subtree); see deploy/README.md.
#   make deploy-full DEPLOY_HOST=<node-ip>               # binary + extension
#   make deploy | deploy-extensions                      # just one piece
# (Deploy knobs — DEPLOY_HOST/USER, GLOCKPEEK_CONF — are defined up top.)

deploy-full: deploy deploy-extensions ## Full deploy: dashboard binary + browser extensions

# Apply DB migrations as an EXPLICIT step, run BEFORE `deploy` — the hosted server
# no longer migrates on boot (N nodes would race on DDL). Installs the current
# config FIRST (so a rotated DB password is in place before we authenticate — the
# migrate binary reads the box's /etc/glocker/glockpeek.yaml), then ships a freshly
# built binary to a temp path and runs `-migrate`, without restarting the running
# service. Migrations are additive/backward-compatible, so the old code keeps
# serving on the new schema until `deploy` rolls the binary.
migrate: ## Apply DB migrations on the box (runs automatically before deploy; server no longer migrates on boot)
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make migrate DEPLOY_HOST=<node-ip>"; exit 1; }
	@test -f $(GLOCKPEEK_CONF) || { echo "$(GLOCKPEEK_CONF) not found (gitignored server config with secrets) — create it from conf/glockpeek.yaml.sample"; exit 1; }
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o dist/glockpeek ./cmd/glockpeek
	scp $(SSH_OPTS) dist/glockpeek $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.migrate
	scp $(SSH_OPTS) $(GLOCKPEEK_CONF) $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.yaml.new
	ssh $(SSH_OPTS) $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -o root -g glockpeek -m 0640 /tmp/glockpeek.yaml.new /etc/glocker/glockpeek.yaml && sudo -u glockpeek /tmp/glockpeek.migrate -migrate && rm -f /tmp/glockpeek.migrate /tmp/glockpeek.yaml.new'

deploy: migrate ## Migrate the DB (idempotent), then deploy the glockpeek binary + server config to the VPS
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy DEPLOY_HOST=<node-ip>"; exit 1; }
	@test -f $(GLOCKPEEK_CONF) || { echo "$(GLOCKPEEK_CONF) not found (gitignored server config with secrets) — create it from conf/glockpeek.yaml.sample"; exit 1; }
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o dist/glockpeek ./cmd/glockpeek
	scp $(SSH_OPTS) dist/glockpeek $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.new
	scp $(SSH_OPTS) $(GLOCKPEEK_CONF) $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.yaml.new
	ssh $(SSH_OPTS) $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -m 0755 /tmp/glockpeek.new /usr/local/bin/glockpeek && sudo install -o root -g glockpeek -m 0640 /tmp/glockpeek.yaml.new /etc/glocker/glockpeek.yaml && rm -f /tmp/glockpeek.new /tmp/glockpeek.yaml.new && sudo systemctl restart glockpeek && sleep 2 && systemctl is-active glockpeek'

# Ship the curated Glocker starter rules (tags.json, hosted-only + gitignored)
# and load them into the default-rules template so NEW accounts seed from them.
# Idempotent (replaces the table); run after `deploy` (needs the new binary's
# -import-rules). Existing accounts are untouched. glockpeek reads the DB DSN
# from /etc/glocker/glockpeek.yaml, so no -db/-driver needed on the box.
deploy-rules:
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@test -f $(TAGS_FILE) || { echo "$(TAGS_FILE) not found (hosted-only asset, gitignored)"; exit 1; }
	scp $(SSH_OPTS) $(TAGS_FILE) $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glocker-tags.json
	ssh $(SSH_OPTS) $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo /usr/local/bin/glockpeek -import-rules /tmp/glocker-tags.json && rm -f /tmp/glocker-tags.json'

# ── Database backup ──────────────────────────────────────────
# Restore relies on DigitalOcean's managed database backups; this is a local
# pg_dump for debugging/inspection of data that can't be re-synced from the agent
# logs (accounts, tokens, the key vault, rules, tag colours, the ignore overlay).
# Dumps carry sensitive data (argon2 hashes, the wrapped master key), so
# DB_BACKUP_DIR (defined up top) is gitignored. With DB_DSN set (the managed DB),
# pg_dump runs on the node against it, so the node needs postgresql-client; with
# DB_DSN empty it falls back to a postgres running on the box itself.

db-backup: ## Pull a compressed pg_dump of the glockpeek DB to DB_BACKUP_DIR (local, gitignored)
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@mkdir -p $(DB_BACKUP_DIR)
	@f="$(DB_BACKUP_DIR)/glockpeek-$$(date -u +%Y%m%dT%H%M%SZ).dump"; \
	 if [ -n "$(DB_DSN)" ]; then \
	   dump="pg_dump -Fc '$(DB_DSN)'"; \
	 else \
	   dump="sudo -u postgres pg_dump -Fc glockpeek"; \
	 fi; \
	 ssh $(SSH_OPTS) $(DEPLOY_USER)@$(DEPLOY_HOST) "$$dump" > "$$f" && \
	 echo "saved $$f ($$(du -h "$$f" | cut -f1))"


# Sign the Firefox extension via Mozilla's AMO API (unlisted self-distribution),
# dropping the signed .xpi into extensions/firefox/web-ext-artifacts/. Automated —
# no human review for unlisted. Needs:
#   - web-ext            (npm i -g web-ext)
#   - AMO_JWT_ISSUER / AMO_JWT_SECRET in the env
#     (addons.mozilla.org → Developer Hub → Manage API Keys; keep them out of git)
# AMO rejects a version it has already signed, so the "version" in
# extensions/firefox/manifest.json must be bumped first — `make sign-firefox-extension`
# does that check for you. Then `make deploy-firefox-extension` ships it.
sign-firefox-extension:
	@command -v web-ext >/dev/null || { echo "web-ext not found — install with: npm i -g web-ext"; exit 1; }
	@test -n "$$AMO_JWT_ISSUER" -a -n "$$AMO_JWT_SECRET" || { echo "set AMO_JWT_ISSUER and AMO_JWT_SECRET (addons.mozilla.org → Developer Hub → Manage API Keys)"; exit 1; }
	@ver=$$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' extensions/firefox/manifest.json | head -1); \
	 if ls extensions/firefox/web-ext-artifacts/*-$$ver.xpi >/dev/null 2>&1; then \
	   echo "manifest version $$ver already has a signed .xpi — bump \"version\" in extensions/firefox/manifest.json first"; exit 1; fi; \
	 echo "signing version $$ver…"
	@dirty=$$(git status --porcelain -- . ':(exclude)extensions/firefox/build-info.js' | head -1); \
	 base=$$(git rev-parse --short HEAD 2>/dev/null || echo dev); \
	 tag=$$base$${dirty:+-dirty}; \
	 printf 'var GLOCKER_BUILD = { commit: "%s" };\n' "$$tag" > extensions/firefox/build-info.js; \
	 echo "stamped build-info.js: $$tag"
	cd extensions/firefox && web-ext sign --channel=unlisted --api-key="$$AMO_JWT_ISSUER" --api-secret="$$AMO_JWT_SECRET"; \
	 rc=$$?; git checkout -- build-info.js 2>/dev/null; exit $$rc

# Sign then publish in one step.
release-firefox-extension: sign-firefox-extension deploy-firefox-extension

# Publish the SIGNED Firefox extension to the apex web root, served at
# https://glockerapp.com/glocker.xpi — where `glocker -install` fetches it on a
# machine without a locally built artifact. Ships the newest signed .xpi already
# in web-ext-artifacts/ (from `make sign-firefox-extension`); re-run after re-signing.
deploy-firefox-extension:
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy-firefox-extension DEPLOY_HOST=<node-ip>"; exit 1; }
	@xpi=$$(ls -t extensions/firefox/web-ext-artifacts/*.xpi 2>/dev/null | head -1); \
	 test -n "$$xpi" || { echo "no signed .xpi in extensions/firefox/web-ext-artifacts/ — run 'web-ext sign --channel unlisted' there first"; exit 1; }; \
	 echo "shipping $$xpi"; \
	 set -x; \
	 scp $(SSH_OPTS) "$$xpi" $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glocker.xpi && \
	 ssh $(SSH_OPTS) $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -D -o caddy -g caddy -m 0644 /tmp/glocker.xpi /var/www/glocker-landing/glocker.xpi && rm -f /tmp/glocker.xpi && echo "extension published at /glocker.xpi"'

# Pack + sign the Chrome extension into extensions/chrome-artifacts/glocker-<ver>.crx.
# The key fixes the extension ID the installer's policy names (chromeExtensionID in
# internal/install/chrome.go), so every release must use the same key — it's
# gitignored, so back it up. No store or account: Chrome on Linux accepts a
# self-signed crx installed by policy. Packing runs chromium, so not inside its
# kill window. Chrome only updates to a higher version: bump "version" in
# extensions/chrome/manifest.json first. Then `make deploy-chrome-extension`.
CHROME_KEY ?= extensions/chrome-signing.pem

sign-chrome-extension:
	@test -f $(CHROME_KEY) || { echo "no signing key at $(CHROME_KEY) — restore it from backup (a new key changes the extension ID)"; exit 1; }
	@id=$$(openssl rsa -in $(CHROME_KEY) -pubout -outform DER 2>/dev/null | sha256sum | cut -c1-32 | tr '0-9a-f' 'a-p'); \
	 grep -q "chromeExtensionID = \"$$id\"" internal/install/chrome.go || { echo "$(CHROME_KEY) gives extension ID $$id, which isn't chromeExtensionID in internal/install/chrome.go"; exit 1; }
	@ver=$$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' extensions/chrome/manifest.json | head -1); \
	 out=extensions/chrome-artifacts/glocker-$$ver.crx; \
	 if test -f $$out; then echo "$$out already exists — bump \"version\" in extensions/chrome/manifest.json first"; exit 1; fi; \
	 dirty=$$(git status --porcelain -- . | head -1); \
	 tag=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)$${dirty:+-dirty}; \
	 tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	 cp -r extensions/chrome "$$tmp/ext"; \
	 printf 'var GLOCKER_BUILD = { commit: "%s" };\n' "$$tag" > "$$tmp/ext/build-info.js"; \
	 echo "+ chromium --pack-extension (signing with $(CHROME_KEY))"; \
	 chromium --headless=new --user-data-dir="$$tmp/profile" --pack-extension="$$tmp/ext" --pack-extension-key="$(abspath $(CHROME_KEY))" >/dev/null 2>&1; \
	 test -s "$$tmp/ext.crx" || { echo "chromium --pack-extension produced no crx"; exit 1; }; \
	 mkdir -p extensions/chrome-artifacts && mv "$$tmp/ext.crx" $$out && echo "signed $$out ($$tag)"

# Sign then publish in one step.
release-chrome-extension: sign-chrome-extension deploy-chrome-extension

# Publishes the newest signed crx plus the update manifest Chrome's policy points
# at (chromeUpdateURL in internal/install/chrome.go), both under the apex web root
# — DEPLOY_HOST is just the box to ssh to.
# Installed browsers pick up the new version on their next update check.
deploy-chrome-extension:
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy-chrome-extension DEPLOY_HOST=<node-ip>"; exit 1; }
	@crx=$$(ls -t extensions/chrome-artifacts/*.crx 2>/dev/null | head -1); \
	 test -n "$$crx" || { echo "no .crx in extensions/chrome-artifacts/ — run 'make sign-chrome-extension' first"; exit 1; }; \
	 ver=$$(basename "$$crx" .crx | sed 's/^glocker-//'); \
	 id=$$(sed -n 's/.*chromeExtensionID = "\([a-p]*\)".*/\1/p' internal/install/chrome.go); \
	 url=$$(sed -n 's/.*chromeUpdateURL = "\(.*\)\/update.xml".*/\1/p' internal/install/chrome.go); \
	 tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	 printf "<?xml version='1.0' encoding='UTF-8'?>\n<gupdate xmlns='http://www.google.com/update2/response' protocol='2.0'>\n  <app appid='%s'>\n    <updatecheck codebase='%s/glocker.crx' version='%s' />\n  </app>\n</gupdate>\n" "$$id" "$$url" "$$ver" > "$$tmp/update.xml"; \
	 cp "$$crx" "$$tmp/glocker.crx"; \
	 echo "shipping $$crx (version $$ver)"; \
	 set -x; \
	 scp $(SSH_OPTS) "$$tmp/glocker.crx" "$$tmp/update.xml" $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/ && \
	 ssh $(SSH_OPTS) $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -D -o caddy -g caddy -m 0644 /tmp/glocker.crx /var/www/glocker-landing/chrome/glocker.crx && sudo install -D -o caddy -g caddy -m 0644 /tmp/update.xml /var/www/glocker-landing/chrome/update.xml && rm -f /tmp/glocker.crx /tmp/update.xml && echo "extension published at /chrome/update.xml"'

# Both browsers at once. The per-browser targets above (sign-/deploy-/release-
# firefox-extension, …-chrome-extension) work on their own too, e.g. to ship one
# browser's update without bumping the other's version.
sign-extensions: sign-firefox-extension sign-chrome-extension ## Sign both browser extensions (bump both manifest versions first)
deploy-extensions: deploy-firefox-extension deploy-chrome-extension ## Publish both signed browser extensions
release-extensions: sign-extensions deploy-extensions ## Sign and publish both browser extensions

# ── Release: signed binaries for a GitHub release (source stays the main path). ──
# Builds static linux/amd64 binaries + a SHA256SUMS file, then signs SHA256SUMS
# with minisign using your OFFLINE key — so a download verifies against a key that
# lives on no server. Signature covers the checksums; the checksums cover the
# binaries. Sigstore/keyless is deferred (it only pays off with CI).
#
# One-time setup:
#   minisign -G -s ~/.minisign/glocker.key -p minisign.pub   # generate your keypair
#   # commit minisign.pub, paste it into the README, publish at glockerapp.com/minisign.pub
# Per release — the version comes from the git tag (VERSION is git describe):
#   git tag -a v0.1.1 -m v0.1.1  # on the clean commit being released
#   make release                 # refuses unless HEAD is exactly on a tag; prompts for your key passphrase
#   # then upload dist/release/* to the GitHub release (the command is printed)
# Point at a different key with MINISIGN_KEY=/path/to/key.
MINISIGN_KEY ?= $(HOME)/.minisign/glocker.key
RELEASE_DIR := dist/release
# Prebuilt install tarball (the curl|sh path): binaries + bundled blocklists +
# conf sample + extras + the packed install.sh, so it installs with no Go and no
# source tree. It rides the same SHA256SUMS + minisign signature as the binaries.
TARBALL_NAME  := glocker-$(VERSION)-linux-amd64
TARBALL       := $(TARBALL_NAME).tar.gz
TARBALL_STAGE := $(RELEASE_DIR)/$(TARBALL_NAME)
release: ## Build + checksum + minisign the release binaries from the tag on HEAD (needs minisign + your key)
	@command -v minisign >/dev/null || { echo "install minisign first — https://jedisct1.github.io/minisign/"; exit 1; }
	@git describe --tags --exact-match HEAD >/dev/null 2>&1 || { echo "HEAD isn't tagged — tag the release commit first: git tag -a vX.Y.Z -m vX.Y.Z"; exit 1; }
	@case "$(VERSION)" in *-dirty) echo "uncommitted changes — commit or stash before releasing $(VERSION)"; exit 1;; esac
	rm -rf $(RELEASE_DIR) && mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glocker   ./cmd/glocker
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glocklock ./cmd/glocklock
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glockpeek ./cmd/glockpeek
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glockdoc  ./cmd/glockdoc
	@echo "→ bundling blocklists into the tarball"
	@# update_domains.py wires its !include lines into conf/conf.yaml, so it needs
	@# one. Make a throwaway from the sample only if none exists (never touch a real
	@# local config); remove only what we created. conf.yaml is gitignored either way.
	@if [ ! -f conf/conf.yaml ]; then cp conf/conf.yaml.sample conf/conf.yaml; : > $(RELEASE_DIR)/.confmade; fi
	python3 scripts/update_domains.py all
	@if [ -f $(RELEASE_DIR)/.confmade ]; then rm -f conf/conf.yaml $(RELEASE_DIR)/.confmade; fi
	rm -rf $(TARBALL_STAGE) && mkdir -p $(TARBALL_STAGE)/conf/conf.d $(TARBALL_STAGE)/extras
	@# glockpeek (the local tracker/dashboard) is left out — it's ~half the size and
	@# hosted users sync to my.glockerapp.com. Self-hosters build it from source.
	cp $(RELEASE_DIR)/glocker $(RELEASE_DIR)/glocklock $(RELEASE_DIR)/glockdoc $(TARBALL_STAGE)/
	cp conf/conf.yaml.sample $(TARBALL_STAGE)/conf/
	cp conf/conf.d/domains-*.yaml conf/conf.d/personal-domains.yaml.sample $(TARBALL_STAGE)/conf/conf.d/
	cp extras/glocker.service extras/lock-background.png $(TARBALL_STAGE)/extras/
	install -m 0755 scripts/install.sh $(TARBALL_STAGE)/install.sh
	tar -C $(RELEASE_DIR) -czf $(RELEASE_DIR)/$(TARBALL) $(TARBALL_NAME)
	rm -rf $(TARBALL_STAGE)
	cd $(RELEASE_DIR) && sha256sum glocker glocklock glockpeek glockdoc $(TARBALL) > SHA256SUMS
	minisign -Sm $(RELEASE_DIR)/SHA256SUMS -s $(MINISIGN_KEY)
	@echo "✓ signed $(VERSION) — publish with:"
	@echo "  gh release create $(VERSION) $(RELEASE_DIR)/* -R glocker-app/glocker --target master --title $(VERSION)"
