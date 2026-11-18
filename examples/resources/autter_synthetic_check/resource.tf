data "autter_repository" "web" {
  full_name = "acme/web"
}

# Header values are stored encrypted and never returned: keep them in sensitive variables.
variable "status_api_key" {
  type      = string
  sensitive = true
}

resource "autter_synthetic_check" "health" {
  repository_id        = data.autter_repository.web.id
  name                 = "Health"
  url                  = "https://status.example.com/health"
  method               = "GET"
  expected_status      = 200
  body_contains        = "ok"
  interval_seconds     = 300
  latency_threshold_ms = 1500
  failures_to_fire     = 3
  service              = "status-page"

  headers = {
    "X-Api-Key" = var.status_api_key
  }
}
