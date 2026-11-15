package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui/theme"
	"github.com/charmbracelet/x/ansi"
)

func receivedTUISigned(t *testing.T) envelope.HubNotice {
	t.Helper()
	n := movingNotice()
	n.Hub = "https://source.example/Vecnet/%2Fexact/"
	n.NewURL = "https://[2001:db8::7]:8443/Vecnet/%2Fexact/"
	key, err := hubid.KeyFromSeedHex(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = key.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestReceivedTUIURLs(t *testing.T) {
	for _, field := range []string{"hub", "new_url"} {
		for _, raw := range []string{"https://alice:T2_USERSECRET@next.example", "https://next.example?token=T2_QUERYSECRET", "https://next.example#T2_FRAGMENT", "https://[T2_MALFORMED"} {
			t.Run(field+"/"+raw, func(t *testing.T) {
				n := receivedTUISigned(t)
				if field == "hub" {
					n.Hub = raw
				} else {
					n.NewURL = raw
				}
				f := seeded()
				f.setNotices(n)
				m, _ := loaded(t, f, newClock())
				if len(m.Data.Notices) != 0 || m.Data.Errs["notices"] == nil {
					t.Error("fake API notice admitted")
				}
				if frame := plainAt(t, m, 140, 42); strings.Contains(frame, "T2_") {
					t.Error("canary rendered")
				}
				// Concrete formatters also defend callers with directly supplied data.
				banner := ansi.Strip(viewNotices(theme.New(theme.TrueColor, true), []envelope.HubNotice{n}, fixedNow, time.UTC, 140))
				ev := event(t, "e1.bad", "hub_notice", envelope.HubNoticeEvent{Change: "cleared", Notice: n})
				pane := inboxEventText(ev, inboxPeers{})
				if strings.Contains(banner+pane, "T2_") || !strings.Contains(pane, "invalid hub notice") {
					t.Errorf("unchecked formatter: banner=%q pane=%q", banner, pane)
				}
				prior := receivedTUISigned(t)
				f.setNotices(prior)
				m.onLoad(m.fetch("notices")().(loadMsg))
				before := append([]envelope.HubNotice(nil), m.Data.Notices...)
				f.setNotices(n)
				m.onLoad(m.fetch("notices")().(loadMsg))
				if !reflect.DeepEqual(before, m.Data.Notices) {
					t.Error("invalid reload changed previous snapshot")
				}
				// A direct batch must reject its entire prefix before remember/reload/ack.
				ack := make(chan error, 1)
				beforeEvents := append([]envelope.Event(nil), m.Data.Events...)
				f.reset()
				good := event(t, "e1.good", "delivery", map[string]any{"delivery_id": "dly_1", "message": map[string]any{"thread_id": "thr_1", "message_id": "msg_1"}})
				m.onTailBatch(tailBatchMsg{batch: opclient.Batch{Events: []envelope.Event{good, ev}, Reset: true, Cursor: "NEW"}, ack: ack})
				select {
				case err := <-ack:
					if err == nil {
						t.Error("invalid batch acknowledged successfully")
					}
				default:
					t.Error("invalid batch not refused immediately")
				}
				if !reflect.DeepEqual(beforeEvents, m.Data.Events) || len(m.accepting) != 0 || len(m.waiters) != 0 || len(f.counts()) != 0 {
					t.Error("invalid batch caused partial state/load effects")
				}
				m.remember(ev)
				if !reflect.DeepEqual(beforeEvents, m.Data.Events) {
					t.Error("direct remember admitted notice")
				}
			})
		}
	}
	for _, raw := range []string{"https://[T2_HOST_PARSE", "https://alice:T2_HOST_USER@next.example", "https://next.example?token=T2_HOST_QUERY", "https://next.example#T2_HOST_FRAGMENT"} {
		if got := hubHost(raw); got != "(unusable)" {
			t.Errorf("unsafe hubHost fallback: %q", got)
		}
	}
}

func TestReceivedTUIFakeTailBeforeCallback(t *testing.T) {
	f := seeded()
	m, p, handle := tailHandle(t, f)
	f.reset()
	n := receivedTUISigned(t)
	n.NewURL = "https://[T2_TAIL"
	ev := event(t, "e1.bad", "hub_notice", envelope.HubNoticeEvent{Change: "posted", Notice: n})
	if err := deliver(p, handle, opclient.Batch{Events: []envelope.Event{ev}, Cursor: "NEW"}); err == nil {
		t.Error("fake tail accepted invalid notice")
	}
	if len(m.Data.Events) != 0 || len(m.Data.Notices) != 0 || len(f.counts()) != 0 {
		t.Error("fake tail reached model/load effects")
	}
}

func TestReceivedTUIHTTPResponses(t *testing.T) {
	var destinations atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinations.Add(1) }))
	defer dst.Close()
	for _, mode := range []string{"redirect", "moved", "null-hub", "null-new_url", "type-hub", "type-new_url"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					w.Header().Set("Location", "https://[T2_REDIRECT")
					w.WriteHeader(302)
					return
				}
				if mode == "moved" {
					w.WriteHeader(410)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": dst.URL + "?token=T2_DIFFERENT_MESSAGE", "details": map[string]any{"new_url": "https://next.example/vecnet/"}}})
					return
				}
				raw, _ := json.Marshal(receivedTUISigned(t))
				var n map[string]any
				json.Unmarshal(raw, &n)
				field := strings.TrimPrefix(strings.TrimPrefix(mode, "null-"), "type-")
				if strings.HasPrefix(mode, "null") {
					n[field] = nil
				} else {
					n[field] = 42
				}
				json.NewEncoder(w).Encode(map[string]any{"notices": []any{n}})
			}))
			defer srv.Close()
			m := newModel(context.Background(), Options{API: opclient.New(srv.URL, "vn_operator_synthetic"), Hub: srv.URL, Now: func() time.Time { return fixedNow }})
			defer m.cancel()
			m.Conn = ConnLive
			msg := m.fetch("notices")().(loadMsg)
			m.onLoad(msg)
			if msg.err == nil || len(m.Data.Notices) != 0 || strings.Contains(m.toast.text+fmt.Sprint(m.Data.Errs), "T2_") {
				t.Errorf("unsafe response error=%v toast=%q", msg.err, m.toast.text)
			}
			if mode == "moved" {
				var e *opclient.Error
				if !errors.As(msg.err, &e) || e.Status != 410 || e.Code != "hub_moved" || e.Message != "hub moved" || e.Details["new_url"] != "https://next.example/vecnet/" {
					t.Error("lost moved classification/recovery")
				}
			}
			if destinations.Load() != 0 {
				t.Error("destination requested")
			}
		})
	}
}

func TestReceivedTUISafeSignedValues(t *testing.T) {
	n := receivedTUISigned(t)
	n.Message = "safe signed user prose https://alice:SIGNED_CONTENT@text.example"
	key, _ := hubid.KeyFromSeedHex(strings.Repeat("1", 64))
	if err := key.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	f := seeded()
	f.setNotices(n)
	m, _ := loaded(t, f, newClock())
	if len(m.Data.Notices) != 1 || !reflect.DeepEqual(m.Data.Notices[0], n) || hubid.VerifyNotice(key.Pubkey(), m.Data.Notices[0]) != nil {
		t.Fatal("signed fields changed")
	}
	if got := hubHost(n.NewURL); got != "[2001:db8::7]:8443" {
		t.Errorf("IPv6 lost: %s", got)
	}
	ev := event(t, "e1.safe", "hub_notice", envelope.HubNoticeEvent{Change: "posted", Notice: n})
	m.remember(ev)
	if len(m.Data.Events) != 1 || !reflect.DeepEqual(m.Data.Events[0], ev) {
		t.Error("safe event bytes changed")
	}
}

func TestReceivedTUIFakeRelocationError(t *testing.T) {
	for _, target := range []string{"https://next.example/prefix/", "https://[T2_FAKE_TARGET"} {
		t.Run(target, func(t *testing.T) {
			f := seeded()
			original := &opclient.Error{Status: 410, Code: "hub_moved", Message: "https://message.example?token=T2_FAKE_MESSAGE", Details: map[string]any{"new_url": target, "extra": "T2_EXTRA"}}
			f.fail("Notices", original)
			m, _ := loaded(t, f, newClock())
			m.Conn = ConnLive
			m.onLoad(m.fetch("notices")().(loadMsg))
			err := m.Data.Errs["notices"]
			var typed *opclient.Error
			if err == nil || strings.Contains(err.Error()+m.toast.text, "T2_") {
				t.Errorf("fake relocation prose leaked: %v %q", err, m.toast.text)
			}
			if !errors.Is(err, original) || !errors.As(err, &typed) || typed.Status != 410 || typed.Code != "hub_moved" {
				t.Error("lost original typed cause")
			}
			if original.Message != "https://message.example?token=T2_FAKE_MESSAGE" || len(original.Details) != 2 {
				t.Error("mutated caller error")
			}
		})
	}
}

type receivedLoadCapture struct {
	staticScreen
	err error
}

func (s *receivedLoadCapture) Update(msg tea.Msg, _ *Model) tea.Cmd {
	if loaded, ok := msg.(Loaded); ok {
		s.err = loaded.Err
	}
	return nil
}

func TestReceivedTUIRelocationDiagnosticPreserved(t *testing.T) {
	for _, source := range []string{"http", "fake"} {
		for _, mode := range []string{"invalid-only", "invalid-sibling-signed", "invalid-notice-safe-target", "safe", "already-rejected"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				var destinations, requests atomic.Int32
				dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinations.Add(1) }))
				defer dst.Close()
				n := receivedTUISigned(t)
				safeTarget := "https://[2001:db8::7]:8443/Vecnet/%2Fexact/"
				details := map[string]any{"unknown": "T2R_UNKNOWN"}
				message := "https://message.example?token=T2R_MESSAGE"
				wantMessage := "invalid hub relocation"
				switch mode {
				case "invalid-only":
					details["new_url"] = dst.URL + "?token=T2R_INVALID"
				case "invalid-sibling-signed":
					details["new_url"] = dst.URL + "?token=T2R_INVALID"
					details["notice"] = n
				case "invalid-notice-safe-target":
					bad := n
					bad.NewURL = dst.URL + "?token=T2R_INVALID_NOTICE"
					details["new_url"] = safeTarget
					details["notice"] = bad
				case "safe":
					details["new_url"] = safeTarget
					details["notice"] = n
					wantMessage = "hub moved"
				case "already-rejected":
					message = "invalid hub relocation"
					details["new_url"] = safeTarget
					if source == "http" {
						wantMessage = "hub moved"
					}
				}
				beforeRaw, _ := json.Marshal(details)
				var api API
				if source == "http" {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						w.WriteHeader(410)
						json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": message, "details": details}})
					}))
					defer srv.Close()
					api = opclient.New(srv.URL, "vn_operator_synthetic")
				} else {
					f := newFake()
					f.fail("Notices", &opclient.Error{Status: 410, Code: "hub_moved", Message: message, Details: details})
					api = f
				}
				m := newModel(context.Background(), Options{API: api, Now: func() time.Time { return fixedNow }})
				defer m.cancel()
				m.Conn = ConnLive
				capture := &receivedLoadCapture{}
				m.screens = []Screen{capture}
				load := m.fetch("notices")().(loadMsg)
				before := load.err
				var original *opclient.Error
				if !errors.As(before, &original) {
					t.Fatal("missing typed API error")
				}
				originalMessage := original.Message
				originalDetails, _ := json.Marshal(original.Details)
				m.onLoad(load)
				err := m.Data.Errs["notices"]
				var checked *opclient.Error
				if !errors.As(err, &checked) || checked.Status != 410 || checked.Code != "hub_moved" {
					t.Fatalf("lost typed error: %v", err)
				}
				if checked.Message != wantMessage || !strings.Contains(m.toast.text, ": "+wantMessage) || capture.err == nil || capture.err.Error() != err.Error() {
					t.Errorf("lost diagnostic: before=%v after=%v toast=%q broadcast=%v", before, err, m.toast.text, capture.err)
				}
				if !errors.Is(err, before) {
					t.Error("original typed cause lost")
				}
				wantFields := 0
				switch mode {
				case "invalid-sibling-signed", "invalid-notice-safe-target", "already-rejected":
					wantFields = 1
				case "safe":
					wantFields = 2
				}
				if len(checked.Details) != wantFields {
					t.Error("unsafe recovery retained or checked sibling lost")
				}
				if mode == "invalid-sibling-signed" || mode == "safe" {
					got, ok := checked.Details["notice"].(envelope.HubNotice)
					key, _ := hubid.KeyFromSeedHex(strings.Repeat("1", 64))
					if !ok || !reflect.DeepEqual(got, n) || hubid.VerifyNotice(key.Pubkey(), got) != nil {
						t.Error("signed sibling changed")
					}
				}
				if mode == "invalid-notice-safe-target" || mode == "safe" || mode == "already-rejected" {
					if checked.Details["new_url"] != safeTarget {
						t.Error("checked prefix/IPv6 target changed")
					}
				}
				afterRaw, _ := json.Marshal(details)
				afterOriginal, _ := json.Marshal(original.Details)
				if string(beforeRaw) != string(afterRaw) || original.Message != originalMessage || string(originalDetails) != string(afterOriginal) {
					t.Error("caller input mutated")
				}
				if destinations.Load() != 0 || (source == "http" && requests.Load() != 1) || strings.Contains(err.Error()+m.toast.text+capture.err.Error(), "T2R_") {
					t.Error("unchecked prose or request escaped")
				}
			})
		}
	}
}
