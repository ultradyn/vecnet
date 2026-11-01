package push_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

const (
	grokKey     = "crsr_00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	routineURL  = "https://api2.cursor.sh/automations/webhook/123e4567-e89b-12d3-a456-426614174000"
	rateLimited = `{"success":false,"error":"Automation run rate limit exceeded: try later"}`
	disabledMsg = `{"success":false,"error":"Automation 123e4567 is disabled"}`
	invalidKey  = `{"code":"error","message":"Invalid API key"}`
)

// started is the routine's answer when a run began (grokbot.md §2).
func started(int, hit) reply {
	return reply{Status: http.StatusOK, Body: `{"success":true,"runUuid":"x"}`}
}

func answer(status int, body string) func(int, hit) reply {
	return func(int, hit) reply { return reply{Status: status, Body: body} }
}

// addGrokbot gives B a grokbot route to a fresh receiver; the routine URL
// pattern is relaxed to loopback for the length of the test.
func (f *fixture) addGrokbot(respond func(int, hit) reply) {
	f.t.Helper()
	old := hub.GrokbotURLPattern
	hub.GrokbotURLPattern = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+$`)
	f.t.Cleanup(func() { hub.GrokbotURLPattern = old })
	f.rcv = newReceiver(f.t, respond)
	var err error
	f.route, err = f.env.Hub.AddGrokbotRoute(f.ctx, "peer", f.b, f.rcv.srv.URL, grokKey)
	if err != nil {
		f.t.Fatalf("AddGrokbotRoute: %v", err)
	}
}

func (f *fixture) setRunsPerHour(n int) {
	f.t.Helper()
	if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET runs_per_hour = ? WHERE route_id = ?`, n, f.route); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) routeRuns() int {
	f.t.Helper()
	var n int
	if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_runs WHERE route_id = ?`, f.route).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// wantDeadLetter asserts the operator's dead-letter list shows dly.
func (f *fixture) wantDeadLetter(dly string) {
	f.t.Helper()
	resp, raw := f.env.Do(f.env.OperatorSecret, "GET", "/v1/admin/dead-letters", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(dly)) {
		f.t.Fatalf("GET /v1/admin/dead-letters = %d, want %s listed; %s", resp.StatusCode, dly, raw)
	}
}

func (f *fixture) retryDeadLetter(dly string) {
	f.t.Helper()
	resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/dead-letters/"+dly+"/retry", nil)
	if resp.StatusCode != http.StatusNoContent {
		f.t.Fatalf("retry dead letter = %d; %s", resp.StatusCode, raw)
	}
}

// seedRuns records n routine runs on a route that no delivery uses, the
// oldest at the given age and the rest newer.
func (f *fixture) seedRuns(n int, oldest time.Duration) {
	f.t.Helper()
	now := f.env.Now.Now()
	for i := range n {
		at := envelope.FormatTime(now.Add(-oldest + time.Duration(i)*time.Second))
		if _, err := f.env.Hub.DB.Exec(`INSERT INTO route_runs(route_id, at) VALUES ('route_seeded', ?)`, at); err != nil {
			f.t.Fatal(err)
		}
	}
}

// wantCode asserts an error envelope.
func wantCode(t *testing.T, resp *http.Response, raw []byte, status int, code string) {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode error envelope: %v; %s", err, raw)
	}
	if resp.StatusCode != status || e.Error.Code != code {
		t.Fatalf("got %d %q, want %d %s; %s", resp.StatusCode, e.Error.Code, status, code, raw)
	}
}

func TestGrokbotRouteValidation(t *testing.T) {
	f := setup(t, nil)
	env := f.env
	add := func(body map[string]any) (*http.Response, []byte) {
		body["owner"] = f.b
		return env.Do(env.OperatorSecret, "POST", "/v1/admin/routes", body)
	}

	for name, u := range map[string]string{
		"other host":     "https://evil.example/automations/webhook/123e4567-e89b-12d3-a456-426614174000",
		"http":           strings.Replace(routineURL, "https:", "http:", 1),
		"short id":       "https://api2.cursor.sh/automations/webhook/123e4567",
		"trailing path":  routineURL + "/x",
		"trailing query": routineURL + "?a=1",
		"uppercase id":   "https://api2.cursor.sh/automations/webhook/123E4567-E89B-12D3-A456-426614174000",
	} {
		t.Run("bad url "+name, func(t *testing.T) {
			resp, raw := add(map[string]any{"kind": "grokbot", "url": u, "key": grokKey})
			wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_url")
			if bytes.Contains(raw, []byte(grokKey)) {
				t.Fatalf("the error echoes the key: %s", raw)
			}
		})
	}
	for name, k := range map[string]string{
		"wrong prefix": "sk_00112233445566778899aabbccddeeff",
		"prefix only":  "crsr_",
		"empty":        "",
		"header split": "crsr_abc\r\nX-Evil: 1",
		"space":        "crsr_abc def",
	} {
		t.Run("bad key "+name, func(t *testing.T) {
			resp, raw := add(map[string]any{"kind": "grokbot", "url": routineURL, "key": k})
			wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_key")
			if len(k) > len("crsr_") && bytes.Contains(raw, []byte(k)) {
				t.Fatalf("the error echoes the key: %s", raw)
			}
		})
	}
	t.Run("missing key", func(t *testing.T) {
		resp, raw := add(map[string]any{"kind": "grokbot", "url": routineURL})
		wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_key")
	})
	t.Run("http route refuses a key", func(t *testing.T) {
		resp, raw := add(map[string]any{"url": "http://127.0.0.1:9/x", "key": grokKey})
		wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	})
	t.Run("unknown kind", func(t *testing.T) {
		resp, raw := add(map[string]any{"kind": "smtp", "url": routineURL})
		wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	})
	t.Run("unknown owner", func(t *testing.T) {
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/routes",
			map[string]any{"owner": "peer_nobody", "kind": "grokbot", "url": routineURL, "key": grokKey})
		wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	})

	resp, raw := add(map[string]any{"kind": "grokbot", "url": routineURL, "key": grokKey})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/admin/routes = %d; %s", resp.StatusCode, raw)
	}
	if bytes.Contains(raw, []byte(grokKey)) || bytes.Contains(raw, []byte("whsec_")) {
		t.Fatalf("the add response carries a secret: %s", raw)
	}
	var created struct {
		RouteID string `json:"route_id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.RouteID == "" {
		t.Fatalf("add body %s (%v)", raw, err)
	}

	// Verified at once: no challenge exists for routines.
	var verified *string
	var sealed string
	var perHour int
	if err := env.Hub.DB.QueryRow(`SELECT verified_at, key_enc, runs_per_hour FROM routes WHERE route_id = ?`, created.RouteID).
		Scan(&verified, &sealed, &perHour); err != nil {
		t.Fatal(err)
	}
	if verified == nil {
		t.Fatalf("a grokbot route must be verified immediately")
	}
	if perHour != 200 {
		t.Fatalf("runs_per_hour = %d, want 200", perHour)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, grokKey) {
		t.Fatalf("key_enc = %q, want the sealed key", sealed)
	}
	if got, err := env.Hub.GrokbotKey(f.ctx, created.RouteID); err != nil || got != grokKey {
		t.Fatalf("GrokbotKey = %q, %v", got, err)
	}

	resp, raw = env.Do(env.OperatorSecret, "GET", "/v1/admin/routes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/admin/routes = %d; %s", resp.StatusCode, raw)
	}
	if bytes.Contains(raw, []byte(grokKey)) || bytes.Contains(raw, []byte(sealed)) ||
		bytes.Contains(raw, []byte("key_enc")) || bytes.Contains(raw, []byte("crsr_")) {
		t.Fatalf("GET /v1/admin/routes leaks the key: %s", raw)
	}
	var list struct {
		Routes []struct {
			RouteID    string  `json:"route_id"`
			Kind       string  `json:"kind"`
			URL        string  `json:"url"`
			VerifiedAt *string `json:"verified_at"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	if len(list.Routes) != 1 || list.Routes[0].RouteID != created.RouteID || list.Routes[0].Kind != "grokbot" ||
		list.Routes[0].URL != routineURL || list.Routes[0].VerifiedAt == nil {
		t.Fatalf("GET /v1/admin/routes = %s", raw)
	}
	if resp, raw := env.Do(f.bSec, "GET", "/v1/admin/routes", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a peer read /v1/admin/routes: %d; %s", resp.StatusCode, raw)
	}

	// A routine has no whsec_: rotating one would invent a key nothing signs
	// with, and a challenge would POST at the routine unauthenticated.
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/routes/"+created.RouteID+"/rotate", nil)
	wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
	if keys, err := env.Hub.RouteKeys(f.ctx, created.RouteID, env.Now.Now()); err != nil || len(keys) != 0 {
		t.Fatalf("grokbot route has whsec keys %v (%v)", keys, err)
	}
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/routes/"+created.RouteID+"/verify", nil)
	wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
}

func TestGrokbotPushHeadersAndBody(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	sr := f.send(f.aSec, f.msg("wake"))
	dly := sr.Deliveries[0].DeliveryID

	if n := f.step(); n != 1 {
		t.Fatalf("Step pushed %d, want 1", n)
	}
	hits := f.rcv.Hits()
	if len(hits) != 1 {
		t.Fatalf("routine got %d POSTs, want 1", len(hits))
	}
	h := hits[0]
	if got := h.Header.Get("Authorization"); got != "Bearer "+grokKey {
		t.Fatalf("Authorization = %q", got)
	}
	if got := h.Header.Get("X-Automation-Key"); got != grokKey {
		t.Fatalf("X-Automation-Key = %q", got)
	}
	if ct := h.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	for _, name := range []string{"webhook-id", "webhook-timestamp", "webhook-signature", "X-Vecnet-Attempt"} {
		if got := h.Header.Get(name); got != "" {
			t.Fatalf("%s = %q, want no such header on a routine POST", name, got)
		}
	}
	if err := envelope.Validate("delivery", h.Body); err != nil {
		t.Fatalf("POST body is not a schema-valid delivery: %v; %s", err, h.Body)
	}
	if want := f.eventData(f.b, dly); !bytes.Equal(h.Body, want) {
		t.Fatalf("POST body differs from the delivery Event data:\n got %s\nwant %s", h.Body, want)
	}
	// The Bot gets only a runUuid back, so the ids must ride in the body
	// and origin stays null (PROTOCOL §13.3, R25).
	var body map[string]any
	if err := json.Unmarshal(h.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["delivery_id"] != dly {
		t.Fatalf("body delivery_id = %v, want %s", body["delivery_id"], dly)
	}
	if msg, _ := body["message"].(map[string]any); msg == nil || msg["message_id"] != sr.MessageID || msg["thread_id"] != sr.ThreadID {
		t.Fatalf("body message = %v, want message_id %s, thread_id %s", body["message"], sr.MessageID, sr.ThreadID)
	} else if origin, has := msg["origin"]; !has || origin != nil {
		t.Fatalf("message.origin = %v (present %v), want null", origin, has)
	}

	if r := f.row(dly); r.State != "accepted" || r.Attempt != 1 {
		t.Fatalf("delivery = %+v, want accepted at attempt 1", r)
	}
	if rs := f.receiptsFor(f.a, dly); len(rs) != 1 || rs[0].State != "accepted" || rs[0].By != "hub" {
		t.Fatalf("A's receipts = %+v, want one accepted by hub", rs)
	}
	if got := f.routeRuns(); got != 1 {
		t.Fatalf("route_runs = %d, want 1", got)
	}
}

// Hold is never POSTed; the Bot pulls it over /mcp (PROTOCOL §13.3).
func TestGrokbotHoldNeverPushed(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	dly := f.send(f.aSec, f.msg("hold")).Deliveries[0].DeliveryID
	if n := f.step(); n != 0 || len(f.rcv.Hits()) != 0 {
		t.Fatalf("a hold delivery was pushed (%d, %d hits)", n, len(f.rcv.Hits()))
	}
	if r := f.row(dly); r.State != "queued" || r.Attempt != 0 {
		t.Fatalf("delivery = %+v, want queued at attempt 0", r)
	}
}

// A route added after sends picks up queued deliveries with no verify step.
func TestGrokbotRouteAddedLaterPicksUpQueued(t *testing.T) {
	f := setup(t, nil)
	dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	f.addGrokbot(started)
	if n := f.step(); n != 1 {
		t.Fatalf("Step pushed %d, want 1", n)
	}
	if r := f.row(dly); r.State != "accepted" {
		t.Fatalf("delivery = %+v, want accepted", r)
	}
}

func TestGrokbotClassification(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"200 success", 200, `{"success":true,"runUuid":"x"}`, hub.GrokbotStarted},
		{"400 rate limit", 400, rateLimited, hub.GrokbotRetry},
		{"400 rate limit bare", 400, `Automation run rate limit exceeded`, hub.GrokbotRetry},
		{"400 rate limit capitalised", 400, `Rate limit exceeded`, hub.GrokbotRetry},
		{"400 disabled", 400, `Automation x is disabled`, hub.GrokbotTerminalAlert},
		{"400 disabled json", 400, disabledMsg, hub.GrokbotTerminalAlert},
		{"400 other", 400, `{"error":"bad JSON"}`, hub.GrokbotTerminal},
		{"401", 401, invalidKey, hub.GrokbotTerminalAlert},
		{"401 empty", 401, ``, hub.GrokbotTerminalAlert},
		{"404", 404, ``, hub.GrokbotTerminal},
		{"408", 408, ``, hub.GrokbotRetry},
		{"429", 429, ``, hub.GrokbotRetry},
		{"500", 500, ``, hub.GrokbotRetry},
		{"503", 503, ``, hub.GrokbotRetry},
		{"200 not success", 200, `{"success":false}`, hub.GrokbotTerminal},
		{"200 no json", 200, `ok`, hub.GrokbotTerminal},
		{"200 over the read cap", 200, `{"success":true,"pad":"` + strings.Repeat("x", hub.PushResponseCap), hub.GrokbotStarted},
		{"200 small but cut off", 200, `{"success":true,"pad":"`, hub.GrokbotTerminal},
		{"302", 302, ``, hub.GrokbotTerminal},
		{"403 mentioning disabled", 403, `disabled`, hub.GrokbotTerminal},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hub.ClassifyGrokbot(c.status, []byte(c.body)); got != c.want {
				t.Fatalf("ClassifyGrokbot(%d, %.80q) = %q, want %q", c.status, c.body, got, c.want)
			}
		})
	}
}

// Review Focus 5: end to end, a rate-limit 400 retries while 401 and
// "disabled" dead letter and alert, and other 4xx dead letter.
func TestGrokbotOutcomes(t *testing.T) {
	t.Run("rate limit 400 retries then lands", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(func(n int, h hit) reply {
			if n == 0 {
				return reply{Status: http.StatusBadRequest, Body: rateLimited}
			}
			return started(n, h)
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		left := f.row(dly).AttemptsLeft
		f.step()
		r := f.row(dly)
		if r.State != "queued" || r.Attempt != 1 || r.AttemptsLeft != left-1 || r.LastStatus == nil || *r.LastStatus != 400 {
			t.Fatalf("delivery = %+v, want queued at attempt 1 with one attempt used", r)
		}
		if got := f.counter("push_auth_failed") + f.counter("dead_letters"); got != 0 {
			t.Fatalf("a transient rate limit was counted as a failure (%d)", got)
		}
		if rs := f.receiptsFor(f.a, dly); len(rs) != 0 {
			t.Fatalf("receipts for a retry: %+v", rs)
		}
		wait := f.untilNext(dly)
		if wait <= 0 {
			t.Fatalf("next attempt in %v, want a backoff", wait)
		}
		f.env.Now.Advance(wait)
		if n := f.step(); n != 1 {
			t.Fatalf("retry Step pushed %d, want 1", n)
		}
		hits := f.rcv.Hits()
		if len(hits) != 2 || !bytes.Equal(hits[0].Body, hits[1].Body) {
			t.Fatalf("retry must resend identical bytes (%d hits)", len(hits))
		}
		if r := f.row(dly); r.State != "accepted" || r.Attempt != 2 {
			t.Fatalf("delivery = %+v, want accepted at attempt 2", r)
		}
	})
	t.Run("503 retries", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(always(http.StatusServiceUnavailable))
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if r := f.row(dly); r.State != "queued" || r.Attempt != 1 {
			t.Fatalf("delivery = %+v, want queued at attempt 1", r)
		}
	})
	t.Run("rate limit retries exhaust into a dead letter", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(answer(http.StatusBadRequest, rateLimited))
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		for i := 0; i < 8; i++ {
			if n := f.step(); n != 1 {
				t.Fatalf("step %d pushed %d, want 1", i, n)
			}
			f.env.Now.Advance(2 * time.Hour)
		}
		if r := f.row(dly); r.State != "dead_letter" || r.Attempt != 8 {
			t.Fatalf("delivery = %+v, want dead_letter at attempt 8", r)
		}
		if got := f.counter("dead_letters"); got != 1 {
			t.Fatalf("dead_letters = %d, want 1", got)
		}
	})
	for name, rep := range map[string]reply{
		"401":          {Status: http.StatusUnauthorized, Body: invalidKey},
		"disabled 400": {Status: http.StatusBadRequest, Body: disabledMsg},
	} {
		t.Run(name+" dead letters and alerts", func(t *testing.T) {
			f := setup(t, nil)
			f.addGrokbot(func(int, hit) reply { return rep })
			alerts := newReceiver(t, nil)
			if resp, raw := f.env.Do(f.env.OperatorSecret, "PUT", "/v1/admin/notify", map[string]any{
				"routes": []map[string]string{{"url": alerts.srv.URL, "filter": "dead_letters"}},
			}); resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT /v1/admin/notify = %d; %s", resp.StatusCode, raw)
			}
			dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
			if n := f.step(); n != 1 {
				t.Fatalf("Step pushed %d, want 1", n)
			}
			if r := f.row(dly); r.State != "dead_letter" || r.Attempt != 1 || r.LastStatus == nil || *r.LastStatus != rep.Status {
				t.Fatalf("delivery = %+v, want dead_letter at attempt 1", r)
			}
			if got := f.counter("push_auth_failed"); got != 1 {
				t.Fatalf("push_auth_failed = %d, want 1", got)
			}
			if got := f.counter("dead_letters"); got != 1 {
				t.Fatalf("dead_letters = %d, want 1", got)
			}
			rs := f.receiptsFor(f.a, dly)
			if len(rs) != 1 || rs[0].State != "dead_letter" || rs[0].By != "hub" {
				t.Fatalf("A's receipts = %+v, want one dead_letter by hub", rs)
			}
			var audits int
			if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE action = 'push_auth_failed' AND target_ids LIKE ?`,
				"%"+dly+"%").Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("push_auth_failed audit rows = %d, %v; want 1", audits, err)
			}
			counters, err := f.env.Hub.Counters(f.ctx)
			if err != nil || counters["push_auth_failed"] != 1 {
				t.Fatalf("Hub.Counters = %v, %v; want push_auth_failed 1", counters, err)
			}
			if r := f.reachability(f.b); r != "unreachable" {
				t.Fatalf("B reachability = %q, want unreachable", r)
			}

			// The operator's dead_letters notification route hears of it.
			deadline := time.Now().Add(5 * time.Second)
			for len(alerts.Hits()) == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			got := alerts.Hits()
			if len(got) != 1 {
				t.Fatalf("notification route got %d POSTs, want 1", len(got))
			}
			var ev struct {
				Type string `json:"type"`
				Data struct {
					DeliveryID string `json:"delivery_id"`
					State      string `json:"state"`
				} `json:"data"`
			}
			if err := json.Unmarshal(got[0].Body, &ev); err != nil {
				t.Fatalf("decode notification: %v; %s", err, got[0].Body)
			}
			if ev.Type != "receipt" || ev.Data.DeliveryID != dly || ev.Data.State != "dead_letter" {
				t.Fatalf("notification = %s", got[0].Body)
			}

			// Terminal: nothing further is posted.
			f.env.Now.Advance(2 * time.Hour)
			if n := f.step(); n != 0 || len(f.rcv.Hits()) != 1 {
				t.Fatalf("a terminal failure was retried (%d, %d hits)", n, len(f.rcv.Hits()))
			}
		})
	}
	t.Run("paused routine recovers by operator retry", func(t *testing.T) {
		var mu sync.Mutex
		paused := true
		f := setup(t, nil)
		f.addGrokbot(func(n int, h hit) reply {
			mu.Lock()
			defer mu.Unlock()
			if paused {
				return reply{Status: http.StatusBadRequest, Body: disabledMsg}
			}
			return started(n, h)
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		f.wantDeadLetter(dly)
		mu.Lock()
		paused = false
		mu.Unlock()
		f.retryDeadLetter(dly)
		if n := f.step(); n != 1 {
			t.Fatalf("Step after retry pushed %d, want 1", n)
		}
		if r := f.row(dly); r.State != "accepted" || r.Attempt != 2 {
			t.Fatalf("delivery = %+v, want accepted at attempt 2", r)
		}
		if hits := f.rcv.Hits(); len(hits) != 2 || !bytes.Equal(hits[0].Body, hits[1].Body) {
			t.Fatalf("a retry must resend identical bytes (%d hits)", len(hits))
		}
	})
	t.Run("stale key recovers by replacing it and retrying", func(t *testing.T) {
		const fresh = "crsr_ffeeddccbbaa99887766554433221100"
		f := setup(t, nil)
		f.addGrokbot(func(n int, h hit) reply {
			if h.Header.Get("Authorization") != "Bearer "+fresh {
				return reply{Status: http.StatusUnauthorized, Body: invalidKey}
			}
			return started(n, h)
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		f.wantDeadLetter(dly)

		path := "/v1/admin/routes/" + f.route + "/rotate"
		resp, raw := f.env.Do(f.env.OperatorSecret, "POST", path, map[string]any{"key": "sk_wrong"})
		wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_key")
		if bytes.Contains(raw, []byte("sk_wrong")) {
			t.Fatalf("the error echoes the key: %s", raw)
		}
		resp, raw = f.env.Do(f.env.OperatorSecret, "POST", path, map[string]any{"key": fresh})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("replace key = %d; %s", resp.StatusCode, raw)
		}
		if bytes.Contains(raw, []byte(fresh)) {
			t.Fatalf("the answer carries the key: %s", raw)
		}
		var sealed string
		if err := f.env.Hub.DB.QueryRow(`SELECT key_enc FROM routes WHERE route_id = ?`, f.route).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sealed, fresh) {
			t.Fatalf("the new key is stored in the clear")
		}
		var leaks int
		if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE detail LIKE ?`, "%"+fresh+"%").Scan(&leaks); err != nil || leaks != 0 {
			t.Fatalf("audit rows carrying the key = %d, %v", leaks, err)
		}

		f.retryDeadLetter(dly)
		if n := f.step(); n != 1 {
			t.Fatalf("Step after retry pushed %d, want 1", n)
		}
		if r := f.row(dly); r.State != "accepted" {
			t.Fatalf("delivery = %+v, want accepted", r)
		}
	})
	t.Run("only a grokbot route takes a replacement key", func(t *testing.T) {
		f := setup(t, always(http.StatusOK))
		resp, raw := f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/routes/"+f.route+"/rotate", map[string]any{"key": grokKey})
		wantCode(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
		resp, raw = f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/routes/route_nope/rotate", map[string]any{"key": grokKey})
		wantCode(t, resp, raw, http.StatusNotFound, "not_found")
	})
	t.Run("200 too long to read is still a started run", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(answer(http.StatusOK, `{"success":true,"runUuid":"x","pad":"`+strings.Repeat("x", 5<<10)+`"}`))
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if r := f.row(dly); r.State != "accepted" {
			t.Fatalf("delivery = %+v, want accepted (PROTOCOL 3.3: the status counts)", r)
		}
		if got := f.routeRuns(); got != 1 {
			t.Fatalf("route_runs = %d, want 1", got)
		}
	})
	t.Run("404 dead letters", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(always(http.StatusNotFound))
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if r := f.row(dly); r.State != "dead_letter" || r.Attempt != 1 {
			t.Fatalf("delivery = %+v, want dead_letter at attempt 1", r)
		}
		if got, auth := f.counter("dead_letters"), f.counter("push_auth_failed"); got != 1 || auth != 0 {
			t.Fatalf("dead_letters = %d, push_auth_failed = %d; want 1, 0", got, auth)
		}
		f.env.Now.Advance(2 * time.Hour)
		if f.step() != 0 {
			t.Fatalf("a 404 was retried")
		}
	})
	t.Run("302 is refused, not followed", func(t *testing.T) {
		var mu sync.Mutex
		var followed []string
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			followed = append(followed, r.Header.Get("Authorization"))
			mu.Unlock()
		}))
		t.Cleanup(other.Close)
		f := setup(t, nil)
		f.addGrokbot(func(int, hit) reply {
			return reply{Status: http.StatusFound, Header: map[string]string{"Location": other.URL + "/x"}}
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		mu.Lock()
		n := len(followed)
		mu.Unlock()
		if n != 0 {
			t.Fatalf("the redirect was followed (%d requests)", n)
		}
		if r := f.row(dly); r.State != "dead_letter" || r.LastStatus == nil || *r.LastStatus != 302 {
			t.Fatalf("delivery = %+v, want dead_letter, last 302", r)
		}
	})
	t.Run("network error retries", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.rcv.srv.Close()
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if r := f.row(dly); r.State != "queued" || r.Attempt != 1 || r.LastStatus != nil {
			t.Fatalf("delivery = %+v, want queued at attempt 1", r)
		}
	})
}

// What surrounds the POST: the routine's own wait, a run nothing used, a
// shutdown mid-request, and the wait on a route already over its limit.
func TestGrokbotEdges(t *testing.T) {
	t.Run("Retry-After sets the retry", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(func(int, hit) reply {
			return reply{Status: http.StatusServiceUnavailable, Header: map[string]string{"Retry-After": "600"}}
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if wait := f.untilNext(dly); wait < 10*time.Minute || wait > 15*time.Minute {
			t.Fatalf("next attempt in %v, want the routine's 10m, jitter aside", wait)
		}
	})
	t.Run("a key that will not unseal gives the run back", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET key_enc = 'v1:AAAA' WHERE route_id = ?`, f.route); err != nil {
			t.Fatal(err)
		}
		f.send(f.aSec, f.msg("wake"))
		f.step()
		if got := len(f.rcv.Hits()); got != 0 {
			t.Fatalf("routine got %d POSTs", got)
		}
		if n := f.routeRuns(); n != 0 {
			t.Fatalf("route_runs = %d, want the run given back", n)
		}
	})
	t.Run("shutdown mid-POST leaves the attempt", func(t *testing.T) {
		f := setup(t, nil)
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		f.ctx = ctx
		stuck := make(chan struct{})
		defer close(stuck)
		f.addGrokbot(func(int, hit) reply {
			cancel()
			<-stuck
			return reply{Status: http.StatusOK}
		})
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		if _, err := f.w.Step(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		// Claimed, and its lease left to lapse: no result was recorded.
		if r := f.row(dly); r.State != "pushed" || r.LastStatus != nil {
			t.Fatalf("delivery = %+v, want pushed under its lease with no result", r)
		}
	})
	t.Run("a route over its limit waits for the excess to leave", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.setRunsPerHour(1)
		now := f.env.Now.Now()
		for _, age := range []time.Duration{50 * time.Minute, 40 * time.Minute, 30 * time.Minute} {
			if _, err := f.env.Hub.DB.Exec(`INSERT INTO route_runs(route_id, at) VALUES (?, ?)`,
				f.route, envelope.FormatTime(now.Add(-age))); err != nil {
				f.t.Fatal(err)
			}
		}
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if wait := f.untilNext(dly); wait != 30*time.Minute {
			t.Fatalf("next attempt in %v, want 30m: the third-oldest run has to leave", wait)
		}
	})
}

func TestGrokbotBudget(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	f.setRunsPerHour(2)
	var dlys []string
	for range 3 {
		dlys = append(dlys, f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID)
	}
	left := f.row(dlys[0]).AttemptsLeft

	// One batch claims all three and pushes them concurrently; the budget
	// still lets only two through.
	if n := f.step(); n != 3 {
		t.Fatalf("Step claimed %d, want 3", n)
	}
	if got := len(f.rcv.Hits()); got != 2 {
		t.Fatalf("routine got %d POSTs, want 2 (runs_per_hour)", got)
	}
	waiting := ""
	for _, d := range dlys {
		switch r := f.row(d); r.State {
		case "accepted":
		case "queued":
			if waiting != "" {
				t.Fatalf("two deliveries queued (%s, %s)", waiting, d)
			}
			waiting = d
		default:
			t.Fatalf("delivery %s = %+v", d, r)
		}
	}
	if waiting == "" {
		t.Fatalf("no delivery is waiting for budget")
	}
	unchanged := func(when string) {
		t.Helper()
		if r := f.row(waiting); r.State != "queued" || r.Attempt != 0 || r.AttemptsLeft != left {
			t.Fatalf("%s: delivery = %+v, want queued at attempt 0 with %d left", when, r, left)
		}
	}
	unchanged("after the budget refused")
	if wait := f.untilNext(waiting); wait != time.Hour {
		t.Fatalf("next attempt in %v, want 1h (when the oldest run leaves the window)", wait)
	}

	// Still inside the hour: not due, not posted, no attempt spent.
	f.env.Now.Advance(59 * time.Minute)
	if n := f.step(); n != 0 {
		t.Fatalf("Step pushed %d inside the budget wait", n)
	}
	unchanged("59 min later")
	if got := len(f.rcv.Hits()); got != 2 {
		t.Fatalf("routine got %d POSTs inside the budget wait", got)
	}

	f.env.Now.Advance(time.Minute)
	if n := f.step(); n != 1 {
		t.Fatalf("Step pushed %d after an hour, want 1", n)
	}
	if got := len(f.rcv.Hits()); got != 3 {
		t.Fatalf("routine got %d POSTs, want 3", got)
	}
	if r := f.row(waiting); r.State != "accepted" || r.Attempt != 1 || r.AttemptsLeft != left {
		t.Fatalf("delivery = %+v, want accepted at attempt 1 with %d left", r, left)
	}
	if got := f.routeRuns(); got != 1 {
		t.Fatalf("route_runs = %d, want 1 (the expired runs are pruned)", got)
	}
}

// A sliding window: after a staggered pair the wait is to the oldest run,
// and a run in flight still counts.
func TestGrokbotBudgetWindowSlides(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	f.setRunsPerHour(2)
	f.send(f.aSec, f.msg("wake"))
	f.step()
	f.env.Now.Advance(40 * time.Minute)
	f.send(f.aSec, f.msg("wake"))
	f.step()
	dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	f.step()
	if got := f.untilNext(dly); got != 20*time.Minute {
		t.Fatalf("next attempt in %v, want 20m", got)
	}
}

// grokbot.md §3: an account gets 300 runs an hour across its routines.
// Routes carry no account identity, so the cap is hub-wide.
func TestGrokbotAccountBudget(t *testing.T) {
	t.Run("two routines share 300 runs", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		first := f.route
		second, err := f.env.Hub.AddGrokbotRoute(f.ctx, "peer", f.a, f.rcv.srv.URL, grokKey)
		if err != nil {
			t.Fatalf("AddGrokbotRoute: %v", err)
		}
		now := f.env.Now.Now()
		runs := 0
		for _, route := range []string{first, second} {
			for range 200 {
				if ok, _ := f.env.Hub.GrokbotBudgetOK(f.ctx, route, now); ok {
					runs++
				}
			}
		}
		if runs != hub.GrokbotAccountRunsPerHour {
			t.Fatalf("runs taken in one hour across two routines = %d, want %d", runs, hub.GrokbotAccountRunsPerHour)
		}
		ok, wait := f.env.Hub.GrokbotBudgetOK(f.ctx, second, now)
		if ok || wait != time.Hour {
			t.Fatalf("over the account cap: %v, %v; want false, 1h", ok, wait)
		}
		if ok, _ := f.env.Hub.GrokbotBudgetOK(f.ctx, second, now.Add(time.Hour)); !ok {
			t.Fatalf("the account budget did not refill after an hour")
		}
	})
	t.Run("a delivery over the cap waits without an attempt", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.seedRuns(hub.GrokbotAccountRunsPerHour-1, 20*time.Minute)
		first := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if r := f.row(first); r.State != "accepted" {
			t.Fatalf("delivery = %+v, want accepted (one run left)", r)
		}
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		left := f.row(dly).AttemptsLeft
		f.step()
		if r := f.row(dly); r.State != "queued" || r.Attempt != 0 || r.AttemptsLeft != left {
			t.Fatalf("delivery = %+v, want queued at attempt 0 with %d left", r, left)
		}
		if got := f.untilNext(dly); got != 40*time.Minute {
			t.Fatalf("next attempt in %v, want 40m (when the oldest run leaves the window)", got)
		}
		if got := len(f.rcv.Hits()); got != 1 {
			t.Fatalf("routine got %d POSTs, want 1", got)
		}
		f.env.Now.Advance(40 * time.Minute)
		f.step()
		if r := f.row(dly); r.State != "accepted" {
			t.Fatalf("delivery = %+v, want accepted", r)
		}
	})
	t.Run("the later of the two limits sets the wait", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.setRunsPerHour(1)
		f.seedRuns(hub.GrokbotAccountRunsPerHour-1, 20*time.Minute)
		f.send(f.aSec, f.msg("wake"))
		f.step()
		f.env.Now.Advance(10 * time.Minute)
		// The route frees up in 50 m; the account, 30 m after its oldest run.
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if got := f.untilNext(dly); got != 50*time.Minute {
			t.Fatalf("next attempt in %v, want 50m", got)
		}
	})
}

// grokbot.md §2: any non-200 means no run started, so it spends no budget;
// a network error may have started one, so it does.
func TestGrokbotBudgetCountsRunsNotAttempts(t *testing.T) {
	t.Run("refused POST spends nothing", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(func(n int, h hit) reply {
			if n == 0 {
				return reply{Status: http.StatusBadRequest, Body: rateLimited}
			}
			return started(n, h)
		})
		f.setRunsPerHour(1)
		dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
		f.step()
		if got := f.routeRuns(); got != 0 {
			t.Fatalf("route_runs after a refused POST = %d, want 0", got)
		}
		f.env.Now.Advance(f.untilNext(dly))
		if n := f.step(); n != 1 {
			t.Fatalf("Step pushed %d, want 1", n)
		}
		if r := f.row(dly); r.State != "accepted" {
			t.Fatalf("delivery = %+v, want accepted", r)
		}
		if got := f.routeRuns(); got != 1 {
			t.Fatalf("route_runs = %d, want 1", got)
		}
	})
	t.Run("network error may have started one", func(t *testing.T) {
		f := setup(t, nil)
		f.addGrokbot(started)
		f.rcv.srv.Close()
		f.send(f.aSec, f.msg("wake"))
		f.step()
		if got := f.routeRuns(); got != 1 {
			t.Fatalf("route_runs after a network error = %d, want 1", got)
		}
	})
}

// syncBuffer is an io.Writer tests can read while pushes log.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestGrokbotKeyNeverLogged(t *testing.T) {
	var logs syncBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	f := setup(t, nil)
	// The routine echoes the key back, the worst answer a log could copy.
	echo := `{"message":"Invalid API key ` + grokKey + `"}`
	f.addGrokbot(func(n int, h hit) reply {
		switch n {
		case 0:
			return started(n, h)
		case 1:
			return reply{Status: http.StatusServiceUnavailable, Body: echo}
		case 2:
			return reply{Status: http.StatusBadRequest, Body: `rate limit ` + grokKey}
		default:
			return reply{Status: http.StatusUnauthorized, Body: echo}
		}
	})
	first := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	f.step() // 200
	if r := f.row(first); r.State != "accepted" {
		t.Fatalf("delivery = %+v, want accepted", r)
	}
	dly := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	f.step() // 503
	f.env.Now.Advance(f.untilNext(dly))
	f.step() // 400 rate limit
	f.env.Now.Advance(f.untilNext(dly))
	f.step() // 401
	if r := f.row(dly); r.State != "dead_letter" {
		t.Fatalf("delivery = %+v, want dead_letter", r)
	}

	// A sealed value that no longer opens must not produce a log carrying it.
	if _, err := f.env.Hub.DB.Exec(`UPDATE routes SET key_enc = 'v1:AAAA' WHERE route_id = ?`, f.route); err != nil {
		t.Fatal(err)
	}
	bad := f.send(f.aSec, f.msg("wake")).Deliveries[0].DeliveryID
	f.step()
	if r := f.row(bad); r.State != "queued" || r.Attempt != 1 {
		t.Fatalf("delivery with an unusable key = %+v, want queued at attempt 1", r)
	}

	out := logs.String()
	// The capture is live: the auth failure was reported.
	if !strings.Contains(out, "push: grokbot") {
		t.Fatalf("captured no push log; capture is not live:\n%s", out)
	}
	if strings.Contains(out, grokKey) {
		t.Fatalf("the key reached slog:\n%s", out)
	}
	if strings.Contains(out, grokKey[len("crsr_"):]) {
		t.Fatalf("the key body reached slog:\n%s", out)
	}

	// Nor is it at rest in the clear: not in routes, audit, last_error or
	// anywhere else in the database files.
	if err := f.env.Hub.DB.Close(); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(f.env.Hub.Cfg.DataDir, "hub.db*"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no database files: %v", err)
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(grokKey)) {
			t.Fatalf("%s holds the key in the clear", p)
		}
	}
}
