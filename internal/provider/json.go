package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// config and sensitive_config are dynamic: an object of plugin settings written in HCL. These functions convert
// between them and the JSON of configJson. They are from terraform-provider-zigbee2mqtt.

// errUnknown is the error for values that are not known yet.
var errUnknown = fmt.Errorf("the value is not known yet")

// dynamicObject returns a dynamic attribute as a JSON-like map. It returns nil if the attribute is null.
func dynamicObject(v types.Dynamic) (map[string]any, error) {
	if v.IsNull() || v.IsUnderlyingValueNull() {
		return nil, nil
	}
	if v.IsUnknown() || v.IsUnderlyingValueUnknown() {
		return nil, errUnknown
	}
	value, err := attrToJSON(v.UnderlyingValue())
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the value must be an object")
	}
	return object, nil
}

func attrToJSON(v attr.Value) (any, error) {
	if v.IsUnknown() {
		return nil, errUnknown
	}
	if v.IsNull() {
		return nil, nil
	}
	elements := func(values []attr.Value) (any, error) {
		out := make([]any, 0, len(values))
		for _, e := range values {
			j, err := attrToJSON(e)
			if err != nil {
				return nil, err
			}
			out = append(out, j)
		}
		return out, nil
	}
	members := func(values map[string]attr.Value) (any, error) {
		out := make(map[string]any, len(values))
		for k, e := range values {
			j, err := attrToJSON(e)
			if err != nil {
				return nil, err
			}
			out[k] = j
		}
		return out, nil
	}
	switch v := v.(type) {
	case basetypes.DynamicValue:
		return attrToJSON(v.UnderlyingValue())
	case basetypes.StringValue:
		return v.ValueString(), nil
	case basetypes.BoolValue:
		return v.ValueBool(), nil
	case basetypes.NumberValue:
		f, _ := v.ValueBigFloat().Float64()
		return f, nil
	case basetypes.Int64Value:
		return float64(v.ValueInt64()), nil
	case basetypes.Float64Value:
		return v.ValueFloat64(), nil
	case basetypes.ObjectValue:
		return members(v.Attributes())
	case basetypes.MapValue:
		return members(v.Elements())
	case basetypes.TupleValue:
		return elements(v.Elements())
	case basetypes.ListValue:
		return elements(v.Elements())
	case basetypes.SetValue:
		return elements(v.Elements())
	}
	return nil, fmt.Errorf("the value %s has a type that is not supported", v)
}

// jsonToAttr is the inverse of attrToJSON. Objects become objects and arrays become tuples, as in HCL literals.
func jsonToAttr(ctx context.Context, v any) attr.Value {
	switch v := v.(type) {
	case string:
		return types.StringValue(v)
	case bool:
		return types.BoolValue(v)
	case float64:
		return types.NumberValue(big.NewFloat(v))
	case map[string]any:
		attrTypes := map[string]attr.Type{}
		attrs := map[string]attr.Value{}
		for k, e := range v {
			if e == nil {
				continue
			}
			attrs[k] = jsonToAttr(ctx, e)
			attrTypes[k] = attrs[k].Type(ctx)
		}
		return types.ObjectValueMust(attrTypes, attrs)
	case []any:
		var elemTypes []attr.Type
		var elems []attr.Value
		for _, e := range v {
			if e == nil {
				continue
			}
			value := jsonToAttr(ctx, e)
			elems = append(elems, value)
			elemTypes = append(elemTypes, value.Type(ctx))
		}
		return types.TupleValueMust(elemTypes, elems)
	}
	return types.StringNull()
}

// dynamicFromJSON returns a value that the provider read back from Matterbridge. If the contents are
// the same, it keeps prior (with its HCL types). Thus, a read never shows a diff of types only.
func dynamicFromJSON(ctx context.Context, actual map[string]any, prior types.Dynamic) types.Dynamic {
	if priorObject, err := dynamicObject(prior); err == nil && jsonEqual(priorObject, actual) {
		return prior
	}
	if actual == nil {
		return types.DynamicNull()
	}
	return types.DynamicValue(jsonToAttr(ctx, actual))
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	var na, nb any
	_ = json.Unmarshal(ja, &na)
	_ = json.Unmarshal(jb, &nb)
	return reflect.DeepEqual(na, nb)
}

// filterKeys returns the top-level keys of actual that managed has. A managed key holds its whole value: the
// provider compares lists and nested objects (for example deviceFeatureBlackList) as one value.
func filterKeys[V any](actual map[string]any, managed map[string]V) map[string]any {
	out := map[string]any{}
	for k := range managed {
		if v, ok := actual[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// configMatches returns true if current has the managed values.
func configMatches(current, managed map[string]any) bool {
	return jsonEqual(filterKeys(current, managed), managed)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
