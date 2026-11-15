package onboarding_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/onboarding"
)

func transportRequest() onboarding.SubmitRequest {
	return grokReq(map[string]any{"name": "Transport Bot", "webhook_url": "https://api2.cursor.sh/automations/webhook/00000000-0000-0000-0000-000000000001", "webhook_key": "crsr_SYNTHETIC_transport"})
}

func TestOnboardingOmittedTransportLegacy(t *testing.T) {
	spec, _ := onboarding.Lookup("grokbot")
	var found bool
	for _, f := range spec.Fields {
		if f.Name == "grokbot_transport" {
			found = true
			if f.Required || f.Type != onboarding.FieldEnum || f.Default != "delivery_v1" || !reflect.DeepEqual(f.Enum, []string{"delivery_v1", "recipient_drain_v1"}) {
				t.Fatalf("transport field = %+v", f)
			}
		}
	}
	if !found || spec.SpecVersion != "2026-11-06" {
		t.Error("optional transport missing or spec version changed")
	}
	req := transportRequest()
	for _, value := range []string{"", "delivery_v1", "recipient_drain_v1"} {
		if value != "" {
			req.Inputs["grokbot_transport"] = value
		}
		n, err := onboarding.Normalize(req)
		want := value
		if want == "" {
			want = "delivery_v1"
		}
		if err != nil || n.GrokbotTransport != want {
			t.Errorf("selection %q: effective %q error %v", value, n.GrokbotTransport, err)
		}
	}
	n, err := onboarding.Normalize(inviteReq("session", map[string]any{"name": "Session"}))
	if err != nil || n.GrokbotTransport != "" {
		t.Fatal("non-Grok acquired transport")
	}
}

func TestTransportInvalidNonGrokOrUnknown(t *testing.T) {
	for _, v := range []any{nil, 42, true, []any{"delivery_v1"}, map[string]any{}, "", "unknown-secret-canary"} {
		req := transportRequest()
		req.Inputs["grokbot_transport"] = v
		_, err := onboarding.Normalize(req)
		if err == nil || strings.Contains(err.Error(), "unknown-secret-canary") {
			t.Fatalf("invalid transport accepted or echoed: %v", err)
		}
	}
	for _, spec := range onboarding.Specs() {
		if spec.Kind == "grokbot" {
			continue
		}
		req := inviteReq(spec.Kind, map[string]any{"name": "Other", "grokbot_transport": "delivery_v1"})
		if _, err := onboarding.Normalize(req); err == nil {
			t.Errorf("%s accepted transport", spec.Kind)
		}
	}
}

func TestTransportDefaultDoesNotRenderDrainSupport(t *testing.T) {
	for _, mode := range []string{onboarding.CredentialHostedOAuth, onboarding.CredentialCLIBearer} {
		for _, selected := range []string{"", "delivery_v1", "recipient_drain_v1"} {
			values := map[string]string{"hub_url": "https://hub.example", "peer_id": "peer_expected", "thread_id": "thr_setup", "connector_name": "vecnet-helper", "profile": "helper", "credential_mode": mode}
			if selected != "" {
				values["grokbot_transport"] = selected
			}
			text, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseSetup, values)
			if err != nil {
				t.Errorf("render %s/%s: %v", mode, selected, err)
				continue
			}
			support := "This routine supports recipient-drain-v1"
			if selected == "recipient_drain_v1" {
				for _, s := range []string{support, "grokbot_wake", "version 1", "named recipient", "cursor order", "per-delivery processed", "safe cursor ack", "pending_count"} {
					if !strings.Contains(text, s) {
						t.Errorf("opted setup lacks %q", s)
					}
				}
			} else if strings.Contains(text, support) || strings.Contains(text, "grokbot_wake") {
				t.Fatal("legacy setup declares drain support")
			}
		}
	}
	for _, phase := range []string{onboarding.PhaseAuthorize, onboarding.PhaseSetup} {
		_, err := onboarding.RenderPrompt("grokbot", phase, map[string]string{"hub_url": "https://hub.example", "connector_name": "vecnet-helper", "peer_id": "peer_expected", "grokbot_transport": "unknown-secret-canary"})
		if err == nil || strings.Contains(err.Error(), "unknown-secret-canary") {
			t.Fatal("prompt accepted or echoed unvalidated transport")
		}
	}
}

func TestTransportPromptRejectsExplicitEmpty(t *testing.T) {
	_, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseSetup, map[string]string{"hub_url": "https://hub.example", "peer_id": "peer_expected", "connector_name": "vecnet-helper", "grokbot_transport": ""})
	if err == nil {
		t.Fatal("explicit empty transport accepted by prompt parser")
	}
}
