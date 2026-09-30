.PHONY: build-all init install full-install update-blocklists test release migrate deploy deploy-full deploy-rules sign-extension release-extension deploy-extension db-backup help

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
DEPLOY_HOST    ?= glockerapp.com
GLOCKPEEK_CONF ?= conf/glockpeek.yaml   # server config with secrets (gitignored)
TAGS_FILE      ?= tags.json             # curated starter rules (hosted-only, gitignored)
DB_BACKUP_DIR  ?= deploy/backups        # local pg_dump destination (gitignored)
DB_DSN         ?=                       # set → dump this DB (e.g. managed PG); empty → on-box postgres

# Internal build step (dependency of install / full-install); not surfaced in help.
build-all:
	go build $(GOLDFLAGS) -o glocker ./cmd/glocker
	go build $(GOLDFLAGS) -o glocklock ./cmd/glocklock
	go build $(GOLDFLAGS) -o glockpeek ./cmd/glockpeek
	go build $(GOLDFLAGS) -o glockdoc ./cmd/glockdoc


init: ## Generate conf/conf.yaml from the sample (fills in your login; keeps comments; won't overwrite)
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
# provisioning — postgres, caddy, the service user — is managed in a private ops
# repo (this repo is consumed there as a git subtree); see deploy/README.md.
#   make deploy-full DEPLOY_HOST=glockerapp.com          # binary + extension
#   make deploy | deploy-extension                       # just one piece
# (Deploy knobs — DEPLOY_HOST/USER, GLOCKPEEK_CONF — are defined up top.)

deploy-full: deploy deploy-extension ## Full deploy: dashboard binary + extension

# Apply DB migrations as an EXPLICIT step, run BEFORE `deploy` — the hosted server
# no longer migrates on boot (N nodes would race on DDL). Installs the current
# config FIRST (so a rotated DB password is in place before we authenticate — the
# migrate binary reads the box's /etc/glocker/glockpeek.yaml), then ships a freshly
# built binary to a temp path and runs `-migrate`, without restarting the running
# service. Migrations are additive/backward-compatible, so the old code keeps
# serving on the new schema until `deploy` rolls the binary.
migrate: ## Apply DB migrations on the box (runs automatically before deploy; server no longer migrates on boot)
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make migrate DEPLOY_HOST=my.example.com"; exit 1; }
	@test -f $(GLOCKPEEK_CONF) || { echo "$(GLOCKPEEK_CONF) not found (gitignored server config with secrets) — create it from conf/glockpeek.yaml.sample"; exit 1; }
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o dist/glockpeek ./cmd/glockpeek
	scp dist/glockpeek $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.migrate
	scp $(GLOCKPEEK_CONF) $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.yaml.new
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -o root -g glockpeek -m 0640 /tmp/glockpeek.yaml.new /etc/glocker/glockpeek.yaml && sudo -u glockpeek /tmp/glockpeek.migrate -migrate && rm -f /tmp/glockpeek.migrate /tmp/glockpeek.yaml.new'

deploy: migrate ## Migrate the DB (idempotent), then deploy the glockpeek binary + server config to the VPS
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy DEPLOY_HOST=my.example.com"; exit 1; }
	@test -f $(GLOCKPEEK_CONF) || { echo "$(GLOCKPEEK_CONF) not found (gitignored server config with secrets) — create it from conf/glockpeek.yaml.sample"; exit 1; }
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o dist/glockpeek ./cmd/glockpeek
	scp dist/glockpeek $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.new
	scp $(GLOCKPEEK_CONF) $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.yaml.new
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -m 0755 /tmp/glockpeek.new /usr/local/bin/glockpeek && sudo install -o root -g glockpeek -m 0640 /tmp/glockpeek.yaml.new /etc/glocker/glockpeek.yaml && rm -f /tmp/glockpeek.new /tmp/glockpeek.yaml.new && sudo systemctl restart glockpeek && sleep 2 && systemctl is-active glockpeek'

# Ship the curated Glocker starter rules (tags.json, hosted-only + gitignored)
# and load them into the default-rules template so NEW accounts seed from them.
# Idempotent (replaces the table); run after `deploy` (needs the new binary's
# -import-rules). Existing accounts are untouched. glockpeek reads the DB DSN
# from /etc/glocker/glockpeek.yaml, so no -db/-driver needed on the box.
deploy-rules:
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@test -f $(TAGS_FILE) || { echo "$(TAGS_FILE) not found (hosted-only asset, gitignored)"; exit 1; }
	scp $(TAGS_FILE) $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glocker-tags.json
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo /usr/local/bin/glockpeek -import-rules /tmp/glocker-tags.json && rm -f /tmp/glocker-tags.json'

# ── Database backup ──────────────────────────────────────────
# Restore relies on DigitalOcean's managed droplet backups; this is a local
# pg_dump for debugging/inspection of data that can't be re-synced from the agent
# logs (accounts, tokens, the key vault, rules, tag colours, the ignore overlay).
# Dumps carry sensitive data (argon2 hashes, the wrapped master key), so
# DB_BACKUP_DIR (defined up top) is gitignored.

db-backup: ## Pull a compressed pg_dump of the glockpeek DB to DB_BACKUP_DIR (local, gitignored)
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@mkdir -p $(DB_BACKUP_DIR)
	@f="$(DB_BACKUP_DIR)/glockpeek-$$(date -u +%Y%m%dT%H%M%SZ).dump"; \
	 if [ -n "$(DB_DSN)" ]; then \
	   dump="pg_dump -Fc '$(DB_DSN)'"; \
	 else \
	   dump="sudo -u postgres pg_dump -Fc glockpeek"; \
	 fi; \
	 ssh $(DEPLOY_USER)@$(DEPLOY_HOST) "$$dump" > "$$f" && \
	 echo "saved $$f ($$(du -h "$$f" | cut -f1))"


# Publish the SIGNED Firefox extension to the apex web root, served at
# https://$(DEPLOY_HOST)/glocker.xpi — where `glocker -install` fetches it on a
# machine without a locally built artifact. Signing is a separate, manual, one-off
# step (`web-ext sign --channel unlisted` in extensions/firefox/); this only ships
# the newest signed .xpi already in web-ext-artifacts/. Re-run after re-signing.
# Sign the Firefox extension via Mozilla's AMO API (unlisted self-distribution),
# dropping the signed .xpi into extensions/firefox/web-ext-artifacts/. Automated —
# no human review for unlisted. Needs:
#   - web-ext            (npm i -g web-ext)
#   - AMO_JWT_ISSUER / AMO_JWT_SECRET in the env
#     (addons.mozilla.org → Developer Hub → Manage API Keys; keep them out of git)
# AMO rejects a version it has already signed, so the "version" in
# extensions/firefox/manifest.json must be bumped first — `make sign-extension`
# does that check for you. Then `make deploy-extension` ships it.
sign-extension:
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
	cd extensions/firefox && web-ext sign --channel=unlisted --api-key="$$AMO_JWT_ISSUER" --api-secret="$$AMO_JWT_SECRET"
	@git checkout -- extensions/firefox/build-info.js 2>/dev/null || true

# Sign then publish in one step.
release-extension: sign-extension deploy-extension ## Sign the Firefox extension and publish it

deploy-extension: ## Publish the signed Firefox extension xpi to the site
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy-extension DEPLOY_HOST=my.example.com"; exit 1; }
	@xpi=$$(ls -t extensions/firefox/web-ext-artifacts/*.xpi 2>/dev/null | head -1); \
	 test -n "$$xpi" || { echo "no signed .xpi in extensions/firefox/web-ext-artifacts/ — run 'web-ext sign --channel unlisted' there first"; exit 1; }; \
	 echo "shipping $$xpi"; \
	 scp "$$xpi" $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glocker.xpi && \
	 ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -D -o caddy -g caddy -m 0644 /tmp/glocker.xpi /var/www/glocker-landing/glocker.xpi && rm -f /tmp/glocker.xpi && echo "extension published at /glocker.xpi"'

# ── Release: signed binaries for a GitHub release (source stays the main path). ──
# Builds static linux/amd64 binaries + a SHA256SUMS file, then signs SHA256SUMS
# with minisign using your OFFLINE key — so a download verifies against a key that
# lives on no server. Signature covers the checksums; the checksums cover the
# binaries. Sigstore/keyless is deferred (it only pays off with CI).
#
# One-time setup:
#   minisign -G -s ~/.minisign/glocker.key -p minisign.pub   # generate your keypair
#   # commit minisign.pub, paste it into the README, publish at glockerapp.com/minisign.pub
# Per release:
#   make release                 # prompts for your key passphrase
#   # then upload dist/release/* to the GitHub release
# Point at a different key with MINISIGN_KEY=/path/to/key.
MINISIGN_KEY ?= $(HOME)/.minisign/glocker.key
RELEASE_DIR := dist/release
release: ## Build + checksum + minisign the release binaries (needs minisign + your key)
	@command -v minisign >/dev/null || { echo "install minisign first — https://jedisct1.github.io/minisign/"; exit 1; }
	rm -rf $(RELEASE_DIR) && mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glocker   ./cmd/glocker
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glocklock ./cmd/glocklock
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glockpeek ./cmd/glockpeek
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o $(RELEASE_DIR)/glockdoc  ./cmd/glockdoc
	cd $(RELEASE_DIR) && sha256sum glocker glocklock glockpeek glockdoc > SHA256SUMS
	minisign -Sm $(RELEASE_DIR)/SHA256SUMS -s $(MINISIGN_KEY)
	@echo "✓ signed — upload $(RELEASE_DIR)/* (binaries + SHA256SUMS + SHA256SUMS.minisig) to the GitHub release"
