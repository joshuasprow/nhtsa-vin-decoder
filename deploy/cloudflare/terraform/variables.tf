variable "account_id" {
  description = "The Cloudflare account that holds the zone and the Worker (TF_VAR_account_id; the repository variable CLOUDFLARE_ACCOUNT_ID)."
  type        = string
}

variable "zone" {
  description = "The zone the Worker serves, registered on Cloudflare Registrar in the same account."
  type        = string
  default     = "nitrovin.app"
}

variable "worker" {
  description = "The Worker's name, as ../worker/wrangler.jsonc gives it."
  type        = string
  default     = "nitrovin"
}
