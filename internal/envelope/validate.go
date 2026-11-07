package envelope

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
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
// Path is a JSON pointer (RFC 6901) made from trusted schema property names
// and array indices. An untrusted property is replaced with UnknownProperty;
// "" is the document root.
type SchemaError struct {
	Path    string
	Message string
}

// UnknownProperty is the fixed path token for a caller-controlled property
// name. Such names may themselves contain credentials or signed URLs.
const UnknownProperty = "<unknown>"

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
// "attachmentReference". Send requests admit grant-aware references;
// backend availability and ownership are checked by the hub.
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
	return firstViolation(verr, sch, v)
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
func firstViolation(root *jsonschema.ValidationError, sch *jsonschema.Schema, v any) *SchemaError {
	path := func(loc []string) string { return trustedPointer(sch, v, loc) }
	var cands []candidate
	collect(root, &cands, path, nil)
	if len(cands) == 0 { // not expected; keep a usable answer
		return &SchemaError{Path: path(root.InstanceLocation), Message: root.ErrorKind.LocalizedString(printer)}
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

func collect(e *jsonschema.ValidationError, out *[]candidate, path func([]string) string, parentLoc []string) {
	switch k := e.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		// Report the failed alternative at its own location rather than
		// guessing which branch the sender meant.
		*out = append(*out, candidate{branchy: true, path: path(e.InstanceLocation), msg: branchMessage(e, path)})
		return
	case *kind.AdditionalProperties:
		// v6 reports these at the parent object. Never retain the raw names
		// in either the path or message, even if another schema names them.
		*out = append(*out, candidate{path: path(append(clone(e.InstanceLocation), UnknownProperty)), msg: "unknown property"})
		return
	case *kind.Required:
		for _, p := range k.Missing {
			*out = append(*out, candidate{path: path(append(clone(e.InstanceLocation), p)), msg: fmt.Sprintf("missing required property %q", p)})
		}
		return
	case *kind.PropertyNames:
		// Its child pattern/enum errors validate the property name as a
		// value and may echo it in their messages. v6.0.3 also assigns this
		// error's InstanceLocation without cloning the validator's shared
		// slice, so later sibling checks can overwrite it. Use the enclosing
		// error's copied location (the same object for a reference, or its
		// nearest enclosing instance) instead of trusting that mutable path.
		*out = append(*out, candidate{path: path(append(clone(parentLoc), UnknownProperty)), msg: "invalid property name"})
		return
	}
	if len(e.Causes) == 0 {
		*out = append(*out, candidate{path: path(e.InstanceLocation), msg: e.ErrorKind.LocalizedString(printer)})
		return
	}
	for _, c := range e.Causes {
		collect(c, out, path, e.InstanceLocation)
	}
}

// branchMessage summarises why every alternative of an anyOf/oneOf failed.
func branchMessage(e *jsonschema.ValidationError, path func([]string) string) string {
	msg := e.ErrorKind.LocalizedString(printer)
	var parts []string
	for _, c := range e.Causes {
		var sub []candidate
		collect(c, &sub, path, e.InstanceLocation)
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

// trustedPointer walks only explicit schema properties at each object level.
// Free-form map keys (including numeric keys) are not schema field names.
// Stop at the first such key rather than disclose its value or descendants.
func trustedPointer(sch *jsonschema.Schema, v any, loc []string) string {
	schemas := []*jsonschema.Schema{sch}
	var safe []string
	for _, tok := range loc {
		var next []*jsonschema.Schema
		switch value := v.(type) {
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(value) || strconv.Itoa(i) != tok {
				return pointer(append(safe, UnknownProperty))
			}
			for _, s := range schemaAlternatives(schemas) {
				if s.Items2020 != nil {
					next = append(next, s.Items2020)
				}
				if i < len(s.PrefixItems) {
					next = append(next, s.PrefixItems[i])
				}
				switch items := s.Items.(type) {
				case *jsonschema.Schema:
					next = append(next, items)
				case []*jsonschema.Schema:
					if i < len(items) {
						next = append(next, items[i])
					}
				}
			}
			v = value[i]
		default:
			known := false
			for _, s := range schemaAlternatives(schemas) {
				if child, ok := s.Properties[tok]; ok {
					known = true
					next = append(next, child)
				}
				for _, name := range s.Required {
					if name == tok {
						known = true
					}
				}
			}
			if !known {
				return pointer(append(safe, UnknownProperty))
			}
			if obj, ok := v.(map[string]any); ok {
				v = obj[tok]
			} else {
				v = nil
			}
		}
		safe = append(safe, tok)
		schemas = next
	}
	return pointer(safe)
}

// schemaAlternatives follows schema links that describe the same instance
// location. References may be cyclic; visit each schema only once per level.
func schemaAlternatives(roots []*jsonschema.Schema) []*jsonschema.Schema {
	var out []*jsonschema.Schema
	seen := map[*jsonschema.Schema]bool{}
	var visit func(*jsonschema.Schema)
	visit = func(s *jsonschema.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
		visit(s.Ref)
		visit(s.RecursiveRef)
		if s.DynamicRef != nil {
			visit(s.DynamicRef.Ref)
		}
		for _, group := range [][]*jsonschema.Schema{s.AllOf, s.AnyOf, s.OneOf} {
			for _, child := range group {
				visit(child)
			}
		}
		visit(s.If)
		visit(s.Then)
		visit(s.Else)
	}
	for _, s := range roots {
		visit(s)
	}
	return out
}

// pointer renders an instance location as an RFC 6901 JSON pointer.
func pointer(loc []string) string {
	var b strings.Builder
	for _, tok := range loc {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(tok))
	}
	return b.String()
}
