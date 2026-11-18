package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Autter-dev/terraform-provider-autter/internal/mockapi"
)

const checkAddr = "autter_synthetic_check.health"

func checkConfig(extra string) string {
	return fmt.Sprintf(`
variable "api_key" {
  type      = string
  sensitive = true
}

resource "autter_synthetic_check" "health" {
  repository_id = "repo-1"
  name          = "Health"
  url           = "https://status.example.com"
  headers = {
    "X-Api-Key" = var.api_key
  }
  %s
}
`, extra)
}

func TestAccSyntheticCheckLifecycle(t *testing.T) {
	s := newMock(t)
	var before int
	key := func(v string) config.Variables { return config.Variables{"api_key": config.StringVariable(v)} }
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				// A URL without a trailing slash: stored with one, never shown as a change.
				PreConfig:       mark(s, &before),
				Config:          testConfig(s, checkConfig("")),
				ConfigVariables: key("first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT created"),
					resource.TestCheckResourceAttr(checkAddr, "url", "https://status.example.com"),
					resource.TestCheckResourceAttr(checkAddr, "kind", "http"),
					resource.TestCheckResourceAttr(checkAddr, "method", "GET"),
					resource.TestCheckResourceAttr(checkAddr, "interval_seconds", "300"),
					resource.TestCheckResourceAttr(checkAddr, "header_names.0", "X-Api-Key"),
					resource.TestCheckResourceAttr(checkAddr, "firing", "false"),
					resource.TestCheckResourceAttrSet(checkAddr, "issue_id"),
					resource.TestCheckResourceAttrSet(checkAddr, "next_due_at"),
					func(*terraform.State) error {
						if got := s.Headers("repo-1", "Health")["X-Api-Key"]; got != "first" {
							return fmt.Errorf("stored header %q", got)
						}
						if got := s.Get("repo-1", mockapi.SyntheticChecks, "Health")["url"]; got != "https://status.example.com/" {
							return fmt.Errorf("stored url %v", got)
						}
						return nil
					},
				),
			},
			{
				// A new header value: one PUT.
				PreConfig:       mark(s, &before),
				Config:          testConfig(s, checkConfig("")),
				ConfigVariables: key("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(checkAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT updated"),
					func(*terraform.State) error {
						if got := s.Headers("repo-1", "Health")["X-Api-Key"]; got != "second" {
							return fmt.Errorf("stored header %q", got)
						}
						return nil
					},
				),
			},
			{
				// A header removed in the dashboard: the plan puts it back.
				PreConfig: func() {
					s.Edit("repo-1", mockapi.SyntheticChecks, "Health", func(f map[string]any) { f["header_names"] = []any{} })
					mark(s, &before)()
				},
				Config:          testConfig(s, checkConfig("")),
				ConfigVariables: key("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(checkAddr, plancheck.ResourceActionUpdate)},
				},
				Check: expectWrites(s, &before, "PUT updated"),
			},
			{
				// Other fields, and drift on one of them.
				PreConfig: func() {
					s.Edit("repo-1", mockapi.SyntheticChecks, "Health", func(f map[string]any) { f["interval_seconds"] = 3600 })
					mark(s, &before)()
				},
				Config:          testConfig(s, checkConfig(`method = "head"`+"\n"+`interval_seconds = 900`+"\n"+`latency_threshold_ms = 800`+"\n"+`service = "status"`)),
				ConfigVariables: key("second"),
				ExpectError:     expectError(`value must be one of`),
			},
			{
				PreConfig:       mark(s, &before),
				Config:          testConfig(s, checkConfig(`method = "HEAD"`+"\n"+`interval_seconds = 900`+"\n"+`latency_threshold_ms = 800`+"\n"+`service = "status"`)),
				ConfigVariables: key("second"),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT updated"),
					resource.TestCheckResourceAttr(checkAddr, "interval_seconds", "900"),
					func(*terraform.State) error {
						if got := s.Get("repo-1", mockapi.SyntheticChecks, "Health")["interval_seconds"]; fmt.Sprint(got) != "900" {
							return fmt.Errorf("stored interval %v", got)
						}
						return nil
					},
				),
			},
			{
				// Import of a check with headers: one in-place update (the header values cannot be read), then nothing.
				ResourceName:       checkAddr,
				ImportState:        true,
				ImportStateKind:    resource.ImportBlockWithID,
				ImportStateId:      "acme/web:Health",
				Config:             testConfig(s, checkConfig(`method = "HEAD"`+"\n"+`interval_seconds = 900`+"\n"+`latency_threshold_ms = 800`+"\n"+`service = "status"`)),
				ConfigVariables:    key("second"),
				ExpectNonEmptyPlan: true,
				ImportPlanChecks: resource.ImportPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(checkAddr, plancheck.ResourceActionUpdate)},
				},
			},
		},
	})
}

func TestAccSyntheticCheckErrors(t *testing.T) {
	s := newMock(t)
	check := func(name, extra string) string {
		return fmt.Sprintf(`
resource "autter_synthetic_check" "c" {
  repository_id = "repo-1"
  name          = %q
  url           = "https://example.com/"
  %s
}
`, name, extra)
	}
	s.Seed("repo-1", mockapi.SyntheticChecks, map[string]any{"name": "Journey", "kind": "journey", "url": "https://example.com/"})
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{Config: testConfig(s, check("Bad kind", `kind = "browser"`)), ExpectError: expectError(`value must be one of`)},
			{Config: testConfig(s, check("Head body", `method = "HEAD"`+"\n"+`body_contains = "ok"`)), ExpectError: expectError(`A HEAD request has no body to search`)},
			{Config: testConfig(s, check("Journey", "")), ExpectError: expectError(`is a "journey" check, which this version of the Autter provider does not support`)},
			{
				PreConfig:   func() { s.SetMaxChecks(1) },
				Config:      testConfig(s, check("Over the limit", "")),
				ExpectError: expectError(`Plan limit reached`),
			},
			{
				PreConfig:   func() { s.SetMaxChecks(0); s.SetFeatureDisabled(mockapi.SyntheticChecks, true) },
				Config:      testConfig(s, check("Feature off", "")),
				ExpectError: expectError(`turned off for this organization`),
			},
		},
	})
}
