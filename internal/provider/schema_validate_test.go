package provider

import (
	"reflect"
	"testing"
)

func TestValidateSchema(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"host"},
		"properties": map[string]any{
			"host": map[string]any{"type": "string"},
			"port": map[string]any{"type": "number", "maximum": 65535.0},
			"mqtt": map[string]any{"type": "object", "required": []any{"topic"}},
			"mode": map[string]any{"enum": []any{"a", "b"}},
			"list": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
	root := func(k string) string { return "config" }
	got := validateSchema(map[string]any{"port": 70000.0, "mqtt": map[string]any{}, "mode": "c", "list": []any{"x", 1.0}}, schema, root)
	want := []schemaProblem{
		{"list", "config.list[1]: must be string"},
		{"mode", "config.mode: must be one of a, b"},
		{"mqtt", "config.mqtt: topic is required"}, // nested: belongs to mqtt, not to topic
		{"port", "config.port: must be 65535 or less"},
		{"host", "the config: config.host is required"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestIsSecret(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{"apiKey": map[string]any{"ui:widget": "password"}, "host": map[string]any{}}}
	for key, want := range map[string]bool{"password": true, "token": true, "key": true, "apiKey": true, "host": false} {
		if got := isSecret(schema, key); got != want {
			t.Errorf("isSecret(%q) = %t", key, got)
		}
	}
	if isSecret(nil, "apiKey") {
		t.Error("without a schema, only the fixed keys are secret")
	}
}
