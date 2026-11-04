package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type relayRoundTrip func(*http.Request) (*http.Response, error)

func (f relayRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type relayReadFailure struct{ err error }

func (f relayReadFailure) Read([]byte) (int, error) { return 0, f.err }

type relayWriteFailure struct{ err error }

func (f relayWriteFailure) Write([]byte) (int, error) { return 0, f.err }

func TestMCPRelayDoesNotRetryLocalResponseErrors(t *testing.T) {
	for _, kind := range []string{"stdout EOF", "stdout network", "schema"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			c := newClient("http://localhost", "test")
			payload := `data: {"jsonrpc":"2.0","id":9,"result":{"tools":[]}}` + "\n\n"
			if kind == "schema" {
				payload = `data: {"jsonrpc":"2.0","id":9,"result":{"tools":null}}` + "\n\n"
			}
			c.hc.Transport = relayRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})
			var out, errs bytes.Buffer
			var writer io.Writer = &out
			if kind == "stdout EOF" {
				writer = relayWriteFailure{io.ErrUnexpectedEOF}
			}
			if kind == "stdout network" {
				writer = relayWriteFailure{&netError{err: &net.OpError{Op: "read", Err: errors.New("local pipe failed")}}}
			}
			r := mcpRelay{a: &app{ctx: context.Background(), stdout: writer, stderr: &errs}, c: c, sleep: func(context.Context, time.Duration) bool { return true }}
			raw := []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)
			var msg mcpRelayMessage
			json.Unmarshal(raw, &msg)
			if err := r.exchange(raw, msg, true); err == nil || calls != 1 || errs.Len() != 0 {
				t.Fatalf("calls=%d err=%v status=%s", calls, err, errs.String())
			}
		})
	}
}

const relayAck = `data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"_meta":{"subscriptionId":9},"notifications":{"resourceSubscriptions":["vecnet://inbox"]}}}` + "\n\n"
const relayFinal = `data: {"jsonrpc":"2.0","id":9,"result":{}}` + "\n\n"

func TestMCPRelayAckResetsOutageBackoff(t *testing.T) {
	calls := 0
	c := newClient("http://localhost", "test")
	c.hc.Transport = relayRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		body := ""
		if calls == 7 {
			body = relayAck
		}
		if calls == 8 {
			body = relayAck + relayFinal
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	var out, errs bytes.Buffer
	var delays []time.Duration
	r := mcpRelay{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: c, sleep: func(_ context.Context, d time.Duration) bool { delays = append(delays, d); return true }}
	raw := []byte(`{"jsonrpc":"2.0","id":9,"method":"subscriptions/listen"}`)
	var msg mcpRelayMessage
	json.Unmarshal(raw, &msg)
	if err := r.exchange(raw, msg, true); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 7 {
		t.Fatalf("delays=%v", delays)
	}
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 20 * time.Second, time.Second} {
		if delays[i] < base*4/5 || delays[i] > base*6/5 {
			t.Fatalf("retry %d=%v, want near %v; all=%v", i, delays[i], base, delays)
		}
	}
}

func TestMCPRelayBodyReadFailureRecovery(t *testing.T) {
	for _, method := range []string{"subscriptions/listen", "ping", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			c := newClient("http://localhost", "test")
			c.hc.Transport = relayRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				media, prefix := "application/json", ""
				if method == "subscriptions/listen" {
					media, prefix = "text/event-stream", relayAck
				}
				var body io.Reader = strings.NewReader(`{"jsonrpc":"2.0","id":9,"result":{}}`)
				if calls == 1 {
					body = io.MultiReader(strings.NewReader(prefix), relayReadFailure{&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}})
				} else if method == "subscriptions/listen" {
					body = strings.NewReader(relayAck + relayFinal)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(body)}, nil
			})
			var out, errs bytes.Buffer
			r := mcpRelay{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: c, sleep: func(context.Context, time.Duration) bool { return true }}
			raw := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":%q}`, method))
			var msg mcpRelayMessage
			json.Unmarshal(raw, &msg)
			err := r.exchange(raw, msg, true)
			if method == "tools/call" {
				if err == nil || calls != 1 {
					t.Fatalf("mutation calls=%d err=%v", calls, err)
				}
				return
			}
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v stdout=%s", calls, err, out.String())
			}
			if method == "subscriptions/listen" && (strings.Count(out.String(), "acknowledged") != 1 || strings.Count(out.String(), "resources/updated") != 1 || !strings.Contains(out.String(), `"subscriptionId":9`)) {
				t.Fatalf("catch-up=%s", out.String())
			}
			if strings.Count(errs.String(), "hub restarting") != 1 || strings.Count(errs.String(), "reconnected") != 1 {
				t.Fatalf("status=%s", errs.String())
			}
		})
	}
}

func TestMCPRelayHTTP2ResetRecovery(t *testing.T) {
	calls := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("protocol=%s", r.Proto)
		}
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, relayAck)
		w.(http.Flusher).Flush()
		if calls == 1 {
			panic(http.ErrAbortHandler)
		}
		fmt.Fprint(w, relayFinal)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	c := newClient(srv.URL, "test")
	c.hc = srv.Client()
	var out, errs bytes.Buffer
	r := mcpRelay{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: c, sleep: func(context.Context, time.Duration) bool { return true }}
	raw := []byte(`{"jsonrpc":"2.0","id":9,"method":"subscriptions/listen"}`)
	var msg mcpRelayMessage
	json.Unmarshal(raw, &msg)
	if err := r.exchange(raw, msg, true); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

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
