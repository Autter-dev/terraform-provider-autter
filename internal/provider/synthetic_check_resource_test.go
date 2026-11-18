package provider

import (
	"fmt"
	"strings"
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
	// A kind this version does not know (a later API's).
	s.Seed("repo-1", mockapi.SyntheticChecks, map[string]any{"name": "Browser", "kind": "browser", "url": "https://example.com/"})
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{Config: testConfig(s, check("Bad kind", `kind = "browser"`)), ExpectError: expectError(`value must be one of`)},
			{Config: testConfig(s, check("Head body", `method = "HEAD"`+"\n"+`body_contains = "ok"`)), ExpectError: expectError(`A HEAD request has no body to search`)},
			{Config: testConfig(s, check("Browser", "")), ExpectError: expectError(`is a "browser" check, which this version of the Autter provider does not support. Upgrade the provider`)},
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

const journeyAddr = "autter_synthetic_check.signup"

func journeyConfig(extra string) string {
	return fmt.Sprintf(`
resource "autter_synthetic_check" "signup" {
  repository_id = "repo-1"
  name          = "Sign-up"
  kind          = "journey"
  url           = "https://app.example.com"
  journey_id    = "wj_signup"
  %s
}
`, extra)
}

func TestAccJourneyCheckLifecycle(t *testing.T) {
	s := newMock(t)
	var before int
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				// No HTTP-only attribute is planned or sent (the API refuses them for a journey check);
				// the interval defaults to the journey default.
				PreConfig: mark(s, &before),
				Config:    testConfig(s, journeyConfig("")),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT created"),
					resource.TestCheckResourceAttr(journeyAddr, "kind", "journey"),
					resource.TestCheckResourceAttr(journeyAddr, "journey_id", "wj_signup"),
					resource.TestCheckResourceAttr(journeyAddr, "interval_seconds", "3600"),
					resource.TestCheckResourceAttr(journeyAddr, "failures_to_fire", "2"),
					resource.TestCheckNoResourceAttr(journeyAddr, "method"),
					resource.TestCheckNoResourceAttr(journeyAddr, "expected_status"),
					resource.TestCheckNoResourceAttr(journeyAddr, "timeout_ms"),
					resource.TestCheckNoResourceAttr(journeyAddr, "first_passed_at"),
					func(*terraform.State) error {
						stored := s.Get("repo-1", mockapi.SyntheticChecks, "Sign-up")
						if stored["kind"] != "journey" || stored["url"] != "https://app.example.com/" || stored["method"] != "GET" {
							return fmt.Errorf("stored %v", stored)
						}
						return nil
					},
				),
			},
			{
				// Apply again: nothing is sent, although the API stores method GET and the URL with a slash.
				PreConfig: mark(s, &before),
				Config:    testConfig(s, journeyConfig("")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: expectWrites(s, &before),
			},
			{
				// The first pass is read back; the interval changed in the dashboard is drift.
				PreConfig: func() {
					s.Edit("repo-1", mockapi.SyntheticChecks, "Sign-up", func(f map[string]any) {
						f["first_passed_at"] = "2026-11-18T11:00:00.000Z"
						f["interval_seconds"] = 86400
					})
					mark(s, &before)()
				},
				Config: testConfig(s, journeyConfig("")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(journeyAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT updated"),
					resource.TestCheckResourceAttr(journeyAddr, "first_passed_at", "2026-11-18T11:00:00.000Z"),
					resource.TestCheckResourceAttr(journeyAddr, "interval_seconds", "3600"),
				),
			},
			{
				// An import block plans no change against the configuration as the export writes it
				// (the URL as stored; a hand-written URL without the slash would show one update).
				ResourceName:    journeyAddr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
				ImportStateId:   "acme/web:Sign-up",
				Config:          testConfig(s, strings.Replace(journeyConfig(""), `"https://app.example.com"`, `"https://app.example.com/"`, 1)),
			},
			{
				// terraform import gives the same state, except the URL: state keeps the configured
				// one, an import takes the stored (normalized) one.
				ResourceName:                         journeyAddr,
				ImportState:                          true,
				ImportStateId:                        "repo-1:Sign-up",
				ImportStateVerify:                    true,
				ImportStateVerifyIgnore:              []string{"url"},
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// The kind is fixed: changing it replaces the check.
				PreConfig: mark(s, &before),
				Config: testConfig(s, `
resource "autter_synthetic_check" "signup" {
  repository_id = "repo-1"
  name          = "Sign-up"
  url           = "https://app.example.com/health"
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(journeyAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "DELETE deleted", "PUT created"),
					resource.TestCheckResourceAttr(journeyAddr, "kind", "http"),
					resource.TestCheckResourceAttr(journeyAddr, "method", "GET"),
					resource.TestCheckResourceAttr(journeyAddr, "interval_seconds", "300"),
					resource.TestCheckNoResourceAttr(journeyAddr, "journey_id"),
				),
			},
		},
	})
}

func TestAccJourneyCheckValidation(t *testing.T) {
	s := newMock(t)
	httpCheck := func(extra string) string {
		return fmt.Sprintf(`
resource "autter_synthetic_check" "signup" {
  repository_id = "repo-1"
  name          = "Plain"
  url           = "https://example.com/"
  %s
}
`, extra)
	}
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{Config: testConfig(s, journeyConfig(`method = "GET"`)), ExpectError: expectError(`method is for HTTP checks`)},
			{Config: testConfig(s, journeyConfig(`timeout_ms = 5000`)), ExpectError: expectError(`timeout_ms is for HTTP checks`)},
			{Config: testConfig(s, journeyConfig(`headers = { "X-Key" = "v" }`)), ExpectError: expectError(`headers is for HTTP checks`)},
			{Config: testConfig(s, journeyConfig(`body_contains = "ok"`)), ExpectError: expectError(`body_contains is for HTTP checks`)},
			{Config: testConfig(s, journeyConfig(`interval_seconds = 60`)), ExpectError: expectError(`Interval not allowed for this kind`)},
			{
				Config:      testConfig(s, strings.Replace(journeyConfig(""), `"https://app.example.com"`, `"https://app.example.com/signup?x=1"`, 1)),
				ExpectError: expectError(`url must be the site's address`),
			},
			{
				Config:      testConfig(s, strings.Replace(journeyConfig(""), `journey_id    = "wj_signup"`, "", 1)),
				ExpectError: expectError(`Missing journey_id`),
			},
			{Config: testConfig(s, httpCheck(`journey_id = "wj_signup"`)), ExpectError: expectError(`journey_id is for journey checks`)},
			{Config: testConfig(s, httpCheck(`interval_seconds = 86400`)), ExpectError: expectError(`Interval not allowed for this kind`)},
			{Config: testConfig(s, journeyConfig(`interval_seconds = 86400`))},
		},
	})
}
