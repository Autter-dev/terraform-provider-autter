---
page_title: "Bring existing resources under Terraform"
subcategory: ""
description: |-
  Export alert rules, synthetic checks and dashboards from the Autter dashboard and import them.
---

# Bring existing resources under Terraform

Every alert rule (repository **Settings → Runtime**), synthetic check (**Runtime → Checks**) and dashboard
(**Runtime → Dashboards**) has **Export as Terraform**. It writes configuration for this provider: the
`required_providers` block, an `autter_repository` data source, and the resource, with each attribute set
to what Autter stores.

1. Export the resource and save the HCL in your Terraform configuration.
2. Add an `import` block for it (or run `terraform import`):

   ```terraform
   import {
     to = autter_alert_rule.checkout_errors
     id = "acme/web:Checkout errors"
   }
   ```

3. Run `terraform plan`. An alert rule or a dashboard imports with no changes.

   A synthetic check with request headers exports each header as a `sensitive` variable, because Autter never
   returns header values. Set the variables (for example in a `.tfvars` file kept out of version control or
   with `TF_VAR_…` environment variables); the plan then shows one in-place update that sends them.

4. Run `terraform apply`.

You can also skip the import: applying the exported configuration adopts the resource with the same name
rather than creating a copy, and prints a warning that it did.

## Notes

- A dashboard filter with one value is written as `value`, and one with a list (`in`, `not_in`) as `values`.
- Fields left at their defaults and empty lists are not written.
- Importing a resource by `owner/repo` stores the repository's id in `repository_id`, which is what
  `data.autter_repository.<label>.id` gives, so plans stay clean.
