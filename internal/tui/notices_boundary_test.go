package tui

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/opclient"
)

// Source-only draft: formatter RED and HTTP controls are UNRUN, parent-owned.
// Empty host denotes an invalid URL; accepted host expectations are literals.
var tuiNoticeURLs = []struct{ name, raw, host string }{
	{"password", "https://alice:TN_PASSWORD@received.example/vecnet", ""},
	{"username", "https://TN_USERNAME@received.example/vecnet", ""},
	{"query", "https://received.example/vecnet?token=TN_QUERY", ""},
	{"bare-query", "https://received.example/TN_QUERY?", ""},
	{"fragment", "https://received.example/vecnet#TN_FRAGMENT", ""},
	{"bare-fragment", "https://received.example/TN_FRAGMENT#", ""},
	{"authority", "https://received.example:TN_AUTHORITY/vecnet", ""},
	{"ipv6", "https://[TN_IPV6/vecnet", ""},
	{"escape", "https://received.example/TN_ESCAPE%zz", ""},
	{"credential-escape", "https://alice:TN_COMBINED@received.example/%zz?x#f", ""},
	{"empty", "", ""},
	{"https-prefix", "https://Safe.Example:8443/Vecnet/%2Fexact/", "Safe.Example:8443"},
	{"https-ipv6", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", "[2001:db8::7]:8443"},
	{"loopback-http", "http://127.0.0.1:8080/Vecnet/", "127.0.0.1:8080"},
	{"lan-http", "http://192.168.1.7:8080/Vecnet/", "192.168.1.7:8080"},
	{"operator-http", "http://public.example:8080/Vecnet/", "public.example:8080"},
}

func tuiNoticeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func tuiNoticeSign(t *testing.T, n envelope.HubNotice) (envelope.HubNotice, string) {
	t.Helper()
	k, err := hubid.KeyFromSeedHex(strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	return n, k.Pubkey()
}

func tuiNoticeOriginal(t *testing.T) envelope.HubNotice {
	t.Helper()
	n := movingNotice()
	n.Hub, n.NewURL = "https://Source.Example:8443/Vecnet/%2Fexact/", "https://[2001:db8::7]:8443/Next/%2Fexact/"
	// Prose is preserved, not used as a structured-URL nonleak oracle.
	n.Message = "Operator prose https://u:TN_PROSE@text.example/?x#f"
	n, _ = tuiNoticeSign(t, n)
	return n
}

func TestHubHostRejectsInvalidURLs(t *testing.T) {
	for _, tc := range tuiNoticeURLs {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.host
			if want == "" {
				want = "invalid hub"
			}
			if got := hubHost(tc.raw); got != want {
				t.Errorf("hubHost = %q, want %q", got, want)
			}
		})
	}
}

func TestNoticeFormatterRejectsInvalidPayloads(t *testing.T) {
	original := tuiNoticeOriginal(t)
	for _, u := range tuiNoticeURLs {
		for _, field := range []string{"hub", "new_url"} {
			for _, state := range []string{"posted", "cleared", "nonmoving"} {
				t.Run(u.name+"/"+field+"/"+state, func(t *testing.T) {
					n, change, host := original, state, "[2001:db8::7]:8443"
					if field == "hub" {
						n.Hub = u.raw
					} else {
						n.NewURL, host = u.raw, u.host
					}
					if state == "nonmoving" {
						n.Kind, change = "maintenance", "posted"
					}
					if state == "cleared" {
						n.ClearedAt, n.ClearedHow = envelope.FormatTime(fixedNow), "operator"
					}
					before := n
					raw := fmt.Sprintf("{ \"change\" : %q, \"notice\" : %s }", change, tuiNoticeJSON(t, n))
					ev := envelope.Event{Type: "hub_notice", Data: json.RawMessage(raw)}
					want := "invalid hub notice"
					if u.host != "" || (field == "new_url" && u.raw == "") {
						want = change + " " + n.Kind + " ntc_…0001"
						if n.Kind == "moving" && n.NewURL != "" {
							want += " → " + host
						}
						want += " " + n.Message
					}
					if got := inboxEventText(ev, inboxPeers{}); got != want {
						t.Errorf("notice line = %q, want %q", got, want)
					}
					var decoded envelope.HubNoticeEvent
					if err := json.Unmarshal(ev.Data, &decoded); err != nil {
						t.Fatal(err)
					}
					if string(ev.Data) != raw || n != before || decoded.Notice != before {
						t.Error("raw payload or original notice/signature changed")
					}
				})
			}
		}
	}
	for _, raw := range []string{
		`null`, `{}`, `7`, `[]`, `{"change":"posted"}`, `{"notice":null}`, `{"notice":7}`,
		`{"notice":[]}`, `{"notice":{}}`, `{"notice":{"hub":null}}`, `{"notice":{"hub":7}}`,
		`{"notice":{"hub":"https://safe.example","new_url":null}}`,
		`{"notice":{"hub":"https://safe.example","new_url":7}}`, `{"notice":"TN_RAW"}`,
		`{"notice":{"hub":"https://safe.example"},"change":7}`, `{"notice":`,
	} {
		t.Run("shape/"+raw, func(t *testing.T) {
			ev := envelope.Event{Type: "hub_notice", Data: json.RawMessage(raw)}
			if got := inboxEventText(ev, inboxPeers{}); got != "invalid hub notice" {
				t.Errorf("malformed notice = %q", got)
			}
			if string(ev.Data) != raw {
				t.Error("refused payload mutated")
			}
		})
	}
	for _, target := range []string{``, `,"new_url":""`} {
		raw := `{"change":"posted","notice":{"hub":"https://safe.example","kind":"maintenance","notice_id":"ntc_short","message":"unchanged"` + target + `}}`
		if got := inboxEventText(envelope.Event{Type: "hub_notice", Data: json.RawMessage(raw)}, inboxPeers{}); got != "posted maintenance ntc_short unchanged" {
			t.Errorf("optional URL: %q", got)
		}
	}
	for _, kind := range []string{"future_event", "presence"} {
		raw := `"https://u:TN_UNRELATED@text.example/?x#f"`
		ev := envelope.Event{Type: kind, Data: json.RawMessage(raw)}
		if got := inboxEventText(ev, inboxPeers{}); got != raw || string(ev.Data) != raw {
			t.Errorf("unrelated fallback changed: %q", got)
		}
	}
}

type tuiNoticeWire func(*http.Request) (*http.Response, error)

func (f tuiNoticeWire) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func tuiNoticeReply(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("fixture response write: %v", err)
	}
}

type tuiNoticeCounts struct {
	source, destination, responses, closed atomic.Int32
	base                                   string
}
type tuiNoticeBody struct {
	io.ReadCloser
	counts *tuiNoticeCounts
	once   atomic.Bool
}

func (b *tuiNoticeBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		b.counts.closed.Add(1)
	}
	return b.ReadCloser.Close()
}

// These tests are serial (no Parallel). Restore the public default transport;
// no private opclient fields or debug seams are accessed. All egress is blocked
// except the real synthetic source server, with proxies disabled.
func tuiNoticeHTTP(t *testing.T, handler http.HandlerFunc, cause error) (*opclient.Client, *tuiNoticeCounts) {
	t.Helper()
	counts := &tuiNoticeCounts{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vn_operator_TN_SYNTHETIC_ONLY" {
			t.Error("synthetic bearer changed")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	counts.base = srv.URL + "/I47"
	wire := &http.Transport{}
	t.Cleanup(wire.CloseIdleConnections)
	old := http.DefaultTransport
	http.DefaultTransport = tuiNoticeWire(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(srv.URL, "http://") || !strings.HasPrefix(r.URL.Path, "/I47/") {
			counts.destination.Add(1)
			return nil, errors.New("fixture blocked destination request")
		}
		counts.source.Add(1)
		if cause != nil {
			return nil, cause
		}
		resp, err := wire.RoundTrip(r)
		if err == nil {
			counts.responses.Add(1)
			resp.Body = &tuiNoticeBody{ReadCloser: resp.Body, counts: counts}
		}
		return resp, err
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return opclient.New(counts.base, "vn_operator_TN_SYNTHETIC_ONLY"), counts
}

func (c *tuiNoticeCounts) check(t *testing.T, source int32) {
	t.Helper()
	if c.source.Load() != source || c.destination.Load() != 0 || c.closed.Load() != c.responses.Load() {
		t.Errorf("source/destination/closed/responses = %d/%d/%d/%d, source want %d", c.source.Load(), c.destination.Load(), c.closed.Load(), c.responses.Load(), source)
	}
}

// Only unrelated snapshots are faked. Notices, tail decode, callbacks and acks
// all use the actual HTTP operator client and normal TUI command/message flow.
type tuiNoticeAPI struct {
	*fakeAPI
	client                              *opclient.Client
	lists, applies, completed, accepted atomic.Int32
	done                                chan error
}

func (a *tuiNoticeAPI) Notices(ctx context.Context) ([]envelope.HubNotice, error) {
	a.lists.Add(1)
	return a.client.Notices(ctx)
}
func (a *tuiNoticeAPI) TailWithAck(ctx context.Context, handle func(opclient.Batch) error, status func(string), accepted func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := a.client.TailWithAck(ctx, func(b opclient.Batch) error {
		a.applies.Add(1)
		err := handle(b)
		if err == nil {
			a.completed.Add(1)
		}
		return err
	}, status, func(cursor string) { a.accepted.Add(1); accepted(cursor) })
	a.done <- err
	return err
}

func tuiNoticeModel(t *testing.T, client *opclient.Client, prior envelope.HubNotice) (*Model, *pump, *tuiNoticeAPI) {
	t.Helper()
	a := &tuiNoticeAPI{fakeAPI: seeded(), client: client, done: make(chan error, 1)}
	m := newTestModel(t, a, newClock())
	m.screens, m.Conn, m.Data.Notices = staticScreens(), ConnLive, []envelope.HubNotice{prior}
	return m, newPump(t, m), a
}

func tuiNoticeLoad(m *Model, p *pump) {
	p.run(m.startLoads([]string{"notices"}, nil))
	p.idle()
}

func TestNoticeBoundaryFramesAndErrors(t *testing.T) {
	original := tuiNoticeOriginal(t)
	good := tuiNoticeJSON(t, original)
	badHub, badTarget := original, original
	badHub.Hub, badTarget.NewURL = tuiNoticeURLs[0].raw, tuiNoticeURLs[2].raw
	for _, tc := range []struct{ name, raw string }{{"hub", tuiNoticeJSON(t, badHub)}, {"target", tuiNoticeJSON(t, badTarget)}, {"null", `null`}} {
		bad := tc.raw
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("list/%s/%t", tc.name, reverse), func(t *testing.T) {
				items := good + "," + bad
				if reverse {
					items = bad + "," + good
				}
				c, counts := tuiNoticeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != "/I47/v1/hub/notices" {
						t.Error("unexpected source request")
					}
					tuiNoticeReply(t, w, `{"notices":[`+items+`]}`)
				}, nil)
				m, p, a := tuiNoticeModel(t, c, original)
				before := strings.Split(plainAt(t, m, 140, 42), "\n")[1]
				tuiNoticeLoad(m, p)
				if err := m.Data.Errs["notices"]; err == nil || err.Error() != "invalid hub notice" {
					t.Errorf("protocol error = %v", err)
				}
				if !reflect.DeepEqual(m.Data.Notices, []envelope.HubNotice{original}) || !m.toast.bad || m.toast.text != "reload notices: invalid hub notice" {
					t.Error("unsafe list applied or safe failure/toast lost")
				}
				if after := strings.Split(plainAt(t, m, 140, 42), "\n")[1]; after != before {
					t.Error("prior banner changed after refused list")
				}
				if a.lists.Load() != 1 || a.applies.Load() != 0 || len(m.Data.Events) != 0 || len(m.accepting) != 0 {
					t.Error("refused list caused unrelated effects")
				}
				counts.check(t, 1)
			})
		}
	}
	for _, tc := range []struct{ name, raw string }{{"valid", good}, {"empty", ""}} {
		t.Run("accepted-list/"+tc.name, func(t *testing.T) {
			items := tc.raw
			c, counts := tuiNoticeHTTP(t, func(w http.ResponseWriter, r *http.Request) { tuiNoticeReply(t, w, `{"notices":[`+items+`]}`) }, nil)
			m, p, _ := tuiNoticeModel(t, c, original)
			tuiNoticeLoad(m, p)
			want := []envelope.HubNotice{}
			if items != "" {
				want = append(want, original)
			}
			if m.Data.Errs["notices"] != nil || !reflect.DeepEqual(m.Data.Notices, want) {
				t.Error("accepted list/signature changed")
			}
			counts.check(t, 1)
		})
	}
	// Whole-page controls include a valid delivery before/after the bad notice.
	for _, mode := range []string{"hub-first", "hub-last", "target-first", "target-last", "null-first", "null-last", "valid", "empty", "unrelated"} {
		t.Run("tail/"+mode, func(t *testing.T) {
			valid := mode == "valid" || mode == "empty" || mode == "unrelated"
			notice := good
			if strings.HasPrefix(mode, "hub-") {
				notice = tuiNoticeJSON(t, badHub)
			}
			if strings.HasPrefix(mode, "target-") {
				notice = tuiNoticeJSON(t, badTarget)
			}
			if strings.HasPrefix(mode, "null-") {
				notice = "null"
			}
			data := []string{`{"message":{"thread_id":"thr_safe"}}`, `{ "change" : "posted", "notice" : ` + notice + ` }`}
			kinds := []string{"delivery", "hub_notice"}
			if strings.HasSuffix(mode, "first") {
				data[0], data[1], kinds[0], kinds[1] = data[1], data[0], kinds[1], kinds[0]
			}
			if mode == "empty" {
				data, kinds = nil, nil
			}
			if mode == "unrelated" {
				data, kinds = []string{`{ "note" : "https://u:TN_UNRELATED@text.example/?x#f" }`}, []string{"future_event"}
			}
			var wireEvents []string
			for i, raw := range data {
				wireEvents = append(wireEvents, fmt.Sprintf(`{"cursor":"e1.%d","type":%q,"at":%q,"data":%s}`, i+1, kinds[i], envelope.FormatTime(fixedNow), raw))
			}
			var polls, acks atomic.Int32
			var a *tuiNoticeAPI
			c, counts := tuiNoticeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/I47/v1/events":
					n := polls.Add(1)
					after, wait := "", "0"
					if n > 1 && valid {
						after = "e1.9"
						if mode == "empty" {
							wait = "25"
						}
					}
					if r.Method != "GET" || r.URL.Query().Get("after") != after || r.URL.Query().Get("wait") != wait {
						t.Error("cursor/wait advanced across unaccepted page")
					}
					if n == 1 {
						tuiNoticeReply(t, w, `{"events":[`+strings.Join(wireEvents, ",")+`],"next_cursor":"e1.9"}`)
					} else {
						w.WriteHeader(401)
					}
				case "/I47/v1/ack":
					acks.Add(1)
					var body struct {
						Cursor string `json:"cursor"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Cursor != "e1.9" || r.Method != "POST" || a.completed.Load() != 1 || a.accepted.Load() != 0 {
						t.Error("ack/application/accepted ordering changed")
					}
					w.WriteHeader(204)
				case "/I47/v1/hub/notices":
					tuiNoticeReply(t, w, `{"notices":[`+good+`]}`)
				default:
					t.Error("unexpected source path")
					w.WriteHeader(401)
				}
			}, nil)
			m, p, api := tuiNoticeModel(t, c, original)
			a = api
			prior := event(t, "e0.1", "future_event", map[string]string{"note": "previously accepted"})
			m.Data.Events = []envelope.Event{prior}
			p.run(m.startTail())
			p.run(m.listenTail())
			var stopped error
			p.until("bounded HTTP tail stop", func() bool {
				select {
				case stopped = <-a.done:
					return true
				default:
					return false
				}
			})
			if !errors.Is(stopped, opclient.ErrUnauthorized) {
				t.Fatalf("tail stop = %v", stopped)
			}
			p.until("terminal message applied", func() bool { return m.Conn == ConnRejected })
			p.idle()
			wantApply, wantAck, wantLists := int32(0), int32(0), int32(0)
			if valid {
				wantApply = 1
				if len(data) > 0 {
					wantAck = 1
				}
				if mode == "valid" {
					wantLists = 1
				}
			}
			if polls.Load() != 2 || a.applies.Load() != wantApply || a.completed.Load() != wantApply || acks.Load() != wantAck || a.accepted.Load() != wantAck || a.lists.Load() != wantLists {
				t.Errorf("poll/apply/completed/ack/accepted/list = %d/%d/%d/%d/%d/%d", polls.Load(), a.applies.Load(), a.completed.Load(), acks.Load(), a.accepted.Load(), a.lists.Load())
			}
			if !reflect.DeepEqual(m.Data.Notices, []envelope.HubNotice{original}) || len(m.waiters) != 0 || len(m.accepting) != 0 {
				t.Error("snapshot or acceptance bookkeeping changed")
			}
			if !valid {
				if !reflect.DeepEqual(m.Data.Events, []envelope.Event{prior}) || a.count("Inbox") != 0 || a.count("Threads") != 0 {
					t.Error("invalid page partially applied/remembered/reloaded")
				}
			} else {
				if len(m.Data.Events) != 1+len(data) {
					t.Fatal("accepted event count changed")
				}
				for i, raw := range data {
					if string(m.Data.Events[i+1].Data) != raw || m.Data.Events[i+1].Type != kinds[i] {
						t.Error("accepted raw payload/signature changed")
					}
				}
			}
			counts.check(t, 2+wantAck+wantLists)
		})
	}
	tuiNoticeErrorControls(t, original)
}

func tuiNoticeErrorControls(t *testing.T, original envelope.HubNotice) {
	t.Helper()
	type recovery struct {
		name          string
		details, want map[string]any
		bad           bool
	}
	cases := []recovery{
		{"absent", nil, nil, false},
		{"target-only", map[string]any{"new_url": original.NewURL}, map[string]any{"new_url": original.NewURL}, false},
		{"notice-only", map[string]any{"notice": original}, map[string]any{"notice": original}, false},
		{"different-valid-targets", map[string]any{"new_url": "https://Different.Example:9443/Target/%2Fexact/", "notice": original}, map[string]any{"new_url": "https://Different.Example:9443/Target/%2Fexact/", "notice": original}, false},
	}
	for _, field := range []string{"new_url", "notice"} {
		for _, value := range []any{nil, 7, []any{}} {
			cases = append(cases, recovery{fmt.Sprint(field, value), map[string]any{field: value}, nil, true})
		}
	}
	for _, field := range []string{"hub", "new_url"} {
		for _, value := range []any{nil, 7} {
			nested := map[string]any{"hub": original.Hub, "new_url": original.NewURL}
			nested[field] = value
			cases = append(cases, recovery{fmt.Sprint("notice-", field, value), map[string]any{"notice": nested}, nil, true})
		}
	}
	cases = append(cases, recovery{"missing-hub", map[string]any{"notice": map[string]any{"new_url": original.NewURL}}, nil, true})
	for _, u := range tuiNoticeURLs {
		for _, field := range []string{"new_url", "notice-hub", "notice-new_url"} {
			n, target := original, original.NewURL
			switch field {
			case "new_url":
				target = u.raw
			case "notice-hub":
				n.Hub = u.raw
			case "notice-new_url":
				n.NewURL = u.raw
			}
			valid := u.host != "" || (field == "notice-new_url" && u.raw == "")
			// Empty optional NewURL is legal even for an unverified moving notice;
			// SignNotice requires a target, so that control keeps its original Sig.
			if valid && n.NewURL != "" {
				n, _ = tuiNoticeSign(t, n)
			}
			details := map[string]any{"new_url": target, "notice": n}
			var want map[string]any
			if valid {
				want = details
			}
			cases = append(cases, recovery{u.name + "/" + field, details, want, !valid})
		}
	}
	for _, tc := range cases {
		for _, message := range []string{"move to https://u:TN_ERROR@received.example/?x#f", "TN_ERROR prose", ""} {
			t.Run(fmt.Sprintf("410/%s/%d", tc.name, len(message)), func(t *testing.T) {
				details := map[string]any{"debug": "TN_EXTRA unvalidated"}
				for k, v := range tc.details {
					details[k] = v
				}
				wire := tuiNoticeJSON(t, map[string]any{"error": map[string]any{"code": "hub_moved", "message": message, "details": details}})
				c, counts := tuiNoticeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != "/I47/v1/hub/notices" {
						t.Error("unexpected relocation source")
					}
					w.WriteHeader(410)
					tuiNoticeReply(t, w, wire)
				}, nil)
				m, p, _ := tuiNoticeModel(t, c, original)
				tuiNoticeLoad(m, p)
				var api *opclient.Error
				if !errors.As(m.Data.Errs["notices"], &api) || api.Status != 410 || api.Code != "hub_moved" {
					t.Fatalf("typed 410 lost: %v", m.Data.Errs["notices"])
				}
				want := "hub moved"
				if tc.bad {
					want = "invalid hub relocation"
				}
				if api.Message != want || m.toast.text != "reload notices: operator API 410 hub_moved: "+want || !m.toast.bad {
					t.Error("received relocation message reached error/toast")
				}
				if len(tc.want) == 0 {
					if len(api.Details) != 0 {
						t.Error("unsafe/absent recovery retained details")
					}
				} else if tuiNoticeJSON(t, api.Details) != tuiNoticeJSON(t, tc.want) {
					t.Error("allowlisted exact recovery changed or unsafe details retained")
				}
				if !reflect.DeepEqual(m.Data.Notices, []envelope.HubNotice{original}) {
					t.Error("410 replaced safe snapshot")
				}
				if n, ok := tc.want["notice"].(envelope.HubNotice); ok && n.NewURL != "" {
					raw := tuiNoticeJSON(t, api.Details["notice"])
					var got envelope.HubNotice
					_, pub := tuiNoticeSign(t, n)
					if json.Unmarshal([]byte(raw), &got) != nil || got != n || hubid.VerifyNotice(pub, got) != nil {
						t.Error("signed recovery original lost")
					}
				}
				counts.check(t, 1)
			})
		}
	}
	for _, tc := range []struct {
		status int
		code   string
	}{{410, "other_error"}, {422, "other_error"}, {422, "hub_moved"}} {
		t.Run(fmt.Sprintf("other-error/%d/%s", tc.status, tc.code), func(t *testing.T) {
			c, counts := tuiNoticeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				tuiNoticeReply(t, w, fmt.Sprintf(`{"error":{"code":%q,"message":"unchanged TN_OTHER prose","details":{"field":"unchanged"}}}`, tc.code))
			}, nil)
			m, p, _ := tuiNoticeModel(t, c, original)
			tuiNoticeLoad(m, p)
			var api *opclient.Error
			if !errors.As(m.Data.Errs["notices"], &api) || api.Status != tc.status || api.Code != tc.code || api.Message != "unchanged TN_OTHER prose" || api.Details["field"] != "unchanged" || !strings.Contains(m.toast.text, api.Message) {
				t.Error("unrelated envelope/toast changed")
			}
			counts.check(t, 1)
		})
	}
	causes := []error{errors.New("TN_CAUSE"), context.Canceled, context.DeadlineExceeded, &tls.RecordHeaderError{Msg: "TN_TLS"}}
	for _, timeout := range []bool{false, true} {
		for _, temporary := range []bool{false, true} {
			causes = append(causes, &net.OpError{Op: "dial", Net: "tcp", Err: &tuiNoticeNetError{timeout, temporary}})
		}
	}
	for i, cause := range causes {
		t.Run(fmt.Sprintf("transport-cause/%d", i), func(t *testing.T) {
			c, counts := tuiNoticeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("fault should precede source I/O") }, cause)
			m, p, _ := tuiNoticeModel(t, c, original)
			tuiNoticeLoad(m, p)
			err := m.Data.Errs["notices"]
			var ue *url.Error
			var got, source net.Error
			if !errors.Is(err, cause) || !errors.As(err, &ue) || ue.URL != counts.base+"/v1/hub/notices" || !errors.As(err, &got) {
				t.Fatalf("cause/URL chain lost: %v", err)
			}
			wantTimeout, wantTemporary := false, false
			if errors.As(cause, &source) {
				wantTimeout, wantTemporary = source.Timeout(), source.Temporary()
			}
			if got.Timeout() != wantTimeout || got.Temporary() != wantTemporary || strings.Contains(err.Error(), "TN_") {
				t.Error("transport classification/safe display changed")
			}
			if !reflect.DeepEqual(m.Data.Notices, []envelope.HubNotice{original}) {
				t.Error("transport failure replaced safe snapshot")
			}
			counts.check(t, 1)
			if counts.responses.Load() != 0 {
				t.Error("transport fault invented an HTTP response")
			}
		})
	}
}

type tuiNoticeNetError struct{ timeout, temporary bool }

func (e *tuiNoticeNetError) Error() string   { return "TN_TRANSPORT" }
func (e *tuiNoticeNetError) Timeout() bool   { return e.timeout }
func (e *tuiNoticeNetError) Temporary() bool { return e.temporary }
