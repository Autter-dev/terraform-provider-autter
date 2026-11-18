package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// Conversions between Terraform values and the API's JSON.
//
// Terraform → API: null attributes are left out (the API reads absence as the
// default), lists and objects follow the schema, numbers are sent as numbers.
//
// API → Terraform follows the same rule as the monorepo's "Export as
// Terraform": null and empty lists become null, objects are kept even when
// every field is null (`slack = {}`), so an imported resource's state equals
// what the export writes.

// toJSON returns the JSON value of v and whether it is set (not null or unknown).
func toJSON(ctx context.Context, v attr.Value) (any, bool) {
	if v == nil || v.IsNull() || v.IsUnknown() {
		return nil, false
	}
	switch value := v.(type) {
	case basetypes.StringValuable:
		s, _ := value.ToStringValue(ctx)
		return s.ValueString(), true
	case basetypes.BoolValuable:
		b, _ := value.ToBoolValue(ctx)
		return b.ValueBool(), true
	case basetypes.Int64Valuable:
		i, _ := value.ToInt64Value(ctx)
		return i.ValueInt64(), true
	case basetypes.Float64Valuable:
		f, _ := value.ToFloat64Value(ctx)
		return f.ValueFloat64(), true
	case basetypes.ListValuable:
		l, _ := value.ToListValue(ctx)
		out := []any{}
		for _, element := range l.Elements() {
			if j, ok := toJSON(ctx, element); ok {
				out = append(out, j)
			}
		}
		return out, true
	case basetypes.MapValuable:
		m, _ := value.ToMapValue(ctx)
		out := map[string]any{}
		for key, element := range m.Elements() {
			if j, ok := toJSON(ctx, element); ok {
				out[key] = j
			}
		}
		return out, true
	case basetypes.ObjectValuable:
		o, _ := value.ToObjectValue(ctx)
		out := map[string]any{}
		for key, element := range o.Attributes() {
			if j, ok := toJSON(ctx, element); ok {
				out[key] = j
			}
		}
		return out, true
	}
	return nil, false
}

// fromJSON converts an API value to a Terraform value of type t, with the export's prune rule.
func fromJSON(raw any, t attr.Type) (attr.Value, error) {
	switch typ := t.(type) {
	case basetypes.StringType:
		switch v := raw.(type) {
		case nil:
			return types.StringNull(), nil
		case string:
			return types.StringValue(v), nil
		case json.Number:
			return types.StringValue(v.String()), nil
		case bool:
			return types.StringValue(strconv.FormatBool(v)), nil
		}
	case basetypes.BoolType:
		switch v := raw.(type) {
		case nil:
			return types.BoolNull(), nil
		case bool:
			return types.BoolValue(v), nil
		}
	case basetypes.Int64Type:
		switch v := raw.(type) {
		case nil:
			return types.Int64Null(), nil
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return types.Int64Value(i), nil
			}
			if f, err := v.Float64(); err == nil {
				return types.Int64Value(int64(f)), nil
			}
		}
	case basetypes.Float64Type:
		switch v := raw.(type) {
		case nil:
			return types.Float64Null(), nil
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return types.Float64Value(f), nil
			}
		}
	case basetypes.ListType:
		switch v := raw.(type) {
		case nil:
			return types.ListNull(typ.ElemType), nil
		case []any:
			if len(v) == 0 {
				return types.ListNull(typ.ElemType), nil
			}
			elements := make([]attr.Value, 0, len(v))
			for i, item := range v {
				element, err := fromJSON(item, typ.ElemType)
				if err != nil {
					return nil, fmt.Errorf("[%d]: %w", i, err)
				}
				if element.IsNull() {
					continue
				}
				elements = append(elements, element)
			}
			if len(elements) == 0 {
				return types.ListNull(typ.ElemType), nil
			}
			list, diags := types.ListValue(typ.ElemType, elements)
			if diags.HasError() {
				return nil, fmt.Errorf("%s", diags)
			}
			return list, nil
		}
	case basetypes.MapType:
		switch v := raw.(type) {
		case nil:
			return types.MapNull(typ.ElemType), nil
		case map[string]any:
			if len(v) == 0 {
				return types.MapNull(typ.ElemType), nil
			}
			elements := map[string]attr.Value{}
			for key, item := range v {
				element, err := fromJSON(item, typ.ElemType)
				if err != nil {
					return nil, fmt.Errorf("[%q]: %w", key, err)
				}
				elements[key] = element
			}
			m, diags := types.MapValue(typ.ElemType, elements)
			if diags.HasError() {
				return nil, fmt.Errorf("%s", diags)
			}
			return m, nil
		}
	case basetypes.ObjectType:
		switch v := raw.(type) {
		case nil:
			return types.ObjectNull(typ.AttrTypes), nil
		case map[string]any:
			attributes := map[string]attr.Value{}
			for name, attrType := range typ.AttrTypes {
				value, err := fromJSON(v[name], attrType)
				if err != nil {
					return nil, fmt.Errorf(".%s: %w", name, err)
				}
				attributes[name] = value
			}
			o, diags := types.ObjectValue(typ.AttrTypes, attributes)
			if diags.HasError() {
				return nil, fmt.Errorf("%s", diags)
			}
			return o, nil
		}
	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
	return nil, fmt.Errorf("the API returned %T where %s was expected", raw, t)
}

// canonicalJSON is v as JSON with sorted keys and numbers as written, so two
// equal values give equal text whatever their key order.
func canonicalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return canonicalText(raw)
}

// canonicalText re-encodes JSON text with sorted keys.
func canonicalText(raw []byte) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}
	out, _ := json.Marshal(value)
	return string(out)
}
