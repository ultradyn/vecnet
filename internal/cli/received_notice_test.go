package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubnotice"
)

const receivedListHub = "https://source.example.invalid"

func receivedListNotice() envelope.HubNotice {
	return envelope.HubNotice{
		NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: receivedListHub,
		NewURL: receivedDestination, Message: "prose https://u:p@x/?q#f <>& 界",
		IssuedAt: "2026-11-09T00:00:00.000Z", ExpiresAt: "2026-11-10T00:00:00.000Z",
		Sig: "Opaque+/=SIG:" + receivedCanary,
	}
}

type receivedNoticeCase struct {
	name  string
	value any
}

// Each URL is crossed independently with Hub and present NewURL, including
// cleared and nonmoving records. No signature/lifecycle rule is the URL oracle.
func receivedBadNotices(t *testing.T) []receivedNoticeCase {
	t.Helper()
	urls := []struct{ name, raw string }{
		{"username", "https://" + receivedCanary + "@hub.invalid"},
		{"password", "https://user:" + receivedCanary + "@hub.invalid"},
		{"query", "https://hub.invalid/?q=" + receivedCanary},
		{"bare-query", "https://hub.invalid/?"},
		{"fragment", "https://hub.invalid/#" + receivedCanary},
		{"bare-fragment", "https://hub.invalid/#"},
		{"authority", "https:///" + receivedCanary},
		{"port", "https://hub.invalid:" + receivedCanary},
		{"ipv6", "https://[2001:db8::" + receivedCanary},
		{"escape", "https://hub.invalid/%" + receivedCanary},
		{"scheme", "ftp://hub.invalid/" + receivedCanary},
	}
	var cases []receivedNoticeCase
	for _, context := range []string{"posted", "cleared", "nonmoving"} {
		base := receivedListNotice()
		if context == "cleared" {
			base.ClearedAt, base.ClearedHow = base.IssuedAt, "operator"
		}
		if context == "nonmoving" {
			base.Kind = "maintenance"
		}
		add := func(name, field string, value any, omit bool) {
			var object map[string]any
			if err := json.Unmarshal(receivedJSON(t, base), &object); err != nil {
				t.Fatal(err)
			}
			if omit {
				delete(object, field)
			} else {
				object[field] = value
			}
			cases = append(cases, receivedNoticeCase{context + "/" + name, object})
		}
		for _, field := range []string{"hub", "new_url"} {
			for _, u := range urls {
				add(field+"/"+u.name, field, u.raw, false)
			}
			for _, shape := range []receivedNoticeCase{{"null", nil}, {"number", 17}, {"bool", false}, {"array", []any{}}, {"object", map[string]any{}}} {
				add(field+"/"+shape.name, field, shape.value, false)
			}
		}
		add("hub/empty", "hub", "", false)
		add("hub/missing", "hub", nil, true)
		add("message/type", "message", 17, false)
		add("sig/type", "sig", map[string]any{}, false)
		add("kind/type", "kind", false, false)
	}
	for _, shape := range []receivedNoticeCase{{"null", nil}, {"string", receivedCanary}, {"number", 17}, {"bool", true}, {"array", []any{}}} {
		cases = append(cases, receivedNoticeCase{"notice/" + shape.name, shape.value})
	}
	return cases
}

func receivedNoticeReply(t *testing.T, f *receivedFixture, body any) {
	t.Helper()
	raw := receivedJSON(t, body)
	f.mu.Lock()
	f.status, f.body = 200, raw
	f.mu.Unlock()
}

func receivedNoticeTraffic(t *testing.T, f *receivedFixture, want ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var got []string
	for _, r := range f.requests {
		if r.origin != f.hub {
			t.Errorf("unexpected destination request: %s %s", r.origin, r.path)
			continue
		}
		if r.path == "/v1/hub/identity" {
			continue // Existing identity bookkeeping precedes receipt.
		}
		got = append(got, r.method+" "+r.path)
		if r.closed != 1 || r.read != len(f.body) {
			t.Errorf("response read/close=%d/%d want=%d/1", r.read, r.closed, len(f.body))
		}
	}
	if !reflect.DeepEqual(got, want) || f.blocked != 0 {
		t.Errorf("HTTP effects=%v blocked=%d; want=%v", got, f.blocked, want)
	}
}

func receivedNoticeRefusal(t *testing.T, f *receivedFixture, args ...string) {
	t.Helper()
	code, out, errs := f.run(args...)
	if code != exitHub || out != "" || errs != "bad_response: invalid hub notice\n" {
		t.Errorf("refusal exit/output/error=%d/%q/%q", code, out, errs)
	}
}

func receivedNoticeArgs(mode string) []string {
	args := []string{"notice", "list"}
	if strings.HasPrefix(mode, "post") {
		args = []string{"notice", "post", "--kind", "maintenance", "--message", "authorized request"}
	}
	if strings.HasSuffix(mode, "json") {
		args = append([]string{"--json"}, args...)
	}
	return args
}

func TestNoticeResponseBoundaries(t *testing.T) {
	for _, bad := range receivedBadNotices(t) {
		for _, mode := range []string{"active", "list", "list-json", "post", "post-json", "list-profile-json", "post-environment-json"} {
			t.Run("invalid/"+bad.name+"/"+mode, func(t *testing.T) {
				f := newReceivedFixture(t, receivedListHub)
				if mode == "list-profile-json" {
					f.profile(true)
				}
				if mode == "post-environment-json" {
					f.env[insecureEnv] = "1"
				}
				body := map[string]any{"notices": []any{bad.value}}
				request := "GET /v1/hub/notices"
				if strings.HasPrefix(mode, "post") {
					body = map[string]any{"notice": bad.value}
					request = "POST /v1/admin/notices" // Authorized request, not a forbidden effect.
				}
				receivedNoticeReply(t, f, body)
				a := f.app()
				c, _, _, err := a.connect()
				if err != nil {
					t.Fatal(err)
				}
				before := f.snapshot()
				if mode == "active" {
					got, err := a.activeNotices(c)
					if !errors.Is(err, hubnotice.ErrInvalidNotice) || len(got) != 0 {
						t.Errorf("activeNotices returned accepted data=%+v error=%v", got, err)
					}
				} else {
					receivedNoticeRefusal(t, f, receivedNoticeArgs(mode)...)
				}
				f.unchanged(before)
				receivedNoticeTraffic(t, f, request)
			})
		}
	}
	for _, mode := range []string{"post", "post-json"} {
		t.Run("missing-notice/"+mode, func(t *testing.T) {
			f := newReceivedFixture(t, receivedListHub)
			receivedNoticeReply(t, f, map[string]any{})
			before := f.snapshot()
			receivedNoticeRefusal(t, f, receivedNoticeArgs(mode)...)
			f.unchanged(before)
			receivedNoticeTraffic(t, f, "POST /v1/admin/notices")
		})
	}
	for _, reverse := range []bool{false, true} {
		for _, mode := range []string{"active", "list", "list-json", "clear", "follow"} {
			t.Run(fmt.Sprintf("whole-list/reverse=%v/%s", reverse, mode), func(t *testing.T) {
				f := newReceivedFixture(t, receivedListHub)
				good := f.notice() // A genuine signed, active move would otherwise follow.
				bad := receivedListNotice()
				bad.Kind, bad.Hub = "maintenance", "https://"+receivedCanary+"@hub.invalid"
				items := []envelope.HubNotice{good, bad}
				if reverse {
					items[0], items[1] = items[1], items[0]
				}
				receivedNoticeReply(t, f, map[string]any{"notices": items})
				args := receivedNoticeArgs(mode)
				if mode == "clear" {
					args = []string{"notice", "clear", "--kind", "moving"}
				}
				if mode == "follow" {
					args = []string{"profile", "follow"}
				}
				before := f.snapshot()
				if mode == "active" {
					a := f.app()
					c, _, _, err := a.connect()
					if err != nil {
						t.Fatal(err)
					}
					got, err := a.activeNotices(c)
					if !errors.Is(err, hubnotice.ErrInvalidNotice) || len(got) != 0 {
						t.Errorf("partial batch accepted: data=%+v error=%v", got, err)
					}
				} else {
					receivedNoticeRefusal(t, f, args...)
				}
				f.unchanged(before)
				receivedNoticeTraffic(t, f, "GET /v1/hub/notices")
			})
		}
	}
	t.Run("preservation", receivedNoticePreservation)
	t.Run("controls", receivedNoticeControls)
}

func receivedNoticePreservation(t *testing.T) {
	urls := []struct{ raw, shown string }{
		{"HTTPS://Hub.Example.Invalid:443/Prefix/%2f/", "https://hub.example.invalid/Prefix/%2f"},
		{"https://[2001:DB8::1]:8443/Prefix/%2F", "https://[2001:db8::1]:8443/Prefix/%2F"},
		{"http://hub.lan:8080/Prefix", "http://hub.lan:8080/Prefix"},
		{"http://127.0.0.1:8080/Prefix", "http://127.0.0.1:8080/Prefix"},
	}
	for i, u := range urls {
		for _, field := range []string{"hub", "new_url", "absent-new-url", "empty-new-url", "cleared"} {
			for _, mode := range []string{"active", "list", "list-json", "post", "post-json"} {
				t.Run(fmt.Sprintf("url%d/%s/%s", i, field, mode), func(t *testing.T) {
					f := newReceivedFixture(t, receivedListHub)
					n := receivedListNotice()
					n.Kind = "maintenance"
					shown := receivedDestination
					switch field {
					case "hub":
						n.Hub = u.raw
					case "new_url":
						n.NewURL, shown = u.raw, u.shown
					case "cleared":
						n.ClearedAt, n.ClearedHow = n.IssuedAt, "operator"
					default:
						n.NewURL, shown = "", ""
					}
					var object map[string]any
					if err := json.Unmarshal(receivedJSON(t, n), &object); err != nil {
						t.Fatal(err)
					}
					if field == "empty-new-url" {
						object["new_url"] = ""
					}
					body := map[string]any{"notices": []any{object}}
					request := "GET /v1/hub/notices"
					if strings.HasPrefix(mode, "post") {
						body, request = map[string]any{"notice": object}, "POST /v1/admin/notices"
					}
					receivedNoticeReply(t, f, body)
					before := f.snapshot()
					if mode == "active" {
						a := f.app()
						c, _, _, err := a.connect()
						if err != nil {
							t.Fatal(err)
						}
						got, err := a.activeNotices(c)
						if err != nil || !reflect.DeepEqual(got, []envelope.HubNotice{n}) {
							t.Errorf("typed preservation=%+v error=%v want=%+v", got, err, n)
						}
					} else {
						code, out, errs := f.run(receivedNoticeArgs(mode)...)
						label := "maintenance"
						if shown != "" {
							label += " -> " + shown
						}
						want := n.NoticeID + " [" + label + "] " + n.Message + "\n"
						if strings.HasSuffix(mode, "json") {
							value := any([]envelope.HubNotice{n})
							if strings.HasPrefix(mode, "post") {
								value = n
							}
							want = string(receivedJSON(t, value)) + "\n"
							// Both sides are the same typed representation, never map JSON order.
						}
						if code != 0 || errs != "" || out != want {
							t.Errorf("preservation exit=%d out=%q stderr=%q want=%q", code, out, errs, want)
						}
					}
					f.unchanged(before)
					receivedNoticeTraffic(t, f, request)
				})
			}
		}
	}
}

func receivedNoticeControls(t *testing.T) {
	for _, body := range []map[string]any{{}, {"notices": nil}, {"notices": []any{}}} {
		t.Run("empty-collection/"+string(receivedJSON(t, body)), func(t *testing.T) {
			f := newReceivedFixture(t, receivedListHub)
			receivedNoticeReply(t, f, body)
			code, out, errs := f.run("--json", "notice", "list")
			if code != 0 || out != "[]\n" || errs != "" {
				t.Errorf("empty list=%d/%q/%q", code, out, errs)
			}
			receivedNoticeTraffic(t, f, "GET /v1/hub/notices")
		})
	}
	for _, field := range []string{"hub", "new_url"} {
		for _, policy := range []string{"default", "profile", "environment"} {
			for _, mode := range []string{"list-json", "post-json"} {
				t.Run("public-http/"+field+"/"+policy+"/"+mode, func(t *testing.T) {
					f := newReceivedFixture(t, receivedListHub)
					f.profile(policy == "profile")
					if policy == "environment" {
						f.env[insecureEnv] = "1"
					}
					n := receivedListNotice()
					if field == "hub" {
						n.Hub = "http://public.example.invalid/Prefix/%2F"
					} else {
						n.NewURL = "http://public.example.invalid/Prefix/%2F"
					}
					body := map[string]any{"notices": []envelope.HubNotice{n}}
					request := "GET /v1/hub/notices"
					want := string(receivedJSON(t, []envelope.HubNotice{n})) + "\n"
					if mode == "post-json" {
						body, request = map[string]any{"notice": n}, "POST /v1/admin/notices"
						want = string(receivedJSON(t, n)) + "\n"
					}
					receivedNoticeReply(t, f, body)
					before := f.snapshot()
					if policy == "default" {
						receivedNoticeRefusal(t, f, receivedNoticeArgs(mode)...)
					} else if code, out, errs := f.run(receivedNoticeArgs(mode)...); code != 0 || out != want || errs != "" {
						t.Errorf("opted-in list=%d/%q/%q", code, out, errs)
					}
					f.unchanged(before)
					receivedNoticeTraffic(t, f, request)
				})
			}
		}
	}
	for _, filtered := range []bool{false, true} {
		t.Run(fmt.Sprintf("authorized-clear/filtered=%v", filtered), func(t *testing.T) {
			f := newReceivedFixture(t, receivedListHub)
			n := receivedListNotice()
			n.Kind = "maintenance"
			receivedNoticeReply(t, f, map[string]any{"notices": []envelope.HubNotice{n}})
			args := []string{"notice", "clear", n.NoticeID}
			want := []string{"POST /v1/admin/notices/" + n.NoticeID + "/clear"}
			if filtered {
				args = []string{"notice", "clear", "--kind", "maintenance"}
				want = append([]string{"GET /v1/hub/notices"}, want...)
			}
			before := f.snapshot()
			if code, out, errs := f.run(args...); code != 0 || out != "notice cleared\n" || errs != "" {
				t.Errorf("authorized clear=%d/%q/%q", code, out, errs)
			}
			f.unchanged(before)
			receivedNoticeTraffic(t, f, want...)
		})
	}
	t.Run("ordinary-outage", func(t *testing.T) {
		f := newReceivedFixture(t, receivedListHub)
		f.reply(503, "unavailable", "ordinary outage", nil)
		before := f.snapshot()
		code, out, errs := f.run("notice", "list")
		if code != exitHub || out != "" || errs != "unavailable: ordinary outage\n" {
			t.Errorf("ordinary outage=%d/%q/%q", code, out, errs)
		}
		f.unchanged(before)
		receivedNoticeTraffic(t, f, "GET /v1/hub/notices")
	})
	t.Run("latest-signed-follow", func(t *testing.T) {
		f := newReceivedFixture(t, receivedListHub)
		latest := f.notice()
		older := latest
		older.NoticeID = "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAW"
		older.NewURL = "https://older.example.invalid"
		older.IssuedAt = envelope.FormatTime(time.Now().Add(-2 * time.Minute))
		if err := f.key.SignNotice(&older); err != nil {
			t.Fatal(err)
		}
		receivedNoticeReply(t, f, map[string]any{"notices": []envelope.HubNotice{older, latest}})
		code, out, errs := f.run("profile", "follow")
		cfg, err := loadConfig(f.env["VECNET_CONFIG"])
		if err != nil || code != 0 || out != "" || !strings.Contains(errs, "now uses "+receivedDestination) || cfg.Profiles["p"].Hub != receivedDestination {
			t.Fatalf("follow=%d/%q/%q config=%+v error=%v", code, out, errs, cfg, err)
		}
		pp, err := pinsPath(func(k string) string { return f.env[k] })
		if err != nil {
			t.Fatal(err)
		}
		records, err := readPins(pp)
		move := records["p"].LastMove
		if err != nil || move == nil || move.NoticeID != latest.NoticeID || move.To != receivedDestination || move.IssuedAt != latest.IssuedAt {
			t.Errorf("latest move record=%+v error=%v", move, err)
		}
		source, destination, closed, read := f.counts()
		if source != 1 || destination != 1 || closed != 1 || read != len(f.body) {
			t.Errorf("follow traffic source/destination/closed/read=%d/%d/%d/%d", source, destination, closed, read)
		}
		if bytes.Contains([]byte(errs), []byte(older.NewURL)) {
			t.Error("follow used the older destination")
		}
	})
}
