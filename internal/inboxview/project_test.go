package inboxview

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
)

func TestInboxProjectionPreservesUnknownValuesAndInput(t *testing.T) {
	verified := true
	raw := json.RawMessage(`{"delivery_id":"dly_one","future":9007199254740993,"message":{"message_id":"msg_one","sender_info":{"name":"Sender","future":1.2300},"metadata":{"sender_chain":["keep"],"large":900719925474099312345},"loop":{"depth":2,"max_depth":32,"expires_at":"later","sender_chain":["src_origin","peer_sender"],"future":{"number":1e100}},"attachments":[{"future":true}]}}`)
	events := []envelope.Event{
		{Cursor: "e1.7", Type: "delivery", At: "original-at", Data: raw, BlobStates: map[string]blobwire.State{"attachment": {State: "available", RetentionClass: "keep", SHA256Verified: &verified, Integrity: &blobwire.Integrity{Algorithm: "sha256", Value: "0000000000000000000000000000000000000000000000000000000000000000", Verified: true}}}},
		{Cursor: "e1.8", Type: "receipt", Data: json.RawMessage(`{"sender_chain":["keep"],"future":9007199254740993}`)},
	}
	before, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Project(events, false)
	if err != nil || len(out) != 2 {
		t.Fatalf("Project: length=%d err=%v", len(out), err)
	}
	want := json.RawMessage(`{"delivery_id":"dly_one","future":9007199254740993,"message":{"message_id":"msg_one","sender_info":{"name":"Sender","future":1.2300},"metadata":{"sender_chain":["keep"],"large":900719925474099312345},"loop":{"depth":2,"max_depth":32,"expires_at":"later","future":{"number":1e100}},"attachments":[{"future":true}]}}`)
	if !reflect.DeepEqual(ergViewValue(t, out[0].Data), ergViewValue(t, want)) {
		t.Fatalf("projection changed more than loop.sender_chain: %s", out[0].Data)
	}
	if out[0].Cursor != events[0].Cursor || out[0].Type != events[0].Type || out[0].At != events[0].At || !reflect.DeepEqual(out[0].BlobStates, events[0].BlobStates) || !reflect.DeepEqual(out[1], events[1]) {
		t.Fatal("projection changed event metadata or a non-delivery event")
	}
	if after, err := json.Marshal(events); err != nil || !bytes.Equal(after, before) {
		t.Fatal("projection mutated source events")
	}
	// A changed Data buffer and the returned slice must not alias the input.
	out[0].Data[0] = ' '
	out[0].Cursor = "changed"
	if after, err := json.Marshal(events); err != nil || !bytes.Equal(after, before) {
		t.Fatal("output mutation reached source events")
	}
}

func TestInboxProjectionIncludeEmptyAndMissing(t *testing.T) {
	for _, raw := range []string{
		`{"message":{"loop":{"sender_chain":["src_first","peer_second"]}}}`,
		`{"message":{"loop":{"sender_chain":[],"depth":0}}}`,
		`{"message":{"loop":{"depth":0}}}`,
		`{"message":{"metadata":{"sender_chain":["keep"]}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			events := []envelope.Event{{Type: "delivery", Data: json.RawMessage(raw)}}
			full, err := Project(events, true)
			if err != nil || len(full) != 1 || !bytes.Equal(full[0].Data, events[0].Data) {
				t.Fatalf("opt-in did not preserve exact source data: %+v, %v", full, err)
			}
			compact, err := Project(events, false)
			if err != nil || len(compact) != 1 {
				t.Fatalf("compact: %+v, %v", compact, err)
			}
			var data struct {
				Message struct {
					Loop map[string]json.RawMessage `json:"loop"`
				} `json:"message"`
			}
			if err := json.Unmarshal(compact[0].Data, &data); err != nil {
				t.Fatal(err)
			}
			if _, present := data.Message.Loop["sender_chain"]; present {
				t.Fatal("compact output still has sender_chain, including an empty array")
			}
		})
	}
	for _, input := range [][]envelope.Event{nil, {}} {
		out, err := Project(input, false)
		if err != nil || len(out) != 0 {
			t.Fatalf("empty input: %+v, %v", out, err)
		}
	}
}

func TestInboxProjectionMalformedDeliveryDoesNotMutateInput(t *testing.T) {
	for _, raw := range []string{`{`, `{"message":[]}`, `{"message":{"loop":[]}}`} {
		t.Run(raw, func(t *testing.T) {
			events := []envelope.Event{
				{Type: "delivery", Data: json.RawMessage(`{"message":{"loop":{"sender_chain":["peer_first"]}}}`)},
				{Type: "delivery", Data: json.RawMessage(raw)},
			}
			first := append([]byte(nil), events[0].Data...)
			if _, err := Project(events, false); err == nil {
				t.Fatal("malformed delivery projection succeeded")
			}
			if !bytes.Equal(events[0].Data, first) || string(events[1].Data) != raw {
				t.Fatal("failed page projection mutated source events")
			}
		})
	}
}

func ergViewValue(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
