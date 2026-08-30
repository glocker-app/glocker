.PHONY: build-all install full-install security-check update-blocklists test release deploy deploy-full deploy-landing deploy-extension help

.DEFAULT_GOAL:=help
help: ## display this help message
	@echo "Please use \`make <target>' where <target> is one of"
	@perl -nle'print $& if m{^[a-zA-Z_-]+:.*?## .*$$}' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m  %-25s\033[0m %s\n", $$1, $$2}'




build-all: ## Build all binaries
	go build -o glocker ./cmd/glocker
	go build -o glocklock ./cmd/glocklock
	go build -o glockpeek ./cmd/glockpeek
	go build -o glockdoc ./cmd/glockdoc


install: build-all ## Rebuild and reinstall
	sudo ./glocker -uninstall "maintenance" -note "upgrade via Make" || true
	sudo ./glocker -install


full-install: build-all update-blocklists ## Rebuild, update blocklists, and reinstall
	sudo ./glocker -uninstall "maintenance" -note "upgrade via Make" || true
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
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/glockpeek ./cmd/glockpeek
	scp dist/glockpeek $(DEPLOY_USER)@$(DEPLOY_HOST):/tmp/glockpeek.new
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) 'sudo install -m 0755 /tmp/glockpeek.new /usr/local/bin/glockpeek && rm -f /tmp/glockpeek.new && sudo systemctl restart glockpeek && sleep 2 && systemctl is-active glockpeek'

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
# Point at a non-default key with MINISIGN_KEY=/path/to/key.
MINISIGN_KEY ?=
RELEASE_DIR := dist/release
release: ## Build + checksum + minisign the release binaries (needs minisign + your key)
	@command -v minisign >/dev/null || { echo "install minisign first — https://jedisct1.github.io/minisign/"; exit 1; }
	rm -rf $(RELEASE_DIR) && mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o $(RELEASE_DIR)/glocker   ./cmd/glocker
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o $(RELEASE_DIR)/glocklock ./cmd/glocklock
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o $(RELEASE_DIR)/glockpeek ./cmd/glockpeek
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o $(RELEASE_DIR)/glockdoc  ./cmd/glockdoc
	cd $(RELEASE_DIR) && sha256sum glocker glocklock glockpeek glockdoc > SHA256SUMS
	minisign -Sm $(RELEASE_DIR)/SHA256SUMS $(if $(MINISIGN_KEY),-s $(MINISIGN_KEY),)
	@echo "✓ signed — upload $(RELEASE_DIR)/* (binaries + SHA256SUMS + SHA256SUMS.minisig) to the GitHub release"
