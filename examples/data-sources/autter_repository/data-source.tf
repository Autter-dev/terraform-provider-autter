# By full name (owner/repo), matched without regard to case.
data "autter_repository" "web" {
  full_name = "acme/web"
}

# By short name, when only one repository has it.
data "autter_repository" "api" {
  name = "api"
}

output "web_repository_id" {
  value = data.autter_repository.web.id
}
