data "autter_repositories" "all" {}

output "repository_names" {
  value = [for r in data.autter_repositories.all.repositories : r.full_name]
}
