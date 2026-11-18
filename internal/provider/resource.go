package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

// The three resources share one implementation: each is a whole-resource
// upsert by (repository, name) through the API, with the same state model
// (see snapshot.go). A resourceSpec says what differs.

// resourceSpec describes one resource type.
type resourceSpec struct {
	// typeName is the part after "autter_": "alert_rule".
	typeName string
	kind     client.Kind
	schema   schema.Schema
	// fields are the configurable top-level attributes other than repository_id
	// and name. Each is the API field of the same name.
	fields []string
	// computed are read-only attributes copied from every API answer, each the API field of the same name.
	computed []string
	// encode adjusts a field's JSON before it is sent; decode adjusts the API's JSON before it is converted.
	encode func(field string, value any) any
	decode func(field string, value any) any
	// validate checks the configuration beyond the schema's validators.
	validate func(ctx context.Context, attrs map[string]attr.Value, diags *diag.Diagnostics)
	// extras let a resource handle attributes that are not plain API fields (a check's headers and kind).
	extras resourceExtras
}

// resourceExtras are hooks for attributes that are not plain API fields.
type resourceExtras interface {
	// addToBody adds them to a request body.
	addToBody(ctx context.Context, attrs map[string]attr.Value, body map[string]any)
	// checkAnswer refuses an API answer this version cannot represent.
	checkAnswer(answer client.Resource) error
	// snapshotFields records what drift detection compares.
	snapshotFields(answer client.Resource, s snapshot)
	// read sets them in state from an answer; imported is true when there is no snapshot.
	read(answer client.Resource, attrs map[string]attr.Value, s snapshot, imported bool) error
}

type apiResource struct {
	spec   *resourceSpec
	client *client.Client
}

var (
	_ resource.Resource                   = (*apiResource)(nil)
	_ resource.ResourceWithConfigure      = (*apiResource)(nil)
	_ resource.ResourceWithImportState    = (*apiResource)(nil)
	_ resource.ResourceWithValidateConfig = (*apiResource)(nil)
)

func (r *apiResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.typeName
}

func (r *apiResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.spec.schema
}

func (r *apiResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *apiResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if r.spec.validate == nil {
		return
	}
	var config types.Object
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.spec.validate(ctx, config.Attributes(), &resp.Diagnostics)
}

func (r *apiResource) label(name string) string {
	return fmt.Sprintf("%s %q", r.spec.kind.Label(), name)
}

func (r *apiResource) attrType(name string) attr.Type {
	return r.spec.schema.Attributes[name].GetType()
}

// body is the PUT body for the planned attributes: every set field, nothing for null ones.
func (r *apiResource) body(ctx context.Context, attrs map[string]attr.Value) map[string]any {
	body := map[string]any{"name": stringOf(attrs["name"])}
	for _, field := range r.spec.fields {
		value, ok := toJSON(ctx, attrs[field])
		if !ok {
			continue
		}
		if r.spec.encode != nil {
			value = r.spec.encode(field, value)
		}
		body[field] = value
	}
	if r.spec.extras != nil {
		r.spec.extras.addToBody(ctx, attrs, body)
	}
	return body
}

func (r *apiResource) takeSnapshot(answer client.Resource) snapshot {
	s := takeSnapshot(answer, r.spec.fields)
	if r.spec.extras != nil {
		r.spec.extras.snapshotFields(answer, s)
	}
	return s
}

// setComputed copies the read-only attributes from an answer.
func (r *apiResource) setComputed(answer client.Resource, attrs map[string]attr.Value) error {
	for _, name := range r.spec.computed {
		value, err := fromJSON(answer[name], r.attrType(name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		attrs[name] = value
	}
	return nil
}

func (r *apiResource) checkAnswer(answer client.Resource) error {
	if r.spec.extras != nil {
		return r.spec.extras.checkAnswer(answer)
	}
	return nil
}

func copyAttrs(o types.Object) map[string]attr.Value {
	out := map[string]attr.Value{}
	for k, v := range o.Attributes() {
		out[k] = v
	}
	return out
}

func stringOf(v attr.Value) string {
	if s, ok := v.(types.String); ok {
		return s.ValueString()
	}
	return ""
}

// apply sends the planned resource and returns the new state.
func (r *apiResource) apply(ctx context.Context, plan types.Object, private privateState, create bool) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	attrs := copyAttrs(plan)
	repository, name := stringOf(attrs["repository_id"]), stringOf(attrs["name"])
	verb := "update"
	if create {
		verb = "create"
	}
	if r.client == nil {
		diags.AddError("Provider not configured", "The Autter provider was not configured. This is a bug in the provider.")
		return plan, diags
	}

	answer, created, err := r.client.PutResource(ctx, repository, r.spec.kind, name, r.body(ctx, attrs))
	if err != nil && !create && client.IsNotFound(err) {
		// "Deleted while it was being updated": the API asks to send the request again.
		answer, created, err = r.client.PutResource(ctx, repository, r.spec.kind, name, r.body(ctx, attrs))
	}
	if err != nil {
		diags.Append(apiDiagnostic(verb+" the "+r.label(name), err))
		return plan, diags
	}
	if err := r.checkAnswer(answer); err != nil {
		diags.AddError("Unsupported "+r.spec.kind.Label(), err.Error())
		return plan, diags
	}
	if create && !created {
		id, _ := answer["id"].(string)
		diags.AddWarning(fmt.Sprintf("Adopted an existing %s", r.spec.kind.Label()),
			fmt.Sprintf("An existing %s named %q (id %s) was adopted: Terraform now manages it, and its settings were replaced by the configuration. "+
				"The Autter API addresses resources by name, so a name already in use is adopted rather than copied.", r.spec.kind.Label(), name, id))
	}
	if err := r.setComputed(answer, attrs); err != nil {
		diags.AddError("Unexpected API answer", err.Error())
		return plan, diags
	}
	state, d := types.ObjectValue(plan.AttributeTypes(ctx), attrs)
	diags.Append(d...)
	if diags.HasError() {
		return plan, diags
	}
	diags.Append(saveSnapshot(ctx, private, r.takeSnapshot(answer))...)
	return state, diags
}

func (r *apiResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, diags := r.apply(ctx, plan, resp.Private, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *apiResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, diags := r.apply(ctx, plan, resp.Private, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *apiResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var current types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &current)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attrs := copyAttrs(current)
	repository, name := stringOf(attrs["repository_id"]), stringOf(attrs["name"])
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The Autter provider was not configured. This is a bug in the provider.")
		return
	}

	answer, err := r.client.FindResource(ctx, repository, r.spec.kind, name)
	switch {
	case client.IsNotFound(err):
		resp.State.RemoveResource(ctx)
		return
	case client.IsRepositoryNotFound(err):
		resp.Diagnostics.AddWarning("Repository no longer connected",
			fmt.Sprintf("The repository %q is no longer connected to the organization, so the %s is no longer managed. %s", repository, r.label(name), err))
		resp.State.RemoveResource(ctx)
		return
	case err != nil:
		resp.Diagnostics.Append(apiDiagnostic("read the "+r.label(name), err))
		return
	}
	if err := r.checkAnswer(answer); err != nil {
		resp.Diagnostics.AddError("Unsupported "+r.spec.kind.Label(), err.Error())
		return
	}

	recorded, diags := loadSnapshot(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	imported := recorded == nil
	if imported {
		recorded = snapshot{}
		if id, ok := answer["repository_id"].(string); ok && id != "" {
			attrs["repository_id"] = types.StringValue(id)
		}
	}
	for _, field := range r.spec.fields {
		if !imported && recorded.matches(field, answer[field]) {
			continue
		}
		raw := answer[field]
		if r.spec.decode != nil {
			raw = r.spec.decode(field, raw)
		}
		value, err := fromJSON(raw, r.attrType(field))
		if err != nil {
			resp.Diagnostics.AddError("Unexpected API answer", fmt.Sprintf("%s: %s", field, err))
			return
		}
		attrs[field] = value
		recorded[field] = canonicalJSON(answer[field])
	}
	if r.spec.extras != nil {
		if err := r.spec.extras.read(answer, attrs, recorded, imported); err != nil {
			resp.Diagnostics.AddError("Unexpected API answer", err.Error())
			return
		}
	}
	if err := r.setComputed(answer, attrs); err != nil {
		resp.Diagnostics.AddError("Unexpected API answer", err.Error())
		return
	}
	state, d := types.ObjectValue(current.AttributeTypes(ctx), attrs)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	resp.Diagnostics.Append(saveSnapshot(ctx, resp.Private, recorded)...)
}

func (r *apiResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var current types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &current)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attrs := current.Attributes()
	repository, name := stringOf(attrs["repository_id"]), stringOf(attrs["name"])
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The Autter provider was not configured. This is a bug in the provider.")
		return
	}
	err := r.client.DeleteResource(ctx, repository, r.spec.kind, name)
	if err != nil && !client.IsNotFound(err) && !client.IsRepositoryNotFound(err) {
		resp.Diagnostics.Append(apiDiagnostic("delete the "+r.label(name), err))
	}
}

func (r *apiResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, name, err := parseImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository_id"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// sortedStrings returns the strings of a JSON list, sorted.
func sortedStrings(raw any) []string {
	list, _ := raw.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
