# What CI cannot hold itself: its own API token, the state bucket, and the
# R2 token that reads and writes the state. A person applies this, once at
# bootstrap (on local state, then moved into the bucket it made; README.md)
# and again only to change a token's scopes, with a short-lived token of
# their own; CI never plans or applies it, so the CI token needs no right
# over tokens or buckets.
terraform {
  required_version = "~> 1.17.0"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.28"
    }
  }

  # As ../versions.tf, under its own key.
  backend "s3" {
    bucket = "nitrovin-tfstate"
    key    = "cloudflare/token.tfstate"
    region = "auto"

    use_lockfile                = true
    use_path_style              = true
    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_s3_checksum            = true
  }
}

provider "cloudflare" {}

variable "account_id" {
  description = "The Cloudflare account that holds nitrovin.app (TF_VAR_account_id)."
  type        = string
}

variable "zone" {
  description = "The zone the CI token may configure."
  type        = string
  default     = "nitrovin.app"
}

data "cloudflare_zone" "nitrovin" {
  filter = {
    name    = var.zone
    account = { id = var.account_id }
  }
}

data "cloudflare_account_api_token_permission_groups_list" "all" {
  account_id = var.account_id
}

locals {
  # What deploy.yml does: wrangler deploy (the Worker, its Durable Object,
  # the container and its image in Cloudflare's registry, rollback), and
  # Terraform over ../ (zone settings, DNS, the custom domain, the redirect
  # and rate-limit rulesets).
  account_permissions = [
    "Account Settings Read",
    "Workers Scripts Write",
    "Workers Containers Write",
    "Cloudchamber Write",
  ]
  zone_permissions = [
    "Zone Read",
    "Zone Settings Write",
    "DNS Write",
    "Workers Routes Write",
    "Zone WAF Write",
    "Dynamic URL Redirects Write",
  ]

  # Permission group ids by name, per scope: an unknown name fails the plan
  # at the lookup.
  groups = data.cloudflare_account_api_token_permission_groups_list.all.result
  account_groups = {
    for g in local.groups : g.name => g.id if contains(g.scopes, "com.cloudflare.api.account")
  }
  zone_groups = {
    for g in local.groups : g.name => g.id if contains(g.scopes, "com.cloudflare.api.account.zone")
  }
}

resource "cloudflare_account_token" "ci" {
  account_id = var.account_id
  name       = "nitrovin deploy (GitHub Actions, deploy.yml)"
  policies = [
    {
      effect            = "allow"
      permission_groups = [for name in local.account_permissions : { id = local.account_groups[name] }]
      resources         = jsonencode({ "com.cloudflare.api.account.${var.account_id}" = "*" })
    },
    {
      effect            = "allow"
      permission_groups = [for name in local.zone_permissions : { id = local.zone_groups[name] }]
      resources         = jsonencode({ "com.cloudflare.api.account.zone.${data.cloudflare_zone.nitrovin.zone_id}" = "*" })
    },
  ]
}

resource "cloudflare_r2_bucket" "state" {
  account_id = var.account_id
  name       = "nitrovin-tfstate"

  lifecycle {
    prevent_destroy = true
  }
}

# The state's credentials, for both roots, CI and a person: objects in this
# bucket alone. R2's S3 API takes an API token as an access key: its id, and
# the SHA-256 of its value as the secret. Replacing it cuts off the backend
# holding this state, so rotate it as a new resource beside this one, then
# remove this one.
resource "cloudflare_account_token" "state" {
  account_id = var.account_id
  name       = "nitrovin Terraform state (R2 nitrovin-tfstate)"
  policies = [{
    effect            = "allow"
    permission_groups = [{ id = one([for g in local.groups : g.id if g.name == "Workers R2 Storage Bucket Item Write"]) }]
    resources         = jsonencode({ "com.cloudflare.edge.r2.bucket.${var.account_id}_default_${cloudflare_r2_bucket.state.name}" = "*" })
  }]
}

# R2 keeps no object versions. deploy.yml copies the state to history/
# before every apply instead; those copies are kept 90 days.
resource "cloudflare_r2_bucket_lifecycle" "state" {
  account_id  = var.account_id
  bucket_name = cloudflare_r2_bucket.state.name
  rules = [{
    id         = "expire-history"
    enabled    = true
    conditions = { prefix = "history/" }
    delete_objects_transition = {
      condition = { type = "Age", max_age = 7776000 }
    }
  }]
}

output "ci_token" {
  description = "The CI token's value: terraform output -raw ci_token | gh secret set CLOUDFLARE_API_TOKEN"
  value       = cloudflare_account_token.ci.value
  sensitive   = true
}

output "r2_access_key_id" {
  description = "AWS_ACCESS_KEY_ID for the state: terraform output -raw r2_access_key_id | gh secret set R2_ACCESS_KEY_ID"
  value       = cloudflare_account_token.state.id
}

output "r2_secret_access_key" {
  description = "AWS_SECRET_ACCESS_KEY for the state: terraform output -raw r2_secret_access_key | gh secret set R2_SECRET_ACCESS_KEY"
  value       = sha256(cloudflare_account_token.state.value)
  sensitive   = true
}
