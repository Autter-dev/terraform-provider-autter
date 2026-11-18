data "autter_repository" "web" {
  full_name = "acme/web"
}

resource "autter_dashboard" "checkout" {
  repository_id = data.autter_repository.web.id
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
        # One value: value. A number is fine; Autter compares number columns as numbers.
        { field = "status_code", op = "gte", value = 500 },
        # A list, for in and not_in: values.
        { field = "environment", op = "in", values = ["production", "staging"] },
      ]
    },
    {
      id      = "latency"
      title   = "p95 latency"
      viz     = "line"
      source  = "spans"
      measure = "p95"
      field   = "duration_ms"
    },
    {
      id     = "slowest"
      title  = "Slowest routes"
      viz    = "table"
      source = "sql"
      sql    = "SELECT route, max(duration_ms) AS slowest FROM runtime_spans GROUP BY route ORDER BY slowest DESC LIMIT 10"
    },
  ]

  # Optional: left out, every widget gets its default size in widget order.
  layout = [
    { id = "errors", w = 6, h = 2 },
    { id = "latency", w = 6, h = 2 },
    { id = "slowest", w = 12, h = 2 },
  ]
}
