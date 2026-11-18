package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// nameValidator checks a resource name the way the API addresses it: the path
// name is trimmed, and "." and ".." cannot be sent as a path segment.
type nameValidator struct{}

func (nameValidator) Description(context.Context) string {
	return "1 to 120 characters, without leading or trailing spaces, not . or .."
}

func (v nameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (nameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	name := req.ConfigValue.ValueString()
	switch {
	case strings.TrimSpace(name) != name:
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name", "The name has leading or trailing spaces. Autter trims them, so the resource could not be found again by this name.")
	case name == "." || name == "..":
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name", `"." and ".." cannot be used as names: they cannot be sent in a URL path.`)
	}
}

// Attributes every resource has.

func repositoryIDAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Required: true,
		Description: "The repository: its id, usually `data.autter_repository.<label>.id`. Its full name (`owner/repo`) also works, " +
			"but after an import the id is stored, so use the id to keep plans clean. Changing it replaces the resource.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
	}
}

func nameAttribute(kind string) schema.StringAttribute {
	return schema.StringAttribute{
		Required: true,
		Description: "The " + kind + "'s name, unique in the repository: the API addresses it by name. " +
			"Changing it replaces the resource (a rename is a delete and a create). If a " + kind + " with this name already exists, it is adopted.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators:    []validator.String{stringvalidator.LengthBetween(1, 120), nameValidator{}},
	}
}

func idAttribute(kind string) schema.StringAttribute {
	return schema.StringAttribute{
		Computed:      true,
		Description:   "The " + kind + "'s id in Autter. Informational: the provider addresses it by repository and name.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func createdAttributes(kind string, attrs map[string]schema.Attribute) {
	attrs["created_by"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The id of the member who created the " + kind + "; null for one Autter created.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["created_at"] = schema.StringAttribute{
		Computed:      true,
		Description:   "When the " + kind + " was created (ISO 8601).",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["updated_at"] = schema.StringAttribute{
		Computed:    true,
		Description: "When the " + kind + " was last changed (ISO 8601). It does not move when an apply changes nothing.",
	}
}

// Helpers for reading configuration values in ValidateConfig.

// object returns an object's attributes, or nil when it is null or unknown.
func object(v attr.Value) map[string]attr.Value {
	o, ok := v.(types.Object)
	if !ok || o.IsNull() || o.IsUnknown() {
		return nil
	}
	return o.Attributes()
}

// list returns a list's elements, or nil when it is null or unknown.
func list(v attr.Value) ([]attr.Value, bool) {
	l, ok := v.(types.List)
	if !ok || l.IsNull() || l.IsUnknown() {
		return nil, ok && l.IsNull()
	}
	return l.Elements(), true
}

func known(v attr.Value) bool { return v != nil && !v.IsUnknown() }

func isSet(v attr.Value) bool { return v != nil && !v.IsNull() && !v.IsUnknown() }

func boolTrue(v attr.Value) bool {
	b, ok := v.(types.Bool)
	return ok && !b.IsNull() && !b.IsUnknown() && b.ValueBool()
}
