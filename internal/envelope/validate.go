package envelope

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// schemaID is the $id of the embedded schema (PROTOCOL §1.2).
const schemaID = "urn:vecnet:envelope:1"

// schemaJSON is a byte-identical copy of docs/protocol/envelope.schema.json;
// `just validate-protocol` and TestEmbeddedSchemaMatchesDocs fail on drift.
//
//go:embed schema.json
var schemaJSON []byte

// defs are the $defs names Validate accepts.
var defs = []string{"sendRequest", "delivery", "event", "receipt", "presence", "threadSummary", "message", "reaction", "reactionEvent", "reactionRequest", "hubNotice", "hubNoticeEvent", "hubIdentity", "attachmentReference"}

var (
	compiled = map[string]*jsonschema.Schema{}
	printer  = message.NewPrinter(language.English)
)

func init() {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		panic("envelope: embedded schema is not JSON: " + err.Error())
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaID, doc); err != nil {
		panic("envelope: add schema: " + err.Error())
	}
	for _, d := range defs {
		sch, err := c.Compile(schemaID + "#/$defs/" + d)
		if err != nil {
			panic("envelope: compile " + d + ": " + err.Error())
		}
		compiled[d] = sch
	}
}

// SchemaError is a schema violation (or unparseable JSON) in a wire body.
// Path is the JSON pointer (RFC 6901) of the violating value: for an
// unknown or missing property it names that property, e.g. "/sender" or
// "/priority"; "" is the document root.
type SchemaError struct {
	Path    string
	Message string
}

func (e *SchemaError) Error() string {
	p := e.Path
	if p == "" {
		p = "/"
	}
	return "schema violation at " + p + ": " + e.Message
}

// Validate checks raw JSON against $defs/<def> of the embedded schema.
// def is one of "sendRequest", "delivery", "event", "receipt", "presence",
// "threadSummary", "message", "reaction", "reactionEvent", "reactionRequest",
// "attachmentReference". The latter validates future grant-aware references;
// the active disk-only sendRequest rejects blob_id until Task 4 integration.
// A violation, including malformed JSON or
// trailing data, is returned as *SchemaError; an unknown def is a plain error.
func Validate(def string, raw []byte) error {
	sch, ok := compiled[def]
	if !ok {
		return fmt.Errorf("envelope: unknown schema def %q", def)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &SchemaError{Path: "", Message: "invalid JSON: " + err.Error()}
	}
	return ValidateValue(sch, v)
}

// SchemaJSON returns a copy of the embedded envelope schema document, for
// callers that publish parts of it (the MCP tool inputSchemas).
func SchemaJSON() []byte { return bytes.Clone(schemaJSON) }

// ValidateValue checks v, a document decoded with jsonschema.UnmarshalJSON,
// against sch. A violation is a *SchemaError naming the same deterministic
// first violation Validate reports.
func ValidateValue(sch *jsonschema.Schema, v any) error {
	err := sch.Validate(v)
	if err == nil {
		return nil
	}
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return &SchemaError{Path: "", Message: err.Error()}
	}
	return firstViolation(verr)
}

// candidate is one reportable violation.
type candidate struct {
	branchy bool // an anyOf/oneOf failure: less specific than a direct one
	path    string
	msg     string
}

// firstViolation picks one violation deterministically. jsonschema/v6
// collects errors while iterating Go maps, so sibling order is unstable;
// candidates are ranked direct-before-anyOf/oneOf, then by path.
func firstViolation(root *jsonschema.ValidationError) *SchemaError {
	var cands []candidate
	collect(root, &cands)
	if len(cands) == 0 { // not expected; keep a usable answer
		return &SchemaError{Path: pointer(root.InstanceLocation), Message: root.ErrorKind.LocalizedString(printer)}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].branchy != cands[j].branchy {
			return !cands[i].branchy
		}
		if cands[i].path != cands[j].path {
			return cands[i].path < cands[j].path
		}
		return cands[i].msg < cands[j].msg
	})
	return &SchemaError{Path: cands[0].path, Message: cands[0].msg}
}

func collect(e *jsonschema.ValidationError, out *[]candidate) {
	switch k := e.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		// Report the failed alternative at its own location rather than
		// guessing which branch the sender meant.
		*out = append(*out, candidate{branchy: true, path: pointer(e.InstanceLocation), msg: branchMessage(e)})
		return
	case *kind.AdditionalProperties:
		// v6 reports these at the parent object; point at each offender.
		for _, p := range k.Properties {
			*out = append(*out, candidate{path: pointer(append(clone(e.InstanceLocation), p)), msg: fmt.Sprintf("unknown property %q", p)})
		}
		return
	case *kind.Required:
		for _, p := range k.Missing {
			*out = append(*out, candidate{path: pointer(append(clone(e.InstanceLocation), p)), msg: fmt.Sprintf("missing required property %q", p)})
		}
		return
	}
	if len(e.Causes) == 0 {
		*out = append(*out, candidate{path: pointer(e.InstanceLocation), msg: e.ErrorKind.LocalizedString(printer)})
		return
	}
	for _, c := range e.Causes {
		collect(c, out)
	}
}

// branchMessage summarises why every alternative of an anyOf/oneOf failed.
func branchMessage(e *jsonschema.ValidationError) string {
	msg := e.ErrorKind.LocalizedString(printer)
	var parts []string
	for _, c := range e.Causes {
		var sub []candidate
		collect(c, &sub)
		for _, s := range sub {
			parts = append(parts, s.msg)
		}
	}
	if len(parts) == 0 {
		return msg
	}
	return msg + ": " + strings.Join(parts, "; ")
}

func clone(s []string) []string { return append([]string(nil), s...) }

// pointer renders an instance location as an RFC 6901 JSON pointer.
func pointer(loc []string) string {
	var b strings.Builder
	for _, tok := range loc {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(tok))
	}
	return b.String()
}
