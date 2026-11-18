package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Autter-dev/terraform-provider-autter/internal/mockapi"
)

const dashboardAddr = "autter_dashboard.ops"

// Widgets without ids, no layout, no field: Autter fills all three in.
const dashboardConfig = `
resource "autter_dashboard" "ops" {
  repository_id = "acme/web"
  name          = "Ops"
  variables = {
    range = "7d"
  }
  widgets = [
    {
      title   = "  Server errors  "
      viz     = "bar"
      source  = "errors"
      measure = "count"
      filters = [
        { field = "status_code", op = "gte", value = 500 },
        { field = "environment", op = "in", values = ["production", "staging"] },
      ]
    },
    {
      title   = "Latency"
      viz     = "line"
      source  = "spans"
      measure = "p95"
    },
    {
      title  = "Slowest"
      viz    = "table"
      source = "sql"
      sql    = "SELECT 1"
    },
  ]
}
`

func TestAccDashboardLifecycle(t *testing.T) {
	s := newMock(t)
	var before int
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				PreConfig: mark(s, &before),
				Config:    testConfig(s, dashboardConfig),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT created"),
					resource.TestCheckResourceAttr(dashboardAddr, "widgets.0.filters.0.value", "500"),
					resource.TestCheckResourceAttr(dashboardAddr, "widgets.0.filters.1.values.1", "staging"),
					resource.TestCheckNoResourceAttr(dashboardAddr, "widgets.0.id"),
					resource.TestCheckNoResourceAttr(dashboardAddr, "layout.0.id"),
					func(*terraform.State) error {
						d := s.Get("repo-1", mockapi.Dashboards, "Ops")
						widgets := d["widgets"].([]any)
						first := widgets[0].(map[string]any)
						if first["id"] != "w1" || first["title"] != "Server errors" {
							return fmt.Errorf("stored widget %v", first)
						}
						filters := first["filters"].([]any)
						if fmt.Sprintf("%v %T", filters[0].(map[string]any)["value"], filters[0].(map[string]any)["value"]) != "500 json.Number" ||
							fmt.Sprint(filters[1].(map[string]any)["value"]) != "[production staging]" {
							return fmt.Errorf("stored filters %v", filters)
						}
						if widgets[1].(map[string]any)["field"] != "duration_ms" || len(d["layout"].([]any)) != 3 {
							return fmt.Errorf("defaults not filled: %v", d)
						}
						return nil
					},
				),
			},
			{
				// Drift: someone resizes a widget. The layout is not configured, so state takes
				// the server's layout and the plan shows removing it; apply goes back to defaults.
				PreConfig: func() {
					s.Edit("repo-1", mockapi.Dashboards, "Ops", func(f map[string]any) {
						f["layout"].([]any)[0].(map[string]any)["w"] = 12
					})
					mark(s, &before)()
				},
				Config: testConfig(s, dashboardConfig),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(dashboardAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectWrites(s, &before, "PUT updated"),
					func(*terraform.State) error {
						w := s.Get("repo-1", mockapi.Dashboards, "Ops")["layout"].([]any)[0].(map[string]any)["w"]
						if fmt.Sprint(w) != "6" {
							return fmt.Errorf("layout not restored: %v", w)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccDashboardValidation(t *testing.T) {
	s := newMock(t)
	widgets := func(w string) string {
		return fmt.Sprintf(`
resource "autter_dashboard" "d" {
  repository_id = "repo-1"
  name          = "Bad"
  widgets       = [%s]
}
`, w)
	}
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Providers,
		Steps: []resource.TestStep{
			{
				Config:      testConfig(s, widgets(`{ title = "a", viz = "bar", source = "errors", measure = "count", filters = [{ field = "x", op = "eq" }] }`)),
				ExpectError: expectError(`Set value or values`),
			},
			{
				Config:      testConfig(s, widgets(`{ title = "a", viz = "bar", source = "errors", measure = "count", filters = [{ field = "x", op = "eq", values = ["a"] }] }`)),
				ExpectError: expectError(`values needs in or not_in`),
			},
			{
				Config:      testConfig(s, widgets(`{ id = "a", title = "a", viz = "bar", source = "errors", measure = "count" }, { title = "b", viz = "bar", source = "errors", measure = "count" }`)),
				ExpectError: expectError(`Give every widget an id or none`),
			},
			{
				Config:      testConfig(s, widgets(`{ title = "a", viz = "bar", source = "sql" }`)),
				ExpectError: expectError(`needs sql`),
			},
			{
				Config:      testConfig(s, widgets(`{ title = "a", viz = "bar", source = "logs" }`)),
				ExpectError: expectError(`needs a measure`),
			},
			{
				PreConfig:   func() { s.SetFeatureDisabled(mockapi.Dashboards, true) },
				Config:      testConfig(s, widgets(``)),
				ExpectError: expectError(`turned off for this organization`),
			},
			{
				// An empty dashboard is allowed.
				PreConfig: func() { s.SetFeatureDisabled(mockapi.Dashboards, false) },
				Config:    testConfig(s, widgets(``)),
			},
		},
	})
}
