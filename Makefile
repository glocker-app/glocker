.PHONY: build-all install full-install security-check update-blocklists test release deploy deploy-full deploy-rules deploy-landing deploy-extension db-backup db-restore help

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

build-all: ## Build all binaries
	go build $(GOLDFLAGS) -o glocker ./cmd/glocker
	go build $(GOLDFLAGS) -o glocklock ./cmd/glocklock
	go build $(GOLDFLAGS) -o glockpeek ./cmd/glockpeek
	go build $(GOLDFLAGS) -o glockdoc ./cmd/glockdoc


install: build-all ## Rebuild and reinstall (in-place; stops+swaps+restarts the daemon)
	sudo ./glocker -install


full-install: build-all update-blocklists ## Rebuild, update blocklists, and reinstall
	sudo ./glocker -install


security-check: build-all ## Report bypass/recovery routes (read-only, needs sudo)
	sudo ./glocker -security-check


update-blocklists: ## Update blocklists
	python3 update_domains.py all


test: ## Run tests
	go test ./...

# ── Deploy: push updated artifacts to the already-provisioned VPS over SSH. ──
# Mirrors the install targets: `deploy-full` is the whole thing (like
# `full-install`); the rest ship one piece each (like `install`). One-time host
# provisioning — postgres, caddy, the service user — is separate; see deploy/
# (terraform + ansible).
#   make deploy-full DEPLOY_HOST=glockerapp.com          # binary + landing + extension
#   make deploy | deploy-landing | deploy-extension      # just one piece
DEPLOY_USER ?= root
DEPLOY_HOST ?= glockerapp.com

deploy-full: deploy deploy-landing deploy-extension ## Full deploy: dashboard binary + landing page + extension

deploy: ## Deploy the glockpeek dashboard binary to the VPS
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy DEPLOY_HOST=my.example.com"; exit 1; }
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOLDFLAGS) -trimpath -o dist/glockpeek ./cmd/glockpeek
	scp dist/glockpeek $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.new
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -m 0755 /tmp/glockpeek.new /usr/local/bin/glockpeek && rm -f /tmp/glockpeek.new && sudo systemctl restart glockpeek && sleep 2 && systemctl is-active glockpeek'

# Ship the curated Glocker starter rules (tags.json, hosted-only + gitignored)
# and load them into the default-rules template so NEW accounts seed from them.
# Idempotent (replaces the table); run after `deploy` (needs the new binary's
# -import-rules). Existing accounts are untouched. glockpeek reads the DB DSN
# from /etc/glocker/config.yaml, so no -db/-driver needed on the box.
deploy-rules: ## Load tags.json into the hosted default-rules template (run after deploy)
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@test -f tags.json || { echo "tags.json not found (hosted-only asset, gitignored)"; exit 1; }
	scp tags.json $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glocker-tags.json
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo /usr/local/bin/glockpeek -import-rules /tmp/glocker-tags.json && rm -f /tmp/glocker-tags.json'

# ── Database backup / restore ────────────────────────────────
# DigitalOcean backups cover droplet loss; a local pg_dump also lets you rebuild
# from scratch (terraform apply + ansible + db-restore) or move hosts — the DB
# holds data that can't be re-synced from the agent logs (accounts, tokens, the
# key vault, rules, tag colours, the ignore overlay). Dumps contain sensitive data
# (argon2 password hashes, the wrapped master key), so DB_BACKUP_DIR is gitignored.
DB_BACKUP_DIR ?= deploy/backups

db-backup: ## Pull a compressed pg_dump of the glockpeek DB to DB_BACKUP_DIR (local, gitignored)
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@mkdir -p $(DB_BACKUP_DIR)
	@f="$(DB_BACKUP_DIR)/glockpeek-$$(date -u +%Y%m%dT%H%M%SZ).dump"; \
	 ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo -u postgres pg_dump -Fc glockpeek' > "$$f" && \
	 echo "saved $$f ($$(du -h "$$f" | cut -f1))"

db-restore: ## Restore a dump into the server DB (DESTRUCTIVE): make db-restore FILE=deploy/backups/x.dump
	@test -n "$(FILE)" || { echo "set FILE=path/to/.dump"; exit 1; }
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST"; exit 1; }
	@echo "Restoring $(FILE) into glockpeek on $(DEPLOY_HOST) — this DROPS and recreates objects."
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo systemctl stop glockpeek'
	cat "$(FILE)" | ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo -u postgres pg_restore --clean --if-exists --no-owner -d glockpeek'
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo systemctl start glockpeek'

# Ship just the static landing page (website/index.html) to the apex web root.
# Same droplet as the dashboard, so DEPLOY_HOST can be the dashboard subdomain.
# For copy tweaks this is all you need — no rebuild, no ansible run.
deploy-landing: ## Deploy the static landing page to the apex web root
	@test -n "$(DEPLOY_HOST)" || { echo "set DEPLOY_HOST, e.g. make deploy-landing DEPLOY_HOST=my.example.com"; exit 1; }
	scp website/index.html $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glocker-landing.html
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -D -o caddy -g caddy -m 0644 /tmp/glocker-landing.html /var/www/glocker-landing/index.html && rm -f /tmp/glocker-landing.html && echo "landing page updated"'

# Publish the SIGNED Firefox extension to the apex web root, served at
# https://$(DEPLOY_HOST)/glocker.xpi — where `glocker -install` fetches it on a
# machine without a locally built artifact. Signing is a separate, manual, one-off
# step (`web-ext sign --channel unlisted` in extensions/firefox/); this only ships
# the newest signed .xpi already in web-ext-artifacts/. Re-run after re-signing.
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
