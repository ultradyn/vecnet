package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

type sendResp struct {
	MessageID  string `json:"message_id"`
	ThreadID   string `json:"thread_id"`
	Deliveries []struct {
		DeliveryID string  `json:"delivery_id"`
		Recipient  string  `json:"recipient"`
		Via        *string `json:"via"`
		State      string  `json:"state"`
	} `json:"deliveries"`
	Spilled bool `json:"spilled"`
}

func sendBody(key string, recipients ...string) map[string]any {
	b := map[string]any{
		"idempotency_key": key,
		"priority":        "wake",
		"content":         map[string]any{"text": "hi " + key},
	}
	if len(recipients) > 0 {
		b["recipients"] = recipients
	}
	return b
}

func postSend(t *testing.T, env *testhub.Env, secret string, body any, wantStatus int) sendResp {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST /v1/messages = %d, want %d; %s", resp.StatusCode, wantStatus, raw)
	}
	var sr sendResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	return sr
}

func rowCount(t *testing.T, env *testhub.Env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// lastMessage returns the message of the newest delivery Event in peer's log.
func lastMessage(t *testing.T, env *testhub.Env, peer string) (envelope.Message, string) {
	t.Helper()
	evs, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil || len(evs) == 0 {
		t.Fatalf("Read(%s) = %d events, %v", peer, len(evs), err)
	}
	var d struct {
		Ingress string           `json:"ingress"`
		Message envelope.Message `json:"message"`
	}
	if err := json.Unmarshal(evs[len(evs)-1].Data, &d); err != nil {
		t.Fatal(err)
	}
	return d.Message, d.Ingress
}

func groupOf(t *testing.T, env *testhub.Env, creator string, members ...string) string {
	t.Helper()
	g := ids.New(ids.Grp)
	now := envelope.FormatTime(env.Now.Now())
	if _, err := env.Hub.DB.Exec(`INSERT INTO groups(grp_id, created_by, created_at, last_active_at) VALUES (?, ?, ?, ?)`,
		g, creator, now, now); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := env.Hub.DB.Exec(`INSERT INTO group_members(grp_id, peer_id) VALUES (?, ?)`, g, m); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	body := sendBody("same-key", b)

	first := postSend(t, env, aSec, body, http.StatusAccepted)
	msgs := rowCount(t, env, `SELECT count(*) FROM messages`)
	dlys := rowCount(t, env, `SELECT count(*) FROM deliveries`)
	evs := rowCount(t, env, `SELECT count(*) FROM events`)

	// Same body, keys in another order and other whitespace: still the same body.
	reordered := `{ "content": {"text": "hi same-key"}, "recipients": ["` + b + `"], "priority": "wake", "idempotency_key": "same-key" }`
	for _, again := range []any{body, reordered} {
		resp, raw := env.Do(aSec, "POST", "/v1/messages", again)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replay = %d, want 200; %s", resp.StatusCode, raw)
		}
		var sr sendResp
		if err := json.Unmarshal(raw, &sr); err != nil {
			t.Fatal(err)
		}
		if sr.MessageID != first.MessageID || sr.ThreadID != first.ThreadID ||
			len(sr.Deliveries) != 1 || sr.Deliveries[0].DeliveryID != first.Deliveries[0].DeliveryID {
			t.Fatalf("replay body %+v, want the original %+v", sr, first)
		}
	}
	if m, d, e := rowCount(t, env, `SELECT count(*) FROM messages`), rowCount(t, env, `SELECT count(*) FROM deliveries`),
		rowCount(t, env, `SELECT count(*) FROM events`); m != msgs || d != dlys || e != evs {
		t.Fatalf("replay wrote rows: messages %d→%d deliveries %d→%d events %d→%d", msgs, m, dlys, d, evs, e)
	}

	changed := sendBody("same-key", b)
	changed["content"] = map[string]any{"text": "something else"}
	resp, raw := env.Do(aSec, "POST", "/v1/messages", changed)
	mustErr(t, resp, raw, http.StatusConflict, "idempotency_conflict")
	if m := rowCount(t, env, `SELECT count(*) FROM messages`); m != msgs {
		t.Fatalf("conflict wrote a message: %d → %d", msgs, m)
	}

	// The key is scoped per sender: B may use it freely.
	postSend(t, env, bSec, sendBody("same-key", b), http.StatusAccepted)
}

func TestRejections(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	_, dSec := env.NewPeer("session", "D")
	denied, _ := env.NewPeer("session", "Denied")
	if _, err := env.Hub.DB.Exec(`INSERT INTO scopes(peer_id, mode, target_id) VALUES (?, 'deny', ?)`, a, denied); err != nil {
		t.Fatal(err)
	}

	prn, prnSec := env.NewPrincipal("P")
	own, _, err := env.Hub.AddPeer(ctx, "session", "P1", prn)
	if err != nil {
		t.Fatal(err)
	}

	var crowd []string
	for i := 0; i < 64; i++ {
		p, _ := env.NewPeer("session", fmt.Sprintf("crowd%02d", i))
		crowd = append(crowd, p)
	}
	bigGroup := groupOf(t, env, a, crowd...)
	archived := groupOf(t, env, a, b)
	if _, err := env.Hub.DB.Exec(`UPDATE groups SET archived_at = ? WHERE grp_id = ?`,
		envelope.FormatTime(env.Now.Now()), archived); err != nil {
		t.Fatal(err)
	}

	// A locked thread created by A with B.
	locked := postSend(t, env, aSec, sendBody("locked-1", b), http.StatusAccepted)
	if _, err := env.Hub.DB.Exec(`UPDATE threads SET locked_participants = 1 WHERE thr_id = ?`, locked.ThreadID); err != nil {
		t.Fatal(err)
	}
	other := postSend(t, env, aSec, sendBody("other-1", c), http.StatusAccepted)

	// A group whose only member is A: fan-out skips the sender.
	selfGroup := createGroup(t, env, aSec, a)

	with := func(b map[string]any, kv ...any) map[string]any {
		for i := 0; i < len(kv); i += 2 {
			b[kv[i].(string)] = kv[i+1]
		}
		return b
	}
	cases := []struct {
		name   string
		secret string
		body   any
		status int
		code   string
	}{
		{"display name", aSec, sendBody("r1", "Max"), 422, "name_not_an_address"},
		{"display name among ids", aSec, sendBody("r1b", b, "max"), 422, "name_not_an_address"},
		{"unenrolled peer", aSec, sendBody("r2", "peer_01JD3Q2K8M4N6P8R0T2V4X6Z8Z"), 404, "unknown_recipient"},
		{"unknown group", aSec, sendBody("r2b", "grp_01JD3Q2K8M4N6P8R0T2V4X6Z8Z"), 404, "unknown_recipient"},
		{"group of only the sender", aSec, sendBody("r2c", selfGroup), 422, "no_recipients"},
		{"scope deny", aSec, sendBody("r3", denied), 403, "scope_denied"},
		{"reserved metadata", aSec, with(sendBody("r4", b), "metadata", map[string]any{"vecnet.x": 1}), 422, "reserved_metadata"},
		{"principal without as", prnSec, sendBody("r6", b), 422, "as_required"},
		{"as another principal's peer", prnSec, with(sendBody("r7", b), "as", a), 403, "forbidden"},
		{"peer credential as another peer", bSec, with(sendBody("r7b", c), "as", a), 403, "forbidden"},
		{"65th participant on a new thread", aSec, sendBody("r8", bigGroup), 422, "thread_full"},
		{"locked thread, non-creator adds", bSec, with(sendBody("r9", c), "thread_id", locked.ThreadID), 403, "participants_locked"},
		{"archived group", aSec, sendBody("r10", archived), 410, "group_archived"},
		{"unknown thread", aSec, with(sendBody("r11"), "thread_id", "thr_01JD3Q2K8M4N6P8R0T2V4X6Z8Z"), 404, "unknown_thread"},
		{"in_reply_to from another thread", aSec,
			with(sendBody("r12"), "thread_id", locked.ThreadID, "in_reply_to", other.MessageID), 422, "not_in_thread"},
		{"non-participant sends into thread", dSec, with(sendBody("r13"), "thread_id", locked.ThreadID), 403, "not_participant"},
		{"unknown field", aSec, with(sendBody("r14", b), "sender", a), 400, "invalid_body"},
		{"not JSON", aSec, `{"idempotency_key":`, 400, "invalid_body"},
	}
	before := rowCount(t, env, `SELECT count(*) FROM messages`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := env.Do(tc.secret, "POST", "/v1/messages", tc.body)
			mustErr(t, resp, raw, tc.status, tc.code)
		})
	}

	t.Run("missing priority", func(t *testing.T) {
		body := sendBody("r5", b)
		delete(body, "priority")
		resp, raw := env.Do(aSec, "POST", "/v1/messages", body)
		e := mustErr(t, resp, raw, 400, "invalid_body")
		path, _ := e.Error.Details["path"].(string)
		if !strings.Contains(path, "priority") {
			t.Fatalf("details.path = %q, want it to name priority", path)
		}
	})
	if after := rowCount(t, env, `SELECT count(*) FROM messages`); after != before {
		t.Fatalf("rejected sends stored %d messages", after-before)
	}

	// The accepted variants of the same shapes.
	sr := postSend(t, env, prnSec, with(sendBody("ok1", b), "as", own), http.StatusAccepted)
	if m, _ := lastMessage(t, env, b); m.Sender != own || m.MessageID != sr.MessageID {
		t.Fatalf("principal send: sender %q, want %q", m.Sender, own)
	}
	postSend(t, env, aSec, with(sendBody("ok2", c), "thread_id", locked.ThreadID), http.StatusAccepted)
}

func TestOriginIgnoredUnlessConnector(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	body := sendBody("o1", b)
	body["origin"] = map[string]any{"system": "github", "thread_id": "acme/repo#7", "event_id": "e1"}
	postSend(t, env, aSec, body, http.StatusAccepted)
	m, ingress := lastMessage(t, env, b)
	if m.Origin != nil {
		t.Fatalf("message.origin = %+v, want null for ingress http_send", m.Origin)
	}
	if ingress != "http_send" {
		t.Fatalf("ingress = %q, want http_send", ingress)
	}
}

func TestOperatorUIIngress(t *testing.T) {
	env := testhub.Start(t)
	b, _ := env.NewPeer("session", "B")

	req, _ := http.NewRequest("POST", env.URL+"/v1/messages",
		strings.NewReader(`{"idempotency_key":"op1","priority":"wake","content":{"text":"hi"},"recipients":["`+b+`"]}`))
	req.Header.Set("Authorization", "Bearer "+env.OperatorSecret)
	req.Header.Set("X-Vecnet-Ingress", "operator_ui")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("operator send = %d, want 202", resp.StatusCode)
	}
	m, ingress := lastMessage(t, env, b)
	if ingress != "operator_ui" || m.Sender != env.OperatorPeer {
		t.Fatalf("ingress %q sender %q, want operator_ui from %s", ingress, m.Sender, env.OperatorPeer)
	}

	// A non-operator cannot claim operator_ui.
	_, aSec := env.NewPeer("session", "A")
	req, _ = http.NewRequest("POST", env.URL+"/v1/messages",
		strings.NewReader(`{"idempotency_key":"a1","priority":"wake","content":{"text":"hi"},"recipients":["`+b+`"]}`))
	req.Header.Set("Authorization", "Bearer "+aSec)
	req.Header.Set("X-Vecnet-Ingress", "operator_ui")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, ingress := lastMessage(t, env, b); ingress != "http_send" {
		t.Fatalf("non-operator ingress = %q, want http_send", ingress)
	}
}

// postWithClient sends one message with an X-Vecnet-Client header.
func postWithClient(t *testing.T, env *testhub.Env, secret, key, to, header string) {
	t.Helper()
	req, _ := http.NewRequest("POST", env.URL+"/v1/messages",
		strings.NewReader(`{"idempotency_key":"`+key+`","priority":"wake","content":{"text":"hi"},"recipients":["`+to+`"],"metadata":{"mine":1}}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set(envelope.ClientHeader, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s = %d, want 202", key, resp.StatusCode)
	}
}

// The operator CLI's bare sends carry X-Vecnet-Client; the hub records a
// sanitized copy in metadata["vecnet.client"] for the operator credential
// only, and ignores the header from anyone else or when it is malformed.
func TestOperatorCLIClientMetadata(t *testing.T) {
	env := testhub.Start(t)
	b, _ := env.NewPeer("session", "B")

	postWithClient(t, env, env.OperatorSecret, "op1", b, `{"kind":"operator-cli","host":"x-tall\n<i>","ssh":true,"agent_suspected":true,"label":"the CEO"}`)
	m, ingress := lastMessage(t, env, b)
	if ingress != "http_send" || m.Sender != env.OperatorPeer {
		t.Fatalf("ingress %q sender %q", ingress, m.Sender)
	}
	got, _ := json.Marshal(m.Metadata[envelope.ClientMetadataKey])
	want := `{"agent_suspected":true,"host":"x-talli","kind":"operator-cli","label":"Operator on x-talli CLI via ssh (agent suspected)","ssh":true}`
	if string(got) != want {
		t.Fatalf("vecnet.client = %s, want %s", got, want)
	}
	if fmt.Sprint(m.Metadata["mine"]) != "1" {
		t.Fatalf("sender metadata lost: %v", m.Metadata)
	}

	// Malformed: the send goes through without the label.
	postWithClient(t, env, env.OperatorSecret, "op2", b, `operator-cli`)
	if m, _ := lastMessage(t, env, b); m.Metadata[envelope.ClientMetadataKey] != nil {
		t.Fatalf("malformed header recorded: %v", m.Metadata)
	}

	// A peer cannot claim to be the operator CLI.
	_, aSec := env.NewPeer("session", "A")
	postWithClient(t, env, aSec, "a1", b, `{"kind":"operator-cli","host":"xsm"}`)
	if m, _ := lastMessage(t, env, b); m.Metadata[envelope.ClientMetadataKey] != nil {
		t.Fatalf("peer header recorded: %v", m.Metadata)
	}

	// Nor write the key itself.
	body := sendBody("a2", b)
	body["metadata"] = map[string]any{envelope.ClientMetadataKey: map[string]any{"kind": "operator-cli"}}
	resp, raw := env.Do(aSec, "POST", "/v1/messages", body)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "reserved_metadata") {
		t.Fatalf("sender vecnet.client = %d %s, want 422 reserved_metadata", resp.StatusCode, raw)
	}
}
