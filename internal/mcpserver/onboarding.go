package mcpserver

// Operator agent onboarding over MCP (PROTOCOL §§4.1, 4.3, 4.5–4.6, 12 and
// 20.1). Kinds and status call the same hub services as the operator HTTP
// routes. vecnet_agent_add accepts no routine credential and does not call
// Hub.OnboardAgent: that would mint an enrol token or a link code, or create
// a grokbot peer, and the tool would then have to strip the secret. The human
// submits on the Add agent form. There is no pending-action record.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/onboarding"
)

// agentRequestIDRE is the request id shape. It is checked before JSON Schema
// validation: the generic pattern renderer quotes the rejected value, and a
// request id is free text a caller can fill with a credential.
var agentRequestIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const agentRequestIDMessage = "request_id must be 1 to 128 characters: letters, digits, and . _ : -"

// defaultEnrolTTLSeconds is the enrolment lifetime Normalize stores when the
// caller omits enrol_ttl_seconds or sends 0. The fragment omits that default.
const defaultEnrolTTLSeconds = 86400

// mcpGrokURLPlaceholder and mcpGrokKeyPlaceholder exist only so the registry
// can check a grokbot name and connector without the model supplying the
// routine credential. They are never returned, stored or logged.
const (
	mcpGrokURLPlaceholder = "https://api2.cursor.sh/automations/webhook/00000000-0000-4000-8000-000000000000"
	mcpGrokKeyPlaceholder = "crsr_mcp-onboarding-placeholder"
)

func operatorAgentTool(name string) bool {
	switch name {
	case "vecnet_agent_kinds", "vecnet_agent_add", "vecnet_agent_status":
		return true
	default:
		return false
	}
}

// listedTools is the static catalogue. The three agent tools are operator
// discovery only, so an ordinary caller still sees the existing list. Kind
// scoping of the rest of the catalogue is a later change.
func listedTools(c auth.Caller) []map[string]any {
	listed := tools().listed
	if c.Kind == "operator" {
		return listed
	}
	out := make([]map[string]any, 0, len(listed))
	for _, entry := range listed {
		name, _ := entry["name"].(string)
		if operatorAgentTool(name) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func agentToolSpecs() []toolSpec {
	kinds := make([]string, 0, 6)
	for _, spec := range onboarding.Specs() {
		kinds = append(kinds, spec.Kind)
	}
	safeInputs := schema{
		"name":              schema{"type": "string"},
		"name_open":         schema{"type": "boolean"},
		"enrol_ttl_seconds": schema{"type": "integer", "minimum": 0, "maximum": 604800},
		"principal_id":      schema{"type": "string"},
		"connector_name":    schema{"type": "string"},
		"skip_mcp":          schema{"type": "boolean"},
	}
	return []toolSpec{{
		name: "vecnet_agent_kinds",
		description: "Read-only: list Add agent kinds for the operator, including prompt templates and field rules. " +
			"Listing grants no write. A connector name is not a connection and is not identity. A whoami mismatch stays a stop.",
		input:       object(schema{}),
		annotations: readAnnotations(),
		precheck:    precheckAgentKinds,
		call:        callAgentKinds,
	}, {
		name: "vecnet_agent_add",
		description: "Read-only: return a browser path where the operator finishes Add agent. " +
			"Accepts kind, spec_version, request_id and safe name or options only. " +
			"Returns request_id, status needs_human and web_path. " +
			"Creates no peer, route, token, link code or pending action, and returns no invite token, link code, bearer or routine credential. " +
			"A named connector is not a connection. A whoami mismatch stays a stop.",
		input: object(schema{
			"kind":         schema{"type": "string", "enum": kinds},
			"spec_version": schema{"type": "string", "minLength": 1},
			"request_id":   schema{"type": "string"},
			"inputs":       object(safeInputs),
		}, "kind", "spec_version", "request_id", "inputs"),
		annotations: readAnnotations(),
		precheck:    precheckAgentAdd,
		output: object(schema{
			"request_id": schema{"type": "string"},
			"status":     schema{"type": "string", "enum": []string{"needs_human"}},
			"web_path":   schema{"type": "string"},
		}, "request_id", "status", "web_path"),
		call: callAgentAdd,
	}, {
		name: "vecnet_agent_status",
		description: "Read-only: safe onboarding status for one request_id. " +
			"Omits invitation text, link codes, bearers and routine credentials. " +
			"auth_state is credential readiness on this hub. A connector name is not a connection. A whoami mismatch stays a stop.",
		input: object(schema{
			"request_id": schema{"type": "string"},
		}, "request_id"),
		output: object(schema{
			"request_id":     schema{"type": "string"},
			"kind":           schema{"type": "string"},
			"peer_id":        schema{"type": "string"},
			"principal_id":   schema{"type": "string"},
			"route_id":       schema{"type": "string"},
			"connector_name": schema{"type": "string"},
			"setup": object(schema{
				"thread_id":   schema{"type": "string"},
				"message_id":  schema{"type": "string"},
				"delivery_id": schema{"type": "string"},
				"state":       schema{"type": "string"},
			}),
			"auth_state":           schema{"type": "string", "enum": []string{onboarding.AuthUnlinked, onboarding.AuthPending, onboarding.AuthVerified}},
			"send_defaults":        schema{"type": "object", "additionalProperties": schema{"type": "string"}},
			"status":               schema{"type": "string"},
			"web_path":             schema{"type": "string"},
			"name":                 schema{"type": "string"},
			"spec_version":         schema{"type": "string"},
			"name_open":            schema{"type": "boolean"},
			"skip_mcp":             schema{"type": "boolean"},
			"enrol_ttl_seconds":    schema{"type": "integer"},
			"request_principal_id": schema{"type": "string"},
			"safe_complete":        schema{"type": "boolean"},
			"grokbot_transport":    schema{"type": "string", "enum": []string{"delivery_v1", "recipient_drain_v1"}},
		}),
		annotations: readAnnotations(),
		precheck:    precheckAgentStatus,
		call:        callAgentStatus,
	}}
}

func callAgentKinds(ctx context.Context, s *Server, c auth.Caller, _ []byte) (any, error) {
	if err := requireOperator(c); err != nil {
		return nil, err
	}
	kinds, err := s.Hub.OnboardingKinds(ctx, c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kinds": kinds}, nil
}

func callAgentStatus(ctx context.Context, s *Server, c auth.Caller, raw []byte) (any, error) {
	if err := requireOperator(c); err != nil {
		return nil, err
	}
	var args struct {
		RequestID string `json:"request_id"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	res, err := s.Hub.OnboardingStatus(ctx, c, args.RequestID)
	if err != nil {
		return nil, err
	}
	res.HumanOutput = ""
	return res, nil
}

func callAgentAdd(ctx context.Context, s *Server, c auth.Caller, raw []byte) (any, error) {
	if err := requireOperator(c); err != nil {
		return nil, err
	}
	var req onboarding.SubmitRequest
	if err := decodeArgs(raw, &req); err != nil {
		return nil, err
	}
	if req.Inputs == nil {
		req.Inputs = map[string]any{}
	}
	n, safe, err := validateAgentAdd(req)
	if err != nil {
		return nil, err
	}
	// OnboardingStatus authorizes again and reads a receipt the human HTTP
	// submit may already have stored. It does not create one.
	existing, err := s.Hub.OnboardingStatus(ctx, c, safe.requestID)
	if err != nil {
		if agentAPICode(err) != "not_found" {
			return nil, err
		}
	} else {
		conflict, err := agentRetryConflict(ctx, s, c, existing, n, safe)
		if err != nil {
			return nil, err
		}
		if conflict {
			e := hub.E(http.StatusConflict, "idempotency_conflict", "request_id was already used with a different request")
			e.Details["request_id"] = safe.requestID
			return nil, e
		}
	}
	return map[string]string{
		"request_id": safe.requestID,
		"status":     "needs_human",
		"web_path":   agentWebPath(safe),
	}, nil
}

// requireOperator refuses a caller Resolve did not bind to an enabled
// operator peer. A disabled operator peer leaves PeerID empty, and an
// ordinary peer or a forged as never reaches this check: as is not in the
// schema. The hub services check the credential again.
func requireOperator(c auth.Caller) error {
	if c.Kind != "operator" || c.PeerKind != "operator" || c.PeerID == "" {
		return hub.E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	return nil
}

type agentSafe struct {
	kind, specVersion, requestID, name, connector, principalID string
	grokbotTransport                                           string
	nameOpen, skipMCP                                          bool
	enrolTTL                                                   int
}

// validateAgentAdd checks safe fields with the registry. Human-only inputs
// are rejected before Normalize runs, so a routine URL or key is never
// parsed. Grokbot's required human fields are filled with placeholders for
// that check only.
func validateAgentAdd(req onboarding.SubmitRequest) (onboarding.Normalized, agentSafe, error) {
	// The read-only human handoff cannot carry transport into the existing UI.
	// Do not accept a value that agentWebPath would silently discard.
	if _, present := req.Inputs["grokbot_transport"]; present {
		return onboarding.Normalized{}, agentSafe{}, agentField(onboarding.CodeUnknownField, "grokbot_transport", "unknown field")
	}
	spec, ok := onboarding.Lookup(req.Kind)
	if !ok {
		return onboarding.Normalized{}, agentSafe{}, agentField(onboarding.CodeUnknownKind, "kind", "unknown peer kind")
	}
	inputs := make(map[string]any, len(req.Inputs)+2)
	for k, v := range req.Inputs {
		inputs[k] = v
	}
	for _, f := range spec.Fields {
		if !f.HumanOnly {
			continue
		}
		if _, present := inputs[f.Name]; present {
			return onboarding.Normalized{}, agentSafe{}, agentField(onboarding.CodeUnknownField, f.Name, "unknown field")
		}
		if f.Required && spec.SubmitAction == onboarding.ActionGrokbotSetup {
			switch f.Name {
			case "webhook_url":
				inputs[f.Name] = mcpGrokURLPlaceholder
			case "webhook_key":
				inputs[f.Name] = mcpGrokKeyPlaceholder
			}
		}
	}
	n, err := onboarding.Normalize(onboarding.SubmitRequest{
		Kind: req.Kind, SpecVersion: req.SpecVersion, RequestID: req.RequestID, Inputs: inputs,
	})
	if err != nil {
		return onboarding.Normalized{}, agentSafe{}, agentFieldError(err)
	}
	return n, agentSafe{
		kind: n.Kind, specVersion: n.SpecVersion, requestID: n.RequestID,
		name: n.Name, connector: n.ConnectorName, principalID: n.PrincipalID,
		nameOpen: n.NameOpen, skipMCP: n.SkipMCP, enrolTTL: n.EnrolTTLSeconds, grokbotTransport: n.GrokbotTransport,
	}, nil
}

// agentRetryConflict reports a different request under an existing receipt.
// A complete receipt, including an f497 grokbot receipt that stored name and
// the other safe options before safe_complete existed, compares that
// projection. Older grokbot receipts are missing those options. Their absent
// fields are not the request, and the MCP placeholder credential is not the
// human HMAC. Session history still uses the keyed digest, which covers no
// webhook secret. The compare does not read the directory or write a digest.
func agentRetryConflict(ctx context.Context, s *Server, c auth.Caller, existing onboarding.SubmitResult, n onboarding.Normalized, safe agentSafe) (bool, error) {
	if existing.SafeComplete || grokHistoricalComplete(existing) {
		return agentRequestChanged(existing, safe), nil
	}
	if existing.Kind == "grokbot" {
		return true, nil
	}
	match, err := s.Hub.OnboardingDigestMatches(ctx, c, n)
	if err != nil {
		return false, err
	}
	return !match, nil
}

// grokHistoricalComplete reports a pre-marker grokbot receipt whose safe
// projection was stored. Grokbot names are required, so a stored name means
// that writer recorded the options; skip_mcp false was omitted by encoding.
func grokHistoricalComplete(existing onboarding.SubmitResult) bool {
	return existing.Kind == "grokbot" && existing.Name != "" && existing.SpecVersion != ""
}

// agentRequestChanged reports whether safe differs from a complete receipt.
// It uses that receipt's original name and options, never the live directory.
func agentRequestChanged(existing onboarding.SubmitResult, safe agentSafe) bool {
	transport := existing.GrokbotTransport
	if existing.Kind == "grokbot" && transport == "" {
		transport = "delivery_v1"
	}
	return transport != safe.grokbotTransport || existing.Kind != safe.kind ||
		existing.SpecVersion != safe.specVersion ||
		existing.Name != safe.name ||
		existing.ConnectorName != safe.connector ||
		existing.NameOpen != safe.nameOpen ||
		existing.SkipMCP != safe.skipMCP ||
		existing.EnrolTTLSeconds != safe.enrolTTL ||
		existing.RequestPrincipalID != safe.principalID
}

// agentWebPath is the Task 9 handoff. kind, name and request_id are always
// present. Other safe options are included only when the normalized value
// is not the default, so an accepted option is visible to the form.
func agentWebPath(safe agentSafe) string {
	q := url.Values{}
	q.Set("kind", safe.kind)
	q.Set("name", safe.name)
	q.Set("request_id", safe.requestID)
	if safe.connector != "" {
		def, err := onboarding.ConnectorName(safe.name, "")
		if err != nil || safe.connector != def {
			q.Set("connector_name", safe.connector)
		}
	}
	if safe.skipMCP {
		q.Set("skip_mcp", "true")
	}
	if safe.nameOpen {
		q.Set("name_open", "true")
	}
	if safe.enrolTTL != 0 && safe.enrolTTL != defaultEnrolTTLSeconds {
		q.Set("enrol_ttl_seconds", strconv.Itoa(safe.enrolTTL))
	}
	if safe.principalID != "" {
		q.Set("principal_id", safe.principalID)
	}
	return "/ui/#add-agent?" + q.Encode()
}

var (
	agentAddKeys = map[string]struct{}{
		"kind": {}, "spec_version": {}, "request_id": {}, "inputs": {},
	}
	agentInputKeys = map[string]struct{}{
		"name": {}, "name_open": {}, "enrol_ttl_seconds": {}, "principal_id": {},
		"connector_name": {}, "skip_mcp": {},
	}
	agentStatusKeys = map[string]struct{}{"request_id": {}}
)

func precheckAgentKinds(raw []byte) error {
	return rejectUnknownKeys(raw, map[string]struct{}{}, "", nil)
}

func precheckAgentAdd(raw []byte) error {
	if err := rejectUnknownKeys(raw, agentAddKeys, "inputs", agentInputKeys); err != nil {
		return err
	}
	return precheckAgentRequestID(raw)
}

func precheckAgentStatus(raw []byte) error {
	if err := rejectUnknownKeys(raw, agentStatusKeys, "", nil); err != nil {
		return err
	}
	return precheckAgentRequestID(raw)
}

// rejectUnknownKeys reports an unknown property before JSON Schema rendering.
// The generic renderer quotes the key in both the message and the path.
// The path here is the container, and the message does not include the key.
func rejectUnknownKeys(raw []byte, allowed map[string]struct{}, nested string, nestedAllowed map[string]struct{}) error {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	for k := range body {
		if _, ok := allowed[k]; !ok {
			return &envelope.SchemaError{Path: "", Message: "unknown property"}
		}
	}
	if nested == "" {
		return nil
	}
	rawNested, ok := body[nested]
	if !ok {
		return nil
	}
	var child map[string]json.RawMessage
	if json.Unmarshal(rawNested, &child) != nil {
		return nil
	}
	for k := range child {
		if _, ok := nestedAllowed[k]; !ok {
			return &envelope.SchemaError{Path: "/" + nested, Message: "unknown property"}
		}
	}
	return nil
}

// precheckAgentRequestID rejects a malformed request id with fixed text.
// Schema validation would quote the rejected value in the -32602 message.
func precheckAgentRequestID(raw []byte) error {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	idRaw, ok := body["request_id"]
	if !ok {
		return nil
	}
	var id string
	if json.Unmarshal(idRaw, &id) != nil || !agentRequestIDRE.MatchString(id) {
		return &envelope.SchemaError{Path: "/request_id", Message: agentRequestIDMessage}
	}
	return nil
}

func agentAPICode(err error) string {
	var api *hub.APIError
	if errors.As(err, &api) {
		return api.Code
	}
	return ""
}

func agentFieldError(err error) error {
	var fe *onboarding.FieldError
	if !errors.As(err, &fe) {
		return err
	}
	return agentField(fe.Code, fe.Field, fe.Message)
}

func agentField(code, field, msg string) error {
	status := http.StatusUnprocessableEntity
	switch code {
	case onboarding.CodeUnknownKind, onboarding.CodeUnknownField, onboarding.CodeUnsupportedAction:
		status = http.StatusBadRequest
	case onboarding.CodeStaleSpec, onboarding.CodeConnectorNameConflict:
		status = http.StatusConflict
	}
	e := hub.E(status, code, msg)
	if field != "" {
		e.Details["field"] = field
	}
	return e
}
