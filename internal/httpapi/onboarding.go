package httpapi

// Operator agent onboarding (PROTOCOL §§2.12–2.13, 4.5, 12, 14 and 20.1).
// Paths, status codes and no-store caching are Decisions §2 of
// docs/plans/2026-11-06-agent-onboarding-kinds.md. The admin middleware
// admits only the operator credential. These handlers call the hub
// onboarding services and do not reimplement field rules or submit
// sequencing. A new committed setup wakes the existing push worker.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/onboarding"
)

// onboardDiagnostics are the property names this adapter may repeat in an
// error. Anything else, including a credential or routine URL used as a
// JSON name, is replaced before the response is written.
var onboardDiagnostics = func() map[string]bool {
	names := map[string]bool{
		"kind": true, "spec_version": true, "request_id": true, "inputs": true,
	}
	for _, spec := range onboarding.Specs() {
		for _, f := range spec.Fields {
			names[f.Name] = true
		}
	}
	return names
}()

// adminAgentKinds is GET /v1/admin/agent-kinds: the hub kind registry.
// Listing does not grant write authority.
func (s *server) adminAgentKinds(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	kinds, err := s.Hub.OnboardingKinds(r.Context(), callerFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Kinds []onboarding.KindSpec `json:"kinds"`
	}{Kinds: kinds})
}

// adminOnboardAgent is POST /v1/admin/agents. A new commit is 201 and may
// include one-time human_output. A replay of the same request is 200 and
// omits that output. A new setup kicks the push worker after commit.
func (s *server) adminOnboardAgent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	req, err := readOnboardRequest(r)
	if err != nil {
		writeOnboardError(w, err)
		return
	}
	res, err := s.Hub.OnboardAgent(r.Context(), callerFrom(r.Context()), req)
	if err != nil {
		writeOnboardError(w, err)
		return
	}
	if res.Status == "created" {
		if res.Setup != nil {
			s.kick()
		}
		writeJSON(w, http.StatusCreated, res)
		return
	}
	res.HumanOutput = ""
	writeJSON(w, http.StatusOK, res)
}

// adminOnboardingStatus is GET /v1/admin/agents/onboarding/{request_id}.
// The body is the safe receipt: no human_output, routine URL, key or ciphertext.
func (s *server) adminOnboardingStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	res, err := s.Hub.OnboardingStatus(r.Context(), callerFrom(r.Context()), r.PathValue("request_id"))
	if err != nil {
		writeOnboardError(w, err)
		return
	}
	res.HumanOutput = ""
	writeJSON(w, http.StatusOK, res)
}

// readOnboardRequest reads a SubmitRequest. Unknown top-level keys, including
// a client-selected submit action, are 400 invalid_body before the hub runs.
// Values inside inputs are the hub's field rules.
func readOnboardRequest(r *http.Request) (onboarding.SubmitRequest, error) {
	top, err := readObject(r, []string{"kind", "spec_version", "request_id", "inputs"}, []string{"kind", "spec_version", "request_id", "inputs"})
	if err != nil {
		return onboarding.SubmitRequest{}, err
	}
	kind, err := stringField(top, "kind")
	if err != nil {
		return onboarding.SubmitRequest{}, err
	}
	spec, err := stringField(top, "spec_version")
	if err != nil {
		return onboarding.SubmitRequest{}, err
	}
	id, err := stringField(top, "request_id")
	if err != nil {
		return onboarding.SubmitRequest{}, err
	}
	inputs, err := inputsField(top["inputs"])
	if err != nil {
		return onboarding.SubmitRequest{}, err
	}
	return onboarding.SubmitRequest{Kind: kind, SpecVersion: spec, RequestID: id, Inputs: inputs}, nil
}

func inputsField(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, &envelope.SchemaError{Path: "/inputs", Message: "inputs must be a JSON object"}
	}
	var inputs map[string]any
	if err := json.Unmarshal(trimmed, &inputs); err != nil || inputs == nil {
		return nil, &envelope.SchemaError{Path: "/inputs", Message: "inputs must be a JSON object"}
	}
	return inputs, nil
}

// writeOnboardError is writeError after diagnostic names are allowlisted.
// An unrecognized top-level name becomes the fixed root path. An
// unrecognized inputs name becomes /inputs. Messages stay the hub's fixed text.
func writeOnboardError(w http.ResponseWriter, err error) {
	writeError(w, redactOnboardError(err))
}

func redactOnboardError(err error) error {
	var schema *envelope.SchemaError
	if errors.As(err, &schema) {
		if onboardTopPath(schema.Path) {
			return err
		}
		cp := *schema
		cp.Path = ""
		return &cp
	}
	var api *hub.APIError
	if errors.As(err, &api) {
		field, _ := api.Details["field"].(string)
		if field == "" || onboardDiagnostics[field] {
			return err
		}
		cp := *api
		cp.Details = make(map[string]any, len(api.Details))
		for k, v := range api.Details {
			if k != "field" {
				cp.Details[k] = v
			}
		}
		cp.Details["path"] = "/inputs"
		return &cp
	}
	return err
}

// onboardNoStore sets Cache-Control: no-store for the three onboarding
// routes before authentication and the body limit run. Handlers set it
// again for the responses they write themselves.
func onboardNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onboardRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func onboardRoute(path string) bool {
	switch path {
	case "/v1/admin/agent-kinds", "/v1/admin/agents":
		return true
	default:
		return strings.HasPrefix(path, "/v1/admin/agents/onboarding/")
	}
}

func onboardTopPath(path string) bool {
	if path == "" {
		return true
	}
	name, ok := strings.CutPrefix(path, "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return false
	}
	switch name {
	case "kind", "spec_version", "request_id", "inputs":
		return true
	default:
		return false
	}
}
