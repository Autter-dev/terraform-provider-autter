package provider

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"gopkg.in/yaml.v3"
)

// The provider's schemas against the API's OpenAPI document
// (testdata/autter-v1.yaml, copied from the monorepo by
// scripts/sync-from-monorepo.sh). It fails when the API gains a field the
// provider does not manage: a PUT is a whole-resource upsert, so an apply
// would reset such a field to its default.

// Provider attributes that are not input fields of the API, with why.
var notInAPIInput = map[string]string{
	"repository_id":          "the repository is in the path",
	"kind":                   "not an API field yet; never sent while it is http",
	"widgets.filters.values": "the API's value when it is a list",
}

// API input fields the provider does not manage yet. Keep this empty: a
// field listed here is reset to its default by every apply.
var notInProvider = map[string]string{}

func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "autter-v1.yaml"))
	if err != nil {
		t.Fatalf("%v (run scripts/sync-from-monorepo.sh)", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc["components"].(map[string]any)["schemas"].(map[string]any)
}

// objectProperties returns the properties of an object schema, looking
// through `anyOf: [object, null]` and arrays of objects.
func objectProperties(s any) map[string]any {
	m, ok := s.(map[string]any)
	if !ok {
		return nil
	}
	if anyOf, ok := m["anyOf"].([]any); ok {
		for _, branch := range anyOf {
			if props := objectProperties(branch); props != nil {
				return props
			}
		}
		return nil
	}
	if m["type"] == "array" {
		return objectProperties(m["items"])
	}
	props, _ := m["properties"].(map[string]any)
	return props
}

// nestedAttributes returns the attributes inside a nested attribute.
func nestedAttributes(a schema.Attribute) map[string]schema.Attribute {
	switch n := a.(type) {
	case schema.SingleNestedAttribute:
		return n.Attributes
	case schema.ListNestedAttribute:
		return n.NestedObject.Attributes
	}
	return nil
}

// compareInput walks provider attributes and API properties together.
func compareInput(t *testing.T, resource, prefix string, attrs map[string]schema.Attribute, props map[string]any) {
	t.Helper()
	for name, attr := range attrs {
		path := prefix + name
		if attr.IsComputed() && !attr.IsOptional() {
			continue
		}
		prop, ok := props[name]
		if !ok {
			if _, allowed := notInAPIInput[path]; !allowed {
				t.Errorf("%s: attribute %s is not a field of the API's input", resource, path)
			}
			continue
		}
		nested := nestedAttributes(attr)
		propProps := objectProperties(prop)
		if (nested == nil) != (propProps == nil) {
			t.Errorf("%s: %s is an object in only one of the provider and the API", resource, path)
			continue
		}
		if nested != nil {
			compareInput(t, resource, path+".", nested, propProps)
		}
	}
	for name := range props {
		path := prefix + name
		if _, ok := attrs[name]; ok {
			continue
		}
		if _, skipped := notInProvider[path]; skipped {
			continue
		}
		t.Errorf("%s: the API's input field %s is not managed by the provider", resource, path)
	}
}

func TestSchemasMatchTheOpenAPIDocument(t *testing.T) {
	schemas := loadOpenAPI(t)
	cases := []struct {
		spec          *resourceSpec
		input, output string
	}{
		{alertRuleSpec(), "AlertRuleInput", "AlertRule"},
		{syntheticCheckSpec(), "SyntheticCheckInput", "SyntheticCheck"},
		{dashboardSpec(), "DashboardInput", "Dashboard"},
	}
	for _, c := range cases {
		input := objectProperties(schemas[c.input])
		output := objectProperties(schemas[c.output])
		if input == nil || output == nil {
			t.Fatalf("%s / %s not in the document", c.input, c.output)
		}
		compareInput(t, c.spec.typeName, "", c.spec.schema.Attributes, input)

		// Every field the provider sends or reads is an API field.
		for _, field := range append(append([]string{}, c.spec.fields...), c.spec.computed...) {
			if _, ok := output[field]; !ok {
				t.Errorf("%s: %s is not a field of the API's %s", c.spec.typeName, field, c.output)
			}
		}
		for _, field := range c.spec.fields {
			if _, ok := input[field]; !ok {
				t.Errorf("%s: %s is sent but is not a field of %s", c.spec.typeName, field, c.input)
			}
		}
		// Every output field is managed, computed, or deliberately left out.
		known := map[string]bool{"name": true, "repository_id": true}
		for _, f := range append(append([]string{}, c.spec.fields...), c.spec.computed...) {
			known[f] = true
		}
		var missing []string
		for field := range output {
			if !known[field] {
				missing = append(missing, field)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: the API returns fields the provider ignores: %s", c.spec.typeName, strings.Join(missing, ", "))
		}
	}
	if repo := objectProperties(schemas["Repository"]); repo != nil {
		for _, field := range []string{"id", "name", "full_name", "default_branch", "enabled"} {
			if _, ok := repo[field]; !ok {
				t.Errorf("Repository has no %s", field)
			}
		}
		if len(repo) != 5 {
			t.Errorf("Repository has %d fields; the data sources expose 5", len(repo))
		}
	}
}
