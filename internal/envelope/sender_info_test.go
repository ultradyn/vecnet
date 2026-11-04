package envelope_test

import (
	"encoding/json"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

// TestSenderInfoSchema: Message.sender_info is optional (older hubs omit
// it), is exactly {name, kind}, and a SendRequest can never carry one: the
// hub sets it from the sender's directory entry (PROTOCOL §2.2 step 12).
func TestSenderInfoSchema(t *testing.T) {
	base := func() map[string]any { return loadExample(t, "02-delivery-direct.json")["message"].(map[string]any) }

	m := base()
	delete(m, "sender_info")
	if err := envelope.Validate("message", mustJSON(t, m)); err != nil {
		t.Fatalf("message without sender_info rejected: %v", err)
	}
	for _, info := range []map[string]any{
		{"name": "codex-hark", "kind": "session"},
		{"name": "hub", "kind": "hub"},
		{"name": "", "kind": "source"},
	} {
		m := base()
		m["sender_info"] = info
		if err := envelope.Validate("message", mustJSON(t, m)); err != nil {
			t.Errorf("sender_info %v rejected: %v", info, err)
		}
	}
	for _, bad := range []any{
		map[string]any{"name": "x"},
		map[string]any{"kind": "session"},
		map[string]any{"name": "x", "kind": "session", "avatar": nil},
		map[string]any{"name": 1, "kind": "session"},
		map[string]any{"name": "x", "kind": ""},
		nil,
		"x",
	} {
		m := base()
		m["sender_info"] = bad
		if err := envelope.Validate("message", mustJSON(t, m)); err == nil {
			t.Errorf("sender_info %#v accepted", bad)
		}
	}

	req := loadExample(t, "01-send-request.json")
	req["sender_info"] = map[string]any{"name": "spoof", "kind": "operator"}
	if err := envelope.Validate("sendRequest", mustJSON(t, req)); err == nil {
		t.Fatal("SendRequest carrying sender_info accepted")
	}
}

// TestSenderInfoRoundTrip: the Go Message keeps sender_info, and a Message
// without it marshals without the key (so legacy bytes round-trip).
func TestSenderInfoRoundTrip(t *testing.T) {
	doc := loadExample(t, "02-delivery-direct.json")
	var d envelope.Delivery
	if err := json.Unmarshal(mustJSON(t, doc), &d); err != nil {
		t.Fatal(err)
	}
	if d.Message.SenderInfo == nil || d.Message.SenderInfo.Name != "codex-hark" || d.Message.SenderInfo.Kind != "session" {
		t.Fatalf("sender_info = %+v", d.Message.SenderInfo)
	}
	d.Message.SenderInfo = nil
	out, err := json.Marshal(d.Message)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(out, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["sender_info"]; ok {
		t.Fatalf("nil sender_info marshalled: %s", out)
	}
}
