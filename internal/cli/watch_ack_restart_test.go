package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Hold the restart/reset behind the first in-stream ack. This exercises both
// flush sites without depending on the line-reader goroutine's scheduling.
type watchAckBoundaryReader struct {
	first io.Reader
	tail  io.Reader
	acked <-chan struct{}
	ctx   context.Context
}

func (r *watchAckBoundaryReader) Read(p []byte) (int, error) {
	if r.first != nil {
		n, err := r.first.Read(p)
		if err != io.EOF {
			return n, err
		}
		r.first = nil
		select {
		case <-r.acked:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	return r.tail.Read(p)
}

func TestWatchAutoAckQuietOutage(t *testing.T) {
	for _, failure := range []string{"502", "503", "504", "body EOF", "transport reset"} {
		for _, monitor := range []bool{false, true} {
			for _, sustained := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/monitor=%t/sustained=%t", failure, monitor, sustained), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					var out, errs bytes.Buffer
					c := newClient("http://localhost", "test")
					now := time.Unix(100, 0)
					w := watcher{a: &app{ctx: ctx, stdout: &out, stderr: &errs}, c: c, autoAck: true,
						forMonitor: monitor, replayed: true, connected: true, cursor: "e1.7", seen: newDedupe(1024),
						now: func() time.Time { return now }}
					// Deliberately begin with an already handled, owed delivery.
					ev := hookDelivery("dly_restart")
					ev.Cursor = "e1.7"
					raw, err := json.Marshal(ev)
					if err != nil {
						t.Fatal(err)
					}
					w.handle("delivery", string(raw), false)
					acked := make(chan struct{})
					events, acks, sleeps := 0, 0, 0
					response := func(status int, contentType string, body io.Reader) (*http.Response, error) {
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(body)}, nil
					}
					c.hc.Transport = relayRoundTrip(func(req *http.Request) (*http.Response, error) {
						switch req.URL.Path {
						case "/v1/peers":
							return response(200, "application/json", strings.NewReader(`{"peers":[]}`))
						case "/v1/ack":
							acks++
							var body struct{ Cursor string }
							if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Cursor != "e1.7" || w.pendingAck != "e1.7" {
								t.Fatalf("ack %d body=%+v pending=%q err=%v", acks, body, w.pendingAck, err)
							}
							if acks == 1 {
								close(acked)
							}
							if events == 4 {
								// POST 204 is a successful ack, not a disabled GET.
								return response(204, "", strings.NewReader(""))
							}
							switch failure {
							case "body EOF":
								return response(200, "", relayReadFailure{io.ErrUnexpectedEOF})
							case "transport reset":
								return nil, errors.New("connection reset by peer")
							default:
								status := map[string]int{"502": 502, "503": 503, "504": 504}[failure]
								return response(status, "text/plain", strings.NewReader("proxy unavailable"))
							}
						case "/v1/events":
							events++
							if events != 4 && req.Header.Get("Last-Event-ID") != "e1.7" {
								t.Fatalf("attempt %d resume=%q", events, req.Header.Get("Last-Event-ID"))
							}
							switch events {
							case 1:
								var tail io.Reader = strings.NewReader(": hb\nevent: hub_restarting\ndata: {}\n\n")
								if failure == "body EOF" || failure == "transport reset" {
									tail = relayReadFailure{io.ErrUnexpectedEOF}
								}
								return response(200, "text/event-stream", &watchAckBoundaryReader{
									first: strings.NewReader("id: e1.7\ndata:\n\n"), tail: tail, acked: acked, ctx: ctx})
							case 2, 3:
								if strings.Contains(errs.String(), "reconnected") || w.pendingAck != "e1.7" {
									t.Fatalf("premature recovery: pending=%q status=%q", w.pendingAck, errs.String())
								}
								return response(503, "text/plain", strings.NewReader("proxy unavailable"))
							case 4:
								if req.Header.Get("Accept") == "text/event-stream" || req.URL.Query().Get("after") != "e1.7" {
									t.Fatalf("fallback did not resume owed cursor: %s", req.URL)
								}
								if strings.Contains(errs.String(), "reconnected") {
									t.Fatalf("ack attempts reported recovery before healthy poll: %q", errs.String())
								}
								return response(200, "application/json", strings.NewReader(`{"events":[`+string(raw)+`],"next_cursor":"e1.7"}`))
							case 5:
								if w.pendingAck != "" {
									t.Fatalf("successful POST 204 retained pending=%q", w.pendingAck)
								}
								return response(204, "", strings.NewReader(""))
							}
						}
						t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
						return nil, errors.New("unexpected request")
					})
					w.sleep = func(context.Context, time.Duration) bool {
						sleeps++
						if now.Sub(time.Unix(100, 0)) < time.Minute && strings.Contains(errs.String(), "ack e1.7:") {
							t.Errorf("early ack diagnostic: %q", errs.String())
						}
						if sustained {
							now = now.Add(31 * time.Second)
						}
						return true
					}
					if err := w.run(); !errors.Is(err, errPeerDisabled) {
						t.Fatalf("terminal=%v status=%q", err, errs.String())
					}
					if events != 5 || acks != 5 || sleeps != 3 || w.cursor != "e1.7" || w.pendingAck != "" {
						t.Fatalf("events=%d acks=%d sleeps=%d cursor=%q pending=%q", events, acks, sleeps, w.cursor, w.pendingAck)
					}
					if strings.Count(errs.String(), "hub restarting; reconnecting") != 1 || strings.Count(errs.String(), "reconnected") != 1 {
						t.Errorf("status=%q", errs.String())
					}
					if strings.Contains(errs.String(), "ack e1.7:") != sustained {
						t.Errorf("sustained=%t diagnostics=%q", sustained, errs.String())
					}
					if strings.Count(out.String(), "e1.7 delivery ") != 1 || strings.Contains(out.String(), "hub_restarting") || strings.Contains(out.String(), "reconnect") {
						t.Errorf("stdout=%q", out.String())
					}
				})
			}
		}
	}
}

func TestWatchAutoAckKeepsRefusalDiagnostics(t *testing.T) {
	for _, status := range []int{401, 403, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var out, errs bytes.Buffer
			c := newClient("http://localhost", "test")
			acks := 0
			c.hc.Transport = relayRoundTrip(func(req *http.Request) (*http.Response, error) {
				code, body := 204, ""
				switch req.URL.Path {
				case "/v1/peers":
					code, body = 200, `{"peers":[]}`
				case "/v1/ack":
					acks++
					code = status
					body = `{"error":{"code":"ack_refused","message":"ack needs attention"}}`
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			w := watcher{a: &app{ctx: context.Background(), stdout: &out, stderr: &errs}, c: c,
				autoAck: true, forMonitor: true, replayed: true, pendingAck: "e1.7"}
			if err := w.run(); !errors.Is(err, errPeerDisabled) || acks != 1 || w.pendingAck != "e1.7" {
				t.Fatalf("err=%v acks=%d pending=%q", err, acks, w.pendingAck)
			}
			if !strings.Contains(errs.String(), "ack e1.7: ack_refused") || strings.Contains(errs.String(), "reconnect") {
				t.Fatalf("refusal diagnostics=%q", errs.String())
			}
		})
	}
}

func TestWatchAutoAckStreamRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, errs := new(hookWatchOutput), new(hookWatchOutput)
	c := newClient("http://localhost", "test")
	w := watcher{a: &app{ctx: ctx, stdout: out, stderr: errs}, c: c, autoAck: true,
		forMonitor: true, replayed: true, connected: true, cursor: "e1.7", seen: newDedupe(1024),
		sleep: func(context.Context, time.Duration) bool { return true }}
	w.handle("presence", `{"cursor":"e1.7","type":"presence","data":{}}`, false)
	events, acks := 0, 0
	c.hc.Transport = relayRoundTrip(func(req *http.Request) (*http.Response, error) {
		status, contentType := 200, "application/json"
		var body io.Reader = strings.NewReader(`{"peers":[]}`)
		switch req.URL.Path {
		case "/v1/ack":
			acks++
			status = 503
			if events == 3 {
				status = 204
			}
			body = strings.NewReader("")
		case "/v1/events":
			events++
			if req.Header.Get("Last-Event-ID") != "e1.7" {
				t.Errorf("resume=%q", req.Header.Get("Last-Event-ID"))
			}
			contentType = "text/event-stream"
			switch events {
			case 1:
				body = strings.NewReader("event: hub_restarting\ndata: {}\n\n")
			case 2:
				status, body = 503, strings.NewReader("")
			case 3:
				body = &watchAckBoundaryReader{first: strings.NewReader("id: e1.7\ndata:\n\n"),
					tail: strings.NewReader(""), acked: ctx.Done(), ctx: ctx}
			default:
				t.Errorf("unexpected stream attempt %d", events)
				status, body = 204, strings.NewReader("")
			}
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(body)}, nil
	})
	done := make(chan error, 1)
	go func() { done <- w.run() }()
	waitHookOutput(t, errs, "reconnected")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if events != 3 || acks != 3 || w.pendingAck != "" || strings.Count(out.String(), "e1.7 presence") != 1 {
		t.Fatalf("events=%d acks=%d pending=%q stdout=%q", events, acks, w.pendingAck, out.String())
	}
	if strings.Count(errs.String(), "hub restarting") != 1 || strings.Count(errs.String(), "reconnected") != 1 || strings.Contains(errs.String(), "ack e1.7:") {
		t.Fatalf("status=%q", errs.String())
	}
}
