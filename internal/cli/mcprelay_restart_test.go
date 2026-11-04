package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type relayFailedDial struct{ calls int }

func (f *relayFailedDial) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}
}

func TestMCPRelayBoundsOrdinaryRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errs bytes.Buffer
	c := newClient("http://localhost", "test")
	f := &relayFailedDial{}
	c.hc.Transport = f
	r := mcpRelay{a: &app{ctx: ctx, stdout: &out, stderr: &errs}, c: c, sleep: func(context.Context, time.Duration) bool {
		if f.calls > 3 {
			cancel()
			return false
		}
		return true
	}}
	raw := []byte(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	var msg mcpRelayMessage
	json.Unmarshal(raw, &msg)
	if err := r.exchange(raw, msg, true); err == nil {
		t.Fatal("expected unreachable")
	}
	if f.calls != 3 {
		t.Fatalf("ordinary request attempts=%d, want 3", f.calls)
	}
}

func TestMCPRelayReconnectsSubscriptionHints(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("unexpected %s", r.Method)
		}
		calls++
		if calls == 2 {
			w.WriteHeader(503)
			fmt.Fprint(w, "private proxy body")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"_meta":{"subscriptionId":9},"notifications":{"resourceSubscriptions":["vecnet://inbox","vecnet://inbox"]}}}`+"\n\n")
		if calls == 1 {
			fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"vecnet://inbox"}}`+"\n\n")
			return
		}
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":9,"result":{}}`+"\n\n")
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	r := mcpRelay{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "private-secret"), sleep: func(context.Context, time.Duration) bool { return true }}
	raw := []byte(`{"jsonrpc":"2.0","id":9,"method":"subscriptions/listen","params":{"resourceSubscriptions":["vecnet://inbox"]}}`)
	var msg mcpRelayMessage
	json.Unmarshal(raw, &msg)
	if err := r.exchange(raw, msg, true); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || strings.Count(out.String(), "subscriptions/acknowledged") != 1 || strings.Count(out.String(), "notifications/resources/updated") != 2 || strings.Count(out.String(), `"id":9`) != 1 {
		t.Fatalf("calls=%d stdout=%s", calls, out.String())
	}
	if strings.Count(errs.String(), "hub restarting") != 1 || strings.Count(errs.String(), "reconnected") != 1 || strings.Contains(errs.String(), "private") {
		t.Fatalf("status=%q", errs.String())
	}
}

func TestMCPRelayDoesNotReplayAmbiguousMutation(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "300")
		io.WriteString(w, `{"jsonrpc":"2.0","id":4,"result":`)
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	r := mcpRelay{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "test")}
	raw := []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"vecnet_send","arguments":{}}}`)
	var msg mcpRelayMessage
	json.Unmarshal(raw, &msg)
	if err := r.exchange(raw, msg, true); err == nil {
		t.Fatal("expected ambiguous response error")
	}
	if calls != 1 || out.Len() != 0 {
		t.Fatalf("calls=%d stdout=%q", calls, out.String())
	}
}

func TestMCPRelayProxyRetryClasses(t *testing.T) {
	for _, method := range []string{"tools/list", "tools/call"} {
		for _, code := range []int{502, 503, 504, 401, 204} {
			t.Run(fmt.Sprintf("%s/%d", method, code), func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls == 1 {
						w.WriteHeader(code)
						return
					}
					fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`)
				}))
				defer srv.Close()
				var out, errs bytes.Buffer
				r := mcpRelay{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: newClient(srv.URL, "secret"), sleep: func(context.Context, time.Duration) bool { return true }}
				raw := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":%q}`, method))
				var msg mcpRelayMessage
				json.Unmarshal(raw, &msg)
				err := r.exchange(raw, msg, true)
				wantRetry := method == "tools/list" && code >= 502 && code <= 504
				if wantRetry {
					if err != nil || calls != 2 {
						t.Fatalf("calls=%d err=%v", calls, err)
					}
				} else if err == nil || calls != 1 {
					t.Fatalf("unsafe or terminal calls=%d err=%v", calls, err)
				}
			})
		}
	}
}
