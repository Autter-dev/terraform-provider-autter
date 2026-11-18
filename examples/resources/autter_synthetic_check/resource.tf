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

# A journey check replays a saved journey of the repository (the Journeys tab) in a
# browser against the site's address. It takes no HTTP-only attributes (method,
# headers, expected_status, body_contains, timeout_ms, latency_threshold_ms).
resource "autter_synthetic_check" "signup" {
  repository_id    = data.autter_repository.web.id
  name             = "Sign-up journey"
  kind             = "journey"
  url              = "https://app.example.com"
  journey_id       = "wj_signup"
  interval_seconds = 3600 # 900, 3600, 21600 or 86400
}
