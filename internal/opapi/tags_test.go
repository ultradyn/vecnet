package opapi_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

func TestTagsFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/tags.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Event envelope.Event `json:"event"`
		Tags  []string       `json:"tags"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 7 {
		t.Fatal("fixture must cover every event type and an unknown type")
	}
	for i, row := range rows {
		if got := opapi.Tags(row.Event); !reflect.DeepEqual(got, row.Tags) {
			t.Errorf("row %d (%s): got %v, want %v", i, row.Event.Type, got, row.Tags)
		}
	}
	for _, kind := range []string{"delivery", "receipt", "thread_updated", "presence", "directory_updated", "approval_expired", "reaction", "unknown"} {
		for _, data := range []string{`{`, `null`, `[]`, `"text"`, `123`} {
			if got := opapi.Tags(envelope.Event{Type: kind, Data: json.RawMessage(data)}); len(got) != 0 {
				t.Errorf("%s with %s: got %v", kind, data, got)
			}
		}
	}
	for _, kind := range []string{"delivery", "receipt", "thread_updated", "approval_expired", "reaction"} {
		for _, data := range []string{`{}`, `{"message":{}}`, `{"thread_id":7}`, `{"thread_id":""}`} {
			if got := opapi.Tags(envelope.Event{Type: kind, Data: json.RawMessage(data)}); len(got) != 0 {
				t.Errorf("%s with missing/bad thread: %v", kind, got)
			}
		}
	}
}

func TestDisplayState(t *testing.T) {
	at := "2026-11-02T00:00:00.000Z"
	for _, tc := range []struct {
		state string
		read  *string
		want  string
	}{
		{"accepted", &at, "read"}, {"pushed", &at, "read"}, {"processed", &at, "processed"}, {"dead_letter", &at, "dead_letter"}, {"accepted", nil, "accepted"}, {"pushed", nil, "pushed"},
	} {
		if got := opapi.DisplayState(tc.state, tc.read); got != tc.want {
			t.Errorf("%s/%v = %s, want %s", tc.state, tc.read, got, tc.want)
		}
	}
}

func TestExcerpt(t *testing.T) {
	long := strings.Repeat("😀", 75) // 300 bytes, four bytes per rune.
	got := opapi.Excerpt(envelope.Message{Content: envelope.Content{Text: long}})
	if len(got) == 0 || len(got) > 200 || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Fatalf("bad truncation: %q (%d bytes)", got, len(got))
	}
	// The 200-byte cap falls inside a rune here, so only the rune-boundary
	// step keeps the cut valid: 1 + 49*4 = 197 bytes.
	offset := "x" + long
	if got := opapi.Excerpt(envelope.Message{Content: envelope.Content{Text: offset}}); len(got) != 197 || !utf8.ValidString(got) || !strings.HasPrefix(offset, got) {
		t.Fatalf("cut inside a rune: %q (%d bytes)", got, len(got))
	}
	for _, tc := range []struct {
		m    envelope.Message
		want string
	}{
		{envelope.Message{Content: envelope.Content{Text: "hello\nworld\r\nagain"}}, "hello world again"},
		{envelope.Message{Kind: "approval_request", ApprovalRequest: &envelope.ApprovalRequest{Question: "Ship\nit?"}}, "Approval: Ship it?"},
		{envelope.Message{Kind: "approval_response", ApprovalResponse: &envelope.ApprovalResponse{Decision: "yes"}}, "Decision: yes"},
		{envelope.Message{Kind: "job_status", JobStatus: &envelope.JobStatus{State: "running"}, Content: envelope.Content{Text: "half\ndone"}}, "Job running · half done"},
		{envelope.Message{}, ""},
	} {
		if got := opapi.Excerpt(tc.m); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestThreadRowJSONKeepsSiblings(t *testing.T) {
	raw, err := json.Marshal(opapi.ThreadRow{Thread: envelope.ThreadSummary{ThreadID: "thr_example"}, LastMessageAt: "2026-11-02T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Thread struct {
			ThreadID string `json:"thread_id"`
		} `json:"thread"`
		LastMessageAt string `json:"last_message_at"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Thread.ThreadID != "thr_example" || got.LastMessageAt != "2026-11-02T00:00:00.000Z" {
		t.Fatalf("siblings lost: %s", raw)
	}
}

func TestWireSlicesNeverNull(t *testing.T) {
	values := []any{opapi.PrincipalRow{}, opapi.OpenApproval{}, opapi.Inbox{}, opapi.ThreadPage{}, opapi.ThreadApproval{}, opapi.ThreadDetail{}, opapi.MatrixCell{}, opapi.MatrixRow{}, opapi.Matrix{}, opapi.Peer{}, opapi.PeerRow{}, opapi.PeerScopes{}, opapi.LoopCuts{}, opapi.DeliveryHealth{}, opapi.AuditEntry{}, opapi.AuditPage{}, opapi.History{}, opapi.SendResult{}, opapi.ReactionResult{}}
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type.Kind() != reflect.Slice {
				continue
			}
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			if string(fields[key]) != "[]" {
				t.Errorf("%s.%s = %s, want []", typ.Name(), field.Name, fields[key])
			}
		}
	}
}

func TestTagsOperatorView(t *testing.T) {
	for _, tc := range []struct {
		data string
		want []string
	}{
		{`{"reason":"read","thread_id":"thr_x"}`, []string{"inbox", "threads", "peers", "thread:thr_x"}},
		{`{"reason":"presence","thread_id":null}`, []string{"inbox", "threads", "peers"}},
		{`{"reason":"sent","thread_id":""}`, []string{"inbox", "threads", "peers"}},
		{`{"reason":7,"thread_id":"thr_x"}`, []string{}},
		{`{"reason":null,"thread_id":null}`, []string{}},
		{`{"reason":null,"Reason":"read","thread_id":null}`, []string{}},
		{`{"Reason":"read","thread_id":null}`, []string{}},
		{`{"reason":"read","THREAD_ID":null}`, []string{}},
		{`{"thread_id":null}`, []string{}},
		{`{"reason":"read"}`, []string{}},
		{`{"reason":"read","thread_id":7}`, []string{}},
		{`"x"`, []string{}},
	} {
		if got := opapi.Tags(envelope.Event{Type: "operator_view", Data: json.RawMessage(tc.data)}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.data, got, tc.want)
		}
	}
}

func TestNoticeTags(t *testing.T) {
	const notice = `{"notice_id":"ntc_01JD3Q5H0X2Z4B6D8F0H2K4M6P","kind":"maintenance","message":"hello","hub":"https://hub.example","issued_at":"2026-11-05T07:00:00.000Z","expires_at":"2026-11-06T07:00:00.000Z","sig":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	for _, tc := range []struct {
		data string
		want []string
	}{
		{`{"change":"posted","notice":` + notice + `}`, []string{"notices"}},
		{`{"change":"cleared","notice":` + strings.TrimSuffix(notice, "}") + `,"cleared_at":"2026-11-05T08:00:00.000Z","cleared_how":"operator"}}`, []string{"notices"}},
		{`{"change":"posted","notice":{"notice_id":"ntc_test","kind":7,"message":false}}`, []string{}},
		{`{"change":"cleared","notice":{"notice_id":"ntc_test"}}`, []string{}},
		{`{`, []string{}}, {`null`, []string{}}, {`[]`, []string{}}, {`{}`, []string{}},
		{`{"change":"posted","notice":null}`, []string{}}, {`{"change":"posted","notice":{"notice_id":7}}`, []string{}},
		{`{"change":"unknown","notice":{"notice_id":"ntc_test"}}`, []string{}},
		{`{"change":"posted","notice":{}}`, []string{}},
	} {
		if got := opapi.Tags(envelope.Event{Type: "hub_notice", Data: json.RawMessage(tc.data)}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.data, got, tc.want)
		}
	}
	for _, field := range []string{"notice_id", "kind", "message", "hub", "issued_at", "expires_at", "sig"} {
		for _, remove := range []bool{false, true} {
			var n map[string]any
			if err := json.Unmarshal([]byte(notice), &n); err != nil {
				t.Fatal(err)
			}
			if remove {
				delete(n, field)
			} else {
				n[field] = 7
			}
			raw, err := json.Marshal(map[string]any{"change": "posted", "notice": n})
			if err != nil {
				t.Fatal(err)
			}
			if got := opapi.Tags(envelope.Event{Type: "hub_notice", Data: raw}); len(got) != 0 {
				t.Errorf("malformed %s (removed %v): got %v", field, remove, got)
			}
		}
	}
}
