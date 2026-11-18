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

variable "health_authorization" {
  description = "The Authorization header of the synthetic check \"Health\". Autter never returns header values."
  type        = string
  sensitive   = true
}

variable "health_x_api_key" {
  description = "The X-Api-Key header of the synthetic check \"Health\". Autter never returns header values."
  type        = string
  sensitive   = true
}

resource "autter_synthetic_check" "health" {
  repository_id        = data.autter_repository.acme_web.id
  name                 = "Health"
  url                  = "https://status.example.com/health"
  method               = "GET"
  expected_status      = 200
  body_contains        = "ok"
  timeout_ms           = 5000
  interval_seconds     = 300
  latency_threshold_ms = 1500
  failures_to_fire     = 3
  service              = "status-page"
  environment          = "production"
  enabled              = true
  headers = {
    Authorization = var.health_authorization
    "X-Api-Key"   = var.health_x_api_key
  }
}
