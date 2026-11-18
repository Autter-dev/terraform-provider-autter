package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Autter-dev/terraform-provider-autter/internal/mockapi"
)

const ruleAddr = "autter_alert_rule.timeouts"

// A rule written the way people write it, not the way the API stores it:
// upper-case triggers, slack = {}, an email action with only the assignee.
func ruleConfig(enabled string) string {
	return fmt.Sprintf(`
data "autter_repository" "web" {
  full_name = "ACME/Web"
}

resource "autter_alert_rule" "timeouts" {
  repository_id = data.autter_repository.web.id
  name          = "Timeouts"
  enabled       = %s
  triggers      = ["NEW", "regressed"]
  filters = {
    services = ["checkout"]
    message = {
      op    = "contains"
      value = "Timeout"
    }
    threshold = {
      min_events = 5
    }
  }
  actions = {
    slack = {}
    email = {
      assignee = true
    }
  }
}
`, enabled)
}

// writesSince checks the mock logged exactly these write outcomes since the step began.
func expectWrites(s *mockapi.Server, before *int, outcomes ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		writes := s.Writes()[*before:]
		got := make([]string, 0, len(writes))
		for _, w := range writes {
			got = append(got, w.Method+" "+w.Outcome)
		}
		if fmt.Sprint(got) != fmt.Sprint(outcomes) {
			return fmt.Errorf("writes: got %v, want %v", got, outcomes)
		}
		return nil
	}
}

// expectError matches text in Terraform's output, where long lines are wrapped.
func expectError(text string) *regexp.Regexp {
	parts := strings.Fields(text)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(parts, `\s+`))
}

func mark(s *mockapi.Server, before *int) func() {
	return func() { *before = len(s.Writes()) }
}

func TestAccAlertRuleLifecycle(t *testing.T) {
	s := newMock(t)
	var before int
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				// Create: one PUT; the server's normalization never shows as a change (the
				// framework plans again after apply and fails on a non-empty plan).
				PreConfig: mark(s, &before),
				Config:    testConfig(s, ruleConfig("true")),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT created"),
					resource.TestCheckResourceAttr(ruleAddr, "repository_id", "repo-1"),
					resource.TestCheckResourceAttr(ruleAddr, "triggers.0", "NEW"),
					resource.TestCheckResourceAttrSet(ruleAddr, "id"),
					resource.TestCheckResourceAttr(ruleAddr, "is_default", "false"),
					resource.TestCheckResourceAttrSet(ruleAddr, "updated_at"),
					func(*terraform.State) error {
						stored := s.Get("repo-1", mockapi.AlertRules, "Timeouts")
						if fmt.Sprint(stored["triggers"]) != "[new regressed]" {
							return fmt.Errorf("stored triggers %v", stored["triggers"])
						}
						return nil
					},
				),
			},
			{
				// Apply again: nothing is sent.
				PreConfig: mark(s, &before),
				Config:    testConfig(s, ruleConfig("true")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: expectWrites(s, &before),
			},
			{
				// Change one field: one PUT, updated.
				PreConfig: mark(s, &before),
				Config:    testConfig(s, ruleConfig("false")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(ruleAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT updated"),
					resource.TestCheckResourceAttr(ruleAddr, "enabled", "false"),
				),
			},
			{
				// Drift: someone turns the rule on in the dashboard. The plan shows it; apply turns it off again.
				PreConfig: func() {
					s.Edit("repo-1", mockapi.AlertRules, "Timeouts", func(f map[string]any) { f["enabled"] = true })
					mark(s, &before)()
				},
				Config: testConfig(s, ruleConfig("false")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(ruleAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT updated"),
					func(*terraform.State) error {
						if s.Get("repo-1", mockapi.AlertRules, "Timeouts")["enabled"] != false {
							return fmt.Errorf("apply did not restore enabled")
						}
						return nil
					},
				),
			},
			{
				// Drift in a nested field only.
				PreConfig: func() {
					s.Edit("repo-1", mockapi.AlertRules, "Timeouts", func(f map[string]any) {
						f["filters"].(map[string]any)["services"] = []any{"payments"}
					})
					mark(s, &before)()
				},
				Config: testConfig(s, ruleConfig("false")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(ruleAddr, plancheck.ResourceActionUpdate)},
				},
				Check: expectWrites(s, &before, "PUT updated"),
			},
			{
				// Deleted in the dashboard: the plan creates it again.
				PreConfig: func() {
					s.Remove("repo-1", mockapi.AlertRules, "Timeouts")
					mark(s, &before)()
				},
				Config: testConfig(s, ruleConfig("false")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(ruleAddr, plancheck.ResourceActionCreate)},
				},
				Check: expectWrites(s, &before, "PUT created"),
			},
			{
				// A rename is a replacement.
				PreConfig: mark(s, &before),
				Config:    testConfig(s, regexp.MustCompile(`"Timeouts"`).ReplaceAllString(ruleConfig("false"), `"Timeouts renamed"`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(ruleAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: expectWrites(s, &before, "DELETE deleted", "PUT created"),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if n := s.Count("repo-1", mockapi.AlertRules); n != 0 {
				return fmt.Errorf("%d rules left after destroy", n)
			}
			return nil
		},
	})
}

// The rule as "Export as Terraform" writes it: what the API returns, pruned.
const exportedRule = `
data "autter_repository" "web" {
  full_name = "acme/web"
}

resource "autter_alert_rule" "imported" {
  repository_id = data.autter_repository.web.id
  name          = "Checkout: errors"
  enabled       = true
  triggers      = ["new"]
  filters = {
    services = ["checkout"]
    assignee = "any"
  }
  actions = {
    slack            = {}
    oncall           = true
    auto_assignments = false
  }
}
`

func TestAccAlertRuleImport(t *testing.T) {
	s := newMock(t)
	seed := func() {
		s.Seed("repo-1", mockapi.AlertRules, map[string]any{
			"name": "Checkout: errors", "enabled": true, "triggers": []any{"new"},
			"filters": map[string]any{"environments": []any{}, "services": []any{"checkout"}, "levels": []any{}, "priorities": []any{},
				"error_type": nil, "message": nil, "assignee": "any", "threshold": nil},
			"actions":    map[string]any{"slack": map[string]any{"channel_id": nil}, "email": nil, "in_app": nil, "oncall": true, "auto_assignments": false},
			"is_default": false, "created_by": nil,
		})
	}
	var before int
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				// Adopt: the name exists, so create adopts it (with a warning) and changes nothing.
				PreConfig: func() { seed(); mark(s, &before)() },
				Config:    testConfig(s, exportedRule),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT unchanged"),
					func(*terraform.State) error {
						if n := s.Count("repo-1", mockapi.AlertRules); n != 1 {
							return fmt.Errorf("%d rules, want the adopted one only", n)
						}
						return nil
					},
				),
			},
			{
				// terraform import by owner/repo: the state matches the applied one.
				ResourceName:      "autter_alert_rule.imported",
				ImportState:       true,
				ImportStateId:     "acme/web:Checkout: errors",
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"updated_at",
				},
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// An import block by repository id plans no change against the exported configuration.
				ResourceName:    "autter_alert_rule.imported",
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
				ImportStateId:   "repo-1:Checkout: errors",
				Config:          testConfig(s, exportedRule),
			},
		},
	})
}

func TestAccAlertRuleErrors(t *testing.T) {
	s := newMock(t)
	s.Seed("repo-1", mockapi.AlertRules, map[string]any{"id": "iar_a", "name": "Twin", "triggers": []any{"new"}})
	s.Seed("repo-1", mockapi.AlertRules, map[string]any{"id": "iar_b", "name": "Twin", "triggers": []any{"new"}})
	rule := func(name string) string {
		return fmt.Sprintf(`
resource "autter_alert_rule" "r" {
  repository_id = "acme/web"
  name          = %q
  triggers      = ["new"]
  actions       = { oncall = true }
}
`, name)
	}
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{Config: testConfig(s, rule("Twin")), ExpectError: expectError(`iar_a, iar_b. Rename all but one`)},
			{Config: testConfig(s, rule(" padded")), ExpectError: expectError(`leading or trailing spaces`)},
			{Config: testConfig(s, rule("..")), ExpectError: expectError(`cannot be used as names`)},
			{
				Config:      testConfig(s, `resource "autter_alert_rule" "r" {`+"\n"+`repository_id = "repo-1"`+"\n"+`name = "None"`+"\n"+`triggers = ["new"]`+"\n"+`actions = { oncall = false }`+"\n}"),
				ExpectError: expectError(`Choose at least one action`),
			},
			{Config: testConfig(s, `resource "autter_alert_rule" "r" {`+"\n"+`repository_id = "repo-1"`+"\n"+`name = "Bad"`+"\n"+`triggers = ["sometimes"]`+"\n"+`actions = { oncall = true }`+"\n}"), ExpectError: expectError(`value must be one of`)},
			{
				PreConfig:   func() { s.SetRole("member") },
				Config:      testConfig(s, rule("Members cannot")),
				ExpectError: expectError(`Only an owner or admin`),
			},
			{
				PreConfig: func() { s.SetRole("admin"); s.FailNext(429, 2) },
				Config:    testConfig(s, rule("After a rate limit")),
				Check:     resource.TestCheckResourceAttrSet("autter_alert_rule.r", "id"),
			},
			{
				PreConfig: func() { s.FailNext(503, 1) },
				Config:    testConfig(s, rule("After a 503")),
			},
		},
	})
}

func TestAccBadToken(t *testing.T) {
	s := newMock(t)
	s.AddToken("mcp-only", []string{"mcp:read", "mcp:write"})
	config := func(token string) string {
		return fmt.Sprintf(`
terraform {
  required_providers {
    autter = { source = "Autter-dev/autter" }
  }
}
provider "autter" {
  base_url  = %q
  api_token = %q
}
data "autter_repositories" "all" {}
`, s.URL, token)
	}
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{Config: config("autter_pat_wrong"), ExpectError: expectError(`Autter rejected the API token`)},
			{Config: config("mcp-only"), ExpectError: expectError(`lacks the api:read / api:write scope`)},
		},
	})
}
