package mcpserver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

func peerURI(id string) string { return "vecnet://peer/" + id + "/inbox" }
func prnURI(id string) string  { return "vecnet://principal/" + id + "/inbox" }

// peerUnder adds and enrols a peer under the principal prnID.
func peerUnder(t *testing.T, env *testhub.Env, prnID, name string) (peerID, secret string) {
	t.Helper()
	ctx := context.Background()
	id, tok, err := env.Hub.AddPeer(ctx, "session", name, prnID)
	if err != nil {
		t.Fatalf("AddPeer(%s): %v", name, err)
	}
	if _, secret, err = env.Hub.Auth.Enrol(ctx, tok); err != nil {
		t.Fatalf("Enrol(%s): %v", name, err)
	}
	return id, secret
}

// rpc2026 posts one 2026-07-28 JSON-RPC request and decodes the response.
func rpc2026(t *testing.T, env *testhub.Env, secret, method string, params map[string]any) rpcResp {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	resp, out := do(t, env, call{secret: secret, headers: modern(), body: body})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", method, resp.StatusCode, out)
	}
	return decodeRPC(t, out)
}

// sendTo posts a wake message from secret to recipient and returns its text.
func sendTo(t *testing.T, env *testhub.Env, secret, recipient, key string) string {
	t.Helper()
	text := "secret-content-" + key
	resp, out := env.Do(secret, "POST", "/v1/messages", map[string]any{
		"idempotency_key": key,
		"priority":        "wake",
		"recipients":      []string{recipient},
		"content":         map[string]any{"text": text},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s: %d %s", key, resp.StatusCode, out)
	}
	return text
}

func errReason(t *testing.T, r rpcResp) string {
	t.Helper()
	if r.Error == nil {
		t.Fatalf("want a JSON-RPC error, got result %s", r.Result)
	}
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(r.Error.Data, &d); err != nil {
		t.Fatalf("error data %s: %v", r.Error.Data, err)
	}
	return d.Reason
}

func listURIs(t *testing.T, env *testhub.Env, secret string) []string {
	t.Helper()
	r := rpc2026(t, env, secret, "resources/list", nil)
	if r.Error != nil {
		t.Fatalf("resources/list: %+v", r.Error)
	}
	var res struct {
		Resources []struct {
			URI      string `json:"uri"`
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
		} `json:"resources"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode %s: %v", r.Result, err)
	}
	if res.NextCursor != nil {
		t.Fatalf("nextCursor present: %s", r.Result)
	}
	var uris []string
	for _, x := range res.Resources {
		if x.Name == "" || x.MimeType != "application/json" {
			t.Fatalf("resource %+v: want a name and mimeType application/json", x)
		}
		uris = append(uris, x.URI)
	}
	slices.Sort(uris)
	return uris
}

func TestResourcesListPerPrincipal(t *testing.T) {
	env := testhub.Start(t)
	peer, peerSecret := env.NewPeer("session", "solo")
	if got := listURIs(t, env, peerSecret); !slices.Equal(got, []string{peerURI(peer)}) {
		t.Fatalf("peer credential lists %v, want [%s]", got, peerURI(peer))
	}

	prn, prnSecret := env.NewPrincipal("max")
	p1, _ := peerUnder(t, env, prn, "one")
	p2, _ := peerUnder(t, env, prn, "two")
	want := []string{peerURI(p1), peerURI(p2), prnURI(prn)}
	slices.Sort(want)
	got := listURIs(t, env, prnSecret)
	if !slices.Equal(got, want) {
		t.Fatalf("principal lists %v, want %v", got, want)
	}
	if len(got) != 3 {
		t.Fatalf("principal lists %d URIs, want 3", len(got))
	}
}

func TestResourcesRead(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	c, _ := env.NewPeer("session", "c1")
	// Enrolled last: a peer's log also carries directory_updated events about
	// peers enrolled after it, and this test wants the delivery alone.
	b, bSecret := env.NewPeer("session", "b1")
	text := sendTo(t, env, aSecret, b, "r1")

	r := rpc2026(t, env, bSecret, "resources/read", map[string]any{"uri": peerURI(b)})
	if r.Error != nil {
		t.Fatalf("resources/read: %+v", r.Error)
	}
	var res struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode %s: %v", r.Result, err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d, want 1 TextResourceContents", len(res.Contents))
	}
	tc := res.Contents[0]
	if tc.URI != peerURI(b) || tc.MimeType != "application/json" {
		t.Fatalf("contents[0] uri %q mimeType %q", tc.URI, tc.MimeType)
	}
	var inbox struct {
		Events []struct {
			Cursor string          `json:"cursor"`
			Type   string          `json:"type"`
			Data   json.RawMessage `json:"data"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(tc.Text), &inbox); err != nil {
		t.Fatalf("text %q does not parse: %v", tc.Text, err)
	}
	if len(inbox.Events) != 1 {
		t.Fatalf("events = %d, want 1: %s", len(inbox.Events), tc.Text)
	}
	if inbox.NextCursor == "" || inbox.NextCursor != inbox.Events[0].Cursor {
		t.Fatalf("next_cursor %q, want the event cursor %q", inbox.NextCursor, inbox.Events[0].Cursor)
	}
	if !strings.Contains(tc.Text, text) {
		t.Fatalf("inbox read lacks the delivered message %q: %s", text, tc.Text)
	}

	// Another peer's inbox is not readable.
	r = rpc2026(t, env, bSecret, "resources/read", map[string]any{"uri": peerURI(c)})
	if r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("other peer's URI: error %+v, want -32602", r.Error)
	}
	if got := errReason(t, r); got != "forbidden" {
		t.Fatalf("reason = %q, want forbidden", got)
	}
}

// frame is one SSE event or comment read off a stream.
type frame struct {
	comment string          // ": x" lines, without the ": "
	data    json.RawMessage // the data of an event
}

// readFrames parses an SSE body into frames until it ends.
func readFrames(body io.Reader, out chan<- frame) {
	defer close(out)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				out <- frame{data: json.RawMessage(strings.Join(data, "\n"))}
				data = nil
			}
		case strings.HasPrefix(line, ":"):
			out <- frame{comment: strings.TrimSpace(strings.TrimPrefix(line, ":"))}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

type notif struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
	Result  json.RawMessage `json:"result"`
}

// listen opens a 2026 subscriptions/listen stream and returns its frames.
func listen(t *testing.T, ctx context.Context, env *testhub.Env, cl *http.Client, secret string, uris []string) (*http.Response, <-chan frame) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "subscriptions/listen",
		"params": map[string]any{"resourceSubscriptions": uris}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.URL+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range modern() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("listen: %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	ch := make(chan frame, 64)
	go readFrames(resp.Body, ch)
	return resp, ch
}

// nextMessage returns the next JSON-RPC message on frames, skipping comments.
func nextMessage(t *testing.T, frames <-chan frame, within time.Duration) (notif, json.RawMessage, bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return notif{}, nil, false
			}
			if f.data == nil {
				continue
			}
			var n notif
			if err := json.Unmarshal(f.data, &n); err != nil {
				t.Fatalf("frame %s: %v", f.data, err)
			}
			return n, f.data, true
		case <-deadline:
			return notif{}, nil, false
		}
	}
}

// wantSubscriptionID checks n's _meta names the listen request id (9), under
// the spec's key and the plain one.
func wantSubscriptionID(t *testing.T, n notif, raw json.RawMessage) {
	t.Helper()
	meta, _ := n.Params["_meta"].(map[string]any)
	for _, k := range []string{"subscriptionId", "io.modelcontextprotocol/subscriptionId"} {
		if id, ok := meta[k].(float64); !ok || id != 9 {
			t.Fatalf("_meta[%q] = %v, want the listen request id 9: %s", k, meta[k], raw)
		}
	}
}

func TestListenAckThenHints2026(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 50 * time.Millisecond })
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	uri := peerURI(b)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, bSecret, []string{uri})
	defer resp.Body.Close()

	ack, raw, ok := nextMessage(t, frames, 2*time.Second)
	if !ok {
		t.Fatal("no first message on the listen stream")
	}
	if ack.Method != "notifications/subscriptions/acknowledged" {
		t.Fatalf("first message %s, want notifications/subscriptions/acknowledged", raw)
	}
	if ack.JSONRPC != "2.0" || ack.ID != nil {
		t.Fatalf("ack is not a JSON-RPC notification: %s", raw)
	}
	wantSubscriptionID(t, ack, raw)
	honoured, _ := ack.Params["notifications"].(map[string]any)
	if got, _ := honoured["resourceSubscriptions"].([]any); len(got) != 1 || got[0] != uri {
		t.Fatalf("ack does not echo the honoured filter: %s", raw)
	}

	// Keep-alives on the idle stream.
	gotHB := false
	for deadline := time.After(2 * time.Second); !gotHB; {
		select {
		case f := <-frames:
			if f.data != nil {
				t.Fatalf("unexpected message on an idle stream: %s", f.data)
			}
			gotHB = f.comment == "hb"
		case <-deadline:
			t.Fatal("no : hb keep-alive on the listen stream")
		}
	}

	text := sendTo(t, env, aSecret, b, "h1")
	start := time.Now()
	up, raw, ok := nextMessage(t, frames, time.Second)
	if !ok {
		t.Fatal("no notifications/resources/updated within 1s of the send")
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("hint after %v", el)
	}
	if up.Method != "notifications/resources/updated" || up.ID != nil {
		t.Fatalf("message %s, want notifications/resources/updated", raw)
	}
	if got, _ := up.Params["uri"].(string); got != uri {
		t.Fatalf("updated uri %q, want %q", got, uri)
	}
	wantSubscriptionID(t, up, raw)
	for k := range up.Params {
		if k != "uri" && k != "_meta" {
			t.Fatalf("hint carries extra param %q: %s", k, raw)
		}
	}
	if strings.Contains(string(raw), text) || strings.Contains(string(raw), "content") {
		t.Fatalf("hint carries message content: %s", raw)
	}

	// Two quick sends after the coalescing window: one or two hints.
	time.Sleep(300 * time.Millisecond)
	sendTo(t, env, aSecret, b, "h2")
	sendTo(t, env, aSecret, b, "h3")
	n := 0
	deadline := time.After(700 * time.Millisecond)
collect:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				break collect
			}
			if f.data == nil {
				continue
			}
			var m notif
			if err := json.Unmarshal(f.data, &m); err != nil {
				t.Fatal(err)
			}
			if m.Method != "notifications/resources/updated" {
				t.Fatalf("unexpected message %s", f.data)
			}
			n++
		case <-deadline:
			break collect
		}
	}
	if n < 1 || n > 2 {
		t.Fatalf("two quick sends produced %d updated frames, want 1 or 2", n)
	}

	// A URI the caller may not read is refused before any stream starts.
	c, _ := env.NewPeer("session", "c1")
	r := rpc2026(t, env, bSecret, "subscriptions/listen", map[string]any{"resourceSubscriptions": []string{peerURI(c)}})
	if r.Error == nil || r.Error.Code != -32602 || errReason(t, r) != "not_subscribable" {
		t.Fatalf("listen on another peer's inbox: %+v", r.Error)
	}
}

func TestLegacySubscribeHintsOnGetStream(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	c, _ := env.NewPeer("session", "c1")
	uri := peerURI(b)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	updated := make(chan string, 16)
	client := mcp.NewClient(&mcp.Implementation{Name: "vecnet-test", Version: "1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			updated <- req.Params.URI
		},
	})
	tr := &mcp.StreamableClientTransport{
		Endpoint:   env.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerRT{secret: bSecret}},
	}
	cs, err := client.Connect(ctx, tr, &mcp.ClientSessionOptions{ProtocolVersion: mcpserver.ProtoLegacy})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cs.Close()

	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: peerURI(c)}); err == nil {
		t.Fatal("subscribe to another peer's inbox succeeded")
	} else if !strings.Contains(err.Error(), "not_subscribable") {
		t.Fatalf("subscribe to another peer's inbox: %v, want not_subscribable", err)
	}
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	sendTo(t, env, aSecret, b, "l1")
	select {
	case got := <-updated:
		if got != uri {
			t.Fatalf("ResourceUpdated uri %q, want %q", got, uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ResourceUpdated on the GET stream after a send")
	}

	// Unsubscribed: no more hints.
	if err := cs.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	for len(updated) > 0 {
		<-updated
	}
	time.Sleep(300 * time.Millisecond)
	sendTo(t, env, aSecret, b, "l2")
	select {
	case got := <-updated:
		t.Fatalf("hint %q after unsubscribe", got)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestHintsStopOnDisconnect(t *testing.T) {
	env := testhub.Start(t)
	b, bSecret := env.NewPeer("session", "b1")
	tr := &http.Transport{}
	cl := &http.Client{Transport: tr}
	defer tr.CloseIdleConnections()

	// Warm the server so its per-test goroutines exist before the baseline.
	if r := rpc2026(t, env, bSecret, "ping", nil); r.Error != nil {
		t.Fatalf("ping: %+v", r.Error)
	}
	http.DefaultClient.CloseIdleConnections()
	runtime.GC()
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	resp, frames := listen(t, ctx, env, cl, bSecret, []string{peerURI(b)})
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	if env.Hub.Log.Waiters(b) == 0 {
		t.Fatal("listen stream is not waiting on the peer's log after the acknowledgement")
	}
	cancel()
	resp.Body.Close()
	tr.CloseIdleConnections()

	deadline := time.Now().Add(time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline+2 && env.Hub.Log.Waiters(b) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after disconnect: %d goroutines (baseline %d), %d log waiters on the peer", n, baseline, env.Hub.Log.Waiters(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestResourcesReadTail: 55 deliveries read back as the newest 50.
func TestResourcesReadTail(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	env.SetLimits(a, 1000, 1000)
	for i := range 55 {
		sendTo(t, env, aSecret, b, "t"+strconv.Itoa(i))
	}
	r := rpc2026(t, env, bSecret, "resources/read", map[string]any{"uri": peerURI(b)})
	if r.Error != nil {
		t.Fatalf("resources/read: %+v", r.Error)
	}
	var res struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil || len(res.Contents) != 1 {
		t.Fatalf("result %s: %v", r.Result, err)
	}
	var inbox struct {
		Events []struct {
			Cursor string `json:"cursor"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Events) != 50 {
		t.Fatalf("events = %d, want 50", len(inbox.Events))
	}
	if !strings.Contains(res.Contents[0].Text, "secret-content-t54") || !strings.Contains(res.Contents[0].Text, "secret-content-t5\"") {
		t.Fatalf("tail does not run from t5 to t54: %s", res.Contents[0].Text)
	}
	if strings.Contains(res.Contents[0].Text, "secret-content-t4\"") {
		t.Fatal("tail holds an event older than the newest 50")
	}
	if want := inbox.Events[49].Cursor; inbox.NextCursor != want {
		t.Fatalf("next_cursor %q, want the newest event's %q", inbox.NextCursor, want)
	}
}

// TestListenEmptyFilterCompletes: nothing to wait for, so the stream gives
// the acknowledgement and the final result rather than hanging open.
func TestListenEmptyFilterCompletes(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "a1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, secret, nil)
	defer resp.Body.Close()
	ack, raw, ok := nextMessage(t, frames, 2*time.Second)
	if !ok || ack.Method != "notifications/subscriptions/acknowledged" {
		t.Fatalf("first message %s", raw)
	}
	res, raw, ok := nextMessage(t, frames, 2*time.Second)
	if !ok || string(res.ID) != "9" || res.Result == nil || res.Method != "" {
		t.Fatalf("second message %s, want the result of request 9", raw)
	}
	if _, raw, ok := nextMessage(t, frames, 2*time.Second); ok {
		t.Fatalf("message after the result: %s", raw)
	}
}

// TestListenSDKClient2026 drives subscriptions/listen with the go-sdk client,
// which puts the filter under params.notifications.
func TestListenSDKClient2026(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	uri := peerURI(b)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	updated := make(chan string, 16)
	client := mcp.NewClient(&mcp.Implementation{Name: "vecnet-test", Version: "1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			updated <- req.Params.URI
		},
	})
	tr := &mcp.StreamableClientTransport{
		Endpoint:   env.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerRT{secret: bSecret}},
	}
	cs, err := client.Connect(ctx, tr, &mcp.ClientSessionOptions{ProtocolVersion: mcpserver.ProtoCurrent})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cs.Close()
	if got := cs.InitializeResult().ProtocolVersion; got != mcpserver.ProtoCurrent {
		t.Fatalf("negotiated %q", got)
	}
	// Subscribe parks on the listen stream until it ends.
	go func() { _ = cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}) }()
	waitFor(t, 5*time.Second, "listen stream to park on the log", func() bool { return env.Hub.Log.Waiters(b) > 0 })

	sendTo(t, env, aSecret, b, "s1")
	select {
	case got := <-updated:
		if got != uri {
			t.Fatalf("ResourceUpdated uri %q, want %q", got, uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ResourceUpdated on the listen stream after a send")
	}
}

// TestPrincipalListenCoversNewPeer: a principal's URI hints for a peer
// enrolled after the stream opened.
func TestPrincipalListenCoversNewPeer(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	prn, prnSecret := env.NewPrincipal("max")
	p1, _ := peerUnder(t, env, prn, "one")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, prnSecret, []string{prnURI(prn)})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	if env.Hub.Log.Waiters(p1) == 0 {
		t.Fatal("not waiting on the existing peer's log")
	}

	p2, _ := peerUnder(t, env, prn, "two")
	if _, raw, ok := nextMessage(t, frames, time.Second); !ok {
		t.Fatalf("no hint for the enrolment's directory_updated: %s", raw)
	}
	waitFor(t, 2*time.Second, "watcher to re-arm on the new peer", func() bool { return env.Hub.Log.Waiters(p2) > 0 })
	time.Sleep(300 * time.Millisecond) // past the coalescing window

	sendTo(t, env, aSecret, p2, "n1")
	up, raw, ok := nextMessage(t, frames, time.Second)
	if !ok || up.Method != "notifications/resources/updated" || up.Params["uri"] != prnURI(prn) {
		t.Fatalf("no hint on the principal URI for its new peer: %s", raw)
	}
}

// getStream opens the 2025 GET stream of session sid.
func getStream(t *testing.T, ctx context.Context, env *testhub.Env, secret, sid string) (*http.Response, <-chan frame) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Mcp-Session-Id", sid)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET: %d %s", resp.StatusCode, b)
	}
	ch := make(chan frame, 64)
	go readFrames(resp.Body, ch)
	return resp, ch
}

// legacySubscribe posts resources/subscribe on session sid.
func legacySubscribe(t *testing.T, env *testhub.Env, secret, sid, uri string) {
	t.Helper()
	resp, out := do(t, env, call{secret: secret,
		headers: map[string]string{"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream"},
		body: map[string]any{"jsonrpc": "2.0", "id": 2, "method": "resources/subscribe",
			"params": map[string]any{"uri": uri}}})
	if r := decodeRPC(t, out); resp.StatusCode != http.StatusOK || r.Error != nil {
		t.Fatalf("resources/subscribe: %d %s", resp.StatusCode, out)
	}
}

// TestLegacyHintHeldUntilGetOpens: a hint due while no GET stream is open
// arrives when one opens. It runs a Server of its own so that it can see the
// hint held, rather than hope the watcher fired before the GET.
func TestLegacyHintHeldUntilGetOpens(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	uri := peerURI(b)

	srv, own := ownFront(t, env)

	sid := legacySession(t, own, bSecret)
	legacySubscribe(t, own, bSecret, sid, uri)
	sendTo(t, env, aSecret, b, "g1")
	waitFor(t, 2*time.Second, "the hint to be held", func() bool { return srv.HeldHints(sid) == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, frames := getStream(t, ctx, own, bSecret, sid)
	defer resp.Body.Close()
	up, raw, ok := nextMessage(t, frames, 2*time.Second)
	if !ok || up.Method != "notifications/resources/updated" || up.Params["uri"] != uri {
		t.Fatalf("no held hint on the GET stream: %s", raw)
	}
	if n := srv.HeldHints(sid); n != 0 {
		t.Fatalf("%d hints still held after the GET opened", n)
	}
}

// TestLegacyHintsGoToOneStream: a second GET replaces the first, and a hint
// goes to the second alone.
func TestLegacyHintsGoToOneStream(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	uri := peerURI(b)
	sid := legacySession(t, env, bSecret)
	legacySubscribe(t, env, bSecret, sid, uri)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, firstFrames := getStream(t, ctx, env, bSecret, sid)
	defer first.Body.Close()
	second, secondFrames := getStream(t, ctx, env, bSecret, sid)
	defer second.Body.Close()
	if _, raw, ok := nextMessage(t, firstFrames, 2*time.Second); ok {
		t.Fatalf("first stream got a message: %s", raw)
	}
	select { // closed, rather than nextMessage's timeout
	case _, open := <-firstFrames:
		if open {
			t.Fatal("first stream still open after a second GET")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first stream still open after a second GET")
	}

	sendTo(t, env, aSecret, b, "o1")
	up, raw, ok := nextMessage(t, secondFrames, 2*time.Second)
	if !ok || up.Method != "notifications/resources/updated" || up.Params["uri"] != uri {
		t.Fatalf("no hint on the second stream: %s", raw)
	}
}

// TestPrincipalListenFromZeroPeers: a principal's URI has no owners until its
// first peer exists, and must still hint for that peer. A stream parked on
// no owners stays silent for good while its keep-alives hide that from the
// client (PROTOCOL §4.2).
func TestPrincipalListenFromZeroPeers(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	prn, prnSecret := env.NewPrincipal("max")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, prnSecret, []string{prnURI(prn)})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}

	p1, _ := peerUnder(t, env, prn, "one")
	sendTo(t, env, aSecret, p1, "z1")
	up, raw, ok := nextMessage(t, frames, time.Second)
	if !ok || up.Method != "notifications/resources/updated" || up.Params["uri"] != prnURI(prn) {
		t.Fatalf("no hint for a principal subscribed with zero peers: %s", raw)
	}
}

// TestLegacyPrincipalSubscribeFromZeroPeers is the same on a 2025 session.
func TestLegacyPrincipalSubscribeFromZeroPeers(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	prn, prnSecret := env.NewPrincipal("max")
	sid := legacySession(t, env, prnSecret)
	legacySubscribe(t, env, prnSecret, sid, prnURI(prn))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := getStream(t, ctx, env, prnSecret, sid)
	defer resp.Body.Close()

	p1, _ := peerUnder(t, env, prn, "one")
	sendTo(t, env, aSecret, p1, "z2")
	up, raw, ok := nextMessage(t, frames, time.Second)
	if !ok || up.Method != "notifications/resources/updated" || up.Params["uri"] != prnURI(prn) {
		t.Fatalf("no hint for a principal subscribed with zero peers: %s", raw)
	}
}

// TestPrincipalListenCoversMovedPeer: a peer moved in from another principal
// joins the URI's owners, with or without peers already there.
func TestPrincipalListenCoversMovedPeer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		peers int
	}{{"zero peers", 0}, {"one peer", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			_, aSecret := env.NewPeer("session", "a1")
			moved, _ := env.NewPeer("session", "moved")
			prn, prnSecret := env.NewPrincipal("max")
			for range tc.peers {
				peerUnder(t, env, prn, "one")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			resp, frames := listen(t, ctx, env, http.DefaultClient, prnSecret, []string{prnURI(prn)})
			defer resp.Body.Close()
			if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
				t.Fatal("no acknowledgement")
			}

			if err := env.Hub.MovePeer(ctx, moved, prn); err != nil {
				t.Fatalf("MovePeer: %v", err)
			}
			sendTo(t, env, aSecret, moved, "m1")
			up, raw, ok := nextMessage(t, frames, time.Second)
			if !ok || up.Method != "notifications/resources/updated" || up.Params["uri"] != prnURI(prn) {
				t.Fatalf("no hint for a peer moved under the principal: %s", raw)
			}
		})
	}
}

// TestListenBurstCoalesced pins the rate: at most one hint per URI per 250 ms
// however fast the log grows.
func TestListenBurstCoalesced(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	env.SetLimits(a, 1000, 1000)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, bSecret, []string{peerURI(b)})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}

	const sends = 20
	start := time.Now()
	for i := range sends {
		sendTo(t, env, aSecret, b, "b"+strconv.Itoa(i))
	}
	n := 0
	deadline := time.After(400 * time.Millisecond)
collect:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("listen stream ended")
			}
			if f.data != nil {
				n++
			}
		case <-deadline:
			break collect
		}
	}
	elapsed := time.Since(start)
	if most := 3 + int(elapsed/(250*time.Millisecond)); n < 1 || n > most {
		t.Fatalf("%d sends in %v gave %d hints, want 1 to %d", sends, elapsed, n, most)
	}
}

// TestNotSubscribableReason: the legacy paths refuse every URI the caller may
// not read with -32602 not_subscribable, whatever the reason beneath.
func TestNotSubscribableReason(t *testing.T) {
	env := testhub.Start(t)
	_, bSecret := env.NewPeer("session", "b1")
	c, _ := env.NewPeer("session", "c1")
	prn, _ := env.NewPrincipal("max")
	sid := legacySession(t, env, bSecret)

	for _, method := range []string{"resources/subscribe", "resources/unsubscribe"} {
		for _, uri := range []string{peerURI(c), prnURI(prn), "vecnet://nonsense", "https://example.com/inbox"} {
			_, out := do(t, env, call{secret: bSecret,
				headers: map[string]string{"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream"},
				body: map[string]any{"jsonrpc": "2.0", "id": 2, "method": method,
					"params": map[string]any{"uri": uri}}})
			r := decodeRPC(t, out)
			if r.Error == nil || r.Error.Code != -32602 || errReason(t, r) != "not_subscribable" {
				t.Fatalf("%s %s: %s, want -32602 not_subscribable", method, uri, out)
			}
		}
	}
}

// inboxText reads uri with secret and returns the resource's text.
func inboxText(t *testing.T, env *testhub.Env, secret, uri string) string {
	t.Helper()
	r := rpc2026(t, env, secret, "resources/read", map[string]any{"uri": uri})
	if r.Error != nil {
		t.Fatalf("resources/read %s: %+v", uri, r.Error)
	}
	var res struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil || len(res.Contents) != 1 {
		t.Fatalf("result %s: %v", r.Result, err)
	}
	return res.Contents[0].Text
}

// TestResourcesReadPrincipal: a principal reads its URI and its own peers',
// and nobody else's.
func TestResourcesReadPrincipal(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	prn, prnSecret := env.NewPrincipal("max")
	p1, p1Secret := peerUnder(t, env, prn, "one")
	p2, _ := peerUnder(t, env, prn, "two")
	other, _ := env.NewPrincipal("other")
	stranger, _ := peerUnder(t, env, other, "stranger")
	t1 := sendTo(t, env, aSecret, p1, "p1")
	t2 := sendTo(t, env, aSecret, p2, "p2")

	all := inboxText(t, env, prnSecret, prnURI(prn))
	if !strings.Contains(all, t1) || !strings.Contains(all, t2) {
		t.Fatalf("principal URI lacks a peer's delivery: %s", all)
	}
	one := inboxText(t, env, prnSecret, peerURI(p2))
	if !strings.Contains(one, t2) || strings.Contains(one, t1) {
		t.Fatalf("peer URI read by its principal: %s", one)
	}

	if err := env.Hub.SetPeerDisabled(context.Background(), p2, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, secret, uri string
	}{
		{"another principal's URI", prnSecret, prnURI(other)},
		{"another principal's peer", prnSecret, peerURI(stranger)},
		{"its own disabled peer", prnSecret, peerURI(p2)},
		{"a principal URI with a peer credential", p1Secret, prnURI(prn)},
		{"a sibling peer", p1Secret, peerURI(p2)},
	} {
		r := rpc2026(t, env, tc.secret, "resources/read", map[string]any{"uri": tc.uri})
		if r.Error == nil || r.Error.Code != -32602 || errReason(t, r) != "forbidden" {
			t.Fatalf("%s: error %+v, want -32602 forbidden", tc.name, r.Error)
		}
	}
}

// wantUnfinished checks the stream ends within a second with no further
// message: a final result would tell the client not to reconnect.
func wantUnfinished(t *testing.T, frames <-chan frame) {
	t.Helper()
	for deadline := time.After(time.Second); ; {
		select {
		case f, open := <-frames:
			if !open {
				return
			}
			if f.data != nil {
				t.Fatalf("message on an ending stream: %s", f.data)
			}
		case <-deadline:
			t.Fatal("stream still open")
		}
	}
}

// TestListenEndsOnStop: hub shutdown leaves the stream unfinished.
func TestListenEndsOnStop(t *testing.T) {
	env := testhub.Start(t)
	stop := make(chan struct{})
	env.Configure(func(d *httpapi.Deps) { d.Stop = stop })
	b, bSecret := env.NewPeer("session", "b1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, bSecret, []string{peerURI(b)})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	close(stop)
	wantUnfinished(t, frames)
}

// TestListenEndsWhenPeerDisabled: a disabled peer's own stream stops hinting
// and ends, as any listen stream does once every URI is lost.
func TestListenEndsWhenPeerDisabled(t *testing.T) {
	env := testhub.Start(t)
	b, bSecret := env.NewPeer("session", "b1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, bSecret, []string{peerURI(b)})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	if err := env.Hub.SetPeerDisabled(ctx, b, true); err != nil {
		t.Fatal(err)
	}
	wantUnfinished(t, frames)
}

// reachability returns peerID's directory reachability.
func reachability(t *testing.T, env *testhub.Env, peerID string) string {
	t.Helper()
	dir, err := env.Hub.Directory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range dir {
		if e.PeerID == peerID {
			return e.Reachability
		}
	}
	t.Fatalf("peer %s not in the directory", peerID)
	return ""
}

// TestStreamsMarkConnected: an open listen or GET stream makes its peer
// connected, as an open /v1/events stream does (PROTOCOL §8).
func TestStreamsMarkConnected(t *testing.T) {
	env := testhub.Start(t)
	b, bSecret := env.NewPeer("session", "b1")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lctx, lcancel := context.WithCancel(ctx)
	resp, frames := listen(t, lctx, env, http.DefaultClient, bSecret, []string{peerURI(b)})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	if got := reachability(t, env, b); got != "connected" {
		t.Fatalf("with a listen stream open: %s, want connected", got)
	}
	lcancel()
	waitFor(t, 2*time.Second, "listen close to end connected", func() bool { return reachability(t, env, b) == "reachable" })

	sid := legacySession(t, env, bSecret)
	gctx, gcancel := context.WithCancel(ctx)
	defer gcancel()
	get, _ := getStream(t, gctx, env, bSecret, sid)
	defer get.Body.Close()
	waitFor(t, 2*time.Second, "GET stream to mark connected", func() bool { return reachability(t, env, b) == "connected" })
	gcancel()
	waitFor(t, 2*time.Second, "GET close to end connected", func() bool { return reachability(t, env, b) == "reachable" })
}

// blockingWriter is a ResponseWriter whose Write parks on release, after the
// first frame it sees past the stream's retry line.
type blockingWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
}

func (b *blockingWriter) Header() http.Header { return b.header }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Flush()              {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "data:") {
		close(b.entered)
		<-b.release
	}
	return len(p), nil
}

// TestStreamerCloseWaitsForSend: a handler returns right after Close, and
// net/http then finishes the response, so Close must not return while a
// Send is still writing. Hints are written from watcher goroutines.
func TestStreamerCloseWaitsForSend(t *testing.T) {
	s := mcpserver.New(nil, nil, "http://hub.example")
	w := &blockingWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	st, err := s.Stream(w, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = st.Send("", "message", map[string]any{"x": 1}) }()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Send never reached Write")
	}
	closed := make(chan struct{})
	go func() {
		st.Close()
		close(closed)
	}()
	select {
	case <-closed:
		close(w.release)
		t.Fatal("Close returned while a Send was mid-write")
	case <-time.After(100 * time.Millisecond):
	}
	close(w.release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return once the Send finished")
	}
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// hintFor waits for a hint on uri, skipping hints on other URIs. It reports
// false when none comes in time or the stream ends.
func hintFor(t *testing.T, frames <-chan frame, uri string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		n, raw, ok := nextMessage(t, frames, time.Until(deadline))
		if !ok {
			return false
		}
		if n.Method != "notifications/resources/updated" {
			t.Fatalf("unexpected message %s", raw)
		}
		if n.Params["uri"] == uri {
			return true
		}
	}
}

// noHintFor fails on a hint for uri during the next 300 ms, which lets a
// watcher that is due one get past the coalescing window and send it.
func noHintFor(t *testing.T, frames <-chan frame, uri string) {
	t.Helper()
	if hintFor(t, frames, uri, 300*time.Millisecond) {
		t.Fatalf("hint for %s while its access is lost", uri)
	}
}

// growLog appends to peer's log and wakes its watchers, with no message: a
// disabled peer takes no deliveries.
func growLog(t *testing.T, env *testhub.Env, peer string) {
	t.Helper()
	if _, err := env.Hub.Log.Append(context.Background(), nil, peer, "test", env.Now.Now(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	env.Hub.Log.Notify(peer)
}

// accessChanges are the two ways a principal loses a peer and gets it back.
var accessChanges = []struct {
	name          string
	lose, restore func(ctx context.Context, env *testhub.Env, peer, prn, elsewhere string) error
}{
	{"disable",
		func(ctx context.Context, env *testhub.Env, peer, _, _ string) error {
			return env.Hub.SetPeerDisabled(ctx, peer, true)
		},
		func(ctx context.Context, env *testhub.Env, peer, _, _ string) error {
			return env.Hub.SetPeerDisabled(ctx, peer, false)
		}},
	{"move",
		func(ctx context.Context, env *testhub.Env, peer, _, elsewhere string) error {
			return env.Hub.MovePeer(ctx, peer, elsewhere)
		},
		func(ctx context.Context, env *testhub.Env, peer, prn, _ string) error {
			return env.Hub.MovePeer(ctx, peer, prn)
		}},
}

// TestLegacySubscriptionSurvivesAccessLoss: a legacy GET stream outlives its
// URIs, so losing access must not end the subscription for good. It stays
// silent while access is gone and hints again once access is back, for what
// grew meanwhile and for what comes later (PROTOCOL §4.2).
func TestLegacySubscriptionSurvivesAccessLoss(t *testing.T) {
	run := func(t *testing.T, peerCredential bool, lose, restore func(context.Context, *testhub.Env, string, string, string) error) {
		env := testhub.Start(t)
		_, aSecret := env.NewPeer("session", "a1")
		prn, secret := env.NewPrincipal("max")
		elsewhere, _ := env.NewPrincipal("elsewhere")
		p, pSecret := peerUnder(t, env, prn, "one")
		if peerCredential {
			secret = pSecret
		}
		uri := peerURI(p)
		sid := legacySession(t, env, secret)
		legacySubscribe(t, env, secret, sid, uri)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, frames := getStream(t, ctx, env, secret, sid)
		defer resp.Body.Close()
		sendTo(t, env, aSecret, p, "s1")
		if !hintFor(t, frames, uri, time.Second) {
			t.Fatal("no baseline hint")
		}

		if err := lose(ctx, env, p, prn, elsewhere); err != nil {
			t.Fatal(err)
		}
		noHintFor(t, frames, uri) // long enough to see the loss, past any pause
		growLog(t, env, p)
		noHintFor(t, frames, uri)

		if err := restore(ctx, env, p, prn, elsewhere); err != nil {
			t.Fatal(err)
		}
		if !hintFor(t, frames, uri, time.Second) {
			t.Fatal("no hint for the growth during the loss")
		}
		time.Sleep(300 * time.Millisecond) // past the coalescing window
		sendTo(t, env, aSecret, p, "s2")
		if !hintFor(t, frames, uri, time.Second) {
			t.Fatal("no hint for a send after access came back")
		}
	}
	for _, tc := range accessChanges {
		t.Run("principal "+tc.name, func(t *testing.T) { run(t, false, tc.lose, tc.restore) })
	}
	t.Run("peer credential disable", func(t *testing.T) {
		run(t, true, accessChanges[0].lose, accessChanges[0].restore)
	})
}

// TestListenSurvivesPartialAccessLoss: one lost URI of a listen stream goes
// quiet and comes back without ending the stream. The stream could not just
// end instead: a reconnect naming the lost URI is refused, and that would
// cost the client its other URIs too. A URI that came back counts as live, so
// losing another one later leaves the stream open.
func TestListenSurvivesPartialAccessLoss(t *testing.T) {
	for _, tc := range accessChanges {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			_, aSecret := env.NewPeer("session", "a1")
			prn, prnSecret := env.NewPrincipal("max")
			elsewhere, _ := env.NewPrincipal("elsewhere")
			p1, _ := peerUnder(t, env, prn, "one")
			p2, _ := peerUnder(t, env, prn, "two")
			uri1 := peerURI(p1)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			resp, frames := listen(t, ctx, env, http.DefaultClient, prnSecret, []string{uri1, peerURI(p2)})
			defer resp.Body.Close()
			if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
				t.Fatal("no acknowledgement")
			}

			if err := tc.lose(ctx, env, p1, prn, elsewhere); err != nil {
				t.Fatal(err)
			}
			noHintFor(t, frames, uri1)
			growLog(t, env, p1)
			noHintFor(t, frames, uri1)

			if err := tc.restore(ctx, env, p1, prn, elsewhere); err != nil {
				t.Fatal(err)
			}
			if !hintFor(t, frames, uri1, time.Second) {
				t.Fatal("no hint for the growth during the loss")
			}
			time.Sleep(300 * time.Millisecond) // past the coalescing window
			sendTo(t, env, aSecret, p1, "s1")
			if !hintFor(t, frames, uri1, time.Second) {
				t.Fatal("no hint for a send after access came back")
			}

			if err := tc.lose(ctx, env, p2, prn, elsewhere); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			sendTo(t, env, aSecret, p1, "s2")
			if !hintFor(t, frames, uri1, time.Second) {
				t.Fatal("stream ended with one URI still live")
			}
		})
	}
}

// TestListenEndsWhenEveryURILost: with nothing left to hint the stream ends
// unfinished, so the client finds out rather than keep a silent stream.
func TestListenEndsWhenEveryURILost(t *testing.T) {
	for _, tc := range accessChanges {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			prn, prnSecret := env.NewPrincipal("max")
			elsewhere, _ := env.NewPrincipal("elsewhere")
			p1, _ := peerUnder(t, env, prn, "one")
			p2, _ := peerUnder(t, env, prn, "two")

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			resp, frames := listen(t, ctx, env, http.DefaultClient, prnSecret, []string{peerURI(p1), peerURI(p2)})
			defer resp.Body.Close()
			if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
				t.Fatal("no acknowledgement")
			}
			for _, p := range []string{p1, p2} {
				if err := tc.lose(ctx, env, p, prn, elsewhere); err != nil {
					t.Fatal(err)
				}
			}
			for deadline := time.After(2 * time.Second); ; {
				select {
				case f, open := <-frames:
					if !open {
						return
					}
					if strings.Contains(string(f.data), `"result"`) {
						t.Fatalf("final result on a stream that lost its URIs: %s", f.data)
					}
				case <-deadline:
					t.Fatal("stream still open with every URI lost")
				}
			}
		})
	}
}

// TestSessionEndStopsWatchers: each way a 2025 session ends stops its
// subscription watchers, which would otherwise wait on the log for good.
func TestSessionEndStopsWatchers(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, env *testhub.Env, secret, sid string)
	}{
		{"delete", func(t *testing.T, env *testhub.Env, secret, sid string) {
			resp, out := do(t, env, call{method: http.MethodDelete, secret: secret,
				headers: map[string]string{"Mcp-Session-Id": sid}})
			if resp.StatusCode >= 300 {
				t.Fatalf("DELETE: %d %s", resp.StatusCode, out)
			}
		}},
		{"idle sweep", func(t *testing.T, env *testhub.Env, secret, _ string) {
			env.Now.Advance(mcpserver.DefaultSessionTTL + time.Minute)
			legacySession(t, env, secret) // minting sweeps
		}},
		{"evicted by the session cap", func(t *testing.T, env *testhub.Env, secret, _ string) {
			for range 64 {
				env.Now.Advance(time.Second) // keeps the subscribed session the oldest
				legacySession(t, env, secret)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			b, bSecret := env.NewPeer("session", "b1")
			sid := legacySession(t, env, bSecret)
			legacySubscribe(t, env, bSecret, sid, peerURI(b))
			if env.Hub.Log.Waiters(b) == 0 {
				t.Fatal("subscription is not waiting on the peer's log")
			}
			tc.end(t, env, bSecret, sid)
			waitFor(t, 2*time.Second, "watcher to stop", func() bool { return env.Hub.Log.Waiters(b) == 0 })
		})
	}
}

// TestSubscribeMethodsAreEraBound: each era has one way to subscribe, and the
// other era's methods do not exist for it.
func TestSubscribeMethodsAreEraBound(t *testing.T) {
	env := testhub.Start(t)
	b, bSecret := env.NewPeer("session", "b1")
	uri := peerURI(b)
	for _, method := range []string{"resources/subscribe", "resources/unsubscribe"} {
		if r := rpc2026(t, env, bSecret, method, map[string]any{"uri": uri}); r.Error == nil || r.Error.Code != -32601 {
			t.Fatalf("%s on 2026-07-28: %+v, want -32601", method, r.Error)
		}
	}
	sid := legacySession(t, env, bSecret)
	_, out := do(t, env, call{secret: bSecret, client: &http.Client{Timeout: 5 * time.Second}, // a stream would never end
		headers: map[string]string{"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream"},
		body: map[string]any{"jsonrpc": "2.0", "id": 2, "method": "subscriptions/listen",
			"params": map[string]any{"resourceSubscriptions": []string{uri}}}})
	if r := decodeRPC(t, out); r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("subscriptions/listen on 2025-11-25: %s, want -32601", out)
	}
}

// TestListenFilterCapped: a filter over 256 URIs is refused before any stream
// starts.
func TestListenFilterCapped(t *testing.T) {
	env := testhub.Start(t)
	b, bSecret := env.NewPeer("session", "b1")
	uris := make([]string, 257)
	for i := range uris {
		uris[i] = peerURI(b) + "?" + strconv.Itoa(i)
	}
	r := rpc2026(t, env, bSecret, "subscriptions/listen", map[string]any{"resourceSubscriptions": uris})
	if r.Error == nil || r.Error.Code != -32602 || errReason(t, r) != "too_many" {
		t.Fatalf("257 URIs: %+v, want -32602 too_many", r.Error)
	}
}

// TestNotifyWithoutGrowthSendsNoHint: the log wakes watchers for more than
// appends, and a hint means the log grew.
func TestNotifyWithoutGrowthSendsNoHint(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a1")
	b, bSecret := env.NewPeer("session", "b1")
	uri := peerURI(b)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, bSecret, []string{uri})
	defer resp.Body.Close()
	if _, _, ok := nextMessage(t, frames, 2*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	env.Hub.Log.Notify(b)
	if _, raw, ok := nextMessage(t, frames, 300*time.Millisecond); ok {
		t.Fatalf("message after a Notify with no append: %s", raw)
	}
	sendTo(t, env, aSecret, b, "n1")
	if !hintFor(t, frames, uri, time.Second) {
		t.Fatal("no hint for a send after the empty Notify")
	}
}

// A rotated bearer's listen stream ends, with no final result, on the wake the
// rotation sends; its reachability goes with it.
func TestListenEndsOnRotation(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "p")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, frames := listen(t, ctx, env, http.DefaultClient, secret, []string{peerURI(peer)})
	if _, _, ok := nextMessage(t, frames, 5*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	if _, err := env.Hub.RotatePeer(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	wantUnfinished(t, frames)
	waitFor(t, 2*time.Second, "reachability to fall", func() bool { return reachability(t, env, peer) != "connected" })
}

// A heartbeat is enough: nothing has to wake the stream.
func TestListenCredentialCheckedOnHeartbeat(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 20 * time.Millisecond })
	peer, secret := env.NewPeer("session", "p")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, frames := listen(t, ctx, env, http.DefaultClient, secret, []string{peerURI(peer)})
	if _, _, ok := nextMessage(t, frames, 5*time.Second); !ok {
		t.Fatal("no acknowledgement")
	}
	if _, err := env.Hub.Auth.Rotate(context.Background(), "peer", peer); err != nil { // no Notify
		t.Fatal(err)
	}
	wantUnfinished(t, frames)
}

// A rotated bearer's GET stream ends and its hints wait for a live one.
func TestGetStreamEndsOnRotation(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 20 * time.Millisecond })
	peer, secret := env.NewPeer("session", "p")
	sid := legacySession(t, env, secret)
	legacySubscribe(t, env, secret, sid, peerURI(peer))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, frames := getStream(t, ctx, env, secret, sid)
	if _, err := env.Hub.RotatePeer(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	wantUnfinished(t, frames)
}

// ownFront serves a bare mcpserver of env's hub, for what the handler hides.
// Its session watchers end with the test.
func ownFront(t *testing.T, env *testhub.Env) (*mcpserver.Server, *testhub.Env) {
	t.Helper()
	srv := mcpserver.New(env.Hub, mcpserver.Chain{&mcpserver.BearerAuth{Resolver: env.Hub.Auth}}, env.URL)
	stop := make(chan struct{})
	srv.Stop = stop
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		close(stop)
		ts.Close()
	})
	return srv, &testhub.Env{T: t, URL: ts.URL, Hub: env.Hub}
}

// heldHintFront serves its own mcpserver with hints held for a subscribed
// session, for tests that open the GET stream afterwards.
func heldHintFront(t *testing.T, env *testhub.Env, secret, uri, send, to string) (*mcpserver.Server, *testhub.Env, string) {
	t.Helper()
	srv, own := ownFront(t, env)
	sid := legacySession(t, own, secret)
	legacySubscribe(t, own, secret, sid, uri)
	sendTo(t, env, send, to, "held")
	waitFor(t, 2*time.Second, "the hint to be held", func() bool { return srv.HeldHints(sid) == 1 })
	return srv, own, sid
}

// A hint held before access was lost is not delivered while it stays lost; it
// waits, since a stream that opens once access is back is entitled to it.
func TestHeldHintWaitsForAccess(t *testing.T) {
	env := testhub.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prn, secret := env.NewPrincipal("pc")
	peer, _ := peerUnder(t, env, prn, "p")
	_, sender := env.NewPeer("session", "s")
	srv, own, sid := heldHintFront(t, env, secret, peerURI(peer), sender, peer)
	if err := env.Hub.SetPeerDisabled(ctx, peer, true); err != nil {
		t.Fatal(err)
	}
	resp, frames := getStream(t, ctx, own, secret, sid)
	noHintFor(t, frames, peerURI(peer))
	resp.Body.Close()
	if n := srv.HeldHints(sid); n != 1 {
		t.Fatalf("held hints = %d, want the one kept for later", n)
	}
	if err := env.Hub.SetPeerDisabled(ctx, peer, false); err != nil {
		t.Fatal(err)
	}
	resp, frames = getStream(t, ctx, own, secret, sid)
	defer resp.Body.Close()
	if !hintFor(t, frames, peerURI(peer), 2*time.Second) {
		t.Fatal("no hint once access returned")
	}
}

// Unsubscribing drops a held hint: the client asked to hear no more of it.
func TestUnsubscribeDropsHeldHint(t *testing.T) {
	env := testhub.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	peer, secret := env.NewPeer("session", "p")
	_, sender := env.NewPeer("session", "s")
	srv, own, sid := heldHintFront(t, env, secret, peerURI(peer), sender, peer)
	resp, body := do(t, own, call{secret: secret, body: map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "resources/unsubscribe", "params": map[string]any{"uri": peerURI(peer)},
	}, headers: map[string]string{"Mcp-Session-Id": sid, "Accept": "application/json, text/event-stream"}})
	if resp.StatusCode != http.StatusOK || decodeRPC(t, body).Error != nil {
		t.Fatalf("unsubscribe: %d %s", resp.StatusCode, body)
	}
	if n := srv.HeldHints(sid); n != 0 {
		t.Fatalf("held hints after unsubscribe = %d", n)
	}
	stream, frames := getStream(t, ctx, own, secret, sid)
	defer stream.Body.Close()
	noHintFor(t, frames, peerURI(peer))
}

func TestListenStreamsCappedPerCredential(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "p")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 16 {
		_, frames := listen(t, ctx, env, http.DefaultClient, secret, []string{peerURI(peer)})
		if _, _, ok := nextMessage(t, frames, 5*time.Second); !ok {
			t.Fatal("no acknowledgement")
		}
	}
	r := rpc2026(t, env, secret, "subscriptions/listen", map[string]any{
		"notifications": map[string]any{"resourceSubscriptions": []string{peerURI(peer)}},
	})
	if r.Error == nil || errReason(t, r) != "too_many" {
		t.Fatalf("listen past the cap = %+v / %s", r.Error, r.Result)
	}
}
