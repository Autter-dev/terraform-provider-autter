package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccRepositoryDataSources(t *testing.T) {
	s := newMock(t)
	s.AddRepository("repo-3", "other/web")
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				Config:      testConfig(s, `data "autter_repository" "r" { name = "web" }`),
				ExpectError: expectError(`2 repositories are named "web": acme/web, other/web. Use full_name instead.`),
			},
			{
				Config:      testConfig(s, `data "autter_repository" "r" { full_name = "acme/missing" }`),
				ExpectError: expectError(`No repository "acme/missing" is connected to the token's Autter organization. Connected repositories: acme/api, acme/web, other/web.`),
			},
			{
				Config:      testConfig(s, `data "autter_repository" "r" {}`),
				ExpectError: expectError(`Exactly one of these attributes must be configured`),
			},
			{
				// The last step is valid, so the post-test destroy can run.
				Config: testConfig(s, `
data "autter_repository" "by_full_name" {
  full_name = "ACME/WEB"
}
data "autter_repository" "by_name" {
  name = "api"
}
data "autter_repositories" "all" {}
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.autter_repository.by_full_name", "id", "repo-1"),
					resource.TestCheckResourceAttr("data.autter_repository.by_full_name", "full_name", "acme/web"),
					resource.TestCheckResourceAttr("data.autter_repository.by_full_name", "default_branch", "main"),
					resource.TestCheckResourceAttr("data.autter_repository.by_name", "id", "repo-2"),
					resource.TestCheckResourceAttr("data.autter_repositories.all", "repositories.#", "3"),
					resource.TestCheckResourceAttr("data.autter_repositories.all", "repositories.2.full_name", "other/web"),
				),
			},
		},
	})
}
