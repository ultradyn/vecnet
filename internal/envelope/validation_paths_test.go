package envelope_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/XertroV/vecnet/internal/envelope"
)

func TestValidationPathsTrustSchemaPropertiesOnly(t *testing.T) {
	// Include a recursive reference, schema alternatives, arrays and a
	// dictionary: dictionary keys remain private even when they happen to
	// match field names declared elsewhere in the schema.
	const schemaJSON = `{
		"$defs":{"entry":{"type":"object","additionalProperties":false,
			"properties":{"value":{"type":"integer"},"child":{"$ref":"#/$defs/entry"}}}},
		"type":"object","additionalProperties":false,
		"properties":{
			"records":{"type":"object","additionalProperties":{"$ref":"#/$defs/entry"}},
			"items":{"type":"array","items":{"$ref":"#/$defs/entry"}},
			"nested":{"allOf":[{"$ref":"#/$defs/entry"}]},
			"trusted/~":{"type":"integer"}
		}
	}`
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemaJSON))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	const uri = "urn:vecnet:test:validation-paths"
	if err := c.AddResource(uri, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(uri)
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{
		"records":   map[string]any{"free-form": map[string]any{"value": 1}},
		"items":     []any{map[string]any{"value": 2}},
		"nested":    map[string]any{"child": map[string]any{"value": 3}},
		"trusted/~": 4,
	}
	if err := envelope.ValidateValue(sch, valid); err != nil {
		t.Fatalf("valid dictionary/array/reference rejected: %v", err)
	}
	const capability = "https://private-path.invalid/object?X-Amz-Signature=path-canary"
	for _, tc := range []struct {
		name  string
		value any
		path  string
	}{
		{"root unknown", map[string]any{capability: true}, "/<unknown>"},
		{"nested unknown", map[string]any{"nested": map[string]any{capability: true}}, "/nested/<unknown>"},
		{"unknown schema name at wrong level", map[string]any{"nested": map[string]any{"records": true}}, "/nested/<unknown>"},
		{"array unknown", map[string]any{"items": []any{map[string]any{capability: true}}}, "/items/0/<unknown>"},
		{"array wrong type", map[string]any{"items": []any{map[string]any{"value": "bad"}}}, "/items/0/value"},
		{"recursive wrong type", map[string]any{"nested": map[string]any{"child": map[string]any{"value": "bad"}}}, "/nested/child/value"},
		{"trusted pointer escaping", map[string]any{"trusted/~": "bad"}, "/trusted~1~0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertPrivateValidationPath(t, envelope.ValidateValue(sch, tc.value), tc.path)
		})
	}
	for _, key := range []string{capability, "value", "0", "trusted/~"} {
		for _, value := range []any{"wrong-entry-type", map[string]any{"value": "wrong-value-type"}, map[string]any{capability: true}} {
			// These keys are allowed by additionalProperties. Their invalid
			// values must still be rejected without exposing the map key.
			doc := map[string]any{"records": map[string]any{key: value}}
			assertPrivateValidationPath(t, envelope.ValidateValue(sch, doc), "/records/<unknown>")
		}
	}
}

func assertPrivateValidationPath(t *testing.T, err error, want string) {
	t.Helper()
	var se *envelope.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("want schema rejection, got %v", err)
	}
	if se.Path != want {
		t.Fatalf("path = %q, want %q", se.Path, want)
	}
	raw, err := json.Marshal(se)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"private-path.invalid", "X-Amz-Signature", "path-canary"} {
		if strings.Contains(string(raw), canary) || strings.Contains(se.Error(), canary) {
			t.Fatal("schema error retained private property name")
		}
	}
}
