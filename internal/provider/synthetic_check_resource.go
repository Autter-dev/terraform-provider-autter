package provider

import (
	"context"
	"fmt"
	"net/url"
	"slices"

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

// checkKinds are the kinds of synthetic check this version knows. "http" is
// never sent (an API from before journey checks has no kind field, and its
// input schema is strict), and an answer without a kind is an http check. Any
// other kind in an answer fails with "upgrade the provider".
var checkKinds = []string{"http", "journey"}

// Intervals per kind: a journey check starts a browser on each run, so the fastest is 15 minutes.
var (
	httpIntervals    = []int64{60, 300, 900, 3600}
	journeyIntervals = []int64{900, 3600, 21600, 86400}
)

// httpOnlyAttributes apply to http checks only. A journey check never sends
// them, they are null in its state, and the API refuses them.
var httpOnlyAttributes = []string{"method", "headers", "expected_status", "body_contains", "timeout_ms", "latency_threshold_ms"}

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
			Description:   "`http` (the default) requests `url`; `journey` replays a saved journey of the repository (the Journeys tab) in a browser against `url`. A check's kind is fixed: changing it replaces the check.",
			Validators:    []validator.String{stringvalidator.OneOf(checkKinds...)},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"url": schema.StringAttribute{
			Required: true,
			Description: "An http or https address reachable from the internet. For a journey check, the site's address only (`https://app.example.com`, no path, query or fragment); the journey opens its own pages on it. " +
				"Autter stores it normalized (`https://example.com` as `https://example.com/`); that is not a change.",
			Validators: []validator.String{stringvalidator.LengthBetween(1, 2000)},
		},
		"journey_id": schema.StringAttribute{
			Optional:    true,
			Description: "Journey checks: the id of the saved journey to replay. Required when `kind` is `journey`; not allowed otherwise. Only signed-out journeys that write nothing can be checked.",
			Validators:  []validator.String{stringvalidator.LengthBetween(1, 200)},
		},
		"method": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "HTTP checks: `GET` (the default) or `HEAD`. Null for a journey check.",
			Validators:    []validator.String{stringvalidator.OneOf("GET", "HEAD")},
			PlanModifiers: []planmodifier.String{kindDefaultString("GET")},
		},
		"headers": schema.MapAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Sensitive:   true,
			Description: "HTTP checks: request headers, at most 20. Autter stores them encrypted and never returns their values, so a value changed outside Terraform is not detected; " +
				"a header added or removed outside Terraform is. Set them from sensitive variables.",
			Validators: []validator.Map{mapvalidator.SizeAtMost(20), mapvalidator.KeysAre(stringvalidator.LengthBetween(1, 200))},
		},
		"expected_status": schema.Int64Attribute{
			Optional: true, Computed: true,
			Description:   "HTTP checks: the status the check expects. Default 200. Null for a journey check.",
			Validators:    []validator.Int64{int64validator.Between(100, 599)},
			PlanModifiers: []planmodifier.Int64{kindDefaultInt64(200, 0)},
		},
		"body_contains": schema.StringAttribute{
			Optional:    true,
			Description: "HTTP checks: text the response body must contain (GET only).",
			Validators:  []validator.String{stringvalidator.LengthBetween(1, 500)},
		},
		"timeout_ms": schema.Int64Attribute{
			Optional: true, Computed: true,
			Description:   "HTTP checks: request timeout in milliseconds, 1000 to 30000. Default 10000. Null for a journey check.",
			Validators:    []validator.Int64{int64validator.Between(1000, 30000)},
			PlanModifiers: []planmodifier.Int64{kindDefaultInt64(10000, 0)},
		},
		"interval_seconds": schema.Int64Attribute{
			Optional: true, Computed: true,
			Description: "How often the check runs, in seconds. HTTP checks: 60, 300, 900 or 3600, default 300 (the free plan: 300 at the fastest). " +
				"Journey checks: 900, 3600, 21600 or 86400, default 3600 (the free plan: 3600 at the fastest).",
			Validators:    []validator.Int64{int64validator.OneOf(60, 300, 900, 3600, 21600, 86400)},
			PlanModifiers: []planmodifier.Int64{kindDefaultInt64(300, 3600)},
		},
		"latency_threshold_ms": schema.Int64Attribute{
			Optional:    true,
			Description: "HTTP checks: a response slower than this many milliseconds counts as a failure.",
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
		"first_passed_at":      computedString("Journey checks: when the check first passed against its url (ISO 8601); it opens no Runtime issue before that. Null for HTTP checks."),
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
		fields: []string{"url", "journey_id", "method", "expected_status", "body_contains", "timeout_ms", "interval_seconds", "latency_threshold_ms",
			"failures_to_fire", "service", "environment", "enabled"},
		computed: []string{"id", "header_names", "issue_id", "firing", "last_ok", "last_status", "last_duration_ms", "last_error",
			"last_checked_at", "next_due_at", "consecutive_failures", "first_passed_at", "created_by", "created_at", "updated_at"},
		validate: validateCheck,
		extras:   checkExtras{},
	}
}

func validateCheck(_ context.Context, attrs map[string]attr.Value, diags *diag.Diagnostics) {
	kindValue, _ := attrs["kind"].(types.String)
	if !known(kindValue) {
		return // Decided at apply time.
	}
	kind := "http"
	if !kindValue.IsNull() {
		kind = kindValue.ValueString()
	}

	if kind == "journey" {
		for _, name := range httpOnlyAttributes {
			if isSet(attrs[name]) {
				diags.AddAttributeError(path.Root(name), name+" is for HTTP checks",
					fmt.Sprintf("A journey check replays a journey in a browser and takes no %s. Remove it, or use kind = \"http\".", name))
			}
		}
		if attrs["journey_id"] != nil && attrs["journey_id"].IsNull() {
			diags.AddAttributeError(path.Root("journey_id"), "Missing journey_id", "A journey check needs journey_id: the saved journey (repository → Journeys) to replay.")
		}
		if u, ok := attrs["url"].(types.String); ok && known(u) && !u.IsNull() {
			if parsed, err := url.Parse(u.ValueString()); err == nil && parsed.Host != "" &&
				((parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery) {
				diags.AddAttributeError(path.Root("url"), "url must be the site's address",
					fmt.Sprintf("For a journey check, give only the site's address (%s://%s), without a path, query or fragment: the journey opens its own pages on it.", parsed.Scheme, parsed.Host))
			}
		}
	} else if isSet(attrs["journey_id"]) {
		diags.AddAttributeError(path.Root("journey_id"), "journey_id is for journey checks", `Only a journey check replays a journey. Set kind = "journey", or remove journey_id.`)
	}

	if interval, ok := attrs["interval_seconds"].(types.Int64); ok && isSet(interval) {
		allowed := httpIntervals
		if kind == "journey" {
			allowed = journeyIntervals
		}
		if !slices.Contains(allowed, interval.ValueInt64()) {
			diags.AddAttributeError(path.Root("interval_seconds"), "Interval not allowed for this kind",
				fmt.Sprintf("A %s check runs every %v seconds; %d is not one of them.", kind, allowed, interval.ValueInt64()))
		}
	}

	method, ok := attrs["method"].(types.String)
	if ok && known(method) && !method.IsNull() && method.ValueString() == "HEAD" && isSet(attrs["body_contains"]) {
		diags.AddAttributeError(path.Root("body_contains"), "body_contains needs GET", "A HEAD request has no body to search. Use method = \"GET\" or remove body_contains.")
	}
}

// plannedKind is the kind in the plan: "http" when null, "" when unknown.
func plannedKind(ctx context.Context, plan interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}, diags *diag.Diagnostics) string {
	var kind types.String
	diags.Append(plan.GetAttribute(ctx, path.Root("kind"), &kind)...)
	if kind.IsUnknown() {
		return ""
	}
	if kind.IsNull() {
		return "http"
	}
	return kind.ValueString()
}

// kindDefaultString plans httpDefault for an unset attribute of an http check, and null for a journey check.
type kindDefaultString string

func (m kindDefaultString) Description(context.Context) string {
	return fmt.Sprintf("Defaults to %q for an http check, null for a journey check.", string(m))
}
func (m kindDefaultString) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m kindDefaultString) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	switch plannedKind(ctx, req.Plan, &resp.Diagnostics) {
	case "":
		resp.PlanValue = types.StringUnknown()
	case "journey":
		resp.PlanValue = types.StringNull()
	default:
		resp.PlanValue = types.StringValue(string(m))
	}
}

// kindDefaultInt64 plans one default for an unset attribute of an http check and another for a
// journey check; a journey default of 0 means null (the attribute is for HTTP checks only).
type kindDefaultInt64Modifier struct{ http, journey int64 }

func kindDefaultInt64(http, journey int64) planmodifier.Int64 {
	return kindDefaultInt64Modifier{http: http, journey: journey}
}

func (m kindDefaultInt64Modifier) Description(context.Context) string {
	if m.journey == 0 {
		return fmt.Sprintf("Defaults to %d for an http check, null for a journey check.", m.http)
	}
	return fmt.Sprintf("Defaults to %d for an http check, %d for a journey check.", m.http, m.journey)
}
func (m kindDefaultInt64Modifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m kindDefaultInt64Modifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.ConfigValue.IsNull() {
		return
	}
	switch plannedKind(ctx, req.Plan, &resp.Diagnostics) {
	case "":
		resp.PlanValue = types.Int64Unknown()
	case "journey":
		if m.journey == 0 {
			resp.PlanValue = types.Int64Null()
		} else {
			resp.PlanValue = types.Int64Value(m.journey)
		}
	default:
		resp.PlanValue = types.Int64Value(m.http)
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

// view hides the HTTP-only fields of a journey check's answer: the API stores
// their defaults (method GET, …), but a journey check has none, and its state
// holds null.
func (checkExtras) view(answer client.Resource) client.Resource {
	if answerKind(answer) != "journey" {
		return answer
	}
	out := client.Resource{}
	for k, v := range answer {
		out[k] = v
	}
	for _, name := range httpOnlyAttributes {
		out[name] = nil
	}
	return out
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
