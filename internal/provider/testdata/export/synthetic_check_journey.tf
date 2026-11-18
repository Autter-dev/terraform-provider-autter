# Exported from Autter on 2026-11-16 for acme/web.
# Apply with the Autter Terraform provider (registry.terraform.io/providers/Autter-dev/autter).
# Each attribute is the Autter API field of the same name; a dashboard filter's list is `values`.

terraform {
  required_providers {
    autter = {
      source  = "Autter-dev/autter"
      version = "~> 0.1"
    }
  }
}

data "autter_repository" "acme_web" {
  full_name = "acme/web"
}

resource "autter_synthetic_check" "sign_up_journey" {
  repository_id    = data.autter_repository.acme_web.id
  kind             = "journey"
  name             = "Sign-up journey"
  url              = "https://app.example.com/"
  journey_id       = "wj_signup"
  interval_seconds = 3600
  failures_to_fire = 2
  service          = "web"
  environment      = "production"
  enabled          = true
}
