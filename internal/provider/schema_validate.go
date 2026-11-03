package provider

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// schemaProblem is a part of the config that the schema does not accept. Key is the top-level key that the
// problem is in ("" for the config as a whole). Text has the path and the rule, never the value, because the
// config can contain secrets.
type schemaProblem struct {
	Key  string
	Text string
}

// validateSchema checks a plugin config against the schemaJson of the plugin. It supports the parts of JSON
// schema that Matterbridge plugins use: type, enum, required, properties, additionalProperties, items, minimum,
// maximum, minLength and maxLength. root names each top-level key with the attribute that holds it.
func validateSchema(value any, schema map[string]any, root func(key string) string) []schemaProblem {
	var problems []schemaProblem
	walkSchema(value, schema, "", "", root, &problems)
	sort.Slice(problems, func(i, j int) bool { return problems[i].Text < problems[j].Text })
	return problems
}

func walkSchema(value any, schema map[string]any, path, key string, root func(string) string, problems *[]schemaProblem) {
	if schema == nil {
		return
	}
	add := func(format string, args ...any) {
		where := path
		if where == "" {
			where = "the config"
		}
		*problems = append(*problems, schemaProblem{Key: key, Text: where + ": " + fmt.Sprintf(format, args...)})
	}
	if types := schemaTypes(schema["type"]); len(types) > 0 && !slices.ContainsFunc(types, func(t string) bool { return hasType(value, t) }) {
		add("must be %s", strings.Join(types, " or "))
		return
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return jsonEqual(e, value) }) {
		allowed := make([]string, 0, len(enum))
		for _, e := range enum {
			allowed = append(allowed, fmt.Sprintf("%v", e))
		}
		add("must be one of %s", strings.Join(allowed, ", "))
	}
	if n, ok := value.(float64); ok {
		if min, ok := schema["minimum"].(float64); ok && n < min {
			add("must be %v or more", min)
		}
		if max, ok := schema["maximum"].(float64); ok && n > max {
			add("must be %v or less", max)
		}
	}
	if s, ok := value.(string); ok {
		if min, ok := schema["minLength"].(float64); ok && float64(len([]rune(s))) < min {
			add("must have %v characters or more", min)
		}
		if max, ok := schema["maxLength"].(float64); ok && float64(len([]rune(s))) > max {
			add("must have %v characters or less", max)
		}
	}
	switch v := value.(type) {
	case []any:
		items, _ := schema["items"].(map[string]any)
		for i, e := range v {
			walkSchema(e, items, fmt.Sprintf("%s[%d]", path, i), key, root, problems)
		}
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, r := range required {
				if name, ok := r.(string); ok {
					if _, has := v[name]; !has {
						if path == "" {
							// A missing top-level key is in the key itself.
							*problems = append(*problems, schemaProblem{Key: name, Text: "the config: " + root(name) + "." + name + " is required"})
						} else {
							add("%s is required", name)
						}
					}
				}
			}
		}
		for _, k := range sortedKeys(v) {
			p, top := path+"."+k, key
			if path == "" {
				p, top = root(k)+"."+k, k
			}
			if sub, ok := properties[k].(map[string]any); ok {
				walkSchema(v[k], sub, p, top, root, problems)
				continue
			}
			switch extra := schema["additionalProperties"].(type) {
			case bool:
				if !extra {
					add("%s is not a setting of the plugin", k)
				}
			case map[string]any:
				walkSchema(v[k], extra, p, top, root, problems)
			}
		}
	}
}

func schemaTypes(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func hasType(value any, t string) bool {
	switch t {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && n == math.Trunc(n)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "null":
		return value == nil
	}
	return true
}

// fixedSecretKeys are secrets in all plugins: password, token, and key for a TLS private key.
var fixedSecretKeys = []string{"key", "password", "token"}

// isSecret returns true if key is a secret: a fixed secret key, or a setting that the schema shows as a password
// ("ui:widget": "password"). schema can be nil. The provider keeps secrets out of config, and an import puts
// them in sensitive_config.
func isSecret(schema map[string]any, key string) bool {
	if slices.Contains(fixedSecretKeys, key) {
		return true
	}
	properties, _ := schema["properties"].(map[string]any)
	p, _ := properties[key].(map[string]any)
	return p["ui:widget"] == "password"
}
