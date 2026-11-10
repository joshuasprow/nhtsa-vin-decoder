# The zone exists already (Cloudflare Registrar creates it); this reads it.
data "cloudflare_zone" "nitrovin" {
  filter = {
    name    = var.zone
    account = { id = var.account_id }
  }
}

locals {
  zone_id = data.cloudflare_zone.nitrovin.zone_id
}

# TLS: Full (strict), HTTPS only, nothing below TLS 1.2, HSTS.

resource "cloudflare_zone_setting" "ssl" {
  zone_id    = local.zone_id
  setting_id = "ssl"
  value      = "strict"
}

resource "cloudflare_zone_setting" "always_use_https" {
  zone_id    = local.zone_id
  setting_id = "always_use_https"
  value      = "on"
}

resource "cloudflare_zone_setting" "min_tls_version" {
  zone_id    = local.zone_id
  setting_id = "min_tls_version"
  value      = "1.2"
}

# A year, subdomains included; not preloaded, which is the one part that is
# hard to take back (.app is on the preload list as a TLD already).
resource "cloudflare_zone_setting" "hsts" {
  zone_id    = local.zone_id
  setting_id = "security_header"
  value = {
    strict_transport_security = {
      enabled            = true
      max_age            = 31536000
      include_subdomains = true
      preload            = false
      nosniff            = true
    }
  }
}
