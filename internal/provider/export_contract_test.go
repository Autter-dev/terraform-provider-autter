package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/Autter-dev/terraform-provider-autter/internal/mockapi"
)

// The contract with the monorepo's "Export as Terraform": testdata/export
// holds the HCL the export writes (*.tf) and the API resources it was written
// from (*.json), copied by scripts/sync-from-monorepo.sh. Each file is used
// verbatim as configuration:
//
//   - imported against the same resource, the plan is an import with no
//     change (a check: one in-place update, because its header values cannot
//     be read back);
//   - applied to an empty organization, it creates the resource, and the plan
//     afterwards is empty.

type exportFixture struct {
	kind     string // the API's path segment
	file     string // testdata/export/<file>.tf and .json
	resource map[string]any
	hcl      string
	address  string
	vars     config.Variables
}

var (
	resourceBlock = regexp.MustCompile(`(?m)^resource "(autter_\w+)" "(\w+)"`)
	variableBlock = regexp.MustCompile(`(?m)^variable "(\w+)"`)
)

func loadExportFixture(t *testing.T, file, kind string) exportFixture {
	t.Helper()
	hcl, err := os.ReadFile(filepath.Join("testdata", "export", file+".tf"))
	if err != nil {
		t.Fatalf("%v (run scripts/sync-from-monorepo.sh)", err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "export", file+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var resource map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&resource); err != nil {
		t.Fatal(err)
	}
	blocks := resourceBlock.FindAllStringSubmatch(string(hcl), -1)
	if len(blocks) != 1 {
		t.Fatalf("%s.tf: want one resource block, found %d", file, len(blocks))
	}
	vars := config.Variables{}
	for _, v := range variableBlock.FindAllStringSubmatch(string(hcl), -1) {
		vars[v[1]] = config.StringVariable("value of " + v[1])
	}
	return exportFixture{kind: kind, file: file, resource: resource, hcl: string(hcl), address: blocks[0][1] + "." + blocks[0][2], vars: vars}
}

var exportFixtures = []struct{ file, kind string }{
	{"alert_rule", mockapi.AlertRules},
	{"synthetic_check", mockapi.SyntheticChecks},
	{"dashboard", mockapi.Dashboards},
}

func TestAccExportContractImport(t *testing.T) {
	for _, f := range exportFixtures {
		t.Run(f.file, func(t *testing.T) {
			fixture := loadExportFixture(t, f.file, f.kind)
			s := newMock(t)
			s.Seed("repo-1", fixture.kind, fixture.resource)
			name := fixture.resource["name"].(string)

			action, writes := plancheck.ResourceActionNoop, []string{}
			if fixture.kind == mockapi.SyntheticChecks {
				// Header values are never returned: the import plans one update that sends them.
				action, writes = plancheck.ResourceActionUpdate, []string{"PUT updated"}
			}
			before := 0
			config := fixture.hcl + providerBlock(s) + fmt.Sprintf("\nimport {\n  to = %s\n  id = %q\n}\n", fixture.address, "acme/web:"+name)
			resource.ParallelTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6Providers,
				Steps: []resource.TestStep{{
					Config:          config,
					ConfigVariables: fixture.vars,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(fixture.address, action)},
					},
					Check: expectWrites(s, &before, writes...),
				}},
			})
		})
	}
}

func TestAccExportContractApply(t *testing.T) {
	for _, f := range exportFixtures {
		t.Run(f.file, func(t *testing.T) {
			fixture := loadExportFixture(t, f.file, f.kind)
			s := newMock(t)
			before := 0
			resource.ParallelTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6Providers,
				Steps: []resource.TestStep{{
					Config:          fixture.hcl + providerBlock(s),
					ConfigVariables: fixture.vars,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(fixture.address, plancheck.ResourceActionCreate)},
					},
					Check: expectWrites(s, &before, "PUT created"),
				}},
			})
		})
	}
}
