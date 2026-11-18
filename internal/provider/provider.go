// Package provider implements the Autter Terraform provider: alert rules,
// synthetic checks and dashboards, managed through the Autter API (/api/v1).
package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

// Environment variables the provider reads when its configuration leaves a value out.
const (
	EnvAPIToken = "AUTTER_API_TOKEN"
	EnvBaseURL  = "AUTTER_BASE_URL"
)

var _ provider.Provider = (*autterProvider)(nil)

type autterProvider struct {
	version string
}

type providerModel struct {
	APIToken types.String `tfsdk:"api_token"`
	BaseURL  types.String `tfsdk:"base_url"`
}

// New returns the provider factory for this version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &autterProvider{version: version} }
}

func (p *autterProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "autter"
	resp.Version = p.version
}

func (p *autterProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Autter alert rules, synthetic checks and dashboards as code, through the Autter API.",
		Attributes: map[string]schema.Attribute{
			"api_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "A personal access token (Settings → Access Tokens in the Autter dashboard) with the api:read and api:write scopes, " +
					"or one without scope restrictions. Defaults to the AUTTER_API_TOKEN environment variable.",
			},
			"base_url": schema.StringAttribute{
				Optional:    true,
				Description: "The Autter API host. Defaults to the AUTTER_BASE_URL environment variable, then https://api.autter.dev. /api/v1 is appended.",
			},
		},
	}
}

func (p *autterProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.APIToken.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Unknown API token",
			"The api_token is not known until apply. Set it from a value known at plan time, or set AUTTER_API_TOKEN.")
	}
	if config.BaseURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Unknown base URL",
			"The base_url is not known until apply. Set it from a value known at plan time, or set AUTTER_BASE_URL.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	token := strings.TrimSpace(config.APIToken.ValueString())
	if token == "" {
		token = strings.TrimSpace(os.Getenv(EnvAPIToken))
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "No Autter API token",
			"Set api_token or AUTTER_API_TOKEN to a personal access token with the api:read and api:write scopes. "+
				"Create one in the Autter dashboard under Settings → Access Tokens.")
		return
	}

	baseURL := config.BaseURL.ValueString()
	if strings.TrimSpace(baseURL) == "" {
		baseURL = os.Getenv(EnvBaseURL)
	}
	baseURL = client.NormalizeBaseURL(baseURL)
	if err := client.ValidateBaseURL(baseURL); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Invalid base URL", err.Error())
		return
	}

	c := client.New(client.Config{
		BaseURL:   baseURL,
		Token:     token,
		UserAgent: fmt.Sprintf("terraform-provider-autter/%s (terraform/%s)", p.version, req.TerraformVersion),
	})
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *autterProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertRuleResource,
		NewSyntheticCheckResource,
		NewDashboardResource,
	}
}

func (p *autterProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewRepositoryDataSource,
		NewRepositoriesDataSource,
	}
}

// clientFrom returns the configured client, or adds an error.
func clientFrom(data any, diags interface {
	AddError(summary, detail string)
}) *client.Client {
	if data == nil {
		return nil // Not configured yet (validation); the framework calls Configure again.
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", data))
		return nil
	}
	return c
}
