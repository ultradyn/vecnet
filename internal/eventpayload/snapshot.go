package eventpayload

import (
	"bytes"
	"sync"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Historical replay accepts unknown fields, while retaining the public schema's
// required fields, version, types and known-field constraints. Only the private
// schema copy changes; Message bytes are never normalized or re-encoded.
var snapshotMessageSchema = sync.OnceValue(func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(envelope.SchemaJSON()))
	if err != nil {
		panic("eventpayload: invalid embedded message schema")
	}
	allowLegacyFields(doc)
	const schemaID = "urn:vecnet:envelope:1"
	compiler := jsonschema.NewCompiler()
	if compiler.AddResource(schemaID, doc) != nil {
		panic("eventpayload: invalid embedded message schema")
	}
	schema, err := compiler.Compile(schemaID + "#/$defs/message")
	if err != nil {
		panic("eventpayload: invalid embedded message schema")
	}
	return schema
})

func allowLegacyFields(v any) {
	switch v := v.(type) {
	case map[string]any:
		if allowed, ok := v["additionalProperties"].(bool); ok && !allowed {
			v["additionalProperties"] = true
		}
		for _, child := range v {
			allowLegacyFields(child)
		}
	case []any:
		for _, child := range v {
			allowLegacyFields(child)
		}
	}
}

func supportedSnapshotMessage(body []byte) bool {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	// Validation diagnostics may include body bytes; return only a boolean.
	return err == nil && snapshotMessageSchema().Validate(v) == nil
}
