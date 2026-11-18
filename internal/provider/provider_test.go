package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
	"github.com/Autter-dev/terraform-provider-autter/internal/mockapi"
)

func TestMain(m *testing.M) {
	// Acceptance tests serve the provider as registry.terraform.io/autter-dev/autter,
	// the address the configurations' required_providers (and the export) name.
	if os.Getenv("TF_ACC_PROVIDER_NAMESPACE") == "" {
		_ = os.Setenv("TF_ACC_PROVIDER_NAMESPACE", "autter-dev")
	}
	os.Exit(m.Run())
}

var protoV6Providers = map[string]func() (tfprotov6.ProviderServer, error){
	"autter": providerserver.NewProtocol6WithError(New("test")()),
}

// newMock starts the mock API with the repositories acme/web (repo-1) and acme/api (repo-2).
func newMock(t *testing.T) *mockapi.Server {
	t.Helper()
	s := mockapi.New(t)
	s.AddRepository("repo-1", "acme/web")
	s.AddRepository("repo-2", "acme/api")
	return s
}

// providerBlock configures the provider against the mock.
func providerBlock(s *mockapi.Server) string {
	return fmt.Sprintf(`
provider "autter" {
  base_url  = %q
  api_token = %q
}
`, s.URL, mockapi.Token)
}

// testConfig is a configuration that requires the provider, configures it for the mock, then body.
func testConfig(s *mockapi.Server, body string) string {
	return `
terraform {
  required_providers {
    autter = {
      source = "Autter-dev/autter"
    }
  }
}
` + providerBlock(s) + body
}

// configure runs the provider's Configure with these attribute values.
func configure(t *testing.T, token, baseURL *string) *provider.ConfigureResponse {
	t.Helper()
	ctx := context.Background()
	p := New("1.2.3")()
	var schemaResp provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &schemaResp)
	value := func(s *string) tftypes.Value {
		if s == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *s)
	}
	raw := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"api_token": tftypes.String, "base_url": tftypes.String}},
		map[string]tftypes.Value{"api_token": value(token), "base_url": value(baseURL)})
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, provider.ConfigureRequest{TerraformVersion: "1.17.2", Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: raw}}, resp)
	return resp
}

func ptr(s string) *string { return &s }

func TestConfigureNeedsAToken(t *testing.T) {
	t.Setenv(EnvAPIToken, "")
	t.Setenv(EnvBaseURL, "")
	resp := configure(t, nil, nil)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "AUTTER_API_TOKEN") ||
		!strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "api:read and api:write") {
		t.Fatalf("diagnostics: %v", resp.Diagnostics)
	}
}

func TestConfigureReadsTheEnvironment(t *testing.T) {
	t.Setenv(EnvAPIToken, "autter_pat_env")
	t.Setenv(EnvBaseURL, "http://127.0.0.1:9999/api/v1/")
	resp := configure(t, nil, nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if _, ok := resp.ResourceData.(*client.Client); !ok {
		t.Fatalf("resource data %T", resp.ResourceData)
	}
	// The configuration wins over the environment.
	resp = configure(t, ptr("autter_pat_config"), ptr("http://api.example.com"))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "https") {
		t.Fatalf("http to a remote host should be refused: %v", resp.Diagnostics)
	}
}

func TestConfigureDefaultsToProduction(t *testing.T) {
	t.Setenv(EnvBaseURL, "")
	resp := configure(t, ptr("autter_pat_x"), nil)
	if resp.Diagnostics.HasError() || resp.DataSourceData == nil {
		t.Fatal(resp.Diagnostics)
	}
}

func TestProviderSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
	for _, name := range []string{"autter_alert_rule", "autter_synthetic_check", "autter_dashboard"} {
		if resp.ResourceSchemas[name] == nil {
			t.Errorf("missing resource %s", name)
		}
	}
	for _, name := range []string{"autter_repository", "autter_repositories"} {
		if resp.DataSourceSchemas[name] == nil {
			t.Errorf("missing data source %s", name)
		}
	}
}
