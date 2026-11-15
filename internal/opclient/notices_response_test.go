package opclient

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
)

// Fixtures exercise public clients through real synthetic HTTP responses.
// Every URL, key, and bearer is synthetic.
var noticeResponseURLs = []struct {
	name, raw string
	valid     bool
}{
	{"relative", "/NR_RELATIVE", false},
	{"scheme", "ftp://received.example/NR_SCHEME", false},
	{"password", "https://alice:NR_PASSWORD@received.example/vecnet", false},
	{"username", "https://NR_USERNAME@received.example/vecnet", false},
	{"query", "https://received.example/vecnet?token=NR_QUERY", false},
	{"bare-query", "https://received.example/NR_EMPTY_QUERY?", false},
	{"fragment", "https://received.example/vecnet#NR_FRAGMENT", false},
	{"bare-fragment", "https://received.example/NR_EMPTY_FRAGMENT#", false},
	{"authority", "https://received.example:NR_AUTHORITY/vecnet", false},
	{"ipv6", "https://[NR_IPV6/vecnet", false},
	{"escape", "https://received.example/NR_ESCAPE%zz", false},
	{"https-prefix", "https://Safe.Example:8443/Vecnet/%2Fexact/", true},
	{"https-ipv6", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", true},
	{"loopback-http", "http://127.0.0.1:8080/Vecnet/", true},
	{"lan-http", "http://192.168.1.7:8080/Vecnet/", true},
	{"operator-http", "http://public.example:8080/Vecnet/", true},
}

func noticeResponseJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noticeResponseSigned(t *testing.T, n envelope.HubNotice) (envelope.HubNotice, string) {
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

func noticeResponseOriginal(t *testing.T) envelope.HubNotice {
	t.Helper()
	n, _ := noticeResponseSigned(t, envelope.HubNotice{
		NoticeID: "ntc_01JD3S6G8J0M2P4R6T8V0X0001", Kind: "moving", Message: "Operator announcement",
		Hub: "https://Source.Example:8443/Vecnet/%2Fexact/", NewURL: "https://[2001:db8::7]:8443/Next/%2Fexact/",
		IssuedAt: "2026-11-01T00:00:00.000Z", At: "2026-11-08T00:00:00.000Z", ExpiresAt: "2026-12-01T00:00:00.000Z",
	})
	return n
}

type noticeResponseCase struct {
	name, notice, data string
	valid              bool
}

func noticeResponseCases(t *testing.T) []noticeResponseCase {
	t.Helper()
	var cases []noticeResponseCase
	for _, u := range noticeResponseURLs {
		for _, field := range []string{"hub", "new_url"} {
			for _, state := range []string{"posted", "cleared", "nonmoving"} {
				n := noticeResponseOriginal(t)
				if field == "hub" {
					n.Hub = u.raw
				} else {
					n.NewURL = u.raw
				}
				change := state
				if state == "nonmoving" {
					n.Kind, change = "maintenance", "posted" // No new kind/signature policy.
				} else if u.valid {
					n, _ = noticeResponseSigned(t, n)
				}
				if state == "cleared" {
					n.ClearedAt, n.ClearedHow = "2026-11-08T01:00:00.000Z", "operator"
				}
				raw := noticeResponseJSON(t, n)
				cases = append(cases, noticeResponseCase{u.name + "/" + field + "/" + state, raw, fmt.Sprintf("{ \"change\" : %q, \"notice\" : %s }", change, raw), u.valid})
			}
		}
	}
	for _, shape := range []struct{ name, notice, data string }{
		{"missing-notice", `{}`, `{"change":"posted"}`},
		{"null-notice", `null`, `{"change":"posted","notice":null}`},
		{"wrong-notice", `7`, `{"change":"posted","notice":7}`},
		{"wrong-hub", `{"hub":7}`, `{"change":"posted","notice":{"hub":7}}`},
		{"wrong-new-url", `{"hub":"https://safe.example","new_url":7}`, `{"change":"posted","notice":{"hub":"https://safe.example","new_url":7}}`},
		{"null-event", `{}`, `null`},
	} {
		cases = append(cases, noticeResponseCase{shape.name, shape.notice, shape.data, false})
	}
	for _, field := range []string{"hub", "new_url"} {
		for _, value := range []string{"null", "true", "[]", "{}", `""`} {
			if field == "new_url" && value == `""` {
				continue // Empty optional NewURL is a valid control below.
			}
			notice := `{"hub":"https://safe.example","new_url":"https://next.example"}`
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(notice), &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = json.RawMessage(value)
			raw := noticeResponseJSON(t, fields)
			cases = append(cases, noticeResponseCase{"shape/" + field + "/" + value, raw, `{"change":"cleared","notice":` + raw + `}`, false})
		}
	}
	n := noticeResponseOriginal(t)
	n.Kind, n.NewURL = "maintenance", ""
	n, _ = noticeResponseSigned(t, n)
	raw := noticeResponseJSON(t, n)
	return append(cases, noticeResponseCase{"empty-new-url", raw, `{ "change" : "posted", "notice" : ` + raw + ` }`, true})
}

type noticeResponseFixture struct {
	c                           *Client
	source, destination, closed atomic.Int32
}

type noticeResponseBody struct {
	io.ReadCloser
	closed *atomic.Int32
	once   atomic.Bool
}

func (b *noticeResponseBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		b.closed.Add(1)
	}
	return b.ReadCloser.Close()
}

func newNoticeResponseFixture(t *testing.T, handle http.HandlerFunc) *noticeResponseFixture {
	t.Helper()
	f := &noticeResponseFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vn_operator_NR_SYNTHETIC_ONLY" {
			t.Error("fixture lost its synthetic bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	wire := &http.Transport{} // Proxy-free; all nonfixture requests are blocked.
	t.Cleanup(wire.CloseIdleConnections)
	f.c = New(srv.URL+"/I47", "vn_operator_NR_SYNTHETIC_ONLY")
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(srv.URL, "http://") || !strings.HasPrefix(r.URL.Path, "/I47/") {
			f.destination.Add(1)
			return nil, errors.New("notice fixture blocked a destination request")
		}
		f.source.Add(1)
		resp, err := wire.RoundTrip(r)
		if err == nil {
			resp.Body = &noticeResponseBody{ReadCloser: resp.Body, closed: &f.closed}
		}
		return resp, err
	})
	f.c.hc.Transport, f.c.uploadHC.Transport = rt, rt
	return f
}

func (f *noticeResponseFixture) check(t *testing.T, source int32) {
	t.Helper()
	if f.source.Load() != source || f.destination.Load() != 0 {
		t.Errorf("source/destination requests = %d/%d, want %d/0", f.source.Load(), f.destination.Load(), source)
	}
	if f.closed.Load() != f.source.Load() {
		t.Error("a real source response body was not closed")
	}
}

func TestNoticesRejectInvalidURLs(t *testing.T) {
	good := noticeResponseJSON(t, noticeResponseOriginal(t))
	cases := append(noticeResponseCases(t), noticeResponseCase{name: "empty-list", valid: true})
	for _, tc := range cases {
		orders := []string{"valid-first"}
		if !tc.valid {
			orders = append(orders, "invalid-first")
		}
		for _, order := range orders {
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				items := good + "," + tc.notice
				if order == "invalid-first" {
					items = tc.notice + "," + good
				}
				if tc.name == "empty-list" {
					items = ""
				}
				f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/I47/v1/hub/notices" {
						t.Error("unexpected notice source request")
					}
					_, _ = io.WriteString(w, `{"notices":[`+items+`]}`)
				})
				got, err := f.c.Notices(context.Background())
				if tc.valid {
					var want []envelope.HubNotice
					if e := json.Unmarshal([]byte("["+items+"]"), &want); e != nil {
						t.Fatal(e)
					}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("accepted originals/signatures changed: %v", err)
					}
				} else {
					if err == nil || err.Error() != "invalid hub notice" || len(got) != 0 {
						t.Errorf("invalid notice list returned %d entries; protocol refusal=%t", len(got), err != nil)
					}
					var ne net.Error
					if err != nil && (errors.As(err, &ne) || strings.Contains(err.Error(), "NR_")) {
						t.Errorf("invalid notice became a transport error or exposed received data: %q", err.Error())
					}
				}
				f.check(t, 1)
			})
		}
	}
}

func TestTailRejectsNoticeBeforeApplyAck(t *testing.T) {
	good := noticeResponseJSON(t, noticeResponseOriginal(t))
	cases := append(noticeResponseCases(t), noticeResponseCase{name: "empty-page", valid: true}, noticeResponseCase{name: "unrelated-event", data: `{ "note" : "https://u:NR_UNRELATED@text.example/?x=1#f" }`, valid: true})
	for _, tc := range cases {
		orders := []string{"valid-first"}
		if !tc.valid {
			orders = append(orders, "invalid-first")
		}
		for _, order := range orders {
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				kind := "hub_notice"
				if tc.name == "unrelated-event" {
					kind = "future_event"
				}
				data := []string{`{ "change" : "posted", "notice" : ` + good + ` }`, tc.data}
				if order == "invalid-first" {
					data[0], data[1] = data[1], data[0]
				}
				var events []string
				for i, d := range data {
					eventKind := "hub_notice"
					if tc.name == "unrelated-event" && i == 1 {
						eventKind = kind
					}
					events = append(events, fmt.Sprintf(`{"cursor":"c%d","type":%q,"data":%s}`, i+1, eventKind, d))
				}
				if tc.name == "empty-page" {
					events, data = nil, nil
				}
				var polls, acks, applies atomic.Int32
				accepted, pauses := 0, 0
				f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/I47/v1/events":
						n := polls.Add(1)
						wantAfter, wantWait := "", "0"
						if n > 1 && tc.valid {
							wantAfter = "c2"
							if len(events) == 0 {
								wantWait = "25"
							}
						}
						if r.Method != http.MethodGet || r.URL.Query().Get("after") != wantAfter || r.URL.Query().Get("wait") != wantWait {
							t.Error("event cursor/wait advanced across an unaccepted page")
						}
						if n == 1 {
							_, _ = io.WriteString(w, `{"events":[`+strings.Join(events, ",")+`],"next_cursor":"c2"}`)
						} else {
							w.WriteHeader(http.StatusUnauthorized)
						}
					case "/I47/v1/ack":
						acks.Add(1)
						var body struct {
							Cursor string `json:"cursor"`
						}
						if json.NewDecoder(r.Body).Decode(&body) != nil || body.Cursor != "c2" || applies.Load() != 1 || r.Method != http.MethodPost {
							t.Error("ack did not follow complete batch application")
						}
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Error("unexpected tail source request")
						w.WriteHeader(http.StatusUnauthorized)
					}
				})
				f.c.sleep = func(context.Context, time.Duration) error {
					pauses++
					if pauses > 1 {
						return errors.New("fixture stopped an unexpected retry loop")
					}
					return nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err := f.c.TailWithAck(ctx, func(b Batch) error {
					applies.Add(1)
					if !tc.valid {
						t.Error("unsafe notice page reached apply before full validation")
					}
					if b.Reset || b.Cursor != "c2" || len(b.Events) != len(data) {
						t.Error("accepted batch changed its cursor/events")
					} else {
						for i, ev := range b.Events {
							if string(ev.Data) != data[i] {
								t.Error("accepted raw Event.Data/signature bytes changed")
							}
						}
					}
					return nil
				}, nil, func(cursor string) {
					accepted++
					if cursor != "c2" || acks.Load() != 1 || applies.Load() != 1 {
						t.Error("accepted callback preceded the paid ack")
					}
				})
				wantApply, wantAck, wantPause := 0, int32(0), 1
				if tc.valid {
					wantApply, wantPause = 1, 0
					if len(events) > 0 {
						wantAck = 1
					}
				}
				if !errors.Is(err, ErrUnauthorized) || polls.Load() != 2 || applies.Load() != int32(wantApply) || acks.Load() != wantAck || accepted != int(wantAck) || pauses != wantPause {
					t.Errorf("tail err=%v polls/apply/ack/accepted/pause=%d/%d/%d/%d/%d", err, polls.Load(), applies.Load(), acks.Load(), accepted, pauses)
				}
				f.check(t, 2+wantAck)
			})
		}
	}
}

func TestHubMovedErrorProjection(t *testing.T) {
	original := noticeResponseOriginal(t)
	type relocationCase struct {
		name          string
		details, want map[string]any
	}
	cases := []relocationCase{
		{"valid-both", map[string]any{"new_url": original.NewURL, "notice": original}, map[string]any{"new_url": original.NewURL, "notice": original}},
		{"valid-target-only", map[string]any{"new_url": original.NewURL}, map[string]any{"new_url": original.NewURL}},
		{"valid-notice-only", map[string]any{"notice": original}, map[string]any{"notice": original}},
		{"absent", nil, nil},
		{"wrong-target", map[string]any{"new_url": 7}, nil},
		{"null-target", map[string]any{"new_url": nil}, nil},
		{"wrong-notice", map[string]any{"notice": 7}, nil},
		{"null-notice", map[string]any{"notice": nil}, nil},
		{"missing-notice-hub", map[string]any{"notice": map[string]any{"new_url": original.NewURL}}, nil},
		{"wrong-notice-hub", map[string]any{"notice": map[string]any{"hub": 7, "new_url": original.NewURL}}, nil},
		{"wrong-notice-new-url", map[string]any{"notice": map[string]any{"hub": original.Hub, "new_url": 7}}, nil},
	}
	for _, u := range noticeResponseURLs {
		for _, field := range []string{"new_url", "notice-hub", "notice-new-url"} {
			n := original
			target := original.NewURL
			switch field {
			case "new_url":
				target = u.raw
			case "notice-hub":
				n.Hub = u.raw
			case "notice-new-url":
				n.NewURL = u.raw
			}
			if u.valid {
				n, _ = noticeResponseSigned(t, n)
			}
			details := map[string]any{"new_url": target, "notice": n}
			var want map[string]any
			if u.valid {
				want = details
			} else if field == "new_url" {
				want = map[string]any{"notice": n}
			} else {
				want = map[string]any{"new_url": target}
			}
			cases = append(cases, relocationCase{u.name + "/" + field, details, want})
		}
	}
	for _, tc := range cases {
		for _, message := range []string{"move to https://alice:NR_MESSAGE@received.example/?token=x#f", "NR_MESSAGE server prose", ""} {
			t.Run(tc.name+"/"+fmt.Sprint(len(message)), func(t *testing.T) {
				details := map[string]any{"debug": "NR_EXTRA unvalidated details"}
				for k, v := range tc.details {
					details[k] = v
				}
				wire := noticeResponseJSON(t, map[string]any{"error": map[string]any{"code": "hub_moved", "message": message, "details": details}})
				f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/I47/v1/admin/peers" {
						t.Error("unexpected relocation source request")
					}
					w.WriteHeader(http.StatusGone)
					_, _ = io.WriteString(w, wire)
				})
				_, err := f.c.Peers(context.Background())
				var api *Error
				if !errors.As(err, &api) || api.Status != http.StatusGone || api.Code != "hub_moved" {
					t.Fatalf("410/code identity lost: %v", err)
				}
				if api.Message != "hub moved" && api.Message != "invalid hub relocation" {
					t.Errorf("unsafe/nonfixed relocation message: %q", api.Message)
				}
				var gotJSON, wantJSON any
				if len(tc.want) == 0 {
					if len(api.Details) != 0 {
						t.Error("invalid/absent recovery retained unvalidated Details")
					}
				} else {
					_ = json.Unmarshal([]byte(noticeResponseJSON(t, api.Details)), &gotJSON)
					_ = json.Unmarshal([]byte(noticeResponseJSON(t, tc.want)), &wantJSON)
					if !reflect.DeepEqual(gotJSON, wantJSON) {
						t.Error("safe allowlisted recovery or signed originals changed")
					}
				}
				if strings.Contains(err.Error()+noticeResponseJSON(t, api.Details), "NR_") {
					t.Errorf("received relocation data exposed: %q", err.Error())
				}
				f.check(t, 1)
			})
		}
	}
	for _, tc := range []struct {
		status int
		code   string
	}{{410, "other_error"}, {422, "other_error"}, {422, "hub_moved"}} {
		t.Run(fmt.Sprintf("unrelated-status-code/%d/%s", tc.status, tc.code), func(t *testing.T) {
			f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":"unchanged NR_OTHER prose","details":{"field":"unchanged"}}}`, tc.code)
			})
			_, err := f.c.Peers(context.Background())
			var api *Error
			if !errors.As(err, &api) || api.Status != tc.status || api.Code != tc.code || api.Message != "unchanged NR_OTHER prose" || api.Details["field"] != "unchanged" {
				t.Errorf("unrelated API envelope changed: %v", err)
			}
			f.check(t, 1)
		})
	}
	causes := []error{errors.New("NR_TRANSPORT"), context.Canceled, context.DeadlineExceeded, &tls.RecordHeaderError{Msg: "NR_TLS"}}
	for _, timeout := range []bool{false, true} {
		for _, temporary := range []bool{false, true} {
			causes = append(causes, &net.OpError{Op: "dial", Net: "tcp", Err: &noticeResponseNetError{timeout, temporary}})
		}
	}
	for i, cause := range causes {
		t.Run(fmt.Sprintf("transport-preservation/%d", i), func(t *testing.T) {
			const base = "https://[2001:db8::7]:8443/Checked/%2Fexact"
			c, calls := New(base, "vn_operator_NR_SYNTHETIC_ONLY"), 0
			c.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != base+"/v1/admin/peers" {
					t.Error("transport control changed its checked source")
				}
				return nil, cause // Controlled source failure; no actual dials.
			})
			_, err := c.Peers(context.Background())
			var ue *url.Error
			if calls != 1 || !errors.Is(err, cause) || !errors.As(err, &ue) || ue.URL != base+"/v1/admin/peers" {
				t.Fatalf("original transport/URL chain changed: %v", err)
			}
			var original, got net.Error
			wantTimeout, wantTemporary := false, false
			if errors.As(cause, &original) {
				wantTimeout, wantTemporary = original.Timeout(), original.Temporary()
			}
			if !errors.As(err, &got) || got.Timeout() != wantTimeout || got.Temporary() != wantTemporary {
				t.Error("Timeout/Temporary semantics changed")
			}
			switch want := cause.(type) {
			case *net.OpError:
				var op *net.OpError
				if !errors.As(err, &op) || op != want {
					t.Error("net.OpError identity lost")
				}
			case *tls.RecordHeaderError:
				var rec *tls.RecordHeaderError
				if !errors.As(err, &rec) || rec != want {
					t.Error("TLS identity lost")
				}
			}
			if !strings.Contains(err.Error(), base) || strings.Contains(err.Error(), "NR_") {
				t.Error("I6 safe display changed")
			}
		})
	}
}

type noticeResponseNetError struct{ timeout, temporary bool }

func (e *noticeResponseNetError) Error() string   { return "NR_TRANSPORT" }
func (e *noticeResponseNetError) Timeout() bool   { return e.timeout }
func (e *noticeResponseNetError) Temporary() bool { return e.temporary }

// Blob discovery and R2 minting use a distinct response boundary from raw PUTs.
// They must retain the same checked follow fields as normal operator requests.
func TestUploadRelocationRecoveryFields(t *testing.T) {
	n := noticeResponseOriginal(t)
	for _, surface := range []string{"discovery", "disk-upload", "r2-mint"} {
		for _, tc := range []struct {
			name    string
			details map[string]any
			valid   bool
		}{
			{"signed", map[string]any{"new_url": n.NewURL, "notice": n}, true},
			{"invalid-target", map[string]any{"new_url": "https://alice:NR_UPLOAD@received.example", "notice": n}, false},
			{"null-notice-hub", map[string]any{"notice": map[string]any{"hub": nil, "new_url": n.NewURL}}, false},
			{"null-notice-new-url", map[string]any{"notice": map[string]any{"hub": n.Hub, "new_url": nil}}, false},
		} {
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/I47/v1/blob-capabilities" && surface != "discovery" {
						if surface == "disk-upload" {
							w.WriteHeader(http.StatusNotFound)
						} else {
							_, _ = io.WriteString(w, `{"backend":"r2","max_size":100,"checksum_profile":"sha256"}`)
						}
						return
					}
					w.WriteHeader(http.StatusGone)
					details := map[string]any{"debug": "NR_UPLOAD_EXTRA"}
					for k, v := range tc.details {
						details[k] = v
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": "NR_UPLOAD_MESSAGE https://alice:NR_UPLOAD@received.example", "details": details}})
				})
				got, err := f.c.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "fixture.txt")
				var api *Error
				if got != (envelope.Attachment{}) || !errors.As(err, &api) || api.Status != 410 || api.Code != "hub_moved" {
					t.Fatalf("relocation identity/effect changed: %v", err)
				}
				wantMessage := "invalid hub relocation"
				if tc.valid {
					wantMessage = "hub moved"
				}
				if api.Message != wantMessage {
					t.Errorf("relocation public prose = %q, want %q", api.Message, wantMessage)
				}
				var want map[string]any
				if tc.valid {
					want = tc.details
				} else if tc.name == "invalid-target" {
					want = map[string]any{"notice": n}
				}
				if noticeResponseJSON(t, api.Details) != noticeResponseJSON(t, want) {
					t.Error("upload lost independently checked recovery or kept invalid details")
				}
				if strings.Contains(err.Error()+noticeResponseJSON(t, api.Details), "NR_UPLOAD") {
					t.Error("upload exposed received error prose")
				}
				wantRequests := int32(2)
				if surface == "discovery" {
					wantRequests = 1
				}
				f.check(t, wantRequests)
			})
		}
	}
}

func TestUploadRelocationDoesNotMaskBodyFailure(t *testing.T) {
	for _, surface := range []string{"discovery", "r2-mint"} {
		t.Run(surface, func(t *testing.T) {
			f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if surface == "r2-mint" && r.URL.Path == "/I47/v1/blob-capabilities" {
					_, _ = io.WriteString(w, `{"backend":"r2","max_size":100,"checksum_profile":"sha256"}`)
					return
				}
				body := `{"error":{"code":"hub_moved","message":"NR_BODY","details":{"new_url":"https://next.example"}}}`
				// A complete JSON value followed by a truncated HTTP body remains a
				// transport failure in blobclient, rather than an accepted API error.
				w.Header().Set("Content-Length", fmt.Sprint(len(body)+1))
				w.WriteHeader(http.StatusGone)
				_, _ = io.WriteString(w, body)
			})
			_, err := f.c.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "fixture.txt")
			var api *Error
			if !errors.As(err, &api) || api.Status != 0 || api.Code != "blob_unavailable" {
				t.Errorf("incomplete response became a relocation API error: %v", err)
			}
			wantRequests := int32(1)
			if surface == "r2-mint" {
				wantRequests = 2
			}
			f.check(t, wantRequests)
		})
	}
}

func TestUploadRelocationRejectsMalformedDocument(t *testing.T) {
	for _, surface := range []string{"discovery", "r2-mint"} {
		for _, tc := range []struct{ name, suffix string }{
			{"trailing-junk", " TRAILING_INVALID_JSON"},
			{"second-document", ` {"synthetic":"second JSON value"}`},
		} {
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if surface == "r2-mint" && r.URL.Path == "/I47/v1/blob-capabilities" {
						if r.Method != http.MethodGet {
							t.Error("capabilities method changed")
						}
						_, _ = io.WriteString(w, `{"backend":"r2","max_size":100,"checksum_profile":"sha256"}`)
						return
					}
					wantMethod, wantPath := http.MethodGet, "/I47/v1/blob-capabilities"
					if surface == "r2-mint" {
						wantMethod, wantPath = http.MethodPost, "/I47/v1/blobs/uploads"
					}
					if r.Method != wantMethod || r.URL.Path != wantPath {
						t.Error("unexpected upload request or follow effect")
					}
					w.WriteHeader(http.StatusGone)
					_, _ = io.WriteString(w, `{"error":{"code":"hub_moved","message":"synthetic","details":{"new_url":"https://next.example"}}}`+tc.suffix)
				})
				got, err := f.c.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "fixture.txt")
				var api *Error
				if got != (envelope.Attachment{}) || !errors.As(err, &api) {
					t.Fatalf("malformed response changed result/error type: attachment=%+v err=%v", got, err)
				}
				if api.Status != http.StatusGone || api.Code != "http_410" || api.Message != "http_410" ||
					!reflect.DeepEqual(api.Details, map[string]any{"retry_after_seconds": float64(0)}) {
					t.Errorf("malformed whole body changed existing error: Status=%d Code=%q Message=%q Details=%v", api.Status, api.Code, api.Message, api.Details)
				}
				wantRequests := int32(1)
				if surface == "r2-mint" {
					wantRequests = 2
				}
				f.check(t, wantRequests)
			})
		}
	}
}

func TestRelocationIndependentRecoveryResponses(t *testing.T) {
	n := noticeResponseOriginal(t)
	for _, surface := range []string{"request", "discovery", "r2-mint"} {
		for _, value := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`7`), json.RawMessage(`true`), json.RawMessage(`[]`), json.RawMessage(`{}`), json.RawMessage(`"https://alice:NR_SIBLING@received.example"`), json.RawMessage(`"https://received.example?NR_SIBLING"`), json.RawMessage(`"https://received.example#NR_SIBLING"`), json.RawMessage(`"https://[NR_SIBLING"`)} {
			for _, field := range []string{"new_url", "notice-hub", "notice-new-url"} {
				t.Run(surface+"/"+string(value)+"/"+field, func(t *testing.T) {
					details := map[string]any{"new_url": n.NewURL, "notice": n, "debug": "NR_SIBLING"}
					want := map[string]any{"new_url": n.NewURL}
					if field == "new_url" {
						details[field] = value
						want = map[string]any{"notice": n}
					} else {
						bad := map[string]any{"hub": n.Hub, "new_url": n.NewURL}
						if field == "notice-hub" {
							bad["hub"] = value
						} else {
							bad["new_url"] = value
						}
						details["notice"] = bad
					}
					f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
						if surface == "r2-mint" && r.URL.Path == "/I47/v1/blob-capabilities" {
							_, _ = io.WriteString(w, `{"backend":"r2","max_size":100,"checksum_profile":"sha256"}`)
							return
						}
						wantMethod, wantPath := http.MethodGet, "/I47/v1/admin/peers"
						if surface == "discovery" {
							wantPath = "/I47/v1/blob-capabilities"
						}
						if surface == "r2-mint" {
							wantMethod, wantPath = http.MethodPost, "/I47/v1/blobs/uploads"
						}
						if r.Method != wantMethod || r.URL.Path != wantPath {
							t.Error("unexpected request/follow effect")
						}
						w.WriteHeader(http.StatusGone)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": "NR_SIBLING", "details": details}})
					})
					var err error
					if surface == "request" {
						_, err = f.c.Peers(context.Background())
					} else {
						var got envelope.Attachment
						got, err = f.c.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "fixture.txt")
						if got != (envelope.Attachment{}) {
							t.Error("rejected upload returned attachment")
						}
					}
					var api *Error
					if !errors.As(err, &api) || api.Status != 410 || api.Code != "hub_moved" {
						t.Fatalf("typed status/code changed: %v", err)
					}
					if api.Message != "invalid hub relocation" || noticeResponseJSON(t, api.Details) != noticeResponseJSON(t, want) {
						t.Errorf("independent checked recovery changed: message=%q details=%v", api.Message, api.Details)
					}
					if accepted, ok := api.Details["notice"].(envelope.HubNotice); ok && accepted != n {
						t.Error("accepted signed fields changed")
					}
					if strings.Contains(err.Error()+noticeResponseJSON(t, api.Details), "NR_") {
						t.Error("unsafe sibling or error prose exposed")
					}
					calls := int32(1)
					if surface == "r2-mint" {
						calls = 2
					}
					f.check(t, calls)
				})
			}
		}
	}
}
