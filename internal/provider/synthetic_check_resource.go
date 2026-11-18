package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

// checkKinds are the kinds of synthetic check this version knows. Only "http"
// exists today; the API has no kind field yet, so "http" is never sent and an
// answer without a kind is an http check. A later kind adds its own optional
// attributes and makes url optional, with a check per kind in validateCheck.
var checkKinds = []string{"http"}

// NewSyntheticCheckResource is the autter_synthetic_check resource.
func NewSyntheticCheckResource() resource.Resource {
	return &apiResource{spec: syntheticCheckSpec()}
}

func computedString(description string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: description}
}

func syntheticCheckSpec() *resourceSpec {
	attrs := map[string]schema.Attribute{
		"repository_id": repositoryIDAttribute(),
		"name":          nameAttribute("synthetic check"),
		"kind": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Default:       stringdefault.StaticString("http"),
			Description:   "The kind of check. Only `http` exists today (the default). Changing it replaces the check.",
			Validators:    []validator.String{stringvalidator.OneOf(checkKinds...)},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"url": schema.StringAttribute{
			Required:    true,
			Description: "An http or https address reachable from the internet. Autter stores it normalized (`https://example.com` as `https://example.com/`); that is not a change.",
			Validators:  []validator.String{stringvalidator.LengthBetween(1, 2000)},
		},
		"method": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString("GET"),
			Description: "`GET` (the default) or `HEAD`.",
			Validators:  []validator.String{stringvalidator.OneOf("GET", "HEAD")},
		},
		"headers": schema.MapAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Sensitive:   true,
			Description: "Request headers, at most 20. Autter stores them encrypted and never returns their values, so a value changed outside Terraform is not detected; " +
				"a header added or removed outside Terraform is. Set them from sensitive variables.",
			Validators: []validator.Map{mapvalidator.SizeAtMost(20), mapvalidator.KeysAre(stringvalidator.LengthBetween(1, 200))},
		},
		"expected_status": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(200),
			Description: "The HTTP status the check expects. Default 200.",
			Validators:  []validator.Int64{int64validator.Between(100, 599)},
		},
		"body_contains": schema.StringAttribute{
			Optional:    true,
			Description: "Text the response body must contain (GET only).",
			Validators:  []validator.String{stringvalidator.LengthBetween(1, 500)},
		},
		"timeout_ms": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(10000),
			Description: "Request timeout in milliseconds, 1000 to 30000. Default 10000.",
			Validators:  []validator.Int64{int64validator.Between(1000, 30000)},
		},
		"interval_seconds": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(300),
			Description: "How often the check runs: 60, 300, 900 or 3600 seconds. Default 300. The free plan runs checks every 300 seconds at the fastest.",
			Validators:  []validator.Int64{int64validator.OneOf(60, 300, 900, 3600)},
		},
		"latency_threshold_ms": schema.Int64Attribute{
			Optional:    true,
			Description: "A response slower than this many milliseconds counts as a failure.",
			Validators:  []validator.Int64{int64validator.Between(1, 30000)},
		},
		"failures_to_fire": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(2),
			Description: "Failures in a row before the check opens a Runtime issue, 1 to 10. Default 2.",
			Validators:  []validator.Int64{int64validator.Between(1, 10)},
		},
		"service": schema.StringAttribute{
			Optional:    true,
			Description: "The service the issue is filed under. Left out: the URL's host.",
			Validators:  []validator.String{stringvalidator.LengthBetween(1, 100)},
		},
		"environment": schema.StringAttribute{
			Optional: true, Computed: true, Default: stringdefault.StaticString("production"),
			Description: "The environment the issue is filed under. Default `production`.",
			Validators:  []validator.String{stringvalidator.LengthBetween(1, 64)},
		},
		"enabled": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(true),
			Description: "Whether the check runs. Default `true`.",
		},
		"id":                   idAttribute("synthetic check"),
		"header_names":         schema.ListAttribute{ElementType: types.StringType, Computed: true, Description: "The names of the stored headers."},
		"issue_id":             schema.StringAttribute{Computed: true, Description: "The Runtime issue the check opens while it fails.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"firing":               schema.BoolAttribute{Computed: true, Description: "The check has failed `failures_to_fire` times in a row and its issue is open. Changes with every run."},
		"last_ok":              schema.BoolAttribute{Computed: true, Description: "Whether the last run passed. Changes with every run."},
		"last_status":          schema.Int64Attribute{Computed: true, Description: "The HTTP status of the last run."},
		"last_duration_ms":     schema.Float64Attribute{Computed: true, Description: "How long the last run took, in milliseconds."},
		"last_error":           computedString("Why the last run failed."),
		"last_checked_at":      computedString("When the check last ran (ISO 8601)."),
		"next_due_at":          computedString("When the check runs next (ISO 8601)."),
		"consecutive_failures": schema.Int64Attribute{Computed: true, Description: "Failures in a row so far."},
	}
	createdAttributes("synthetic check", attrs)
	return &resourceSpec{
		typeName: "synthetic_check",
		kind:     client.SyntheticChecks,
		schema: schema.Schema{
			Description: "A synthetic check: Autter requests a URL on a schedule and opens a Runtime issue when it fails. Addressed by its name within the repository. " +
				"Synthetic checks must be turned on for the organization (Runtime → Checks).",
			Attributes: attrs,
		},
		fields: []string{"url", "method", "expected_status", "body_contains", "timeout_ms", "interval_seconds", "latency_threshold_ms",
			"failures_to_fire", "service", "environment", "enabled"},
		computed: []string{"id", "header_names", "issue_id", "firing", "last_ok", "last_status", "last_duration_ms", "last_error",
			"last_checked_at", "next_due_at", "consecutive_failures", "created_by", "created_at", "updated_at"},
		validate: validateCheck,
		extras:   checkExtras{},
	}
}

func validateCheck(_ context.Context, attrs map[string]attr.Value, diags *diag.Diagnostics) {
	method, ok := attrs["method"].(types.String)
	if ok && known(method) && !method.IsNull() && method.ValueString() == "HEAD" && isSet(attrs["body_contains"]) {
		diags.AddAttributeError(path.Root("body_contains"), "body_contains needs GET", "A HEAD request has no body to search. Use method = \"GET\" or remove body_contains.")
	}
}

// checkExtras handles the attributes of a check that are not plain API fields: headers and kind.
type checkExtras struct{}

// headerNamesField is the snapshot key for the sorted header names.
const headerNamesField = "header_names"

func (checkExtras) addToBody(ctx context.Context, attrs map[string]attr.Value, body map[string]any) {
	if headers, ok := toJSON(ctx, attrs["headers"]); ok {
		body["headers"] = headers
	}
	// kind is not sent while it is "http": the API has no such field yet and its input schema is strict.
	kind, _ := toJSON(ctx, attrs["kind"])
	if k, _ := kind.(string); k != "" && k != "http" {
		body["kind"] = k
	}
}

func answerKind(answer client.Resource) string {
	if kind, ok := answer["kind"].(string); ok && kind != "" {
		return kind
	}
	return "http"
}

func (checkExtras) checkAnswer(answer client.Resource) error {
	kind := answerKind(answer)
	for _, k := range checkKinds {
		if k == kind {
			return nil
		}
	}
	name, _ := answer["name"].(string)
	return fmt.Errorf("the synthetic check %q is a %q check, which this version of the Autter provider does not support. Upgrade the provider", name, kind)
}

func (checkExtras) snapshotFields(answer client.Resource, s snapshot) {
	s[headerNamesField] = canonicalJSON(sortedStrings(answer["header_names"]))
}

// read sets kind, and headers when their names changed outside Terraform (or
// on import): header values are never returned, so state then holds each
// stored name with an empty value, which differs from any configuration and
// makes the next apply send the configured headers.
func (checkExtras) read(answer client.Resource, attrs map[string]attr.Value, s snapshot, imported bool) error {
	attrs["kind"] = types.StringValue(answerKind(answer))
	names := sortedStrings(answer["header_names"])
	recorded := canonicalJSON(names)
	if !imported && s[headerNamesField] == recorded {
		return nil
	}
	s[headerNamesField] = recorded
	if len(names) == 0 {
		attrs["headers"] = types.MapNull(types.StringType)
		return nil
	}
	elements := map[string]attr.Value{}
	for _, name := range names {
		elements[name] = types.StringValue("")
	}
	headers, diags := types.MapValue(types.StringType, elements)
	if diags.HasError() {
		return fmt.Errorf("headers: %s", diags)
	}
	attrs["headers"] = headers
	return nil
}
