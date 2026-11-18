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

resource "autter_dashboard" "checkout_health" {
  repository_id = data.autter_repository.acme_web.id
  name          = "Checkout health"
  variables = {
    service = "checkout"
    range   = "24h"
  }
  widgets = [
    {
      id       = "errors"
      title    = "Server errors by route"
      viz      = "bar"
      source   = "errors"
      measure  = "count"
      group_by = ["route"]
      filters = [
        {
          field = "status_code"
          op    = "gte"
          value = 500
        },
        {
          field  = "environment"
          op     = "in"
          values = ["production", "staging"]
        },
      ]
    },
    {
      id      = "latency"
      title   = "p95 latency"
      viz     = "line"
      source  = "spans"
      measure = "p95"
      field   = "duration_ms"
      filters = [
        {
          field = "route"
          op    = "starts_with"
          value = "/checkout"
        },
      ]
    },
    {
      id     = "slowest"
      title  = "Slowest routes"
      viz    = "table"
      source = "sql"
      sql    = "SELECT route, max(duration_ms) AS slowest FROM runtime_spans GROUP BY route ORDER BY slowest DESC LIMIT 10"
    },
  ]
  layout = [
    {
      id = "errors"
      w  = 6
      h  = 2
    },
    {
      id = "latency"
      w  = 6
      h  = 2
    },
    {
      id = "slowest"
      w  = 12
      h  = 2
    },
  ]
}
