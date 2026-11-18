package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Every example in examples/ (shown on the registry) applies against the mock
// API, and plans empty afterwards.
func TestAccExamples(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", "*", "*.tf"))
	if err != nil || len(examples) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	for _, file := range examples {
		if filepath.Base(file) == "import-by-string-id.tf" {
			continue
		}
		t.Run(filepath.Base(filepath.Dir(file)), func(t *testing.T) {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			s := newMock(t)
			resource.ParallelTest(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6Providers,
				Steps: []resource.TestStep{{
					Config:          testConfig(s, string(body)),
					ConfigVariables: config.Variables{"status_api_key": config.StringVariable("example")},
				}},
			})
		})
	}
}
