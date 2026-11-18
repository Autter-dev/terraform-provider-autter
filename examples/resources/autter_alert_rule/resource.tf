data "autter_repository" "web" {
  full_name = "acme/web"
}

resource "autter_alert_rule" "checkout_errors" {
  repository_id = data.autter_repository.web.id
  name          = "Checkout errors"
  triggers      = ["new", "regressed", "spiking"]

  filters = {
    environments = ["production"]
    services     = ["checkout", "payments"]
    priorities   = ["p0", "p1"]
    message = {
      op    = "contains"
      value = "Timeout"
    }
    threshold = {
      min_events     = 5
      window_minutes = 30
    }
  }

  actions = {
    # {} posts to the repository's Slack route; set channel_id for another channel.
    slack = {}
    email = {
      assignee = true
      watchers = true
    }
    oncall = true
  }
}
