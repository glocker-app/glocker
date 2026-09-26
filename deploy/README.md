# Deploying glockpeek

The production deployment (DigitalOcean droplet, Terraform + Ansible, TLS via
Caddy) and all real config/secrets/state are managed in a **separate private ops
repo**, which consumes this repository as a git **subtree**. That keeps secrets,
terraform state, and DB backups out of the open-source tree entirely.

## Self-hosting

glockpeek is a single Go binary. To run your own:

1. **Host**: any Linux box.
2. **Database**: PostgreSQL (or SQLite via `-driver sqlite -db path.db` for a small
   single-user setup).
3. **Config**: copy `conf/glockpeek.yaml.sample` → `glockpeek.yaml`, fill in
   `listen`, `database`, and (optionally) `mail` / `billing`. It's read from
   `/etc/glocker/glockpeek.yaml` by default.
4. **Build + run**: `go build ./cmd/glockpeek`, then run it behind a reverse proxy
   (Caddy/nginx/Traefik) terminating TLS and forwarding to `127.0.0.1:4317`.
5. **Accounts**: `glockpeek -adduser <email>` and `-addtoken <email>` (see
   `glockpeek -help`).

`make deploy DEPLOY_HOST=… GLOCKPEEK_CONF=/path/to/glockpeek.yaml` builds the
binary and ships it + your config over SSH to an already-provisioned host.

For a scaled deployment (load balancer, managed Postgres, CDN for static assets),
note that the app tier is stateless — sessions/tokens live in Postgres — so it
scales horizontally behind a load balancer.

## `testing/`

`testing/simulate-device.sh` posts synthetic batches to `/api/ingest` so you can
exercise the multi-device dashboard without owning several machines. See
`testing/README.md`.
