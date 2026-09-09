# Existing SSH key + project, looked up by name.
data "digitalocean_ssh_key" "default" {
  name = var.ssh_key_name
}

data "digitalocean_project" "target" {
  name = var.project_name
}

resource "digitalocean_droplet" "glockpeek" {
  name     = var.droplet_name
  image    = var.droplet_image
  region   = var.region
  size     = var.droplet_size
  ssh_keys = [data.digitalocean_ssh_key.default.id]
  ipv6     = true
  tags     = ["glockpeek"]

  # Managed, off-box backups (~20% of droplet cost). Protects the DB-only data
  # that can't be re-synced from the agent logs: accounts, API tokens, the key
  # vault, usage rules, tag colours, and the ignore overlay. Daily so the restore
  # window is a day, not a week.
  backups = true
  backup_policy {
    plan = "daily"
    hour = 4 # UTC backup window (~low activity)
  }
}

# Place the droplet under the existing "Personal" project.
resource "digitalocean_project_resources" "personal" {
  project   = data.digitalocean_project.target.id
  resources = [digitalocean_droplet.glockpeek.urn]
}

resource "digitalocean_firewall" "glockpeek" {
  name        = "glockpeek-fw"
  droplet_ids = [digitalocean_droplet.glockpeek.id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.ssh_source_addresses
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }
  inbound_rule {
    protocol         = "icmp"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

# Optional A record (only when the zone is on DigitalOcean).
resource "digitalocean_record" "peek" {
  count  = var.manage_dns && var.dns_zone != "" ? 1 : 0
  domain = var.dns_zone
  type   = "A"
  name   = var.dns_record
  value  = digitalocean_droplet.glockpeek.ipv4_address
  ttl    = 300
}
