package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/XertroV/vecnet/internal/envelope"
)

// The fixture is the pre-projection tools/list captured by TestToolsBudget's
// red run at 0bf4beb, with prose removed and the existing hubValues exceptions
// applied. It is deliberately independent of the new projection and tables.
// These tests compare CALL validation, not the deliberately smaller discovery
// schemas. Existing HTTP tests cover the handlers and precheck refusal order.
func TestProjectionCallValidationEquivalent(t *testing.T) {
	raw, err := os.ReadFile("testdata/validation-before-projection.json")
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]schema
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	// Keep the original 23-tool fixture frozen. Master's additions were
	// independently captured from unprojected 6a3a17e (see testdata README).
	raw, err = os.ReadFile("testdata/validation-master-additions.json")
	if err != nil {
		t.Fatal(err)
	}
	var additions map[string]schema
	if err := json.Unmarshal(raw, &additions); err != nil {
		t.Fatal(err)
	}
	for name, doc := range additions {
		if _, exists := before[name]; exists {
			t.Fatalf("master fixture overwrites frozen baseline for %s", name)
		}
		before[name] = doc
	}
	const peer = "peer_00000000000000000000000001"
	const message = "msg_00000000000000000000000001"
	const thread = "thr_00000000000000000000000001"
	const group = "grp_00000000000000000000000001"
	const delivery = "dly_00000000000000000000000001"
	const send = `{"idempotency_key":"key-1","priority":"wake_idle","content":{"text":"hello"},"recipients":["` + peer + `"]}`
	const reply = `{"message_id":"` + message + `","text":"hello","priority":"hold","idempotency_key":"reply-1"}`
	type cases struct{ valid, invalid []string }
	table := map[string]cases{
		"vecnet_send": {
			[]string{send, strings.Replace(send, `"recipients":["`+peer+`"]`, `"thread_id":"`+thread+`"`, 1),
				withField(send, "origin", `{"system":"github","thread_id":"42"}`),
				withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain"}]`),
				withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","blob_id":"blo_00000000000000000000000001","retention_class":"keep","expires_at":null}]`),
				withField(send, "job_request", `{"version":1,"job_key":"j1","operation":"review","instructions":"Check it","input":{}}`),
				withField(send, "job_status", `{"job_id":"job_00000000000000000000000001","sequence":1,"state":"running","output":{}}`),
				withField(send, "approval_request", `{"approval_id":"apr_00000000000000000000000001","question":"Proceed?","options":["yes","no"],"expires_at":"2030-01-02T03:04:05.006Z"}`),
				withField(send, "approval_response", `{"approval_id":"apr_00000000000000000000000001","decision":"yes"}`),
				withField(send, "history_access", `"full"`)},
			[]string{withField(send, "origin", `{"system":"github"}`), withField(send, "origin", `null`),
				withField(send, "priority", `"invalid"`), withField(send, "idempotency_key", `"bad key"`),
				withField(send, "max_depth", `65`), withField(send, "job_request", `{}`),
				withField(send, "job_status", `{"job_id":"job_00000000000000000000000001","sequence":0,"state":"running"}`),
				withField(send, "approval_request", `{"approval_id":"apr_00000000000000000000000001","question":"Proceed?","options":["yes","no"],"expires_at":"bad"}`),
				withField(send, "approval_response", `{"approval_id":"apr_00000000000000000000000001"}`),
				withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","sha256_verified":true}]`),
				withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","integrity":{"algorithm":"sha256","value":"`+strings.Repeat("a", 64)+`","verified":true}}]`),
				withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","blob_id":"blo_00000000000000000000000001","retention_class":"keep","expires_at":"2030-01-02T03:04:05.006Z"}]`),
				withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","origin_url":"https://example.invalid"}]`)},
		},
		"vecnet_reply": {[]string{reply, withField(reply, "metadata", `{"source":"test"}`)}, []string{withField(reply, "origin", `{}`), withField(reply, "message_id", `"bad"`), withField(reply, "text", `null`)}},
		// Semantic enum refusals still belong to the hub, after authority checks.
		"vecnet_react":              {[]string{`{"message_id":"` + message + `","emoji":"👍"}`, `{"message_id":"` + message + `","emoji":"thumbsup","op":"unknown","priority":"unknown"}`}, []string{`{"message_id":"bad","emoji":"👍"}`, `{"message_id":"` + message + `","emoji":"` + strings.Repeat("x", 33) + `"}`}},
		"vecnet_inbox":              {[]string{`{}`, `{"after":"head","limit":1}`, `{"after":"e1.2","limit":200,"peer":"` + peer + `"}`, `{"limit":5.0}`}, []string{`{"after":"bad"}`, `{"limit":0}`, `{"limit":201}`, `{"limit":1.5}`}},
		"vecnet_subscriptions":      {[]string{`{}`}, []string{`{"peer":"` + peer + `"}`}},
		"vecnet_ack":                {[]string{`{"cursor":"e1.2"}`, `{"cursor":"e1.2","peer":"` + peer + `"}`}, []string{`{"cursor":"head"}`, `{"cursor":2}`}},
		"vecnet_receipt":            {[]string{`{"delivery_id":"` + delivery + `","state":"processed"}`}, []string{`{"delivery_id":"` + delivery + `","state":"accepted"}`}},
		"vecnet_presence":           {[]string{`{"state":"idle","at":"2030-01-02T03:04:05.006Z"}`, `{"state":"working","at":"2030-01-02T03:04:05.006Z","quota":{"weekly_remaining_pct":50},"location":""}`}, []string{`{"state":"unknown","at":"2030-01-02T03:04:05.006Z"}`, `{"state":"idle","at":"2030-01-02"}`, `{"state":"idle","at":"2030-01-02T03:04:05.006Z","peer_id":"` + peer + `"}`}},
		"vecnet_presence_subscribe": {[]string{`{"subscribe":"teams"}`}, []string{`{"subscribe":"messages"}`}},
		"vecnet_threads":            {[]string{`{}`, `{"participant":"me","group":"` + group + `","limit":2}`}, []string{`{"limit":201}`, `{"since":"yesterday"}`}},
		"vecnet_thread":             {[]string{`{"id":"` + thread + `"}`, `{"id":"` + thread + `","after":"` + message + `","limit":3}`}, []string{`{"id":"bad"}`, `{"id":"` + thread + `","after":"bad"}`}},
		"vecnet_peers":              {[]string{`{}`}, []string{`{"as":"` + peer + `"}`}},
		"vecnet_peer_create":        {[]string{`{"kind":"session","name":"peer","idempotency_key":"new-1"}`, `{"kind":"unknown","name":"","idempotency_key":"bad key"}`}, []string{`{"kind":1,"name":"peer","idempotency_key":"new-1"}`}},
		"vecnet_peer_set_disabled":  {[]string{`{"peer_id":"` + peer + `","disabled":true}`, `{"peer_id":"bad","disabled":false}`}, []string{`{"peer_id":"` + peer + `","disabled":"true"}`}},
		"vecnet_peer_lifecycle": {[]string{
			`{"peer_id":"` + peer + `","action":"disable"}`, `{"peer_id":"` + peer + `","action":"enable"}`,
			`{"peer_id":"` + peer + `","action":"archive"}`, `{"peer_id":"` + peer + `","action":"unarchive"}`,
			`{"peer_id":"bad","action":"unknown"}`,
		}, []string{`{"peer_id":1,"action":"disable"}`, `{"peer_id":"` + peer + `","action":false}`, `{"peer_id":"` + peer + `","action":"disable","unexpected":true}`}},
		"vecnet_peer_invite_get":    {[]string{`{"peer_id":"` + peer + `"}`, `{"peer_id":"bad"}`}, []string{`{"peer_id":1}`, `{"peer_id":"` + peer + `","unexpected":true}`}},
		"vecnet_peer_invite_revoke": {[]string{`{"peer_id":"` + peer + `"}`, `{"peer_id":"bad"}`}, []string{`{"peer_id":null}`, `{"peer_id":"` + peer + `","unexpected":true}`}},
		"vecnet_groups":             {[]string{`{}`}, []string{`{"limit":1}`}},
		"vecnet_group_create":       {[]string{`{"members":["` + peer + `"]}`}, []string{`{"members":[]}`, `{"members":["bad"]}`}},
		"vecnet_group_add":          {[]string{`{"group_id":"` + group + `","members":["` + peer + `"]}`}, []string{`{"group_id":"` + group + `","members":[]}`}},
		"vecnet_whoami":             {[]string{`{}`}, []string{`{"as":"` + peer + `"}`}},
		"vecnet_blob_put":           {[]string{`{"data_base64":"aGk=","media_type":"text/plain"}`, `{"data_base64":"","media_type":"text/plain","ttl_seconds":0}`}, []string{`{"data_base64":"aGk=","media_type":"text/plain","ttl_seconds":-1}`, `{"data_base64":"aGk=","media_type":"text/plain","ttl_seconds":2147483649}`}},
		"vecnet_blob_get":           {[]string{`{"sha256":"` + strings.Repeat("a", 64) + `"}`, `{"sha256":"bad","offset":0,"limit":131072}`}, []string{`{"sha256":"bad","offset":-1}`, `{"sha256":"bad","limit":131073}`}},
		// Exactly-one and base64 decoding are handler/precheck rules as before.
		"vecnet_set_avatar":   {[]string{`{}`, `{"data_base64":"aGk="}`, `{"url":"https://example.invalid/a.png"}`, `{"data_base64":"aGk=","url":"https://example.invalid/a.png"}`}, []string{`{"url":"http://example.invalid/a.png"}`, `{"data_base64":"` + strings.Repeat("a", 218457) + `"}`}},
		"vecnet_clear_avatar": {[]string{`{}`, `{"as":"` + peer + `"}`}, []string{`{"as":"bad"}`}},
		"vecnet_guide":        {[]string{`{}`, `{"topic":"replying"}`}, []string{`{"topic":"unknown"}`, `{"topic":null}`}},
	}
	if len(table) != len(before) || len(table) != len(tools().byName) {
		t.Fatalf("tool coverage: cases=%d before=%d after=%d", len(table), len(before), len(tools().byName))
	}
	// Round three's only timestamp acceptance change: valid RFC3339 inputs
	// formerly refused for spelling now normalize before the original schema.
	// Keep the frozen fixture unchanged so these exceptions remain explicit.
	normalizedAccepted := map[string][]string{
		"vecnet_threads":  {`{"since":"2030-01-02T03:04:05Z"}`, `{"since":"2030-01-02T13:04:05.123456+10:00"}`},
		"vecnet_presence": {`{"state":"idle","at":"2030-01-02T03:04:05Z"}`, `{"state":"idle","at":"2030-01-01T21:34:05.1-05:30"}`},
		"vecnet_send": {
			withField(send, "expires_at", `"2030-01-02T03:04:05Z"`),
			withField(send, "approval_request", `{"approval_id":"apr_00000000000000000000000001","question":"Proceed?","options":["yes","no"],"expires_at":"2030-01-02T13:04:05+10:00"}`),
			withField(send, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","blob_id":"blo_00000000000000000000000001","retention_class":"1h","expires_at":"2030-01-02T03:04:05.123456Z"}]`),
		},
		"vecnet_reply": {
			withField(reply, "expires_at", `"2030-01-02T13:04:05+10:00"`),
			withField(reply, "attachments", `[{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"media_type":"text/plain","blob_id":"blo_00000000000000000000000001","retention_class":"1h","expires_at":"2030-01-02T03:04:05Z"}]`),
		},
	}
	for name, tc := range table {
		t.Run(name, func(t *testing.T) {
			doc, ok := before[name]
			if !ok {
				t.Fatal("missing baseline schema")
			}
			original, err := compileBaseline(name, doc)
			if err != nil {
				t.Fatal(err)
			}
			current, ok := tools().byName[name]
			if !ok {
				t.Fatal("missing tool")
			}
			check := func(label, raw string, wantValid bool) {
				t.Helper()
				value, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				oldErr := envelope.ValidateValue(original, value)
				newErr := envelope.ValidateValue(current.schema, value)
				if (oldErr == nil) != wantValid {
					t.Fatalf("%s: baseline validity=%t, want %t: %v", label, oldErr == nil, wantValid, oldErr)
				}
				if !reflect.DeepEqual(oldErr, newErr) {
					t.Errorf("%s: before=%v after=%v", label, oldErr, newErr)
				}
			}
			for i, raw := range tc.valid {
				check(fmt.Sprintf("valid-%d", i), raw, true)
			}
			for i, raw := range append(tc.invalid, `[]`, `null`, `{"unexpected":true}`) {
				check(fmt.Sprintf("invalid-%d", i), raw, false)
			}
			for i, raw := range normalizedAccepted[name] {
				check(fmt.Sprintf("formerly-refused-timestamp-%d", i), raw, false)
				value, err := jsonschema.UnmarshalJSON(bytes.NewReader(normalizeToolTimestamps(name, []byte(raw))))
				if err != nil {
					t.Fatal(err)
				}
				if err := envelope.ValidateValue(current.schema, value); err != nil {
					t.Errorf("normalized timestamp %d refused: %v", i, err)
				}
			}
			// Check every required field independently, even for tools with
			// many optional parameters. All zero-argument tools still have
			// invalid shape/unknown-property cases above.
			required, _ := doc["required"].([]any)
			for _, field := range required {
				var args map[string]json.RawMessage
				if err := json.Unmarshal([]byte(tc.valid[0]), &args); err != nil {
					t.Fatal(err)
				}
				delete(args, field.(string))
				b, _ := json.Marshal(args)
				check("missing-"+field.(string), string(b), false)
			}
		})
	}
}

func compileBaseline(name string, doc schema) (*jsonschema.Schema, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	uri := "urn:vecnet:baseline:" + name
	if err := c.AddResource(uri, value); err != nil {
		return nil, err
	}
	return c.Compile(uri)
}

func withField(raw, key, value string) string {
	return strings.TrimSuffix(raw, "}") + `,"` + key + `":` + value + `}`
}
