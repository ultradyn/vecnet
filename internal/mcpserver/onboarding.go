package mcpserver

// Operator agent onboarding over MCP (PROTOCOL §§4.1, 4.3, 4.5–4.6, 12 and
// 20.1). Kinds and status call the same hub services as the operator HTTP
// routes. vecnet_agent_add accepts no routine credential and does not call
// Hub.OnboardAgent: that would mint an enrol token or a link code, or create
// a grokbot peer, and the tool would then have to strip the secret. The human
// submits on the Add agent form. There is no pending-action record.

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/onboarding"
)

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
			"request_id":   schema{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$"},
			"inputs":       object(safeInputs),
		}, "kind", "spec_version", "request_id", "inputs"),
		output: object(schema{
			"request_id": schema{"type": "string"},
			"status":     schema{"type": "string", "enum": []string{"needs_human"}},
			"web_path":   schema{"type": "string"},
		}, "request_id", "status", "web_path"),
		annotations: readAnnotations(),
		call:        callAgentAdd,
	}, {
		name: "vecnet_agent_status",
		description: "Read-only: safe onboarding status for one request_id. " +
			"Omits invitation text, link codes, bearers and routine credentials. " +
			"auth_state is credential readiness on this hub. A connector name is not a connection. A whoami mismatch stays a stop.",
		input: object(schema{
			"request_id": schema{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$"},
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
			"auth_state":    schema{"type": "string", "enum": []string{onboarding.AuthUnlinked, onboarding.AuthPending, onboarding.AuthVerified}},
			"send_defaults": schema{"type": "object", "additionalProperties": schema{"type": "string"}},
			"status":        schema{"type": "string"},
			"web_path":      schema{"type": "string"},
		}),
		annotations: readAnnotations(),
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
	safe, err := validateAgentAdd(req)
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
		changed, err := agentRequestChanged(ctx, s, existing, safe)
		if err != nil {
			return nil, err
		}
		if changed {
			e := hub.E(http.StatusConflict, "idempotency_conflict", "request_id was already used with a different request")
			e.Details["request_id"] = safe.requestID
			return nil, e
		}
	}
	return map[string]string{
		"request_id": safe.requestID,
		"status":     "needs_human",
		"web_path":   agentWebPath(safe.kind, safe.name, safe.requestID),
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
	kind, requestID, name, connector string
}

// validateAgentAdd checks safe fields with the registry. Human-only inputs
// are rejected before Normalize runs, so a routine URL or key is never
// parsed. Grokbot's required human fields are filled with placeholders for
// that check only.
func validateAgentAdd(req onboarding.SubmitRequest) (agentSafe, error) {
	spec, ok := onboarding.Lookup(req.Kind)
	if !ok {
		return agentSafe{}, agentField(onboarding.CodeUnknownKind, "kind", "unknown peer kind")
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
			return agentSafe{}, agentField(onboarding.CodeUnknownField, f.Name, "unknown field")
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
		return agentSafe{}, agentFieldError(err)
	}
	return agentSafe{kind: n.Kind, requestID: n.RequestID, name: n.Name, connector: n.ConnectorName}, nil
}

func agentRequestChanged(ctx context.Context, s *Server, existing onboarding.SubmitResult, safe agentSafe) (bool, error) {
	if existing.Kind != safe.kind || existing.ConnectorName != safe.connector {
		return true, nil
	}
	if safe.name == "" {
		return false, nil
	}
	dir, err := s.Hub.Directory(ctx)
	if err != nil {
		return false, err
	}
	for _, peer := range dir {
		if peer.PeerID == existing.PeerID {
			return peer.Name != safe.name, nil
		}
	}
	return true, nil
}

func agentWebPath(kind, name, requestID string) string {
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("name", name)
	q.Set("request_id", requestID)
	return "/ui/#add-agent?" + q.Encode()
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
