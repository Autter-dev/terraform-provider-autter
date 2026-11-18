package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func decode(t *testing.T, text string) any {
	t.Helper()
	var v any
	d := json.NewDecoder(bytes.NewReader([]byte(text)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFromJSONPrunesLikeTheExport(t *testing.T) {
	spec := alertRuleSpec()
	actions, err := fromJSON(decode(t, `{"slack":{"channel_id":null},"email":{"assignee":true,"watchers":false,"user_ids":[],"everyone":false},"in_app":null,"oncall":false,"auto_assignments":false}`),
		spec.schema.Attributes["actions"].GetType())
	if err != nil {
		t.Fatal(err)
	}
	a := actions.(types.Object).Attributes()
	slack := a["slack"].(types.Object)
	if slack.IsNull() || !slack.Attributes()["channel_id"].IsNull() {
		t.Errorf("slack = {} is kept as an object with a null channel: %v", slack)
	}
	email := a["email"].(types.Object).Attributes()
	if !email["user_ids"].IsNull() || email["watchers"].(types.Bool).ValueBool() || email["watchers"].IsNull() {
		t.Errorf("an empty list is null, false stays false: %v", email)
	}
	if !a["in_app"].IsNull() {
		t.Errorf("null stays null")
	}

	filters, err := fromJSON(decode(t, `{"environments":[],"services":["a"],"levels":[],"priorities":[],"error_type":null,"message":null,"assignee":"any","threshold":{"min_events":5,"min_users":null,"window_minutes":60}}`),
		spec.schema.Attributes["filters"].GetType())
	if err != nil {
		t.Fatal(err)
	}
	f := filters.(types.Object).Attributes()
	if !f["environments"].IsNull() || f["threshold"].(types.Object).Attributes()["min_events"].(types.Int64).ValueInt64() != 5 {
		t.Errorf("filters: %v", f)
	}
}

func TestToJSONOmitsNulls(t *testing.T) {
	ctx := context.Background()
	o := types.ObjectValueMust(map[string]attr.Type{"a": types.StringType, "b": types.ListType{ElemType: types.Int64Type}, "c": types.BoolType, "d": types.Float64Type},
		map[string]attr.Value{
			"a": types.StringNull(),
			"b": types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(1), types.Int64Value(2)}),
			"c": types.BoolValue(false),
			"d": types.Float64Value(1.5),
		})
	got, ok := toJSON(ctx, o)
	if !ok || canonicalJSON(got) != `{"b":[1,2],"c":false,"d":1.5}` {
		t.Errorf("got %s", canonicalJSON(got))
	}
	if _, ok := toJSON(ctx, types.StringUnknown()); ok {
		t.Error("unknown is not sent")
	}
}

func TestFromJSONRejectsTheWrongType(t *testing.T) {
	if _, err := fromJSON("x", types.Int64Type); err == nil {
		t.Error("a string for an int64")
	}
	if v, err := fromJSON(json.Number("500"), types.StringType); err != nil || v.(types.String).ValueString() != "500" {
		t.Errorf("a number for a string is its text: %v %v", v, err)
	}
}

func TestDashboardValuesRoundTrip(t *testing.T) {
	ctx := context.Background()
	spec := dashboardSpec()
	widgetsType := spec.schema.Attributes["widgets"].GetType()
	api := decode(t, `[{"id":"w1","title":"T","viz":"bar","source":"errors","measure":"count","field":null,"group_by":[],
		"filters":[{"field":"status_code","op":"gte","value":500},{"field":"x","op":"lt","value":-3},{"field":"y","op":"eq","value":1.5},
		{"field":"environment","op":"in","value":["production","staging"]},{"field":"status_code","op":"not_in","value":[500,502]}]}]`)
	value, err := fromJSON(decodeDashboardField("widgets", api), widgetsType)
	if err != nil {
		t.Fatal(err)
	}
	widget := value.(types.List).Elements()[0].(types.Object).Attributes()
	var got []string
	for _, f := range widget["filters"].(types.List).Elements() {
		attrs := f.(types.Object).Attributes()
		got = append(got, fmt.Sprintf("%s|%s", attrs["value"], attrs["values"]))
	}
	want := `"500"|<null> "-3"|<null> "1.5"|<null> <null>|["production","staging"] <null>|["500","502"]`
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	if !widget["group_by"].IsNull() || !widget["field"].IsNull() {
		t.Errorf("empty group_by and null field are null")
	}

	sent, _ := toJSON(ctx, value)
	body := canonicalJSON(encodeDashboardField("widgets", sent))
	if !strings.Contains(body, `{"field":"environment","op":"in","value":["production","staging"]}`) || strings.Contains(body, "values") {
		t.Errorf("values goes back as value: %s", body)
	}
	// The answer used for the snapshot is not changed by decoding.
	if !strings.Contains(canonicalJSON(api), `"value":["production","staging"]`) {
		t.Errorf("decode mutated its input")
	}
}

type memPrivate map[string][]byte

func (m memPrivate) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return m[key], nil
}
func (m memPrivate) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	if !json.Valid(value) {
		var d diag.Diagnostics
		d.AddError("invalid", string(value))
		return d
	}
	m[key] = value
	return nil
}

func TestSnapshot(t *testing.T) {
	ctx := context.Background()
	answer := decode(t, `{"triggers":["new"],"filters":{"b":1,"a":[2,1]},"enabled":true}`).(map[string]any)
	s := takeSnapshot(answer, []string{"triggers", "filters", "enabled", "missing"})
	private := memPrivate{}
	if d := saveSnapshot(ctx, private, s); d.HasError() {
		t.Fatal(d)
	}
	loaded, d := loadSnapshot(ctx, private)
	if d.HasError() || loaded == nil {
		t.Fatal(d)
	}
	if !loaded.matches("filters", decode(t, `{"a":[2,1],"b":1}`)) {
		t.Error("key order must not matter")
	}
	if loaded.matches("filters", decode(t, `{"a":[1,2],"b":1}`)) {
		t.Error("list order matters")
	}
	if !loaded.matches("missing", nil) || loaded.matches("enabled", false) || loaded.matches("unknown", nil) {
		t.Error("matches")
	}
	none, _ := loadSnapshot(ctx, memPrivate{})
	if none != nil {
		t.Error("no snapshot is nil")
	}
	garbage, _ := loadSnapshot(ctx, memPrivate{snapshotKey: []byte(`[1]`)})
	if garbage != nil {
		t.Error("an unreadable snapshot is none")
	}
}

func TestParseImportID(t *testing.T) {
	cases := map[string][2]string{
		"repo-1:Checkout errors":  {"repo-1", "Checkout errors"},
		"acme/web:5xx on /check":  {"acme/web", "5xx on /check"},
		"acme/web:Ratio: 5 to 1":  {"acme/web", "Ratio: 5 to 1"},
		" acme/web :Name":         {"acme/web", "Name"},
		"repo-1:  spaced  name  ": {"repo-1", "  spaced  name  "},
	}
	for id, want := range cases {
		repo, name, err := parseImportID(id)
		if err != nil || repo != want[0] || name != want[1] {
			t.Errorf("%q: %q %q %v", id, repo, name, err)
		}
	}
	for _, bad := range []string{"", "no-colon", ":name", "repo:", "repo:   "} {
		if _, _, err := parseImportID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckBodyNeverSendsHTTPKind(t *testing.T) {
	ctx := context.Background()
	r := &apiResource{spec: syntheticCheckSpec()}
	attrs := map[string]attr.Value{
		"name":    types.StringValue("Health"),
		"kind":    types.StringValue("http"),
		"url":     types.StringValue("https://example.com"),
		"headers": types.MapValueMust(types.StringType, map[string]attr.Value{"X-Key": types.StringValue("secret")}),
	}
	for _, f := range r.spec.fields {
		if _, ok := attrs[f]; !ok {
			attrs[f] = types.StringNull()
		}
	}
	body := canonicalJSON(r.body(ctx, attrs))
	if body != `{"headers":{"X-Key":"secret"},"name":"Health","url":"https://example.com"}` {
		t.Errorf("body %s", body)
	}
}
