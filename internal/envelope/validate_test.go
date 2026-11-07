package envelope_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

const examplesDir = "../../docs/protocol/examples"

// loadExample reads an example file and strips its top-level "$comment".
func loadExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(examplesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	delete(doc, "$comment")
	return doc
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestExamplesValidate mirrors scripts/validate_examples.py. Examples 04 and
// 05 are SendRequests (their $comment and the python script say so) and 06 is
// an object whose "events" array holds Events; 07 is .http and skipped.
func TestExamplesValidate(t *testing.T) {
	cases := map[string]string{
		"01-send-request.json":            "sendRequest",
		"02-delivery-direct.json":         "delivery",
		"03-delivery-group-hold.json":     "delivery",
		"04-approval-request.json":        "sendRequest",
		"05-job-status.json":              "sendRequest",
		"08-github-ingress-delivery.json": "delivery",
		"09-reaction-delivery.json":       "delivery",
		"10-reaction-event.json":          "event",
	}
	for f, def := range cases {
		doc := loadExample(t, f)
		if err := envelope.Validate(def, mustJSON(t, doc)); err != nil {
			t.Errorf("%s as %s: %v", f, def, err)
		}
	}
	doc := loadExample(t, "06-events-stream.json")
	events, ok := doc["events"].([]any)
	if !ok || len(events) == 0 {
		t.Fatalf("06: events array missing: %#v", doc["events"])
	}
	for i, e := range events {
		if err := envelope.Validate("event", mustJSON(t, e)); err != nil {
			t.Errorf("06[%d] as event: %v", i, err)
		}
	}
}

func TestReactionMessageSchema(t *testing.T) {
	base := func() map[string]any { return loadExample(t, "02-delivery-direct.json")["message"].(map[string]any) }
	ref := map[string]any{"target": "msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P", "emoji": "✅"}
	for _, kind := range []string{"reaction", "message", "job_status", "approval_request"} {
		m := base()
		m["kind"], m["reaction"] = kind, ref
		if kind == "job_status" {
			m["job_status"] = loadExample(t, "05-job-status.json")["job_status"]
		}
		if kind == "approval_request" {
			m["approval_request"] = loadExample(t, "04-approval-request.json")["approval_request"]
		}
		err := envelope.Validate("message", mustJSON(t, m))
		if (err == nil) != (kind == "reaction") {
			t.Errorf("kind %s: %v", kind, err)
		}
	}
	m := base()
	m["kind"] = "reaction"
	if err := envelope.Validate("message", mustJSON(t, m)); err == nil {
		t.Fatal("reaction without ref accepted")
	}
	m["reaction"] = ref
	for _, field := range []string{"job_request", "job_status", "approval_request", "approval_response"} {
		var payload any
		switch field {
		case "job_request":
			payload = map[string]any{"version": 1, "job_key": "k", "operation": "op", "instructions": "do", "input": map[string]any{}}
		case "job_status":
			payload = loadExample(t, "05-job-status.json")[field]
		case "approval_request":
			payload = loadExample(t, "04-approval-request.json")[field]
		case "approval_response":
			payload = map[string]any{"approval_id": "apr_01JD3Q5H0X2Z4B6D8F0H2K4M6P", "decision": "yes"}
		}
		m[field] = payload
		if err := envelope.Validate("message", mustJSON(t, m)); err == nil {
			t.Errorf("reaction carrying %s accepted", field)
		}
		delete(m, field)
	}
}

func TestSendRequestRefusesReactionKind(t *testing.T) {
	doc := loadExample(t, "01-send-request.json")
	doc["kind"] = "reaction"
	var se *envelope.SchemaError
	if err := envelope.Validate("sendRequest", mustJSON(t, doc)); !errors.As(err, &se) || se.Path != "/kind" {
		t.Fatalf("want /kind error, got %v", err)
	}
}

func TestReactionEventAndRequestSchema(t *testing.T) {
	for def, doc := range map[string]map[string]any{
		"reaction":        {"target": "msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P", "emoji": "✅"},
		"reactionRequest": {"message_id": "msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P", "emoji": "🙂", "op": "add", "as": "peer_01JD3Q2K8M4N6P8R0T2V4X6Z8C", "priority": "hold"},
		"reactionEvent":   {"message_id": "msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P", "thread_id": "thr_01JD3Q5G9Z1B3D5F7H9K1M3P5R", "peer_id": "peer_01JD3Q2K8M4N6P8R0T2V4X6Z8C", "emoji": "✅", "op": "remove", "at": "2026-11-03T00:00:00.000Z"},
	} {
		if err := envelope.Validate(def, mustJSON(t, doc)); err != nil {
			t.Errorf("%s: %v", def, err)
		}
		doc["unknown"] = true
		if err := envelope.Validate(def, mustJSON(t, doc)); err == nil {
			t.Errorf("%s accepted unknown field", def)
		}
		delete(doc, "unknown")
		for field, value := range doc {
			if def == "reactionRequest" && (field == "as" || field == "priority") {
				continue
			}
			delete(doc, field)
			if err := envelope.Validate(def, mustJSON(t, doc)); err == nil {
				t.Errorf("%s missing %s accepted", def, field)
			}
			doc[field] = value
		}
	}
	if err := envelope.Validate("reactionRequest", []byte(`{"message_id":"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P","emoji":"✅","op":"add"}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"target":"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P","emoji":"❤"}`, `{"target":"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P","emoji":"🙂"}`} {
		if err := envelope.Validate("reaction", []byte(raw)); err == nil {
			t.Errorf("accepted noncanonical ref %s", raw)
		}
	}
	for _, raw := range []string{`{"message_id":"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P","emoji":"x","op":"toggle"}`, `{"message_id":"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P","emoji":"x","op":"add","priority":"urgent"}`, `{"message_id":"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P","emoji":"123456789012345678901234567890123","op":"add"}`} {
		if err := envelope.Validate("reactionRequest", []byte(raw)); err == nil {
			t.Errorf("accepted bad request %s", raw)
		}
	}
}

// TestNegativeExamples mirrors the negative checks of scripts/validate_examples.py.
func TestNegativeExamples(t *testing.T) {
	bad := loadExample(t, "02-delivery-direct.json")
	bad["message"].(map[string]any)["kind"] = "job_status"
	var se *envelope.SchemaError
	if err := envelope.Validate("delivery", mustJSON(t, bad)); !errors.As(err, &se) {
		t.Errorf("kind=job_status without job_status: want *SchemaError, got %v", err)
	}
	bad2 := loadExample(t, "01-send-request.json")
	bad2["recipients"] = []any{"Mira"}
	se = nil
	if err := envelope.Validate("sendRequest", mustJSON(t, bad2)); !errors.As(err, &se) {
		t.Errorf("name-addressed recipient: want *SchemaError, got %v", err)
	} else if !strings.HasPrefix(se.Path, "/recipients/0") {
		t.Errorf("name-addressed recipient: path %q, want /recipients/0", se.Path)
	}
}

func TestUnknownFieldIsSchemaError(t *testing.T) {
	err := envelope.Validate("sendRequest", []byte(`{"idempotency_key":"k","priority":"wake","content":{"text":"hi"},"sender":"peer_x"}`))
	var se *envelope.SchemaError
	if !errors.As(err, &se) {
		t.Fatal(err)
	}
	if se.Path != "/<unknown>" {
		t.Fatalf("path %q, want /<unknown>", se.Path)
	}
	if se.Message == "" || se.Error() == "" {
		t.Fatalf("empty message: %#v", se)
	}
}

func TestMissingPriorityIsSchemaError(t *testing.T) {
	err := envelope.Validate("sendRequest", []byte(`{"idempotency_key":"k","content":{"text":"hi"},"recipients":["peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A"]}`))
	var se *envelope.SchemaError
	if !errors.As(err, &se) || !strings.Contains(se.Path, "priority") {
		t.Fatal(err)
	}
	if se.Path != "/priority" {
		t.Fatalf("path %q, want /priority", se.Path)
	}
}

func TestNestedViolationPath(t *testing.T) {
	err := envelope.Validate("sendRequest", []byte(`{"idempotency_key":"k","priority":"wake","content":{"text":"hi","bogus":1},"recipients":["peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A"]}`))
	var se *envelope.SchemaError
	if !errors.As(err, &se) || se.Path != "/content/<unknown>" {
		t.Fatalf("want path /content/<unknown>, got %v", err)
	}
}

func TestWrongTypeIsSchemaError(t *testing.T) {
	err := envelope.Validate("sendRequest", []byte(`{"idempotency_key":"k","priority":"wake","content":{"text":"hi"},"recipients":["peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A"],"max_depth":"eight"}`))
	var se *envelope.SchemaError
	if !errors.As(err, &se) || se.Path != "/max_depth" {
		t.Fatalf("want path /max_depth, got %v", err)
	}
}

func TestMalformedJSONIsSchemaError(t *testing.T) {
	for _, raw := range []string{`{`, `{"a":1} trailing`, ``, `[1,`} {
		var se *envelope.SchemaError
		if err := envelope.Validate("sendRequest", []byte(raw)); !errors.As(err, &se) {
			t.Errorf("%q: want *SchemaError, got %v", raw, err)
		}
	}
}

func TestUnknownDefIsError(t *testing.T) {
	err := envelope.Validate("nope", []byte(`{}`))
	if err == nil {
		t.Fatal("want error for unknown def")
	}
	var se *envelope.SchemaError
	if errors.As(err, &se) {
		t.Fatal("unknown def is a programming error, not a *SchemaError")
	}
}

func TestDeliveryRoundTrip(t *testing.T) {
	doc := loadExample(t, "02-delivery-direct.json")
	var d envelope.Delivery
	if err := json.Unmarshal(mustJSON(t, doc), &d); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate("delivery", out); err != nil {
		t.Fatalf("round-tripped delivery invalid: %v\n%s", err, out)
	}
	if d.Recipient != "peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A" {
		t.Fatalf("recipient %q", d.Recipient)
	}
	if d.Via != nil || d.Message.InReplyTo != nil || d.Message.Origin != nil {
		t.Fatalf("nullable fields should decode to nil: %+v", d)
	}
}

func TestEmptyMessageMarshalsValidCollections(t *testing.T) {
	// A hub-built Message with nil slices/maps must still satisfy the schema's
	// array/object types (null is not an array).
	ts := "2026-11-01T06:12:03.412Z"
	thr := "thr_01JD3Q5G9Z1B3D5F7H9K1M3P5R"
	d := envelope.Delivery{
		SchemaVersion: envelope.SchemaVersion,
		DeliveryID:    "dly_01JD3Q5H2A4C6E8G0J2M4N6P8R",
		Recipient:     "peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A",
		Attempt:       1,
		Ingress:       "http_send",
		Thread:        envelope.ThreadSummary{ThreadID: thr, MessageCount: 1, HistoryAccess: "from_join"},
		Message: envelope.Message{
			SchemaVersion: envelope.SchemaVersion,
			MessageID:     "msg_01JD3Q5H0X2Z4B6D8F0H2K4M6P",
			ThreadID:      thr,
			Kind:          "message",
			Sender:        "hub",
			Recipients:    []string{"peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A"},
			Priority:      "hold",
			CreatedAt:     ts,
			Content:       envelope.Content{Text: "x"},
			Loop:          envelope.Loop{MaxDepth: 32, ExpiresAt: ts},
		},
	}
	if err := envelope.Validate("delivery", mustJSON(t, d)); err != nil {
		t.Fatal(err)
	}
}

func TestSendRequestRoundTrip(t *testing.T) {
	for _, f := range []string{"01-send-request.json", "04-approval-request.json", "05-job-status.json"} {
		var sr envelope.SendRequest
		if err := json.Unmarshal(mustJSON(t, loadExample(t, f)), &sr); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := envelope.Validate("sendRequest", mustJSON(t, sr)); err != nil {
			t.Errorf("%s round trip: %v", f, err)
		}
	}
}

func TestEventRoundTrip(t *testing.T) {
	doc := loadExample(t, "06-events-stream.json")
	for i, e := range doc["events"].([]any) {
		var ev envelope.Event
		if err := json.Unmarshal(mustJSON(t, e), &ev); err != nil {
			t.Fatal(err)
		}
		if err := envelope.Validate("event", mustJSON(t, ev)); err != nil {
			t.Errorf("06[%d] round trip: %v", i, err)
		}
	}
}

func TestFormatTimeMillis(t *testing.T) {
	got := envelope.FormatTime(time.Date(2026, 11, 1, 6, 12, 3, 412_000_000, time.UTC))
	if got != "2026-11-01T06:12:03.412Z" {
		t.Fatalf("got %q", got)
	}
	// Non-UTC input is converted; sub-millisecond digits are truncated.
	loc := time.FixedZone("AEDT", 11*3600)
	got = envelope.FormatTime(time.Date(2026, 11, 1, 17, 12, 3, 412_999_999, loc))
	if got != "2026-11-01T06:12:03.412Z" {
		t.Fatalf("non-UTC: got %q", got)
	}
}

func TestParseTime(t *testing.T) {
	tm, err := envelope.ParseTime("2026-11-01T06:12:03.412Z")
	if err != nil {
		t.Fatal(err)
	}
	if !tm.Equal(time.Date(2026, 11, 1, 6, 12, 3, 412_000_000, time.UTC)) || tm.Location() != time.UTC {
		t.Fatalf("got %v", tm)
	}
	for _, bad := range []string{"2026-11-01T06:12:03Z", "2026-11-01T06:12:03.41Z", "2026-11-01T06:12:03.412+00:00", "2026-11-01T06:12:03.4123Z", ""} {
		if _, err := envelope.ParseTime(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

// TestEmbeddedSchemaMatchesDocs guards against drift between the normative
// docs/protocol/envelope.schema.json and the embedded copy.
func TestEmbeddedSchemaMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/protocol/envelope.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	local, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(docs, local) {
		t.Fatal("internal/envelope/schema.json differs from docs/protocol/envelope.schema.json")
	}
}

func TestHubNoticeEventSchema(t *testing.T) {
	doc := loadExample(t, "11-hub-notice-event.json")
	if err := envelope.Validate("event", mustJSON(t, doc)); err != nil {
		t.Fatalf("example 11: %v", err)
	}
	var event envelope.Event
	if err := json.Unmarshal(mustJSON(t, doc), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "hub_notice" {
		t.Fatal(event.Type)
	}
	if err := envelope.Validate("event", mustJSON(t, event)); err != nil {
		t.Fatal(err)
	}
	payload := doc["data"].(map[string]any)
	for _, change := range []string{"posted", "cleared"} {
		payload["change"] = change
		if err := envelope.Validate("hubNoticeEvent", mustJSON(t, payload)); err != nil {
			t.Fatal(err)
		}
	}
	n := payload["notice"].(map[string]any)
	if err := envelope.Validate("hubNotice", mustJSON(t, n)); err != nil {
		t.Fatal(err)
	}
	n["unknown"] = true
	if err := envelope.Validate("hubNotice", mustJSON(t, n)); err == nil {
		t.Fatal("unknown notice field accepted")
	}
	if err := envelope.Validate("event", mustJSON(t, doc)); err == nil {
		t.Fatal("event accepted unknown notice field")
	}
	delete(n, "unknown")
	payload["change"] = "other"
	if err := envelope.Validate("hubNoticeEvent", mustJSON(t, payload)); err == nil {
		t.Fatal("unknown change accepted")
	}
	identity := map[string]any{"pubkey": "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "fingerprint": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "public_url": "https://hub.example", "key_created_at": "2026-11-05T07:00:00.000Z"}
	if err := envelope.Validate("hubIdentity", mustJSON(t, identity)); err != nil {
		t.Fatal(err)
	}
	identity["unknown"] = true
	if err := envelope.Validate("hubIdentity", mustJSON(t, identity)); err == nil {
		t.Fatal("unknown identity field accepted")
	}
}
