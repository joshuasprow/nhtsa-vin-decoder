# The account resources around the nitrovin.app Worker: zone settings, DNS,
# the custom domain, redirect and rate-limit rules. deploy.yml applies it
# with the CI token token/ creates; the Worker and its container are
# wrangler's (../worker). deploy/cloudflare/README.md has the bootstrap.
terraform {
  required_version = "~> 1.17.0"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.28"
    }
  }

  # State in R2, never in a company project. The endpoint is the account's
  # (AWS_ENDPOINT_URL_S3=https://<account id>.r2.cloudflarestorage.com) and
  # the credentials an R2 token scoped to this bucket (AWS_ACCESS_KEY_ID,
  # AWS_SECRET_ACCESS_KEY). use_lockfile is the lock: R2 honours the
  # conditional write it takes.
  backend "s3" {
    bucket = "nitrovin-tfstate"
    key    = "cloudflare/main.tfstate"
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

# CLOUDFLARE_API_TOKEN: the CI token (token/), or a person's for a plan.
provider "cloudflare" {}
