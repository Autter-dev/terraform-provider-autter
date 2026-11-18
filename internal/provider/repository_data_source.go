package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

type repositoryModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	FullName      types.String `tfsdk:"full_name"`
	DefaultBranch types.String `tfsdk:"default_branch"`
	Enabled       types.Bool   `tfsdk:"enabled"`
}

func repositoryModelOf(r client.Repository) repositoryModel {
	return repositoryModel{
		ID:            types.StringValue(r.ID),
		Name:          types.StringValue(r.Name),
		FullName:      types.StringValue(r.FullName),
		DefaultBranch: types.StringPointerValue(r.DefaultBranch),
		Enabled:       types.BoolValue(r.Enabled),
	}
}

// NewRepositoryDataSource is the autter_repository data source.
func NewRepositoryDataSource() datasource.DataSource { return &repositoryDataSource{} }

type repositoryDataSource struct{ client *client.Client }

var (
	_ datasource.DataSourceWithConfigure        = (*repositoryDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*repositoryDataSource)(nil)
)

func (d *repositoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository"
}

func (d *repositoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A repository connected to the token's Autter organization, found by its full name or its short name. Its `id` is what resources take as `repository_id`.",
		Attributes: map[string]schema.Attribute{
			"full_name":      schema.StringAttribute{Optional: true, Computed: true, Description: "`owner/repo`, matched without regard to case. Set this or `name`."},
			"name":           schema.StringAttribute{Optional: true, Computed: true, Description: "The short name, as the dashboard shows it. Set this or `full_name`; it must be unique in the organization."},
			"id":             schema.StringAttribute{Computed: true, Description: "The repository's id."},
			"default_branch": schema.StringAttribute{Computed: true, Description: "The default branch, when known."},
			"enabled":        schema.BoolAttribute{Computed: true, Description: "Whether Autter is enabled for the repository."},
		},
	}
}

func (d *repositoryDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("full_name"), path.MatchRoot("name")),
	}
}

func (d *repositoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *repositoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config repositoryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || d.client == nil {
		return
	}
	repositories, err := d.client.ListRepositories(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiagnostic("list the organization's repositories", err))
		return
	}
	var matches []client.Repository
	wanted := ""
	if !config.FullName.IsNull() {
		wanted = config.FullName.ValueString()
		for _, r := range repositories {
			if strings.EqualFold(r.FullName, wanted) {
				matches = append(matches, r)
			}
		}
	} else {
		wanted = config.Name.ValueString()
		for _, r := range repositories {
			if r.Name == wanted {
				matches = append(matches, r)
			}
		}
	}
	switch len(matches) {
	case 1:
		resp.Diagnostics.Append(resp.State.Set(ctx, repositoryModelOf(matches[0]))...)
	case 0:
		names := make([]string, 0, len(repositories))
		for i, r := range repositories {
			if i == 20 {
				names = append(names, fmt.Sprintf("and %d more", len(repositories)-20))
				break
			}
			names = append(names, r.FullName)
		}
		known := "The token's organization has no connected repositories."
		if len(names) > 0 {
			known = "Connected repositories: " + strings.Join(names, ", ") + "."
		}
		resp.Diagnostics.AddError("Repository not found",
			fmt.Sprintf("No repository %q is connected to the token's Autter organization. %s", wanted, known))
	default:
		candidates := make([]string, 0, len(matches))
		for _, r := range matches {
			candidates = append(candidates, r.FullName)
		}
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous repository name",
			fmt.Sprintf("%d repositories are named %q: %s. Use full_name instead.", len(matches), wanted, strings.Join(candidates, ", ")))
	}
}

// NewRepositoriesDataSource is the autter_repositories data source.
func NewRepositoriesDataSource() datasource.DataSource { return &repositoriesDataSource{} }

type repositoriesDataSource struct{ client *client.Client }

var _ datasource.DataSourceWithConfigure = (*repositoriesDataSource)(nil)

func (d *repositoriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repositories"
}

func (d *repositoriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Every repository connected to the token's Autter organization (up to 500), ordered by full name.",
		Attributes: map[string]schema.Attribute{
			"repositories": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The repositories.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":             schema.StringAttribute{Computed: true, Description: "The repository's id."},
						"name":           schema.StringAttribute{Computed: true, Description: "The short name."},
						"full_name":      schema.StringAttribute{Computed: true, Description: "`owner/repo`."},
						"default_branch": schema.StringAttribute{Computed: true, Description: "The default branch, when known."},
						"enabled":        schema.BoolAttribute{Computed: true, Description: "Whether Autter is enabled for the repository."},
					},
				},
			},
		},
	}
}

func (d *repositoriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *repositoriesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		return
	}
	repositories, err := d.client.ListRepositories(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiagnostic("list the organization's repositories", err))
		return
	}
	models := make([]repositoryModel, 0, len(repositories))
	for _, r := range repositories {
		models = append(models, repositoryModelOf(r))
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repositories"), models)...)
}
