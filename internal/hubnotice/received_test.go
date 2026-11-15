package hubnotice

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

func TestDecodeNoticeHTTPPolicyAndSignedBytes(t *testing.T) {
	key, err := hubid.KeyFromSeedHex(strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://Safe.Example:8443/Prefix/%2Fexact/", "https://[2001:db8::7]:8443/Prefix/", "http://public.example/Prefix/", "http://127.0.0.1:8080/Prefix/"} {
		for _, allow := range []bool{false, true} {
			name := raw + "/policy-strict"
			if allow {
				name = raw + "/policy-allow"
			}
			t.Run(name, func(t *testing.T) {
				n := envelope.HubNotice{NoticeID: "ntc_01JD3S6G8J0M2P4R6T8V0X0001", Kind: "moving", Hub: raw, NewURL: raw, IssuedAt: "2026-11-01T00:00:00.000Z", ExpiresAt: "2026-12-01T00:00:00.000Z", Message: "signed text"}
				if err := key.SignNotice(&n); err != nil {
					t.Fatal(err)
				}
				b, err := json.Marshal(n)
				if err != nil {
					t.Fatal(err)
				}
				got, err := DecodeNotice(b, allow)
				refused := !allow && raw == "http://public.example/Prefix/"
				if refused {
					if !errors.Is(err, ErrInvalidNotice) || got != (envelope.HubNotice{}) {
						t.Fatal("public HTTP accepted without caller permission")
					}
					return
				}
				if err != nil || !reflect.DeepEqual(got, n) || hubid.VerifyNotice(key.Pubkey(), got) != nil {
					t.Fatalf("signed bytes changed: %v", err)
				}
			})
		}
	}
}

func TestDecodeNoticeFixedRefusals(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `7`, `{}`, `{"hub":null}`, `{"hub":7}`, `{"hub":true}`, `{"hub":{}}`, `{"hub":[]}`, `{"hub":"https://safe.example","new_url":null}`, `{"hub":"https://safe.example","new_url":[]}`, `{"hub":"https://alice:CANARY@hub.example"}`, `{"hub":"https://hub.example?CANARY"}`, `{"hub":"https://hub.example#CANARY"}`, `{"hub":"https://[CANARY"}`} {
		t.Run(raw, func(t *testing.T) {
			n, err := DecodeNotice(json.RawMessage(raw), false)
			if err != ErrInvalidNotice || err.Error() != "invalid hub notice" || n != (envelope.HubNotice{}) {
				t.Fatalf("nonfixed refusal: %v", err)
			}
		})
	}
}

func TestRelocationHTTPPolicyAndInputUnchanged(t *testing.T) {
	details := map[string]any{"new_url": "http://public.example/Prefix/", "debug": "CANARY"}
	before, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	message, safe := ProjectRelocation(details, false)
	if message != "invalid hub relocation" || len(safe) != 0 {
		t.Fatal("relocation inferred HTTP permission")
	}
	message, safe = ProjectRelocation(details, true)
	if message != "hub moved" || !reflect.DeepEqual(safe, map[string]any{"new_url": details["new_url"]}) {
		t.Fatal("safe recovery fields lost")
	}
	safe["new_url"] = "https://other.example"
	after, err := json.Marshal(details)
	if err != nil || string(before) != string(after) {
		t.Fatal("projection changed input")
	}
}

func TestValidateBatchHTTPPolicyAndNoMutation(t *testing.T) {
	for _, field := range []string{"hub", "new_url"} {
		t.Run(field, func(t *testing.T) {
			n := envelope.HubNotice{Hub: "https://safe.example", NewURL: "https://next.example"}
			if field == "hub" {
				n.Hub = "http://public.example"
			} else {
				n.NewURL = "http://public.example"
			}
			raw, err := json.Marshal(envelope.HubNoticeEvent{Change: "cleared", Notice: n})
			if err != nil {
				t.Fatal(err)
			}
			events := []envelope.Event{{Type: "future_event", Data: json.RawMessage(`{"text":"CANARY arbitrary text"}`)}, {Type: "hub_notice", Data: raw}}
			before, err := json.Marshal(events)
			if err != nil {
				t.Fatal(err)
			}
			if ValidateBatch(events, false) != ErrInvalidNotice {
				t.Fatal("batch inferred public HTTP permission")
			}
			if err := ValidateBatch(events, true); err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(events)
			if err != nil || string(before) != string(after) {
				t.Fatal("batch validation changed event bytes")
			}
		})
	}
}

func TestProjectRelocationIndependentRecovery(t *testing.T) {
	key, err := hubid.KeyFromSeedHex(strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	n := envelope.HubNotice{NoticeID: "ntc_01JD3S6G8J0M2P4R6T8V0X0001", Kind: "moving", Hub: "https://Source.Example:8443/Prefix/%2Fexact/", NewURL: "https://[2001:db8::7]:8443/Next/%2Fexact/", IssuedAt: "2026-11-01T00:00:00.000Z", ExpiresAt: "2026-12-01T00:00:00.000Z", Message: "signed text"}
	if err := key.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name  string
		value any
	}{
		{"null", nil}, {"number", 7}, {"bool", true}, {"array", []any{}}, {"object", map[string]any{}},
		{"userinfo", "https://alice:CANARY@received.example"}, {"query", "https://received.example?CANARY"},
		{"fragment", "https://received.example#CANARY"}, {"malformed", "https://[CANARY"},
	}
	for _, tc := range bad {
		for _, field := range []string{"new_url", "notice", "notice-hub", "notice-new-url"} {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				details := map[string]any{"new_url": n.NewURL, "notice": n, "debug": "CANARY"}
				want := map[string]any{"new_url": n.NewURL}
				if field == "new_url" {
					details[field] = tc.value
					want = map[string]any{"notice": n}
				} else if field == "notice" {
					details[field] = tc.value
				} else {
					fields := map[string]any{"hub": n.Hub, "new_url": n.NewURL}
					if field == "notice-hub" {
						fields["hub"] = tc.value
					} else {
						fields["new_url"] = tc.value
					}
					details["notice"] = fields
				}
				before, err := json.Marshal(details)
				if err != nil {
					t.Fatal(err)
				}
				message, safe := ProjectRelocation(details, false)
				if message != "invalid hub relocation" || !reflect.DeepEqual(safe, want) {
					t.Fatalf("independent recovery lost or unsafe sibling retained: message=%q safe=%v", message, safe)
				}
				if accepted, ok := safe["notice"].(envelope.HubNotice); ok && (accepted != n || hubid.VerifyNotice(key.Pubkey(), accepted) != nil) {
					t.Fatal("accepted signed fields changed")
				}
				after, err := json.Marshal(details)
				if err != nil || string(before) != string(after) {
					t.Fatal("input mutated")
				}
				b, err := json.Marshal(safe)
				if err != nil || strings.Contains(string(b), "CANARY") {
					t.Fatal("unchecked sibling/unknown detail exposed")
				}
			})
		}
	}
	for _, field := range []string{"new_url", "notice"} {
		for _, allow := range []bool{false, true} {
			name := field + "/strict"
			if allow {
				name = field + "/allow"
			}
			t.Run(name, func(t *testing.T) {
				details := map[string]any{"new_url": n.NewURL, "notice": n}
				want := map[string]any{"new_url": n.NewURL}
				if field == "new_url" {
					details[field] = "http://public.example/Prefix/"
					want = map[string]any{"notice": n}
				} else {
					httpNotice := n
					httpNotice.Hub = "http://public.example/Prefix/"
					if err := key.SignNotice(&httpNotice); err != nil {
						t.Fatal(err)
					}
					details[field] = httpNotice
				}
				message := "invalid hub relocation"
				if allow {
					message = "hub moved"
					want = details
				}
				got, safe := ProjectRelocation(details, allow)
				if got != message || !reflect.DeepEqual(safe, want) {
					t.Fatalf("sibling HTTP policy changed: message=%q safe=%v", got, safe)
				}
			})
		}
	}
}
