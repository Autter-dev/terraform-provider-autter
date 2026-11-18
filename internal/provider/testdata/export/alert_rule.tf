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

resource "autter_alert_rule" "checkout_errors" {
  repository_id = data.autter_repository.acme_web.id
  name          = "Checkout errors"
  enabled       = true
  triggers      = ["new", "regressed", "spiking"]
  filters = {
    environments = ["production"]
    services     = ["checkout", "payments"]
    levels       = ["error", "fatal"]
    priorities   = ["p0", "p1"]
    message = {
      op    = "contains"
      value = "Timeout"
    }
    assignee = "any"
    threshold = {
      min_events     = 5
      window_minutes = 30
    }
  }
  actions = {
    slack = {}
    email = {
      assignee = true
      watchers = true
      everyone = false
    }
    oncall           = true
    auto_assignments = false
  }
}
