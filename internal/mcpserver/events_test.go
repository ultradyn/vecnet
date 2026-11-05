package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// subscribeResult is the result object of events/subscribe.
type subscribeResult struct {
	ID            string  `json:"id"`
	RefreshBefore *string `json:"refreshBefore"`
	Cursor        string  `json:"cursor"`
	Truncated     bool    `json:"truncated"`
}

// eventBody is one event POST body.
type eventBody struct {
	EventID   string          `json:"eventId"`
	Name      string          `json:"name"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
	Cursor    string          `json:"cursor"`
}

func decodeEvent(t *testing.T, raw []byte) eventBody {
	t.Helper()
	var b eventBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("event body %s: %v", raw, err)
	}
	return b
}

func subParams(url, secret string, extra map[string]any) map[string]any {
	p := map[string]any{
		"name":      events.Name,
		"arguments": map[string]any{},
		"delivery":  map[string]any{"mode": "webhook", "url": url, "secret": secret},
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func subscribeOK(t *testing.T, env *testhub.Env, bearer string, params map[string]any) subscribeResult {
	t.Helper()
	r := rpc2026(t, env, bearer, "events/subscribe", params)
	if r.Error != nil {
		t.Fatalf("events/subscribe: %+v (data %s)", r.Error, r.Error.Data)
	}
	var res subscribeResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode %s: %v", r.Result, err)
	}
	return res
}

// sendPriority posts a message and returns its delivery id for recipient.
func sendPriority(t *testing.T, env *testhub.Env, secret, recipient, key, priority string) string {
	t.Helper()
	resp, out := env.Do(secret, "POST", "/v1/messages", map[string]any{
		"idempotency_key": key,
		"priority":        priority,
		"recipients":      []string{recipient},
		"content":         map[string]any{"text": "hello " + key},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s: %d %s", key, resp.StatusCode, out)
	}
	var sent struct {
		Deliveries []hub.DeliveryRef `json:"deliveries"`
	}
	if err := json.Unmarshal(out, &sent); err != nil || len(sent.Deliveries) != 1 {
		t.Fatalf("send %s: decode %s: %v", key, out, err)
	}
	return sent.Deliveries[0].DeliveryID
}

// stepEvents runs the Events worker once on the fake clock.
func stepEvents(t *testing.T, env *testhub.Env) {
	t.Helper()
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatalf("events worker Step: %v", err)
	}
}

// eventsFront serves an mcpserver.New of env's hub, adjusted by mutate, on
// its own port, for what the testhub handler does not expose: a resolver, a
// short challenge client, the OAuth seam. The result is usable with rpc2026.
func eventsFront(t *testing.T, env *testhub.Env, a mcpserver.Authenticator, mutate func(*mcpserver.Server)) *testhub.Env {
	t.Helper()
	if a == nil {
		a = mcpserver.Chain{&mcpserver.BearerAuth{Resolver: env.Hub.Auth}}
	}
	m := mcpserver.New(env.Hub, a, env.URL)
	if mutate != nil {
		mutate(m)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return &testhub.Env{T: t, URL: srv.URL, Hub: env.Hub, Now: env.Now}
}

func TestEventsListShape(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "d")
	r := rpc2026(t, env, secret, "events/list", nil)
	if r.Error != nil {
		t.Fatalf("events/list: %+v", r.Error)
	}
	var res struct {
		Events []struct {
			Name          string   `json:"name"`
			Description   string   `json:"description"`
			Delivery      []string `json:"delivery"`
			InputSchema   schemaDoc
			PayloadSchema schemaDoc
		} `json:"events"`
		NextCursor *string `json:"nextCursor"`
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(r.Result, &top); err != nil {
		t.Fatalf("decode %s: %v", r.Result, err)
	}
	if raw, ok := top["nextCursor"]; !ok || string(raw) != "null" {
		t.Fatalf("nextCursor = %s, want an explicit null", raw)
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode %s: %v", r.Result, err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("events = %s, want exactly one", r.Result)
	}
	e := res.Events[0]
	if e.Name != "vecnet.message" || !slices.Equal(e.Delivery, []string{"webhook"}) {
		t.Fatalf("event = %+v, want vecnet.message on delivery [webhook]", e)
	}
	if !strings.Contains(e.Description, "untrusted") {
		t.Fatalf("description %q does not label content untrusted", e.Description)
	}
	for _, p := range []string{"peer_id", "thread_id", "types"} {
		if _, ok := e.InputSchema.Properties[p]; !ok {
			t.Fatalf("inputSchema lacks %s: %+v", p, e.InputSchema)
		}
	}
	if e.InputSchema.AdditionalProperties == nil || *e.InputSchema.AdditionalProperties {
		t.Fatalf("inputSchema is not strict: %+v", e.InputSchema)
	}
	for _, p := range []string{"cursor", "type", "at", "data"} {
		if _, ok := e.PayloadSchema.Properties[p]; !ok {
			t.Fatalf("payloadSchema event lacks %s: %+v", p, e.PayloadSchema)
		}
	}
	var listed any
	if err := json.Unmarshal(r.Result, &listed); err != nil {
		t.Fatal(err)
	}
	assertSimpleEventSchema(t, listed)
}

// assertSimpleEventSchema rejects compatibility hazards at every depth,
// including definitions that a shallow top-level schema check would miss.
func assertSimpleEventSchema(t *testing.T, value any) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		for _, key := range []string{"$ref", "$defs", "anyOf", "oneOf", "allOf"} {
			if _, ok := node[key]; ok {
				t.Errorf("events/list contains unsupported schema keyword %s", key)
			}
		}
		for _, child := range node {
			assertSimpleEventSchema(t, child)
		}
	case []any:
		for _, child := range node {
			assertSimpleEventSchema(t, child)
		}
	}
}

// publishedPayloadSchema compiles the payloadSchema events/list returns.
func publishedPayloadSchema(t *testing.T, env *testhub.Env, secret string) *jsonschema.Schema {
	t.Helper()
	r := rpc2026(t, env, secret, "events/list", nil)
	if r.Error != nil {
		t.Fatalf("events/list: %+v", r.Error)
	}
	var res struct {
		Events []struct {
			PayloadSchema json.RawMessage `json:"payloadSchema"`
		} `json:"events"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil || len(res.Events) != 1 {
		t.Fatalf("decode %s: %v", r.Result, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(res.Events[0].PayloadSchema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("urn:test:payload", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("urn:test:payload")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// schemaDoc is the part of a published JSON Schema these tests read.
type schemaDoc struct {
	Properties           map[string]json.RawMessage `json:"properties"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Defs                 map[string]schemaDoc       `json:"$defs"`
}

func (d *schemaDoc) UnmarshalJSON(b []byte) error {
	type plain schemaDoc
	var p plain
	// additionalProperties may be a schema elsewhere in the document.
	var loose struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Additional json.RawMessage            `json:"additionalProperties"`
		Defs       map[string]schemaDoc       `json:"$defs"`
	}
	if err := json.Unmarshal(b, &loose); err != nil {
		return err
	}
	p.Properties, p.Defs = loose.Properties, loose.Defs
	if len(loose.Additional) > 0 {
		var v bool
		if json.Unmarshal(loose.Additional, &v) == nil {
			p.AdditionalProperties = &v
		}
	}
	*d = schemaDoc(p)
	return nil
}

func TestSubscribeChallengeAndDeliver(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()

	res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	if !strings.HasPrefix(res.ID, "sub_") || len(res.ID) != len("sub_")+26 {
		t.Fatalf("id = %q, want sub_<ULID>", res.ID)
	}
	if res.Truncated {
		t.Fatal("truncated on a new subscription")
	}
	if res.RefreshBefore == nil {
		t.Fatal("refreshBefore is null")
	}
	rb, err := venv.ParseTime(*res.RefreshBefore)
	if err != nil {
		t.Fatalf("refreshBefore %q: %v", *res.RefreshBefore, err)
	}
	if want := env.Now.Now().Add(23*time.Hour + 50*time.Minute); !rb.Equal(want) {
		t.Fatalf("refreshBefore = %v, want %v", rb, want)
	}
	if _, err := eventlog.ParseCursor(res.Cursor); err != nil {
		t.Fatalf("cursor %q: %v", res.Cursor, err)
	}

	chs := cb.Challenges()
	if len(chs) != 1 {
		t.Fatalf("challenges = %d, want 1", len(chs))
	}
	if chs[0].Subscription != res.ID || !strings.HasPrefix(chs[0].ID, "vrf_") {
		t.Fatalf("challenge headers = %+v", chs[0])
	}
	if err := webhook.Verify([]string{whsec}, chs[0].ID, chs[0].Timestamp, chs[0].Signature, chs[0].Body, env.Now.Now()); err != nil {
		t.Fatalf("challenge signature: %v", err)
	}

	sendPriority(t, env, sender, dot, "held-back", "hold")
	dly := sendPriority(t, env, sender, dot, "go-now", "wake")
	stepEvents(t, env)
	stepEvents(t, env)

	posts := cb.Posts()
	if len(posts) != 1 {
		t.Fatalf("POSTs = %d, want 1 (wake only)", len(posts))
	}
	p := posts[0]
	if p.ID != dly || p.Subscription != res.ID {
		t.Fatalf("headers webhook-id %q, subscription %q; want %q, %q", p.ID, p.Subscription, dly, res.ID)
	}
	if err := webhook.Verify([]string{whsec}, p.ID, p.Timestamp, p.Signature, p.Body, env.Now.Now()); err != nil {
		t.Fatalf("signature: %v", err)
	}
	body := decodeEvent(t, p.Body)
	if body.Name != "vecnet.message" || body.EventID != dly {
		t.Fatalf("body = %+v", body)
	}
	if err := venv.Validate("event", body.Data); err != nil {
		t.Fatalf("body.data is not $defs/event: %v\n%s", err, body.Data)
	}
	var ev venv.Event
	if err := json.Unmarshal(body.Data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "delivery" || body.Cursor != ev.Cursor || body.Timestamp != ev.At {
		t.Fatalf("body cursor %q timestamp %q, event %+v", body.Cursor, body.Timestamp, ev)
	}
	logged, err := env.Hub.Log.Read(context.Background(), []string{dot}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 10)
	if err != nil || len(logged) == 0 {
		t.Fatalf("log = %+v, %v", logged, err)
	}
	if last := logged[len(logged)-1]; last.Type != "delivery" || last.Cursor != body.Cursor {
		t.Fatalf("body cursor %q, newest log entry %+v", body.Cursor, last)
	}

	// data matches the published payloadSchema, whole or as a preview.
	payload := publishedPayloadSchema(t, env, dotSecret)
	for _, data := range []string{string(body.Data),
		`{"delivery_id":"` + dly + `","preview":"text","blob_url":"https://hub.example/b"}`} {
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := payload.Validate(v); err != nil {
			t.Fatalf("%s does not match payloadSchema: %v", data, err)
		}
	}
}

func TestSubscribeRejectsBadUrlAndSecret(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = false })
	_, secret := env.NewPeer("session", "d")
	cb := testhub.NewCallback(t)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(strings.SplitN(cb.URL, "/hook", 2)[0], "http://"))
	if err != nil {
		t.Fatal(err)
	}
	front := eventsFront(t, env, nil, func(m *mcpserver.Server) {
		m.Egress.Resolver = func(_ context.Context, host string) ([]net.IP, error) {
			switch host {
			case "rebind.example":
				return []net.IP{net.ParseIP("10.1.2.3")}, nil
			case "loopback.example":
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			case "public.example":
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			}
			return nil, errors.New("no such host")
		}
	})
	whsec := webhook.NewSecret()
	for _, url := range []string{
		cb.URL, // http://127.0.0.1:<port>/...
		"https://127.0.0.1:" + port + "/hook",
		"https://10.0.0.1/hook",
		"https://u@public.example/hook",
		"https://u:p@public.example/hook",
		"https://rebind.example/hook",
		"https://loopback.example:" + port + "/hook",
		"https://public.example:8080/hook",
		"https://nosuch.example/hook",
		"",
	} {
		r := rpc2026(t, front, secret, "events/subscribe", subParams(url, whsec, nil))
		if r.Error == nil || r.Error.Code != mcpserver.ErrCodeCallback {
			t.Fatalf("%q: %+v / %s, want -32015", url, r.Error, r.Result)
		}
		if got := errReason(t, r); got != "invalid_url" {
			t.Fatalf("%q: reason %q, want invalid_url", url, got)
		}
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("receiver got %d requests, want 0", n)
	}
	for _, bad := range []string{
		"",
		"whsec_",
		"whsec_" + "c2hvcnQ=", // 5 bytes
		"not-prefixed",
		"whsec_not base64",
		"whsec_" + strings.Repeat("QUJD", 30), // 90 bytes
	} {
		r := rpc2026(t, env, secret, "events/subscribe", subParams(cb.URL, bad, nil))
		if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
			t.Fatalf("secret %q: %+v, want -32602", bad, r.Error)
		}
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("receiver got %d requests after bad secrets, want 0", n)
	}
	var rows int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_subscriptions`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("subscription rows = %d, %v; want none", rows, err)
	}
}

func TestChallengeFailure(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "d")

	cb := testhub.NewCallback(t)
	cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": "wrong"})
	})
	r := rpc2026(t, env, secret, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrCodeCallback || errReason(t, r) != "challenge_failed" {
		t.Fatalf("wrong echo: %+v, want -32015 challenge_failed", r.Error)
	}

	cb2 := testhub.NewCallback(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	cb2.OnChallenge(func(_ string, w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	front := eventsFront(t, env, nil, func(m *mcpserver.Server) {
		m.EventsClient = env.Hub.EgressPolicy().Client(2 * time.Second)
	})
	r = rpc2026(t, front, secret, "events/subscribe", subParams(cb2.URL, webhook.NewSecret(), nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrCodeCallback || errReason(t, r) != "timeout" {
		t.Fatalf("slow 500: %+v, want -32015 timeout", r.Error)
	}

	cb3 := testhub.NewCallback(t)
	cb3.OnChallenge(func(c string, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": c})
	})
	r = rpc2026(t, env, secret, "events/subscribe", subParams(cb3.URL, webhook.NewSecret(), nil))
	if r.Error == nil || errReason(t, r) != "http_5xx" {
		t.Fatalf("500 with a correct echo: %+v, want http_5xx", r.Error)
	}

	cb4 := testhub.NewCallback(t)
	cb4.OnChallenge(func(c string, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	r = rpc2026(t, env, secret, "events/subscribe", subParams(cb4.URL, webhook.NewSecret(), nil))
	if r.Error == nil || errReason(t, r) != "challenge_failed" {
		t.Fatalf("redirect: %+v, want challenge_failed", r.Error)
	}
	if n := len(cb4.Challenges()); n != 1 {
		t.Fatalf("redirect followed: %d requests", n)
	}

	var rows int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_subscriptions`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("subscription rows after failed challenges = %d, %v; want none", rows, err)
	}
}

func TestRefreshKeepsCursorRotatesSecret(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	oldKey, newKey := webhook.NewSecret(), webhook.NewSecret()

	first := subscribeOK(t, env, dotSecret, subParams(cb.URL, oldKey, nil))
	dly1 := sendPriority(t, env, sender, dot, "one", "wake")
	dly2 := sendPriority(t, env, sender, dot, "two", "wake")
	stepEvents(t, env)
	posts := cb.Posts()
	if len(posts) != 2 || posts[0].ID != dly1 || posts[1].ID != dly2 {
		t.Fatalf("first deliveries = %+v", posts)
	}
	if got := strings.Fields(posts[1].Signature); len(got) != 1 {
		t.Fatalf("signatures before rotation = %v, want one", got)
	}
	firstCursor := decodeEvent(t, posts[0].Body).Cursor
	secondCursor := decodeEvent(t, posts[1].Body).Cursor

	again := subscribeOK(t, env, dotSecret, subParams(cb.URL, newKey, map[string]any{"cursor": firstCursor}))
	if again.ID != first.ID {
		t.Fatalf("refresh id = %s, want %s", again.ID, first.ID)
	}
	if again.Cursor != firstCursor || again.Truncated {
		t.Fatalf("refresh = %+v, want cursor %s, not truncated", again, firstCursor)
	}
	if n := len(cb.Challenges()); n != 2 {
		t.Fatalf("challenges = %d; a rotated secret must verify independently", n)
	}
	stepEvents(t, env)
	posts = cb.Posts()
	if len(posts) != 3 {
		t.Fatalf("POSTs = %d, want one replay after the given cursor", len(posts))
	}
	p := posts[2]
	if p.ID != dly2 || decodeEvent(t, p.Body).Cursor != secondCursor {
		t.Fatalf("replay = %s %s, want the event after %s", p.ID, p.Body, firstCursor)
	}
	if got := strings.Fields(p.Signature); len(got) != 2 {
		t.Fatalf("signatures = %v, want old and new", got)
	}
	for _, key := range []string{oldKey, newKey} {
		if err := webhook.Verify([]string{key}, p.ID, p.Timestamp, p.Signature, p.Body, env.Now.Now()); err != nil {
			t.Fatalf("signature under one key: %v", err)
		}
	}

	// With no cursor param the stored cursor stands.
	kept := subscribeOK(t, env, dotSecret, subParams(cb.URL, newKey, nil))
	if kept.Cursor != secondCursor {
		t.Fatalf("refresh without cursor = %s, want stored %s", kept.Cursor, secondCursor)
	}

	// Past the overlap only the new key signs.
	env.Now.Advance(11 * time.Minute)
	sendPriority(t, env, sender, dot, "three", "wake")
	stepEvents(t, env)
	posts = cb.Posts()
	if len(posts) != 4 {
		t.Fatalf("POSTs = %d, want 4", len(posts))
	}
	if got := strings.Fields(posts[3].Signature); len(got) != 1 {
		t.Fatalf("signatures after the overlap = %v, want one", got)
	}
	if err := webhook.Verify([]string{newKey}, posts[3].ID, posts[3].Timestamp, posts[3].Signature, posts[3].Body, env.Now.Now()); err != nil {
		t.Fatalf("signature after the overlap: %v", err)
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, otherSecret := env.NewPeer("session", "other")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribeOK(t, env, dotSecret, subParams(cb.URL, webhook.NewSecret(), nil))

	params := map[string]any{"name": events.Name, "arguments": map[string]any{},
		"delivery": map[string]any{"mode": "webhook", "url": cb.URL}}

	wantResult, err := json.Marshal(map[string]any{
		"resultType": "complete",
		"_meta": map[string]any{
			"io.modelcontextprotocol/serverInfo": map[string]any{"name": "vecnet", "version": mcpserver.Version},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Another account's unsubscribe returns the same envelope and removes nothing.
	r := rpc2026(t, env, otherSecret, "events/unsubscribe", params)
	if r.Error != nil || !bytes.Equal(r.Result, wantResult) {
		t.Fatalf("foreign unsubscribe: %+v %s", r.Error, r.Result)
	}
	sendPriority(t, env, sender, dot, "one", "wake")
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs = %d after a foreign unsubscribe, want 1", n)
	}

	for i := range 2 {
		r := rpc2026(t, env, dotSecret, "events/unsubscribe", params)
		if r.Error != nil || !bytes.Equal(r.Result, wantResult) {
			t.Fatalf("unsubscribe %d: %+v %s", i, r.Error, r.Result)
		}
	}
	sendPriority(t, env, sender, dot, "two", "wake")
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs = %d after unsubscribe, want still 1", n)
	}
}

func TestAdminSubscriptions(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET paused_reason = 'delivery_failed'`); err != nil {
		t.Fatal(err)
	}

	type row struct {
		SubID          string  `json:"sub_id"`
		Principal      string  `json:"principal"`
		PeerID         string  `json:"peer_id"`
		URL            string  `json:"url"`
		Name           string  `json:"name"`
		Cursor         string  `json:"cursor"`
		ExpiresAt      *string `json:"expires_at"`
		VerifiedAt     *string `json:"verified_at"`
		PausedReason   *string `json:"paused_reason"`
		LastDeliveryAt *string `json:"last_delivery_at"`
		LastStatus     *int    `json:"last_status"`
		Attempts       int     `json:"attempts"`
	}
	list := func() []row {
		t.Helper()
		resp, out := env.Do(env.OperatorSecret, "GET", "/v1/admin/subscriptions", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list: %d %s", resp.StatusCode, out)
		}
		if strings.Contains(string(out), "/hook") || strings.Contains(string(out), "whsec_") ||
			strings.Contains(string(out), "secret") || strings.Contains(string(out), "http://") {
			t.Fatalf("list leaks the path or secret: %s", out)
		}
		var rows []row
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatalf("decode %s: %v", out, err)
		}
		return rows
	}
	rows := list()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}
	r := rows[0]
	if r.SubID != res.ID || r.PeerID != dot || r.URL != "127.0.0.1" || r.Name != events.Name || r.Cursor != res.Cursor {
		t.Fatalf("row = %+v", r)
	}
	if !strings.Contains(r.Principal, dot) {
		t.Fatalf("principal = %q, want the peer credential's %s", r.Principal, dot)
	}
	if r.ExpiresAt == nil || r.VerifiedAt == nil || r.PausedReason == nil || *r.PausedReason != "delivery_failed" {
		t.Fatalf("row health = %+v", r)
	}

	if resp, out := env.Do(dotSecret, "GET", "/v1/admin/subscriptions", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("list as a peer: %d %s", resp.StatusCode, out)
	}
	if resp, out := env.Do(env.OperatorSecret, "POST", "/v1/admin/subscriptions/sub_NOPE/resume", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resume unknown: %d %s", resp.StatusCode, out)
	}
	if resp, out := env.Do(env.OperatorSecret, "POST", "/v1/admin/subscriptions/"+res.ID+"/resume", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resume: %d %s", resp.StatusCode, out)
	}
	if got := list()[0]; got.PausedReason != nil {
		t.Fatalf("after resume paused_reason = %q", *got.PausedReason)
	}
}

// fakeOAuth authenticates every request as one OAuth principal.
type fakeOAuth struct {
	prn     string
	expires func() time.Time
}

func (f fakeOAuth) Authenticate(context.Context, *http.Request) (mcpserver.Principal, error) {
	return mcpserver.Principal{
		Caller: auth.Caller{Kind: "principal", PrincipalID: f.prn},
		OAuth:  &mcpserver.OAuthInfo{PrincipalID: f.prn, Expires: f.expires(), Scopes: []string{"vecnet", "events:read", "events:write"}},
	}, nil
}

func TestSubscribeOAuthPrincipal(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("session", "d")
	other, _ := env.NewPeer("dot", "other")
	prn, err := env.Hub.PeerPrincipal(context.Background(), dot)
	if err != nil {
		t.Fatal(err)
	}
	cb := testhub.NewCallback(t)
	expiry := env.Now.Now().Add(time.Hour)
	a := fakeOAuth{prn: prn, expires: func() time.Time { return expiry }}

	unbound := eventsFront(t, env, a, nil)
	r := rpc2026(t, unbound, "any", "events/subscribe", subParams(cb.URL, webhook.NewSecret(), nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams || errReason(t, r) != "not_linked" {
		t.Fatalf("no seam: %+v, want -32602 not_linked", r.Error)
	}

	var bound string
	front := eventsFront(t, env, a, func(m *mcpserver.Server) {
		m.ResolveDotPeer = func(_ context.Context, principalID string) (string, error) {
			if principalID != prn {
				return "", fmt.Errorf("asked about %s", principalID)
			}
			return bound, nil
		}
	})
	r = rpc2026(t, front, "any", "events/subscribe", subParams(cb.URL, webhook.NewSecret(), nil))
	if r.Error == nil || errReason(t, r) != "not_linked" {
		t.Fatalf("unbound principal: %+v, want not_linked", r.Error)
	}
	bound = dot
	r = rpc2026(t, front, "any", "events/subscribe", subParams(cb.URL, webhook.NewSecret(),
		map[string]any{"arguments": map[string]any{"peer_id": other}}))
	if r.Error == nil || errReason(t, r) != "not_your_peer" {
		t.Fatalf("another peer: %+v, want not_your_peer", r.Error)
	}

	res := subscribeOK(t, front, "any", subParams(cb.URL, webhook.NewSecret(), nil))
	if res.RefreshBefore == nil {
		t.Fatal("refreshBefore is null")
	}
	if rb, err := venv.ParseTime(*res.RefreshBefore); err != nil || !rb.Equal(expiry.Add(-10*time.Minute)) {
		t.Fatalf("refreshBefore = %v, %v; want token expiry - 10 min", rb, err)
	}
	s, err := events.NewStore(env.Hub).Get(context.Background(), res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.PrincipalKind != "oauth" || s.PrincipalID != prn || s.PeerID != dot {
		t.Fatalf("subscription = %+v", s)
	}
	if s.Expires == nil || !s.Expires.Equal(expiry) {
		t.Fatalf("expires = %v, want the token's %v", s.Expires, expiry)
	}
}

func TestSubscribeTtlClamp(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "d")
	now := env.Now.Now()
	for _, c := range []struct {
		name string
		ttl  any
		want time.Duration
	}{
		{"absent", "omit", 24 * time.Hour},
		{"null", nil, 24 * time.Hour},
		{"over", 48 * 3600 * 1000, 24 * time.Hour},
		{"huge", 1e300, 24 * time.Hour},
		{"short", 3600 * 1000, time.Hour},
		{"under the margin", 5 * 60 * 1000, 5 * time.Minute}, // refreshBefore clamps to now, never the past
	} {
		cb := testhub.NewCallback(t)
		extra := map[string]any{"ttlMs": c.ttl}
		if c.ttl == "omit" {
			extra = nil
		}
		res := subscribeOK(t, env, secret, subParams(cb.URL, webhook.NewSecret(), extra))
		if res.RefreshBefore == nil {
			t.Fatalf("%s: refreshBefore is null", c.name)
		}
		want := now.Add(c.want - 10*time.Minute)
		if want.Before(now) {
			want = now
		}
		if rb, err := venv.ParseTime(*res.RefreshBefore); err != nil || !rb.Equal(want) {
			t.Fatalf("%s: refreshBefore = %v, %v", c.name, rb, err)
		}
	}
	for _, ttl := range []any{0, -5, 0.5, 1.5, "soon"} {
		cb := testhub.NewCallback(t)
		r := rpc2026(t, env, secret, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), map[string]any{"ttlMs": ttl}))
		if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
			t.Fatalf("ttlMs %v: %+v, want -32602", ttl, r.Error)
		}
	}
}

func TestSubscribeParamsStrict(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "d")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	for name, params := range map[string]map[string]any{
		"unknown event": subParams(cb.URL, whsec, map[string]any{"name": "vecnet.other"}),
		"mode":          subParams(cb.URL, whsec, map[string]any{"delivery": map[string]any{"mode": "poll", "url": cb.URL, "secret": whsec}}),
		"argument":      subParams(cb.URL, whsec, map[string]any{"arguments": map[string]any{"color": "red"}}),
		"type":          subParams(cb.URL, whsec, map[string]any{"arguments": map[string]any{"types": []string{"presence"}}}),
		"empty types":   subParams(cb.URL, whsec, map[string]any{"arguments": map[string]any{"types": []string{}}}),
		"repeat types":  subParams(cb.URL, whsec, map[string]any{"arguments": map[string]any{"types": []string{"delivery", "delivery"}}}),
		"peer":          subParams(cb.URL, whsec, map[string]any{"arguments": map[string]any{"peer_id": "nope"}}),
		"thread":        subParams(cb.URL, whsec, map[string]any{"arguments": map[string]any{"thread_id": "nope"}}),
		"cursor":        subParams(cb.URL, whsec, map[string]any{"cursor": "1.2"}),
		"future cursor": subParams(cb.URL, whsec, map[string]any{"cursor": fmt.Sprintf("e%d.999999", env.Hub.Log.Epoch())}),
	} {
		r := rpc2026(t, env, secret, "events/subscribe", params)
		if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams {
			t.Fatalf("%s: %+v / %s, want -32602", name, r.Error, r.Result)
		}
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("receiver got %d requests, want 0", n)
	}
}

func TestSubscribeForeignPeer(t *testing.T) {
	env := testhub.Start(t)
	prnA, secretA := env.NewPrincipal("a")
	prnB, secretB := env.NewPrincipal("b")
	peerA, _ := peerUnder(t, env, prnA, "pa")
	peerB, _ := peerUnder(t, env, prnB, "pb")
	cb := testhub.NewCallback(t)

	r := rpc2026(t, env, secretA, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams || errReason(t, r) != "peer_required" {
		t.Fatalf("principal without peer_id: %+v, want -32602 peer_required", r.Error)
	}
	foreign := map[string]any{"arguments": map[string]any{"peer_id": peerB}}
	r = rpc2026(t, env, secretA, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), foreign))
	if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams || errReason(t, r) != "not_your_peer" {
		t.Fatalf("foreign peer: %+v, want -32602 not_your_peer", r.Error)
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("receiver got %d requests, want 0", n)
	}
	if res := subscribeOK(t, env, secretA, subParams(cb.URL, webhook.NewSecret(),
		map[string]any{"arguments": map[string]any{"peer_id": peerA}})); res.ID == "" {
		t.Fatal("no id")
	}
	if res := subscribeOK(t, env, secretB, subParams(cb.URL, webhook.NewSecret(),
		map[string]any{"arguments": map[string]any{"peer_id": peerB}})); res.ID == "" {
		t.Fatal("no id")
	}
}

func TestSubscribeCursorTruncated(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	first := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	sendPriority(t, env, sender, dot, "one", "wake")
	sendPriority(t, env, sender, dot, "two", "wake")
	stepEvents(t, env)
	posts := cb.Posts()
	if len(posts) != 2 {
		t.Fatalf("POSTs = %d", len(posts))
	}
	cur, err := eventlog.ParseCursor(decodeEvent(t, posts[1].Body).Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE seq < ?`, cur.Seq); err != nil {
		t.Fatal(err)
	}
	oldest, err := env.Hub.Log.Oldest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, map[string]any{"cursor": first.Cursor}))
	if !res.Truncated || res.Cursor != oldest.String() {
		t.Fatalf("refresh = %+v, want truncated at %s", res, oldest)
	}
	stale := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, map[string]any{"cursor": "e9999.1"}))
	if !stale.Truncated || stale.Cursor != oldest.String() {
		t.Fatalf("other epoch = %+v, want truncated at %s", stale, oldest)
	}
	if head := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, map[string]any{"cursor": "head"})); head.Cursor != cur.String() || head.Truncated {
		t.Fatalf("head = %+v, want %s", head, cur)
	}
}

func TestEventsOnLegacySession(t *testing.T) {
	env := testhub.Start(t)
	dot, secret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sid := legacySession(t, env, secret)
	resp, out := do(t, env, call{secret: secret, headers: map[string]string{"Mcp-Session-Id": sid, "Accept": "application/json"},
		body: map[string]any{"jsonrpc": "2.0", "id": 2, "method": "events/subscribe",
			"params": subParams(cb.URL, webhook.NewSecret(), nil)}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events/subscribe: %d %s", resp.StatusCode, out)
	}
	if r := decodeRPC(t, out); r.Error != nil {
		t.Fatalf("events/subscribe: %+v", r.Error)
	}
	sendPriority(t, env, sender, dot, "one", "wake")
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs = %d, want 1", n)
	}
}

// eventSeq is the log seq of the event one POST carried.
func eventSeq(t *testing.T, p testhub.CallbackPost) int64 {
	t.Helper()
	return cursorSeq(t, decodeEvent(t, p.Body).Cursor)
}

func cursorSeq(t *testing.T, raw string) int64 {
	t.Helper()
	c, err := eventlog.ParseCursor(raw)
	if err != nil {
		t.Fatalf("cursor %q: %v", raw, err)
	}
	return c.Seq
}

func answerFirst(status int) func(int, http.ResponseWriter) {
	return func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(status)
		}
	}
}

// The cursor a refresh reports is a true replay position: a client that saves
// it and passes it back loses nothing still queued (PROTOCOL §4.4).
func TestQueuedRowSurvivesTwoRefreshes(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	cb.OnPost(answerFirst(http.StatusServiceUnavailable))
	queued := sendPriority(t, env, sender, dot, "queued", "wake")
	stepEvents(t, env)
	posts := cb.Posts()
	if len(posts) != 1 {
		t.Fatalf("POSTs = %d", len(posts))
	}
	seq := eventSeq(t, posts[0])

	first := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	if got := cursorSeq(t, first.Cursor); got >= seq || first.Truncated {
		t.Fatalf("refresh = %+v, want a cursor before the queued event at seq %d", first, seq)
	}
	second := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, map[string]any{"cursor": first.Cursor}))
	if second.Cursor != first.Cursor || second.Truncated {
		t.Fatalf("second refresh = %+v, want %s", second, first.Cursor)
	}
	stepEvents(t, env)
	posts = cb.Posts()
	if len(posts) != 2 || posts[1].ID != queued {
		t.Fatalf("POSTs = %+v, want %s redelivered", posts, queued)
	}

	// Delivered, the row no longer holds the position back.
	settled := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	if got := cursorSeq(t, settled.Cursor); got != seq {
		t.Fatalf("after delivery cursor seq = %d, want %d", got, seq)
	}
}

// A cursor below retention says nothing about what the client holds, so
// what is queued still goes out.
func TestPurgedCursorKeepsQueuedRow(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	first := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	cb.OnPost(answerFirst(http.StatusServiceUnavailable))
	queued := sendPriority(t, env, sender, dot, "queued", "wake")
	stepEvents(t, env)
	if _, err := env.Hub.DB.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, map[string]any{"cursor": first.Cursor}))
	if !res.Truncated {
		t.Fatalf("refresh = %+v, want truncated", res)
	}
	stepEvents(t, env)
	if posts := cb.Posts(); len(posts) != 2 || posts[1].ID != queued {
		t.Fatalf("POSTs = %+v, want %s redelivered", posts, queued)
	}
}

// 410 and 413 are never retried, and a rewind is not an exception, even past
// the day settled rows are kept: ChatGPT's saved cursor stays before an event
// the callback refused until a later one arrives.
func TestTerminalRowSurvivesRewind(t *testing.T) {
	for _, code := range []int{http.StatusGone, http.StatusRequestEntityTooLarge} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			env := testhub.Start(t)
			dot, dotSecret := env.NewPeer("session", "d")
			_, sender := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			whsec := webhook.NewSecret()
			first := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
			saved := map[string]any{"cursor": first.Cursor}
			cb.OnPost(answerFirst(code))
			sendPriority(t, env, sender, dot, "refused", "wake")
			stepEvents(t, env)

			subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, saved))
			stepEvents(t, env)
			if n := len(cb.Posts()); n != 1 {
				t.Fatalf("POSTs after a rewind = %d, want the refused event left alone", n)
			}

			env.Now.Advance(25 * time.Hour)
			stepEvents(t, env) // prunes settled rows
			subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, saved))
			stepEvents(t, env)
			if n := len(cb.Posts()); n != 1 {
				t.Fatalf("POSTs after a rewind past retention = %d, want the refused event left alone", n)
			}

			// What follows the refused event still goes out.
			later := sendPriority(t, env, sender, dot, "later", "wake")
			stepEvents(t, env)
			if posts := cb.Posts(); len(posts) != 2 || posts[1].ID != later {
				t.Fatalf("POSTs = %+v, want %s", posts, later)
			}
		})
	}
}

// Giving up after 8 attempts is not "never retry": the client never got the
// event, so the reported cursor stays before it and a re-subscribe from there
// drains it (PROTOCOL §4.4 Outage).
func TestExhaustedRowReplaysFromCursor(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	dly := sendPriority(t, env, sender, dot, "doomed", "wake")
	for i := 1; i <= 8; i++ {
		stepEvents(t, env)
		if i < 8 {
			subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil)) // clears the backoff
		}
	}
	posts := cb.Posts()
	if len(posts) != 8 {
		t.Fatalf("POSTs = %d, want 8", len(posts))
	}
	cb.OnPost(nil)
	res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	if got := cursorSeq(t, res.Cursor); got >= eventSeq(t, posts[0]) || res.Truncated {
		t.Fatalf("refresh = %+v, want a cursor before the given-up event", res)
	}
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 8 {
		t.Fatalf("POSTs = %d; a refresh with no cursor stays put", n)
	}
	subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, map[string]any{"cursor": res.Cursor}))
	stepEvents(t, env)
	if posts := cb.Posts(); len(posts) != 9 || posts[8].ID != dly {
		t.Fatalf("POSTs = %d, want %s replayed", len(posts), dly)
	}
}

// Review Focus 3 through the method the plan names: the token expires with a
// row in backoff and a later event waiting, and a fresh token's
// events/subscribe delivers both, whether or not it passes a cursor.
func TestTokenExpiryResumesThroughSubscribe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		saved bool
	}{{"no cursor", false}, {"saved cursor", true}} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			dot, _ := env.NewPeer("session", "d")
			_, sender := env.NewPeer("session", "s")
			prn, err := env.Hub.PeerPrincipal(context.Background(), dot)
			if err != nil {
				t.Fatal(err)
			}
			cb := testhub.NewCallback(t)
			whsec := webhook.NewSecret()
			expiry := env.Now.Now().Add(time.Hour)
			env.Events.ResolveDotPeer = func(context.Context, string) (string, error) { return dot, nil }
			front := eventsFront(t, env, fakeOAuth{prn: prn, expires: func() time.Time { return expiry }}, func(m *mcpserver.Server) {
				m.ResolveDotPeer = func(context.Context, string) (string, error) { return dot, nil }
			})
			first := subscribeOK(t, front, "any", subParams(cb.URL, whsec, nil))

			cb.OnPost(answerFirst(http.StatusServiceUnavailable))
			queued := sendPriority(t, env, sender, dot, "queued", "wake")
			stepEvents(t, env)
			if n := len(cb.Posts()); n != 1 {
				t.Fatalf("POSTs = %d", n)
			}

			env.Now.Advance(2 * time.Hour)
			later := sendPriority(t, env, sender, dot, "later", "wake")
			stepEvents(t, env)
			if n := len(cb.Posts()); n != 1 {
				t.Fatalf("POSTs after expiry = %d, want none new", n)
			}
			paused, err := events.NewStore(env.Hub).Get(context.Background(), first.ID)
			if err != nil || paused.PausedReason != "token_expired" {
				t.Fatalf("subscription = %+v, %v; want paused token_expired", paused, err)
			}

			expiry = env.Now.Now().Add(time.Hour)
			extra := map[string]any{}
			if tc.saved {
				extra["cursor"] = first.Cursor
			}
			res := subscribeOK(t, front, "any", subParams(cb.URL, whsec, extra))
			if res.ID != first.ID || res.Truncated {
				t.Fatalf("resume = %+v", res)
			}
			if got := cursorSeq(t, res.Cursor); got >= eventSeq(t, cb.Posts()[0]) {
				t.Fatalf("resume cursor %s is past the queued event", res.Cursor)
			}
			stepEvents(t, env)
			posts := cb.Posts()
			if len(posts) != 3 || posts[1].ID != queued || posts[2].ID != later {
				t.Fatalf("POSTs = %+v, want %s then %s", posts, queued, later)
			}
		})
	}
}

// A purge the worker met is reported once, on the next events/subscribe.
func TestSubscribeReportsWorkerTruncation(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	whsec := webhook.NewSecret()
	subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil))
	sendPriority(t, env, sender, dot, "lost", "wake")
	if _, err := env.Hub.DB.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 0 {
		t.Fatalf("POSTs = %d, want none", n)
	}
	if res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil)); !res.Truncated {
		t.Fatalf("first refresh = %+v, want truncated", res)
	}
	if res := subscribeOK(t, env, dotSecret, subParams(cb.URL, whsec, nil)); res.Truncated {
		t.Fatalf("second refresh = %+v, want the report once", res)
	}
}

// The challenge goes out on the guarded client even if the pre-check lets a
// URL through (a name that re-resolves between the check and the dial).
func TestDefaultChallengeClientRefusesLoopback(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = false })
	_, secret := env.NewPeer("session", "d")
	cb := testhub.NewCallback(t)
	front := eventsFront(t, env, nil, func(m *mcpserver.Server) { m.Egress.AllowPrivate = true })
	r := rpc2026(t, front, secret, "events/subscribe", subParams(cb.URL, webhook.NewSecret(), nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrCodeCallback {
		t.Fatalf("challenge on a strict hub = %+v / %s, want -32015", r.Error, r.Result)
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("strict hub's default challenge client reached a loopback callback: %d requests", n)
	}
}

// A passed challenge stands for ChallengeMaxAge. Past it a re-subscribe sends a
// signed challenge, and failing that keeps the row, its cursor and its queue
// while delivering nothing. A new secret must verify independently.
func TestChallengeAgesOut(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	first := subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	sendPriority(t, env, sender, dot, "one", "wake")
	stepEvents(t, env)
	seen := decodeEvent(t, cb.Posts()[0].Body).Cursor

	// Inside the bound: an unchanged key reuses the original proof.
	env.Now.Advance(events.ChallengeMaxAge - time.Minute)
	subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	if n := len(cb.Challenges()); n != 1 {
		t.Fatalf("challenges inside the bound = %d, want 1", n)
	}

	// Past it, against a callback that no longer answers.
	env.Now.Advance(2 * time.Minute)
	cb.OnChallenge(func(_ string, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	r := rpc2026(t, env, dotSecret, "events/subscribe", subParams(cb.URL, key, nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrCodeCallback || errReason(t, r) != "http_4xx" {
		t.Fatalf("re-subscribe past the bound = %+v / %s, want -32015 http_4xx", r.Error, r.Result)
	}
	chs := cb.Challenges()
	if len(chs) != 2 {
		t.Fatalf("challenges past the bound = %d, want 2", len(chs))
	}
	if err := webhook.Verify([]string{key}, chs[1].ID, chs[1].Timestamp, chs[1].Signature, chs[1].Body, env.Now.Now()); err != nil {
		t.Fatalf("challenge signature: %v", err)
	}
	store := events.NewStore(env.Hub)
	kept, err := store.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("a failed re-challenge lost the row: %v", err)
	}
	if kept.Verified || kept.Cursor.String() != seen {
		t.Fatalf("after the failure: %+v, want unverified at %s", kept, seen)
	}
	queued := sendPriority(t, env, sender, dot, "two", "wake")
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs while unverified = %d, want none new", n)
	}

	// A later subscribe that passes resumes from the stored cursor.
	cb.OnChallenge(nil)
	back := subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	if back.ID != first.ID || back.Cursor != seen || len(cb.Challenges()) != 3 {
		t.Fatalf("resume = %+v, %d challenges; want %s at %s, 3 challenges", back, len(cb.Challenges()), first.ID, seen)
	}
	got, err := store.Get(context.Background(), first.ID)
	if err != nil || got.VerifiedAt == nil || !got.VerifiedAt.Equal(env.Now.Now()) {
		t.Fatalf("verified at %v, %v; want now", got.VerifiedAt, err)
	}
	stepEvents(t, env)
	if posts := cb.Posts(); len(posts) != 2 || posts[1].ID != queued {
		t.Fatalf("POSTs = %+v, want %s to arrive", posts, queued)
	}
}

// A lapsed row with an expired callback proof must verify again, and the
// worker sends nothing while that challenge is outstanding.
func TestLapsedSubscriptionChallengesAgain(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	subscribeOK(t, env, dotSecret, subParams(cb.URL, key, map[string]any{"ttlMs": 60000}))
	env.Now.Advance(events.ChallengeMaxAge + time.Minute)
	sendPriority(t, env, sender, dot, "during", "wake")
	cb.OnChallenge(func(c string, w http.ResponseWriter, _ *http.Request) {
		stepEvents(t, env) // a Step that lands mid-challenge
		if n := len(cb.Posts()); n != 0 {
			t.Errorf("POSTs during the challenge = %d, want 0", n)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": c})
	})
	subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	if n := len(cb.Challenges()); n != 2 {
		t.Fatalf("challenges = %d, want 2", n)
	}
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs after the challenge = %d, want 1", n)
	}
}

// Rotation answers a leaked bearer: what that bearer subscribed stops in the
// same transaction, keeps its place, and the new holder takes it up.
func TestRotationPausesSubscriptions(t *testing.T) {
	ctx := context.Background()
	t.Run("peer", func(t *testing.T) {
		env := testhub.Start(t)
		peer, secret := env.NewPeer("session", "p")
		_, sender := env.NewPeer("session", "s")
		cb := testhub.NewCallback(t)
		key := webhook.NewSecret()
		res := subscribeOK(t, env, secret, subParams(cb.URL, key, nil))
		fresh, err := env.Hub.RotatePeer(ctx, peer)
		if err != nil {
			t.Fatal(err)
		}
		sendPriority(t, env, sender, peer, "after-rotate", "wake")
		stepEvents(t, env)
		if n := len(cb.Posts()); n != 0 {
			t.Fatalf("POSTs after rotation = %d, want 0", n)
		}
		got, err := events.NewStore(env.Hub).Get(ctx, res.ID)
		if err != nil || got.PausedReason != events.PausedCredentialRotated {
			t.Fatalf("subscription = %+v, %v", got, err)
		}
		subscribeOK(t, env, fresh, subParams(cb.URL, key, nil))
		stepEvents(t, env)
		if n := len(cb.Posts()); n != 1 {
			t.Fatalf("POSTs after the new bearer subscribed = %d, want 1", n)
		}
	})
	t.Run("principal", func(t *testing.T) {
		env := testhub.Start(t)
		prn, secret := env.NewPrincipal("pc")
		peer, peerSecret := peerUnder(t, env, prn, "p")
		other := testhub.NewCallback(t)
		byPeer := subscribeOK(t, env, peerSecret, subParams(other.URL, webhook.NewSecret(), nil))
		cb := testhub.NewCallback(t)
		res := subscribeOK(t, env, secret, subParams(cb.URL, webhook.NewSecret(), map[string]any{"arguments": map[string]any{"peer_id": peer}}))
		if _, err := env.Hub.RotatePrincipal(ctx, prn); err != nil {
			t.Fatal(err)
		}
		store := events.NewStore(env.Hub)
		if got, err := store.Get(ctx, res.ID); err != nil || got.PausedReason != events.PausedCredentialRotated {
			t.Fatalf("principal's subscription = %+v, %v", got, err)
		}
		if got, err := store.Get(ctx, byPeer.ID); err != nil || got.PausedReason != "" {
			t.Fatalf("the peer's own credential was not rotated: %+v, %v", got, err)
		}
	})
}

// The operator can stop one subscription, and a re-subscribe does not undo it.
func TestAdminPauseSubscription(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	res := subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))

	resp, body := env.Do(env.OperatorSecret, "POST", "/v1/admin/subscriptions/"+res.ID+"/pause", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("pause: %d %s", resp.StatusCode, body)
	}
	if resp, _ = env.Do(env.OperatorSecret, "POST", "/v1/admin/subscriptions/sub_NOPE/pause", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("pause unknown: %d", resp.StatusCode)
	}
	if resp, _ = env.Do(dotSecret, "POST", "/v1/admin/subscriptions/"+res.ID+"/pause", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pause by a peer: %d", resp.StatusCode)
	}
	subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	sendPriority(t, env, sender, dot, "one", "wake")
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 0 {
		t.Fatalf("POSTs while paused = %d, want 0", n)
	}
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/admin/audit?limit=5", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "subscription_pause") {
		t.Fatalf("audit: %d %s", resp.StatusCode, body)
	}
	if resp, body = env.Do(env.OperatorSecret, "POST", "/v1/admin/subscriptions/"+res.ID+"/resume", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resume: %d %s", resp.StatusCode, body)
	}
	stepEvents(t, env)
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("POSTs after resume = %d, want 1", n)
	}
}

// Operators see the last refresh apart from every other write to the row, and
// a new subscription leaves an audit row naming the host only.
func TestSubscriptionHealthAndAudit(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	key := webhook.NewSecret()
	subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	lastRefresh := func() *string {
		t.Helper()
		resp, body := env.Do(env.OperatorSecret, "GET", "/v1/admin/subscriptions", nil)
		var rows []struct {
			LastRefreshAt *string `json:"last_refresh_at"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rows) != nil || len(rows) != 1 {
			t.Fatalf("subscriptions: %d %s", resp.StatusCode, body)
		}
		return rows[0].LastRefreshAt
	}
	if at := lastRefresh(); at != nil {
		t.Fatalf("last_refresh_at before any refresh = %s", *at)
	}
	env.Now.Advance(time.Minute)
	subscribeOK(t, env, dotSecret, subParams(cb.URL, key, nil))
	want := venv.FormatTime(env.Now.Now())
	env.Now.Advance(time.Minute)
	sendPriority(t, env, sender, dot, "one", "wake")
	stepEvents(t, env) // moves the cursor, which writes updated_at
	if at := lastRefresh(); at == nil || *at != want {
		t.Fatalf("last_refresh_at = %v, want %s", at, want)
	}

	resp, body := env.Do(env.OperatorSecret, "GET", "/v1/admin/audit?limit=50", nil)
	if resp.StatusCode != http.StatusOK || strings.Count(string(body), `"event_subscribe"`) != 1 {
		t.Fatalf("audit should hold one event_subscribe: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "path-with-token") {
		t.Fatalf("audit keeps the callback path: %s", body)
	}
}

func TestSubscribeCapAnswersTooMany(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "d")
	cb := testhub.NewCallback(t)
	for i := range events.MaxPerPrincipal {
		subscribeOK(t, env, secret, subParams(fmt.Sprintf("%s/%d", cb.URL, i), webhook.NewSecret(), nil))
	}
	r := rpc2026(t, env, secret, "events/subscribe", subParams(cb.URL+"/over", webhook.NewSecret(), nil))
	if r.Error == nil || r.Error.Code != mcpserver.ErrInvalidParams || errReason(t, r) != "too_many" {
		t.Fatalf("over the cap = %+v / %s", r.Error, r.Result)
	}
}
