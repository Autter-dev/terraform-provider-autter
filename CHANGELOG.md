# Changelog

## 0.1.0 (November 19, 2026)

The first release.

FEATURES:

- **Resource `autter_alert_rule`**: which runtime issue events reach whom (triggers, filters, Slack, email,
  in-app and on-call actions).
- **Resource `autter_synthetic_check`**: a check on a schedule, with the last run's status as read-only
  attributes. `kind = "http"` (the default) requests a URL, with sensitive request headers.
  `kind = "journey"` replays a saved journey of the repository in a browser against the site's address
  (`journey_id`, intervals of 900, 3600, 21600 or 86400 seconds, `first_passed_at`); the HTTP-only attributes
  are rejected at plan for it. A check's kind is fixed: changing it replaces the check. A check of a kind this
  version does not know fails with "upgrade the provider".
- **Resource `autter_dashboard`**: a Runtime dashboard with its variables, widgets and layout. A filter's
  single value is `value`, a list (`in`, `not_in`) is `values`.
- **Data source `autter_repository`**: a connected repository by `full_name` or `name`.
- **Data source `autter_repositories`**: every connected repository.
- Resources are identified by repository and name; import with `<repository>:<name>`. Creating a resource
  whose name exists adopts it, with a warning.
- Autter's normalization of stored values never shows as a change; changes made outside Terraform do.
- Writes are sent one at a time and wait out the API's rate limit (`Retry-After`).
- Configuration exported with "Export as Terraform" in the Autter dashboard applies and imports as is.
