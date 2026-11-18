package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

// Enums of the API (lib/runtime-dashboard-query).
var (
	dashboardRanges   = []string{"1h", "6h", "24h", "7d", "30d"}
	dashboardViz      = []string{"line", "bar", "stat", "table"}
	dashboardSources  = []string{"logs", "spans", "errors", "metrics", "llm", "sql"}
	dashboardMeasures = []string{"count", "rate", "p50", "p95", "p99", "sum"}
	dashboardFilterOp = []string{"eq", "neq", "contains", "not_contains", "starts_with", "in", "not_in", "gt", "gte", "lt", "lte"}
	widgetIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// NewDashboardResource is the autter_dashboard resource.
func NewDashboardResource() resource.Resource {
	return &apiResource{spec: dashboardSpec()}
}

func dashboardSpec() *resourceSpec {
	attrs := map[string]schema.Attribute{
		"repository_id": repositoryIDAttribute(),
		"name":          nameAttribute("dashboard"),
		"variables": schema.SingleNestedAttribute{
			Optional:    true,
			Description: "The dashboard's variables, applied to every widget.",
			Attributes: map[string]schema.Attribute{
				"service":     schema.StringAttribute{Optional: true, Description: "Only this service.", Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
				"environment": schema.StringAttribute{Optional: true, Description: "Only this environment; `all` for every environment. Left out: every environment except test ones.", Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
				"range":       schema.StringAttribute{Optional: true, Description: "The time range: `1h`, `6h`, `24h` (the API's default), `7d` or `30d`.", Validators: []validator.String{stringvalidator.OneOf(dashboardRanges...)}},
			},
		},
		"widgets": schema.ListNestedAttribute{
			Required:    true,
			Description: "The widgets, at most 24 (may be empty).",
			Validators:  []validator.List{listvalidator.SizeAtMost(24)},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						Optional:    true,
						Description: "1 to 40 letters, digits, dashes or underscores. Left out: `w1`, `w2`, … by position. Give every widget an id or none.",
						Validators:  []validator.String{stringvalidator.RegexMatches(widgetIDPattern, "must be 1 to 40 letters, digits, dashes or underscores")},
					},
					"title":   schema.StringAttribute{Required: true, Description: "The widget's title.", Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
					"viz":     schema.StringAttribute{Required: true, Description: "`line`, `bar`, `stat` or `table`.", Validators: []validator.String{stringvalidator.OneOf(dashboardViz...)}},
					"source":  schema.StringAttribute{Required: true, Description: "`logs`, `spans`, `errors`, `metrics`, `llm` or `sql`.", Validators: []validator.String{stringvalidator.OneOf(dashboardSources...)}},
					"measure": schema.StringAttribute{Optional: true, Description: "`count`, `rate`, `p50`, `p95`, `p99` or `sum`. Required unless `source` is `sql`.", Validators: []validator.String{stringvalidator.OneOf(dashboardMeasures...)}},
					"field":   schema.StringAttribute{Optional: true, Description: "The field the measure reads. Left out: the source's default (Autter fills it in; that is not a change).", Validators: []validator.String{stringvalidator.LengthBetween(1, 60)}},
					"group_by": schema.ListAttribute{
						ElementType: types.StringType, Optional: true, Description: "Up to 3 columns to group by.",
						Validators: []validator.List{listvalidator.SizeAtMost(3), listvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 60))},
					},
					"filters": schema.ListNestedAttribute{
						Optional:    true,
						Description: "Up to 10 filters.",
						Validators:  []validator.List{listvalidator.SizeAtMost(10)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"field": schema.StringAttribute{Required: true, Description: "The column.", Validators: []validator.String{stringvalidator.LengthBetween(1, 60)}},
								"op":    schema.StringAttribute{Required: true, Description: "`eq`, `neq`, `contains`, `not_contains`, `starts_with`, `in`, `not_in`, `gt`, `gte`, `lt` or `lte`.", Validators: []validator.String{stringvalidator.OneOf(dashboardFilterOp...)}},
								"value": schema.StringAttribute{
									Optional:    true,
									Description: "One value. A number (`value = 500`) is accepted and sent as text; Autter compares number columns as numbers. Set `value` or `values`, not both.",
									Validators:  []validator.String{stringvalidator.LengthAtMost(200)},
								},
								"values": schema.ListAttribute{
									ElementType: types.StringType,
									Optional:    true,
									Description: "A list of 1 to 50 values, for `in` and `not_in`. This is the API's `value` when it is a list.",
									Validators:  []validator.List{listvalidator.SizeBetween(1, 50), listvalidator.ValueStringsAre(stringvalidator.LengthAtMost(200))},
								},
							},
						},
					},
					"sql": schema.StringAttribute{Optional: true, Description: "For `source = \"sql\"`: one read-only SELECT.", Validators: []validator.String{stringvalidator.LengthBetween(1, 8000)}},
				},
			},
		},
		"layout": schema.ListNestedAttribute{
			Optional:    true,
			Description: "Where each widget goes. Left out: every widget at its default size, in widget order (Autter fills it in; that is not a change).",
			Validators:  []validator.List{listvalidator.SizeAtMost(24)},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{Required: true, Description: "The widget's id.", Validators: []validator.String{stringvalidator.LengthBetween(1, 40)}},
					"w":  schema.Int64Attribute{Required: true, Description: "Width in columns of 12.", Validators: []validator.Int64{int64validator.Between(1, 12)}},
					"h":  schema.Int64Attribute{Required: true, Description: "Height in rows, 1 to 4.", Validators: []validator.Int64{int64validator.Between(1, 4)}},
				},
			},
		},
		"id": idAttribute("dashboard"),
	}
	createdAttributes("dashboard", attrs)
	return &resourceSpec{
		typeName: "dashboard",
		kind:     client.Dashboards,
		schema: schema.Schema{
			Description: "A Runtime dashboard of a repository. Addressed by its name within the repository. Runtime dashboards must be turned on for the organization.",
			Attributes:  attrs,
		},
		fields:   []string{"variables", "widgets", "layout"},
		computed: []string{"id", "created_by", "created_at", "updated_at"},
		encode:   encodeDashboardField,
		decode:   decodeDashboardField,
		validate: validateDashboard,
	}
}

// mapFilters returns widgets with fn applied to a copy of each filter.
func mapFilters(widgets any, fn func(filter map[string]any) map[string]any) any {
	list, ok := widgets.([]any)
	if !ok {
		return widgets
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		widget, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		copied := map[string]any{}
		for k, v := range widget {
			copied[k] = v
		}
		if filters, ok := widget["filters"].([]any); ok {
			mapped := make([]any, 0, len(filters))
			for _, f := range filters {
				filter, ok := f.(map[string]any)
				if !ok {
					mapped = append(mapped, f)
					continue
				}
				c := map[string]any{}
				for k, v := range filter {
					c[k] = v
				}
				mapped = append(mapped, fn(c))
			}
			copied["filters"] = mapped
		}
		out = append(out, copied)
	}
	return out
}

// encodeDashboardField sends a filter's `values` as the API's `value` list.
func encodeDashboardField(field string, value any) any {
	if field != "widgets" {
		return value
	}
	return mapFilters(value, func(filter map[string]any) map[string]any {
		if values, ok := filter["values"]; ok {
			filter["value"] = values
			delete(filter, "values")
		}
		return filter
	})
}

// decodeDashboardField reads the API's `value` list as `values`.
func decodeDashboardField(field string, value any) any {
	if field != "widgets" {
		return value
	}
	return mapFilters(value, func(filter map[string]any) map[string]any {
		if values, ok := filter["value"].([]any); ok {
			filter["values"] = values
			delete(filter, "value")
		}
		return filter
	})
}

func validateDashboard(_ context.Context, attrs map[string]attr.Value, diags *diag.Diagnostics) {
	widgets, ok := list(attrs["widgets"])
	if !ok {
		return
	}
	withID, withoutID := 0, 0
	for i, w := range widgets {
		widget := object(w)
		if widget == nil {
			continue
		}
		at := path.Root("widgets").AtListIndex(i)
		if known(widget["id"]) {
			if isSet(widget["id"]) {
				withID++
			} else {
				withoutID++
			}
		}
		source, _ := widget["source"].(types.String)
		if known(source) && !source.IsNull() && known(widget["measure"]) && known(widget["sql"]) {
			if source.ValueString() == "sql" && !isSet(widget["sql"]) {
				diags.AddAttributeError(at.AtName("sql"), "Missing sql", `A widget with source = "sql" needs sql: one read-only SELECT.`)
			}
			if source.ValueString() != "sql" && !isSet(widget["measure"]) {
				diags.AddAttributeError(at.AtName("measure"), "Missing measure", fmt.Sprintf("A widget with source = %q needs a measure: count, rate, p50, p95, p99 or sum.", source.ValueString()))
			}
		}
		filters, _ := list(widget["filters"])
		for j, f := range filters {
			filter := object(f)
			if filter == nil || !known(filter["value"]) || !known(filter["values"]) {
				continue
			}
			fat := at.AtName("filters").AtListIndex(j)
			hasValue, hasValues := isSet(filter["value"]), isSet(filter["values"])
			if hasValue == hasValues {
				diags.AddAttributeError(fat, "Set value or values", "A filter needs exactly one of value (one value) or values (a list, for in and not_in).")
				continue
			}
			op, _ := filter["op"].(types.String)
			if hasValues && known(op) && !op.IsNull() && op.ValueString() != "in" && op.ValueString() != "not_in" {
				diags.AddAttributeError(fat.AtName("values"), "values needs in or not_in", fmt.Sprintf("op = %q takes one value: use value, or op = \"in\" / \"not_in\" for a list.", op.ValueString()))
			}
		}
	}
	if withID > 0 && withoutID > 0 {
		diags.AddAttributeError(path.Root("widgets"), "Give every widget an id or none",
			"Widgets without an id get w1, w2, … by position, which can collide with the ids you gave. Give every widget an id, or none.")
	}
}
