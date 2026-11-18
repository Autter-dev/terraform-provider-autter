package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Autter-dev/terraform-provider-autter/internal/client"
)

// Enums of the API (lib/runtime-alert-rules). The API lower-cases triggers,
// levels and priorities, so they are matched case-insensitively here.
var (
	alertTriggers       = []string{"new", "regressed", "spiking", "assigned", "resolved"}
	alertPriorities     = []string{"p0", "p1", "p2", "p3", "none"}
	alertMatchOperators = []string{"equals", "contains", "starts_with", "regex"}
)

// NewAlertRuleResource is the autter_alert_rule resource.
func NewAlertRuleResource() resource.Resource {
	return &apiResource{spec: alertRuleSpec()}
}

// stringList is a list of up to 50 strings of 1 to 200 characters, as the API's alert rule lists are.
func stringList(description string, values ...validator.String) schema.ListAttribute {
	validators := []validator.List{listvalidator.SizeAtMost(50), listvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 200))}
	if len(values) > 0 {
		validators = append(validators, listvalidator.ValueStringsAre(values...))
	}
	return schema.ListAttribute{ElementType: types.StringType, Optional: true, Description: description, Validators: validators}
}

func textMatchAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: description,
		Attributes: map[string]schema.Attribute{
			"op": schema.StringAttribute{
				Required:    true,
				Description: "How to match: `equals`, `contains`, `starts_with` or `regex`.",
				Validators:  []validator.String{stringvalidator.OneOf(alertMatchOperators...)},
			},
			"value": schema.StringAttribute{
				Required:    true,
				Description: "The text or pattern, up to 200 characters.",
				Validators:  []validator.String{stringvalidator.LengthAtMost(200)},
			},
		},
	}
}

func recipientsAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: description,
		Attributes: map[string]schema.Attribute{
			"assignee": schema.BoolAttribute{Optional: true, Description: "The issue's assignee."},
			"watchers": schema.BoolAttribute{Optional: true, Description: "The issue's assignee and every member who assigned, resolved, ignored or reopened it."},
			"everyone": schema.BoolAttribute{Optional: true, Description: "Every member of the organization."},
			"user_ids": stringList("These members, by user id."),
		},
	}
}

func alertRuleSpec() *resourceSpec {
	attrs := map[string]schema.Attribute{
		"repository_id": repositoryIDAttribute(),
		"name":          nameAttribute("alert rule"),
		"enabled": schema.BoolAttribute{
			Optional:    true,
			Computed:    true,
			Default:     booldefault.StaticBool(true),
			Description: "Whether the rule fires. Default `true`.",
		},
		"triggers": schema.ListAttribute{
			ElementType: types.StringType,
			Required:    true,
			Description: "The issue events the rule fires on: `new`, `regressed`, `spiking`, `assigned`, `resolved`.",
			Validators: []validator.List{
				listvalidator.SizeAtLeast(1),
				listvalidator.ValueStringsAre(stringvalidator.OneOfCaseInsensitive(alertTriggers...)),
			},
		},
		"filters": schema.SingleNestedAttribute{
			Optional:    true,
			Description: "Which issues the rule applies to. Left out: every issue.",
			Attributes: map[string]schema.Attribute{
				"environments": stringList("Environments; empty or left out: any environment."),
				"services":     stringList("Services; empty or left out: any service."),
				"levels":       stringList("Issue levels, for example `fatal`, `error`, `warning`."),
				"priorities":   stringList("Priorities: `p0` to `p3`, or `none` for an issue not ranked yet.", stringvalidator.OneOfCaseInsensitive(alertPriorities...)),
				"error_type":   textMatchAttribute("Match the issue's error type."),
				"message":      textMatchAttribute("Match the issue's message, or its title when it has none."),
				"assignee": schema.StringAttribute{
					Optional:    true,
					Description: "`any` (the default), `assigned`, `unassigned`, `me` (the rule's author) or `user:<id>`.",
					Validators:  []validator.String{stringvalidator.LengthBetween(1, 205)},
				},
				"threshold": schema.SingleNestedAttribute{
					Optional:    true,
					Description: "Alert only once the issue has at least this many events or users within the window.",
					Attributes: map[string]schema.Attribute{
						"min_events":     schema.Int64Attribute{Optional: true, Description: "Minimum events.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
						"min_users":      schema.Int64Attribute{Optional: true, Description: "Minimum users.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
						"window_minutes": schema.Int64Attribute{Optional: true, Description: "The window in minutes, up to 10080 (7 days). The API's default is 60.", Validators: []validator.Int64{int64validator.Between(1, 10080)}},
					},
				},
			},
		},
		"actions": schema.SingleNestedAttribute{
			Required:    true,
			Description: "Where the alert goes. At least one of `slack`, `email`, `in_app` or `oncall = true`.",
			Attributes: map[string]schema.Attribute{
				"slack": schema.SingleNestedAttribute{
					Optional:    true,
					Description: "Post to Slack. `slack = {}` uses the repository's Slack route.",
					Attributes: map[string]schema.Attribute{
						"channel_id": schema.StringAttribute{Optional: true, Description: "A Slack channel id; left out: the repository's Slack route.", Validators: []validator.String{stringvalidator.LengthAtMost(200)}},
					},
				},
				"email":            recipientsAttribute("A line in each recipient's daily update summary."),
				"in_app":           recipientsAttribute("An in-app notification."),
				"oncall":           schema.BoolAttribute{Optional: true, Description: "Page on-call when the issue is critical (p0, or a fatal error)."},
				"auto_assignments": schema.BoolAttribute{Optional: true, Description: "Also send automatic assignments to Slack and on-call."},
			},
		},
		"id": idAttribute("alert rule"),
		"is_default": schema.BoolAttribute{
			Computed:      true,
			Description:   "Whether this is one of the rules a repository starts with.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		},
	}
	createdAttributes("alert rule", attrs)
	return &resourceSpec{
		typeName: "alert_rule",
		kind:     client.AlertRules,
		schema: schema.Schema{
			Description: "An alert rule of a repository: which runtime issue events reach whom. Addressed by its name within the repository.",
			Attributes:  attrs,
		},
		fields:   []string{"enabled", "triggers", "filters", "actions"},
		computed: []string{"id", "is_default", "created_by", "created_at", "updated_at"},
		validate: validateAlertRule,
	}
}

func validateAlertRule(_ context.Context, attrs map[string]attr.Value, diags *diag.Diagnostics) {
	actions := object(attrs["actions"])
	if actions == nil {
		return
	}
	for _, key := range []string{"slack", "email", "in_app", "oncall"} {
		if !known(actions[key]) {
			return // Decided at apply time.
		}
	}
	chosen := boolTrue(actions["oncall"])
	for _, key := range []string{"slack", "email", "in_app"} {
		chosen = chosen || isSet(actions[key])
	}
	if !chosen {
		diags.AddAttributeError(path.Root("actions"), "No action",
			"Choose at least one action: slack (slack = {} for the repository's Slack route), email, in_app, or oncall = true.")
	}
}
