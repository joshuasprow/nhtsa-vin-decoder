# The apex is the Worker's: a custom domain owns the hostname's DNS record
# and certificate, so there is no apex record to write besides it. The
# Worker must exist first: deploy.yml runs wrangler deploy before this.
resource "cloudflare_workers_custom_domain" "apex" {
  account_id = var.account_id
  zone_id    = local.zone_id
  hostname   = var.zone
  service    = var.worker
}

# www answers only to redirect to the apex: a proxied record with no origin
# (100:: is the discard prefix) and the redirect rule below.
resource "cloudflare_dns_record" "www" {
  zone_id = local.zone_id
  name    = "www.${var.zone}"
  type    = "AAAA"
  content = "100::"
  proxied = true
  ttl     = 1
  comment = "Redirected to the apex (terraform/worker.tf); no origin."
}

resource "cloudflare_ruleset" "redirects" {
  zone_id = local.zone_id
  name    = "nitrovin redirects"
  kind    = "zone"
  phase   = "http_request_dynamic_redirect"
  rules = [{
    ref         = "www_to_apex"
    description = "www.${var.zone} to the apex, path and query kept"
    expression  = "(http.host eq \"www.${var.zone}\")"
    action      = "redirect"
    action_parameters = {
      from_value = {
        status_code = 301
        target_url = {
          expression = "concat(\"https://${var.zone}\", http.request.uri.path)"
        }
        preserve_query_string = true
      }
    }
  }]
}
