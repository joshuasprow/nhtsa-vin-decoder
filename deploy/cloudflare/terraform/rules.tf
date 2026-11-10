# One container serves every decode, so one client must not be able to
# take it all. 100 requests in 10 s from one IP (per data center) is far
# above a person and a well-behaved integration (a batch POST carries up to
# 1,000 VINs); past it, that IP is refused for 10 s. The Free plan allows
# one rule, a 10 s period and timeout, and the path alone in the expression,
# which is all this needs.
#
# There are no cache rules here: zone cache rules govern fetches through the
# zone to an origin, and the Worker answers every request itself; the
# Worker's Cache API keys and stores the cacheable GETs (../worker).
resource "cloudflare_ruleset" "rate_limit" {
  zone_id = local.zone_id
  name    = "nitrovin rate limit"
  kind    = "zone"
  phase   = "http_ratelimit"
  rules = [{
    ref         = "decode_and_mcp_per_ip"
    description = "/v1/decode and /mcp: 100 requests per 10 s per IP"
    expression  = "(starts_with(http.request.uri.path, \"/v1/decode\") or http.request.uri.path eq \"/mcp\")"
    action      = "block"
    ratelimit = {
      characteristics     = ["cf.colo.id", "ip.src"]
      period              = 10
      requests_per_period = 100
      mitigation_timeout  = 10
    }
  }]
}
