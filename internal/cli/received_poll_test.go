package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubnotice"
)

const receivedPageSecret = "vn_peer_received"

type receivedPageStep struct {
	after  string
	status int
	raw    []byte
}

type receivedPageFixture struct {
	*receivedFixture
	mu                  sync.Mutex
	steps               []receivedPageStep
	polls, acks         []string
	servedBytes, writes int
}

// Reuse A's bounded HTTP routing/body/TempDir fixture, with an additional real
// loopback handler: hooks need different responses for head, replay and retry.
func newReceivedPageFixture(t *testing.T) *receivedPageFixture {
	t.Helper()
	p := &receivedPageFixture{receivedFixture: newReceivedFixture(t, receivedListHub)}
	p.peerProfile(false)
	srv := httptest.NewServer(http.HandlerFunc(p.servePage))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.serverURL = u
	return p
}

func (p *receivedPageFixture) peerProfile(allow bool) {
	p.t.Helper()
	profile := Profile{Hub: p.hub, HubPubkey: p.key.Pubkey(), Secret: receivedPageSecret, AllowInsecureHTTP: allow}
	if err := saveConfig(p.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{"p": profile}}); err != nil {
		p.t.Fatal(err)
	}
}

func (p *receivedPageFixture) servePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/hub/identity":
		p.receivedFixture.serve(w, r)
	case "/v1/peers/me":
		fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_fixture"}`)
	case "/v1/events":
		p.mu.Lock()
		after := r.URL.Query().Get("after")
		i := len(p.polls)
		p.polls = append(p.polls, after)
		step := receivedPageStep{after, 200, []byte(`{"events":[],"next_cursor":"e1.9"}`)}
		if i < len(p.steps) {
			step = p.steps[i]
		}
		// A baseline consumer may continue after accepting the rejected page.
		// Answer finitely; exact traffic assertions diagnose that acceptance effect.
		p.servedBytes += len(step.raw)
		p.mu.Unlock()
		if r.Method != "GET" || after != step.after || r.URL.Query().Get("wait") != "0" {
			p.t.Errorf("poll=%s %s; want GET after=%q wait=0", r.Method, r.URL, step.after)
		}
		w.WriteHeader(step.status)
		if _, err := w.Write(step.raw); err != nil {
			p.t.Error(err)
		}
	case "/v1/ack":
		var body struct{ Cursor string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			p.t.Error(err)
		}
		p.mu.Lock()
		p.acks = append(p.acks, body.Cursor)
		p.mu.Unlock()
		if r.Method != "POST" {
			p.t.Errorf("ack method=%s", r.Method)
		}
		fmt.Fprint(w, `{}`)
	default:
		p.mu.Lock()
		p.writes++ // Includes any receipt/proof or unexpected response-driven request.
		p.mu.Unlock()
		p.t.Errorf("unexpected HTTP effect: %s %s", r.Method, r.URL)
		w.WriteHeader(500)
	}
}

func receivedPage(t *testing.T, after string, events []envelope.Event, next string) receivedPageStep {
	t.Helper()
	return receivedPageStep{after, 200, receivedJSON(t, pollBody{Events: events, NextCursor: next, HubRestarting: &restartHint{RetryAfterMS: 37}})}
}

func (p *receivedPageFixture) traffic(t *testing.T, wantPolls, wantAcks []string) {
	t.Helper()
	p.mu.Lock()
	if !reflect.DeepEqual(p.polls, wantPolls) || !reflect.DeepEqual(p.acks, wantAcks) || len(p.polls) != len(p.steps) || p.writes != 0 {
		t.Errorf("polls/acks/writes=%v/%v/%d; want=%v/%v/0, steps=%d", p.polls, p.acks, p.writes, wantPolls, wantAcks, len(p.steps))
	}
	wantRead := p.servedBytes
	p.mu.Unlock()
	p.receivedFixture.mu.Lock()
	defer p.receivedFixture.mu.Unlock()
	read := 0
	for _, r := range p.requests {
		if r.closed != 1 || r.origin != p.hub {
			t.Errorf("HTTP body/destination effect: %+v", r)
		}
		if r.path == "/v1/events" {
			read += r.read
		}
	}
	if read != wantRead || p.blocked != 0 {
		t.Errorf("poll bytes=%d want=%d blocked=%d", read, wantRead, p.blocked)
	}
}

type receivedPageEvent struct {
	name   string
	event  envelope.Event
	policy string
}

func receivedPageNotice(t *testing.T, value any, change string) envelope.Event {
	t.Helper()
	return envelope.Event{Cursor: "e1.2", Type: "hub_notice", At: "2026-11-09T00:00:00.000Z", Data: receivedJSON(t, map[string]any{"change": change, "notice": value})}
}

func receivedBadPageEvents(t *testing.T) []receivedPageEvent {
	t.Helper()
	var cases []receivedPageEvent
	for _, bad := range receivedBadNotices(t) {
		change := "posted"
		if strings.HasPrefix(bad.name, "cleared/") {
			change = "cleared"
		}
		cases = append(cases, receivedPageEvent{bad.name, receivedPageNotice(t, bad.value, change), ""})
	}
	for _, data := range []receivedNoticeCase{{"missing-notice", map[string]any{"change": "posted"}}, {"null-data", nil}, {"string-data", receivedCanary}, {"number-data", 17}, {"bool-data", true}, {"array-data", []any{}}} {
		cases = append(cases, receivedPageEvent{data.name, envelope.Event{Cursor: "e1.2", Type: "hub_notice", Data: receivedJSON(t, data.value)}, ""})
	}
	for _, field := range []string{"hub", "new_url"} {
		n := receivedListNotice()
		if field == "hub" {
			n.Hub = "http://public.example.invalid/Prefix/%2F"
		} else {
			n.NewURL = "http://public.example.invalid/Prefix/%2F"
		}
		cases = append(cases, receivedPageEvent{"default-public-http/" + field, receivedPageNotice(t, n, "posted"), ""})
		for _, policy := range []string{"profile", "environment"} {
			bad := n
			if field == "hub" {
				bad.Hub = "https://hub.invalid/?" + receivedCanary
			} else {
				bad.NewURL = "https://hub.invalid/#" + receivedCanary
			}
			cases = append(cases, receivedPageEvent{"opt-in-refusal/" + field + "/" + policy, receivedPageNotice(t, bad, "posted"), policy})
		}
	}
	ev := receivedPageNotice(t, receivedListNotice(), "posted")
	ev.Data = receivedJSON(t, map[string]any{"change": 17, "notice": receivedListNotice()})
	cases = append(cases, receivedPageEvent{"wrong-change-type", ev, ""})
	return cases
}

func receivedPageDelivery(t *testing.T, n int) envelope.Event {
	t.Helper()
	ev := hookEvent(n, receivedCanary+" delivery")
	var data map[string]any
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		t.Fatal(err)
	}
	data["message"].(map[string]any)["priority"] = "wake_idle"
	ev.Data = receivedJSON(t, data)
	return ev
}

func receivedMixedPage(t *testing.T, bad envelope.Event, order string) []envelope.Event {
	t.Helper()
	if order == "notice-only" {
		return []envelope.Event{bad}
	}
	delivery := receivedPageDelivery(t, 1)
	if order == "bad-first" {
		return []envelope.Event{bad, delivery}
	}
	return []envelope.Event{delivery, bad}
}

func TestInboxNoticeBatchRefusal(t *testing.T) {
	for _, bad := range receivedBadPageEvents(t) {
		for _, order := range []string{"bad-first", "bad-last", "notice-only"} {
			for _, mode := range []string{"poll", "json", "plain", "limited-json", "limited-plain"} {
				t.Run(bad.name+"/"+order+"/"+mode, func(t *testing.T) {
					p := newReceivedPageFixture(t)
					receivedPagePolicy(p, bad.policy)
					receivedPageState(t, p, "e1.0", "e1.0", nil)
					p.steps = []receivedPageStep{receivedPage(t, "e1.0", receivedMixedPage(t, bad.event, order), "e1.9")}
					before := p.snapshot()
					if mode == "poll" {
						a := p.app()
						c, _, _, err := a.connect()
						if err != nil {
							t.Fatal(err)
						}
						got, err := a.poll(c, "e1.0", 0)
						if !errors.Is(err, hubnotice.ErrInvalidNotice) || !reflect.DeepEqual(got, pollBody{}) {
							t.Errorf("poll accepted page=%+v error=%v", got, err)
						}
						if a.stdout.(*bytes.Buffer).Len() != 0 || a.stderr.(*bytes.Buffer).Len() != 0 {
							t.Errorf("poll emitted refused data: %q/%q", a.stdout, a.stderr)
						}
					} else {
						args := []string{"inbox", "--since", "e1.0", "--ack"}
						if strings.HasSuffix(mode, "json") {
							args = append([]string{"--json"}, args...)
						}
						if strings.HasPrefix(mode, "limited") {
							args = append(args, "--limit", "1")
						}
						receivedNoticeRefusal(t, p.receivedFixture, args...)
					}
					p.unchanged(before)
					p.traffic(t, []string{"e1.0"}, nil)
				})
			}
		}
	}
	t.Run("preservation", receivedPagePreservation)
	t.Run("ordinary-and-output-controls", receivedInboxControls)
}

func receivedGoodPageEvents(t *testing.T) []receivedPageEvent {
	t.Helper()
	var cases []receivedPageEvent
	for _, field := range []string{"hub", "new_url"} {
		for i, u := range []string{"HTTPS://Hub.Example.Invalid:443/Prefix/%2f/", "https://[2001:DB8::1]:8443/Prefix/%2F", "http://127.0.0.1:8080/Prefix", "http://hub.lan:8080/Prefix", "https://hub.invalid/Prefix/%2F"} {
			n := receivedListNotice()
			n.Kind = "maintenance"
			if field == "hub" {
				n.Hub = u
			} else {
				n.NewURL = u
			}
			cases = append(cases, receivedPageEvent{fmt.Sprintf("%s/url%d", field, i), receivedPageNotice(t, n, "posted"), ""})
		}
		for _, policy := range []string{"profile", "environment"} {
			n := receivedListNotice()
			if field == "hub" {
				n.Hub = "http://public.example.invalid/Prefix/%2F"
			} else {
				n.NewURL = "http://public.example.invalid/Prefix/%2F"
			}
			cases = append(cases, receivedPageEvent{field + "/public/" + policy, receivedPageNotice(t, n, "posted"), policy})
		}
	}
	for _, variant := range []string{"absent-new-url", "empty-new-url", "cleared"} {
		n := receivedListNotice()
		n.Kind, n.NewURL = "maintenance", ""
		change := "posted"
		if variant == "cleared" {
			n.ClearedAt, n.ClearedHow, change = n.IssuedAt, "operator", "cleared"
		}
		ev := receivedPageNotice(t, n, change)
		if variant == "empty-new-url" {
			ev.Data = []byte(`{"change":"posted","notice":` + strings.TrimSuffix(string(receivedJSON(t, n)), "}") + `,"new_url":""}}`)
		}
		cases = append(cases, receivedPageEvent{variant, ev, ""})
	}
	ev := receivedPageNotice(t, map[string]any{"hub": "https://" + receivedCanary + "@hub.invalid"}, "posted")
	ev.Type = "unrelated"
	return append(cases, receivedPageEvent{"unrelated", ev, ""})
}

func receivedPagePolicy(p *receivedPageFixture, policy string) {
	if policy == "profile" {
		p.peerProfile(true)
	}
	if policy == "environment" {
		p.env[insecureEnv] = "1"
	}
}

func receivedPagePreservation(t *testing.T) {
	for _, good := range receivedGoodPageEvents(t) {
		for _, mode := range []string{"poll", "inbox"} {
			t.Run(good.name+"/"+mode, func(t *testing.T) {
				p := newReceivedPageFixture(t)
				receivedPagePolicy(p, good.policy)
				path := receivedPageState(t, p, "e1.0", "e1.0", nil)
				ev := good.event
				if mode == "poll" {
					// Construct wire bytes directly: marshaling a RawMessage would compact
					// away the internal whitespace this preservation assertion observes.
					ev.Data = []byte("{ \"wrapper\" : 0, " + strings.TrimSuffix(strings.TrimPrefix(string(ev.Data), "{"), "}") + " }")
					raw := fmt.Sprintf(`{"events":[{"cursor":%s,"type":%s,"at":%s,"data":%s}],"next_cursor":"e1.9","hub_restarting":{"retry_after_ms":37}}`, receivedJSON(t, ev.Cursor), receivedJSON(t, ev.Type), receivedJSON(t, ev.At), ev.Data)
					p.steps = []receivedPageStep{{"e1.0", 200, []byte(raw)}}
					a := p.app()
					c, _, _, err := a.connect()
					if err != nil {
						t.Fatal(err)
					}
					got, err := a.poll(c, "e1.0", 0)
					want := pollBody{Events: []envelope.Event{ev}, NextCursor: "e1.9", HubRestarting: &restartHint{RetryAfterMS: 37}}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("raw/typed page changed: got=%+v error=%v want=%+v", got, err, want)
					}
					p.traffic(t, []string{"e1.0"}, nil)
				} else {
					events := []envelope.Event{receivedPageDelivery(t, 1), ev}
					p.steps = []receivedPageStep{receivedPage(t, "e1.0", events, "e1.9")}
					code, out, errs := p.run("--json", "inbox", "--since", "e1.0", "--ack")
					want := string(receivedJSON(t, pollBody{Events: events, NextCursor: "e1.2"})) + "\n"
					if code != 0 || out != want || errs != "acked e1.2\n" {
						t.Errorf("accepted inbox=%d/%q/%q want=%q", code, out, errs, want)
					}
					s := readHookState(t, path)
					if !reflect.DeepEqual(s.Displayed, []string{"dly_kept", "dly_1"}) {
						t.Errorf("accepted page display proof=%v", s.Displayed)
					}
					p.traffic(t, []string{"e1.0"}, []string{"e1.2"})
				}
			})
		}
	}
}

func receivedInboxControls(t *testing.T) {
	t.Run("plain-inbox", func(t *testing.T) {
		p := newReceivedPageFixture(t)
		path := receivedPageState(t, p, "e1.0", "e1.0", nil)
		n := receivedListNotice()
		n.Kind, n.NewURL = "maintenance", ""
		p.steps = []receivedPageStep{receivedPage(t, "e1.0", []envelope.Event{receivedPageDelivery(t, 1), receivedPageNotice(t, n, "posted")}, "e1.9")}
		code, out, errs := p.run("inbox", "--since", "e1.0", "--ack")
		want := "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT\ne1.1 delivery thr_X peer_h msg_1 dly_1 " + receivedCanary + " delivery\ne1.2 hub_notice - hub - - [maintenance] prose https://u:p@x/?q#f <>& 界\n"
		if code != 0 || out != want || errs != "acked e1.2\n" || !reflect.DeepEqual(readHookState(t, path).Displayed, []string{"dly_kept", "dly_1"}) {
			t.Errorf("plain inbox preservation=%d/%q/%q want=%q", code, out, errs, want)
		}
		p.traffic(t, []string{"e1.0"}, []string{"e1.2"})
	})
	for _, raw := range []string{`{}`, `{"events":null}`, `{"events":[]}`} {
		t.Run("empty-poll/"+raw, func(t *testing.T) {
			p := newReceivedPageFixture(t)
			p.steps = []receivedPageStep{{"e1.0", 200, []byte(raw)}}
			a := p.app()
			c, _, _, err := a.connect()
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.poll(c, "e1.0", 0)
			want := pollBody{}
			if raw == `{"events":[]}` {
				want.Events = []envelope.Event{}
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("empty collection compatibility=%+v error=%v want=%+v", got, err, want)
			}
			p.traffic(t, []string{"e1.0"}, nil)
		})
	}
	for _, variant := range []string{"empty", "ordinary-http", "ordinary-json", "failed-output", "failed-commit"} {
		t.Run(variant, func(t *testing.T) {
			p := newReceivedPageFixture(t)
			path := receivedPageState(t, p, "e1.0", "e1.0", nil)
			before := readHookState(t, path)
			step := receivedPage(t, "e1.0", []envelope.Event{receivedPageDelivery(t, 1)}, "e1.9")
			if variant == "empty" {
				step = receivedPage(t, "e1.0", nil, "e1.9")
			}
			if variant == "ordinary-http" {
				step.status, step.raw = 503, []byte(`{"error":{"code":"unavailable","message":"ordinary outage"}}`)
			}
			if variant == "ordinary-json" {
				step.raw = []byte(`{"events":false}`)
			}
			p.steps = []receivedPageStep{step}
			a := p.app()
			a.json = true
			writes := 0
			if strings.HasPrefix(variant, "failed-") {
				a.stdout = inboxOutputFunc(func(raw []byte) (int, error) {
					writes++
					if variant == "failed-output" {
						return 0, errors.New("synthetic stdout failure")
					}
					receivedBlockStateCommit(t, path)
					return len(raw), nil
				})
			}
			err := a.cmdInbox([]string{"--since", "e1.0", "--ack"})
			if variant == "empty" {
				if err != nil || a.stdout.(*bytes.Buffer).String() != "{\"events\":[],\"next_cursor\":\"e1.9\"}\n" {
					t.Errorf("empty inbox error=%v output=%q", err, a.stdout)
				}
			} else if err == nil || errors.Is(err, hubnotice.ErrInvalidNotice) {
				t.Errorf("ordinary/output/commit error changed classification: %v", err)
			}
			if strings.HasPrefix(variant, "ordinary-") {
				var he *hubError
				if !errors.As(err, &he) || (variant == "ordinary-http" && (he.Status != 503 || he.Code != "unavailable" || he.Message != "ordinary outage")) || (variant == "ordinary-json" && (he.Status != 200 || he.Code != "bad_response" || !strings.HasPrefix(he.Message, "the hub answered with unexpected JSON:"))) {
					t.Errorf("ordinary error identity/prose changed: %v", err)
				}
			}
			if strings.HasPrefix(variant, "failed-") && writes != 1 {
				t.Errorf("output attempts=%d want=1", writes)
			}
			if variant == "failed-commit" {
				if err == nil || !strings.Contains(err.Error(), "display state commit failed") {
					t.Errorf("missing actual commit failure: %v", err)
				}
				path += ".saved"
			}
			if got := readHookState(t, path); !reflect.DeepEqual(got, before) {
				t.Errorf("failed/empty inbox changed proof/cursors: %+v", got)
			}
			p.traffic(t, []string{"e1.0"}, nil)
		})
	}
}
