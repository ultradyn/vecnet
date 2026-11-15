package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

type receivedURLCase struct {
	name   string
	value  any
	canary string
}

func receivedURLCases(destination string) []receivedURLCase {
	return []receivedURLCase{
		{"userinfo", strings.Replace(destination, "://", "://alice:T2_USERSECRET@", 1), "T2_USERSECRET"},
		{"query", destination + "?token=T2_QUERYSECRET", "T2_QUERYSECRET"},
		{"fragment", destination + "#T2_FRAGMENT", "T2_FRAGMENT"},
		{"malformed", "https://[T2_MALFORMED", "T2_MALFORMED"},
		{"null", nil, ""}, {"number", 42, ""}, {"object", map[string]string{"x": "T2_TYPE"}, "T2_TYPE"},
	}
}
func receivedNoticeMap(t *testing.T) map[string]any {
	t.Helper()
	key := testIdentityKey(t, "1")
	n := noticeWithSecrets(t, "https://source.example/Vecnet/%2Fexact/", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", &key)
	raw, _ := json.Marshal(n)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func receivedEvent(t *testing.T, notice any, change string) envelope.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"change": change, "notice": notice})
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Event{Cursor: "unsafe-cursor", Type: "hub_notice", Data: raw}
}
func receivedDelivery() envelope.Event {
	return envelope.Event{Cursor: "delivery-cursor", Type: "delivery", Data: json.RawMessage(`{"delivery_id":"dly_synthetic","message":{"message_id":"msg_synthetic","thread_id":"thr_synthetic","sender":"peer_synthetic","content":{"text":"FIRST_EVENT"}}}`)}
}
func receivedApp(t *testing.T, base string, isJSON bool) (*app, *bytes.Buffer, *bytes.Buffer, cache) {
	t.Helper()
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = \"vn_operator_synthetic\"\n", base))
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdout: &out, stderr: &errs, stdin: strings.NewReader(""), getenv: env, json: isJSON}
	return a, &out, &errs, cache{dir: t.TempDir()}
}
func assertReceivedRefusal(t *testing.T, err error, output, canary string) {
	t.Helper()
	if err == nil {
		t.Error("unchecked received notice accepted")
	}
	if canary != "" && strings.Contains(output, canary) {
		t.Error("received URL canary reached output")
	}
}

func TestReceivedNoticeCLI(t *testing.T) {
	var destinations atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinations.Add(1); w.WriteHeader(500) }))
	defer dst.Close()
	for _, field := range []string{"hub", "new_url"} {
		for _, tc := range receivedURLCases(dst.URL) {
			for _, mode := range []string{"list-human", "list-json", "post-human", "post-json"} {
				t.Run(field+"/"+tc.name+"/"+mode, func(t *testing.T) {
					n := receivedNoticeMap(t)
					n[field] = tc.value
					var requests atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/v1/hub/notices" {
							requests.Add(1)
							json.NewEncoder(w).Encode(map[string]any{"notices": []any{receivedNoticeMap(t), n}})
							return
						}
						if r.URL.Path == "/v1/admin/notices" {
							requests.Add(1)
							json.NewEncoder(w).Encode(map[string]any{"notice": n})
							return
						}
						http.NotFound(w, r)
					}))
					defer srv.Close()
					_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = \"vn_operator_synthetic\"\n", srv.URL))
					args := []string{"notice", "list"}
					if strings.HasPrefix(mode, "post") {
						args = []string{"notice", "post", "--kind", "maintenance", "--message", "synthetic"}
					}
					if strings.HasSuffix(mode, "json") {
						args = append([]string{"--json"}, args...)
					}
					code, out, errs := pinRun(env, args...)
					if code == 0 || out != "" || !strings.Contains(errs, "invalid hub notice") {
						t.Errorf("exit=%d output=%q error=%q", code, out, errs)
					}
					if tc.canary != "" && strings.Contains(out+errs, tc.canary) {
						t.Error("canary printed")
					}
					if requests.Load() != 1 || destinations.Load() != 0 {
						t.Errorf("source=%d destination=%d", requests.Load(), destinations.Load())
					}
				})
			}
		}
	}
}

func TestReceivedEventPagesBeforeEffects(t *testing.T) {
	var destinations atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinations.Add(1) }))
	defer dst.Close()
	for _, field := range []string{"hub", "new_url"} {
		for _, tc := range receivedURLCases(dst.URL) {
			for _, mode := range []string{"inbox-json", "watch-human", "watch-json", "monitor-human", "monitor-json"} {
				for _, badFirst := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/bad-first-%t", field, tc.name, mode, badFirst), func(t *testing.T) {
						n := receivedNoticeMap(t)
						n[field] = tc.value
						events := []envelope.Event{receivedDelivery(), receivedEvent(t, n, "cleared")}
						if badFirst {
							events[0], events[1] = events[1], events[0]
						}
						var acks, receipts atomic.Int32
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							switch r.URL.Path {
							case "/v1/events":
								if after := r.URL.Query().Get("after"); after != "" && after != "HELD_CURSOR" {
									json.NewEncoder(w).Encode(pollBody{NextCursor: "NEW_CURSOR"})
									return
								}
								json.NewEncoder(w).Encode(pollBody{Events: events, NextCursor: "NEW_CURSOR", HubRestarting: &restartHint{RetryAfterMS: 1000}})
							case "/v1/ack":
								acks.Add(1)
								fmt.Fprint(w, `{}`)
							default:
								if r.Method == http.MethodPost {
									receipts.Add(1)
								}
								http.NotFound(w, r)
							}
						}))
						defer srv.Close()
						a, out, errs, ch := receivedApp(t, srv.URL, strings.HasSuffix(mode, "json"))
						if err := ch.rememberMessages(map[string]string{"msg_held": "thr_held"}); err != nil {
							t.Fatal(err)
						}
						before, _ := os.ReadFile(filepath.Join(ch.dir, "messages.json"))
						var err error
						if mode == "inbox-json" {
							// --limit must not hide an invalid page suffix before --ack/output/cache.
							// Use the real command/profile and point its cache at our witness.
							_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\n", srv.URL))
							a.getenv = env
							dir, _ := cacheDir(env)
							ch = cache{dir: dir}
							ch.rememberMessages(map[string]string{"msg_held": "thr_held"})
							before, _ = os.ReadFile(filepath.Join(ch.dir, "messages.json"))
							err = a.cmdInbox([]string{"--ack", "--limit", "1"})
						} else {
							w := &watcher{a: a, c: newClient(srv.URL, "vn_peer_synthetic"), ch: ch, cursor: "HELD_CURSOR", pendingAck: "HELD_ACK", seen: newDedupe(10), autoAck: true, identityOff: true, forMonitor: strings.HasPrefix(mode, "monitor"), names: map[string]string{}}
							if strings.HasPrefix(mode, "monitor") {
								err = w.replay()
							} else {
								err = w.longPoll()
							}
							if w.cursor != "HELD_CURSOR" || w.pendingAck != "HELD_ACK" || w.replayed || w.move != nil {
								t.Error("watch state changed")
							}
							if len(w.seen.set) != 0 || w.deferred != nil || !w.restartUntil.IsZero() {
								t.Error("partial page state retained")
							}
						}
						assertReceivedRefusal(t, err, out.String()+errs.String(), tc.canary)
						after, _ := os.ReadFile(filepath.Join(ch.dir, "messages.json"))
						if !bytes.Equal(before, after) || out.Len() != 0 || acks.Load() != 0 || receipts.Load() != 0 || destinations.Load() != 0 {
							t.Errorf("partial page effects: output=%q ack=%d receipt=%d destination=%d cacheChanged=%t", out.String(), acks.Load(), receipts.Load(), destinations.Load(), !bytes.Equal(before, after))
						}
					})
				}
			}
		}
	}
}

func TestReceivedWatchEvent(t *testing.T) {
	for _, field := range []string{"hub", "new_url"} {
		for _, tc := range receivedURLCases("http://127.0.0.1:1") {
			for _, mode := range []string{"human", "json", "monitor", "monitor-json"} {
				t.Run(field+"/"+tc.name+"/"+mode, func(t *testing.T) {
					n := receivedNoticeMap(t)
					n[field] = tc.value
					ev := receivedEvent(t, n, "posted")
					raw, _ := json.Marshal(ev)
					a, out, errs, ch := receivedApp(t, "https://source.example", strings.Contains(mode, "json"))
					var destinations atomic.Int32
					c := newClient("https://source.example", "")
					c.hc.Transport = receivedRoundTrip(func(*http.Request) (*http.Response, error) {
						destinations.Add(1)
						return nil, fmt.Errorf("unexpected request")
					})
					w := &watcher{a: a, c: c, ch: ch, seen: newDedupe(10), autoAck: true, forMonitor: strings.HasPrefix(mode, "monitor")}
					err := w.handle("hub_notice", string(raw), false)
					assertReceivedRefusal(t, err, out.String()+errs.String(), tc.canary)
					if out.Len() != 0 || w.pendingAck != "" || w.move != nil || destinations.Load() != 0 {
						t.Errorf("event effects output=%q ack=%q requests=%d", out.String(), w.pendingAck, destinations.Load())
					}
				})
			}
		}
	}
}

type receivedRoundTrip func(*http.Request) (*http.Response, error)

func (f receivedRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReceivedNoticeSafeAndHTTPPolicy(t *testing.T) {
	for _, policy := range []string{"strict", "profile", "environment"} {
		for _, field := range []string{"hub", "new_url"} {
			t.Run(policy+"/"+field, func(t *testing.T) {
				key := testIdentityKey(t, "1")
				n := noticeWithSecrets(t, "https://source.example/Vecnet/%2Fexact/", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", &key)
				if policy != "strict" {
					if field == "hub" {
						n.Hub = "http://public.example/Vecnet/%2Fexact/"
					} else {
						n.NewURL = "http://public.example/Vecnet/%2Fexact/"
					}
					if err := key.SignNotice(&n); err != nil {
						t.Fatal(err)
					}
				}
				n.Message = "signed user content https://alice:SIGNED_CONTENT@text.example"
				if err := key.SignNotice(&n); err != nil {
					t.Fatal(err)
				}
				srv := noticeServer(t, []envelope.HubNotice{n})
				extra := ""
				if policy == "profile" {
					extra = "allow_insecure_http=true\n"
				}
				_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\n%s", srv.URL, extra))
				if policy == "environment" {
					prior := env
					env = func(k string) string {
						if k == insecureEnv {
							return "1"
						}
						return prior(k)
					}
				}
				code, out, errs := pinRun(env, "--json", "notice", "list")
				var got []envelope.HubNotice
				if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got) != 1 || !reflect.DeepEqual(got[0], n) || hubid.VerifyNotice(key.Pubkey(), got[0]) != nil {
					t.Fatalf("safe signed notice changed: %d %s %s", code, out, errs)
				}
				if policy != "strict" {
					_, strictEnv := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\n", srv.URL))
					code, out, errs = pinRun(strictEnv, "--json", "notice", "list")
					if code == 0 || out != "" || !strings.Contains(errs, "invalid hub notice") {
						t.Errorf("public HTTP admitted without opt-in: %d %s %s", code, out, errs)
					}
				}
			})
		}
	}
}

func TestReceivedWatchSSE(t *testing.T) {
	for _, field := range []string{"hub", "new_url"} {
		for _, tc := range receivedURLCases("http://127.0.0.1:1") {
			for _, mode := range []string{"human", "json", "monitor", "monitor-json"} {
				t.Run(field+"/"+tc.name+"/"+mode, func(t *testing.T) {
					n := receivedNoticeMap(t)
					n[field] = tc.value
					ev := receivedEvent(t, n, "posted")
					raw, _ := json.Marshal(ev)
					var source, destinations atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						source.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "id: NEW_CURSOR\nevent: hub_notice\ndata: %s\n\n", raw)
					}))
					defer srv.Close()
					a, out, errs, ch := receivedApp(t, srv.URL, strings.Contains(mode, "json"))
					c := newClient(srv.URL, "")
					wire := &http.Transport{}
					defer wire.CloseIdleConnections()
					c.hc.Transport = receivedRoundTrip(func(r *http.Request) (*http.Response, error) {
						if r.URL.Host != strings.TrimPrefix(srv.URL, "http://") {
							destinations.Add(1)
							return nil, fmt.Errorf("unexpected destination")
						}
						return wire.RoundTrip(r)
					})
					w := &watcher{a: a, c: c, ch: ch, seen: newDedupe(10), autoAck: true, forMonitor: strings.HasPrefix(mode, "monitor"), cursor: "HELD_CURSOR"}
					err := w.stream()
					assertReceivedRefusal(t, err, out.String()+errs.String(), tc.canary)
					if source.Load() != 1 || destinations.Load() != 0 || out.Len() != 0 || w.cursor != "HELD_CURSOR" || w.pendingAck != "" || w.move != nil || w.hadLive {
						t.Errorf("unsafe SSE effects: source=%d destination=%d out=%q cursor=%q ack=%q", source.Load(), destinations.Load(), out.String(), w.cursor, w.pendingAck)
					}
				})
			}
		}
	}
}

func TestReceivedEventHTTPPolicy(t *testing.T) {
	for _, field := range []string{"hub", "new_url"} {
		for _, policy := range []string{"strict", "profile", "environment"} {
			for _, mode := range []string{"inbox-json", "watch-json", "watch-human", "monitor-json"} {
				t.Run(field+"/"+policy+"/"+mode, func(t *testing.T) {
					key := testIdentityKey(t, "1")
					n := noticeWithSecrets(t, "https://source.example/prefix/", "https://[2001:db8::7]:8443/prefix/", nil)
					if field == "hub" {
						n.Hub = "http://public.example/Vecnet/%2Fexact/"
					} else {
						n.NewURL = "http://public.example/Vecnet/%2Fexact/"
					}
					if err := key.SignNotice(&n); err != nil {
						t.Fatal(err)
					}
					ev := receivedEvent(t, n, "posted")
					var acks atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/v1/events":
							json.NewEncoder(w).Encode(pollBody{Events: []envelope.Event{ev}, NextCursor: ev.Cursor})
						case "/v1/ack":
							acks.Add(1)
							fmt.Fprint(w, `{}`)
						default:
							http.NotFound(w, r)
						}
					}))
					defer srv.Close()
					extra := ""
					if policy == "profile" {
						extra = "allow_insecure_http=true\n"
					}
					_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\n%s", srv.URL, extra))
					if policy == "environment" {
						prior := env
						env = func(k string) string {
							if k == insecureEnv {
								return "1"
							}
							return prior(k)
						}
					}
					a, out, errs, ch := receivedApp(t, srv.URL, strings.HasSuffix(mode, "json"))
					a.getenv = env
					var err error
					if mode == "inbox-json" {
						err = a.cmdInbox([]string{"--ack"})
					} else {
						c, _, _, e := a.connect()
						if e != nil {
							t.Fatal(e)
						}
						w := &watcher{a: a, c: c, ch: ch, seen: newDedupe(10), forMonitor: strings.HasPrefix(mode, "monitor")}
						err = w.longPoll()
						if policy != "strict" && w.cursor != ev.Cursor {
							t.Error("safe page cursor lost")
						}
					}
					if policy == "strict" {
						if err == nil || out.Len() != 0 || acks.Load() != 0 {
							t.Errorf("public HTTP admitted without opt-in: %v %s %s", err, out.String(), errs.String())
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if a.json {
						var got envelope.Event
						if mode == "inbox-json" {
							var body pollBody
							if err := json.Unmarshal(out.Bytes(), &body); err != nil || len(body.Events) != 1 {
								t.Fatalf("safe inbox: %s", out.String())
							}
							got = body.Events[0]
						} else {
							if err := json.Unmarshal(out.Bytes(), &got); err != nil {
								t.Fatal(err)
							}
						}
						var payload envelope.HubNoticeEvent
						if json.Unmarshal(got.Data, &payload) != nil || !reflect.DeepEqual(payload.Notice, n) || hubid.VerifyNotice(key.Pubkey(), payload.Notice) != nil {
							t.Error("signed event changed")
						}
					} else if !strings.Contains(out.String(), n.NewURL) {
						t.Error("human notice URL changed")
					}
					if mode == "inbox-json" && acks.Load() != 1 {
						t.Error("safe inbox not acknowledged")
					}
				})
			}
		}
	}
}

// profile follow builds its own client after selecting a profile, rather than
// calling connect. Its explicit HTTP opt-in must reach notice admission too.
func TestReceivedNoticeSelectedProfilePolicy(t *testing.T) {
	key := testIdentityKey(t, "1")
	n := noticeWithSecrets(t, "https://source.example/prefix/", "http://public.example/Vecnet/%2Fexact/", &key)
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			var requests, destinations atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"notices": []any{n}})
			}))
			defer srv.Close()
			_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\nallow_insecure_http=%t\n", srv.URL, allow))
			a, out, errs, _ := receivedApp(t, srv.URL, true)
			a.getenv = env
			c := newClient(srv.URL, "vn_operator_synthetic")
			wire := &http.Transport{}
			defer wire.CloseIdleConnections()
			c.hc.Transport = receivedRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != strings.TrimPrefix(srv.URL, "http://") {
					destinations.Add(1)
					return nil, fmt.Errorf("unexpected destination")
				}
				return wire.RoundTrip(r)
			})
			notices, err := a.activeNotices(c)
			if allow {
				if err != nil || len(notices) != 1 || notices[0].NewURL != n.NewURL {
					t.Errorf("selected profile opt-in lost: %v", err)
				}
			} else if err == nil || notices != nil {
				t.Error("strict selected profile admitted public HTTP")
			}
			if requests.Load() != 1 || destinations.Load() != 0 || out.Len() != 0 || errs.Len() != 0 {
				t.Error("notice admission side effects")
			}
		})
	}
}
