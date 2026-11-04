package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/testhub"
)

const relayMetaKey = "io.modelcontextprotocol/protocolVersion"

// relayCurrent is a 2026-07-28 request: its revision rides in params._meta.
func relayCurrent(id int, method string, params map[string]any) string {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{relayMetaKey: "2026-07-28"}
	return relayLine(id, method, params)
}

// A 2026-07-28 client never sends initialize: the relay must carry the
// revision its requests name (or that discover answered) in the header.
func TestMCPRelayCurrentEra(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "A")
	b, bSecret := env.NewPeer("session", "B")
	replies := relayOK(t, profileUser(t, env.URL, secret), []string{
		relayCurrent(1, "server/discover", nil),
		relayCurrent(2, "tools/list", nil),
		// No _meta: the revision discover answered still applies.
		relayLine(3, "tools/list", map[string]any{}),
		relayCurrent(4, "tools/call", map[string]any{"name": "vecnet_send", "arguments": relaySend(b, "current-era")}),
	})
	relayIDs(t, replies, 1, 2, 3, 4)
	var discover struct {
		ProtocolVersion string `json:"protocolVersion"`
		ResultType      string `json:"resultType"`
	}
	if replies[0].Error != nil || json.Unmarshal(replies[0].Result, &discover) != nil ||
		discover.ProtocolVersion != "2026-07-28" || discover.ResultType != "complete" {
		t.Fatalf("discover = %+v", replies[0])
	}
	for _, r := range replies[1:3] {
		if r.Error != nil || !strings.Contains(string(r.Result), `"vecnet_send"`) {
			t.Fatalf("tools/list = %+v", r)
		}
	}
	relayTool(t, replies[3], nil)
	deliveries := profileUser(t, env.URL, bSecret).inboxJSON()
	if len(deliveries) != 1 || deliveries[0].Data.Message.Sender != a {
		t.Fatalf("B inbox = %+v", deliveries)
	}
}

// Under --as, a stateless caller's first tools/call makes the relay fetch
// the catalogue privately, in the caller's revision, and binds the call.
func TestMCPRelayCurrentEraAsBound(t *testing.T) {
	env := testhub.Start(t)
	prn, secret := env.NewPrincipal("relay principal")
	p1, _, err := env.Hub.AddPeer(context.Background(), "session", "P1", prn)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := env.Hub.AddPeer(context.Background(), "session", "P2", prn)
	if err != nil {
		t.Fatal(err)
	}
	b, bSecret := env.NewPeer("session", "B")
	wrong := relaySend(b, "current-wrong")
	wrong["as"] = p2
	replies := relayOK(t, profileUser(t, env.URL, secret), []string{
		relayCurrent(1, "server/discover", nil),
		relayCurrent(2, "tools/call", map[string]any{"name": "vecnet_send", "arguments": relaySend(b, "current-bound")}),
		relayCurrent(3, "tools/call", map[string]any{"name": "vecnet_send", "arguments": wrong}),
	}, "--as", p1)
	relayIDs(t, replies, 1, 2, 3)
	if replies[0].Error != nil {
		t.Fatalf("discover = %+v", replies[0])
	}
	relayTool(t, replies[1], nil)
	relayError(t, replies[2], -32602, "relay is bound to --as "+p1)
	deliveries := profileUser(t, env.URL, bSecret).inboxJSON()
	if len(deliveries) != 1 || deliveries[0].Data.Message.Sender != p1 {
		t.Fatalf("B inbox = %+v, want one send from %s", deliveries, p1)
	}
}

// --as binds inbox reads through resources too: a principal's other peer
// and its aggregate inbox are refused, and the listing shows only X.
func TestMCPRelayAsBindsResources(t *testing.T) {
	env := testhub.Start(t)
	prn, secret := env.NewPrincipal("relay principal")
	p1, _, err := env.Hub.AddPeer(context.Background(), "session", "P1", prn)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := env.Hub.AddPeer(context.Background(), "session", "P2", prn)
	if err != nil {
		t.Fatal(err)
	}
	_, bSecret := env.NewPeer("session", "B")
	sender := profileUser(t, env.URL, bSecret)
	sender.send(p1, "only P1", "--hold")
	sender.send(p2, "only P2", "--hold")
	own := "vecnet://peer/" + p1 + "/inbox"
	other := "vecnet://peer/" + p2 + "/inbox"
	aggregate := "vecnet://principal/" + prn + "/inbox"
	replies := relayOK(t, profileUser(t, env.URL, secret), []string{
		relayInit("2025-11-25"),
		relayLine(2, "resources/list", map[string]any{}),
		relayLine(3, "resources/read", map[string]any{"uri": other}),
		relayLine(4, "resources/read", map[string]any{"uri": aggregate}),
		relayLine(5, "resources/subscribe", map[string]any{"uri": other}),
		relayLine(6, "subscriptions/listen", map[string]any{"resourceSubscriptions": []string{own, other}}),
		relayLine(7, "subscriptions/listen", map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{aggregate}}}),
		relayLine(8, "resources/read", map[string]any{"URI": other, "uri": own}),
		relayLine(9, "resources/read", map[string]any{"uri": own}),
	}, "--as", p1)
	relayIDs(t, replies, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	var listed struct {
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	if replies[1].Error != nil || json.Unmarshal(replies[1].Result, &listed) != nil ||
		len(listed.Resources) != 1 || listed.Resources[0].URI != own {
		t.Fatalf("resources/list = %+v (%s)", replies[1], replies[1].Result)
	}
	for _, i := range []int{2, 3, 4, 5, 6} {
		relayError(t, replies[i], -32602, "relay is bound to --as "+p1)
	}
	relayError(t, replies[7], -32602, "invalid relay request fields")
	var read struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if replies[8].Error != nil || json.Unmarshal(replies[8].Result, &read) != nil || len(read.Contents) != 1 {
		t.Fatalf("own read = %+v", replies[8])
	}
	var inbox struct {
		Events []inboxEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &inbox); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range inbox.Events {
		if event.Type == "delivery" {
			n++
			if event.Data.Recipient != p1 || event.Data.Message.Content.Text != "only P1" {
				t.Fatalf("bound resource event %+v", event)
			}
		}
	}
	if n != 1 {
		t.Fatalf("bound resource deliveries = %d", n)
	}
}

// SSE lines end in CR, LF or CRLF (PROTOCOL §13.1), in any mix.
func TestMCPRelaySSELineEndings(t *testing.T) {
	first := `{"jsonrpc":"2.0","id":2,"result":{"n":1}}`
	second := `{"jsonrpc":"2.0","id":2,"result":{"n":2}}`
	for name, body := range map[string]string{
		"cr":      "event: message\rdata: " + first + "\r\r: hb\rdata: " + second + "\r\r",
		"cr-eof":  "event: message\rdata: " + first + "\r\rdata: " + second,
		"mixed":   "event: message\r\ndata: " + first + "\r\n\rdata: " + second + "\n\r\n",
		"split":   "data: {\"jsonrpc\":\"2.0\",\rdata: \"id\":2,\"result\":{\"n\":1}}\r\n\ndata: " + second + "\r",
		"lf-only": "data: " + first + "\n\ndata: " + second + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
				relayRequest(t, r)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			})
			code, out, errs := relayRun(t, profileUser(t, server.URL, "relay-test-secret"), relayCall(2, "vecnet_peers", map[string]any{})+"\n")
			if code != 0 || errs != "" {
				t.Fatalf("exit=%d stderr=%q", code, errs)
			}
			if out != first+"\n"+second+"\n" {
				t.Fatalf("stdout = %q", out)
			}
		})
	}
}

// A CR-terminated frame on a live stream is relayed when it ends, not when
// the next byte or EOF arrives.
func TestMCPRelaySSELiveCRFrame(t *testing.T) {
	for name, frame := range map[string]string{"cr": "\r\r", "crlf": "\r\n\r\n", "lf": "\n\n"} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			server := relayFake(t, func(w http.ResponseWriter, r *http.Request) {
				req := relayRequest(t, r)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"live\":true}}%s", req["id"], frame)
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
			})
			var once sync.Once
			stop := func() { once.Do(func() { close(release) }) }
			defer stop()
			u := profileUser(t, server.URL, "relay-test-secret")
			pr, pw := io.Pipe()
			done := make(chan int, 1)
			go func() {
				var errs bytes.Buffer
				done <- cli.Run([]string{"mcp"}, strings.NewReader(relayCall(2, "vecnet_peers", map[string]any{})+"\n"), pw, &errs, u.getenv)
				pw.Close()
			}()
			lines := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(pr).ReadString('\n')
				lines <- line
				_, _ = io.Copy(io.Discard, pr)
			}()
			select {
			case line := <-lines:
				if line != `{"jsonrpc":"2.0","id":2,"result":{"live":true}}`+"\n" {
					t.Fatalf("live frame = %q", line)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("live frame not relayed before the stream ended")
			}
			stop()
			if code := <-done; code != 0 {
				t.Fatalf("exit = %d", code)
			}
		})
	}
}
