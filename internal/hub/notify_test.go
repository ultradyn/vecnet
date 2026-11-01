package hub_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// notifyHit is one notification webhook a receiver got.
type notifyHit struct {
	Header http.Header
	Body   []byte
}

// notifyReceiver records notification webhooks and answers 200.
type notifyReceiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []notifyHit
}

func newNotifyReceiver(t *testing.T) *notifyReceiver {
	t.Helper()
	r := &notifyReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits = append(r.hits, notifyHit{Header: req.Header.Clone(), Body: body})
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *notifyReceiver) Hits() []notifyHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notifyHit(nil), r.hits...)
}

type notifyRoute struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Filter string `json:"filter"`
	Whsec  string `json:"whsec"`
}

// putNotify replaces the notify routes and returns them keyed by URL.
func putNotify(t *testing.T, env *testhub.Env, routes ...map[string]string) map[string]notifyRoute {
	t.Helper()
	resp, raw := env.Do(env.OperatorSecret, "PUT", "/v1/admin/notify", map[string]any{"routes": routes})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /v1/admin/notify = %d; %s", resp.StatusCode, raw)
	}
	var body struct {
		Routes []notifyRoute `json:"routes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	out := map[string]notifyRoute{}
	for _, r := range body.Routes {
		if r.ID == "" || !strings.HasPrefix(r.Whsec, "whsec_") {
			t.Fatalf("notify route %+v lacks an id or whsec", r)
		}
		out[r.URL] = r
	}
	if len(out) != len(routes) {
		t.Fatalf("PUT /v1/admin/notify returned %d routes, want %d; %s", len(out), len(routes), raw)
	}
	return out
}

// verifyNotify checks h is signed by whsec with webhook-id = the Event's
// cursor (or, for an Event with no cursor, any id) and returns the Event.
// now is the hub's (fake) clock, which signed it.
func verifyNotify(t *testing.T, h notifyHit, whsec string, now time.Time) envelope.Event {
	t.Helper()
	if err := webhook.Verify([]string{whsec}, h.Header.Get(webhook.HeaderID), h.Header.Get(webhook.HeaderTimestamp),
		h.Header.Get(webhook.HeaderSignature), h.Body, now); err != nil {
		t.Fatalf("notification signature does not verify with its route's whsec: %v", err)
	}
	var ev envelope.Event
	if err := json.Unmarshal(h.Body, &ev); err != nil {
		t.Fatalf("notification body is not an Event: %v; %s", err, h.Body)
	}
	if ev.Cursor != "" && h.Header.Get(webhook.HeaderID) != ev.Cursor {
		t.Fatalf("webhook-id = %q, want the Event cursor %q", h.Header.Get(webhook.HeaderID), ev.Cursor)
	}
	return ev
}

func TestNotifyRoutesFilter(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, aSec := env.NewPeer("grokbot", "a")
	b, _ := env.NewPeer("dot", "b")
	env.SetLimits(a, 1000, 1000)

	wakeRcv, dlRcv := newNotifyReceiver(t), newNotifyReceiver(t)
	routes := putNotify(t, env,
		map[string]string{"url": wakeRcv.srv.URL, "filter": "wake"},
		map[string]string{"url": dlRcv.srv.URL, "filter": "dead_letters"})

	// A wake send to the operator fires only the wake route.
	sr := mustSend(t, env, aSec, req("n-1", env.OperatorPeer))
	env.Hub.WaitNotifies()
	hits := wakeRcv.Hits()
	if len(hits) != 1 || len(dlRcv.Hits()) != 0 {
		t.Fatalf("after a wake send to the operator: wake route %d hits, dead_letters route %d; want 1 and 0",
			len(hits), len(dlRcv.Hits()))
	}
	ev := verifyNotify(t, hits[0], routes[wakeRcv.srv.URL].Whsec, env.Now.Now())
	if ev.Type != "delivery" || ev.Cursor == "" || !strings.Contains(string(ev.Data), sr.MessageID) {
		t.Fatalf("wake notification Event = %+v, want the operator's delivery of %s", ev, sr.MessageID)
	}
	// It is not signed with the other route's key.
	if err := webhook.Verify([]string{routes[dlRcv.srv.URL].Whsec}, hits[0].Header.Get(webhook.HeaderID),
		hits[0].Header.Get(webhook.HeaderTimestamp), hits[0].Header.Get(webhook.HeaderSignature), hits[0].Body, env.Now.Now()); err == nil {
		t.Fatal("wake notification also verifies with the dead_letters route's key")
	}

	// A hold send to the operator fires nothing; nor does a wake send to
	// another peer.
	mustSend(t, env, aSec, holdReq("n-2", env.OperatorPeer))
	env.Hub.WaitNotifies()
	if len(wakeRcv.Hits()) != 1 || len(dlRcv.Hits()) != 0 {
		t.Fatalf("after a hold send: %d and %d hits, want 1 and 0", len(wakeRcv.Hits()), len(dlRcv.Hits()))
	}

	// A dead letter fires only the dead_letters route.
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var probe struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Type == "verification" {
			json.NewEncoder(w).Encode(map[string]string{"challenge": probe.Challenge})
			return
		}
		w.WriteHeader(http.StatusGone)
	}))
	t.Cleanup(gone.Close)
	route, _, err := env.Hub.AddRoute(ctx, "peer", b, gone.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Worker.Verify(ctx, route); err != nil {
		t.Fatal(err)
	}
	sr3 := mustSend(t, env, aSec, req("n-3", b))
	if _, err := env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := deliveryState(t, env, sr3.Deliveries[0].DeliveryID); st != "dead_letter" {
		t.Fatalf("delivery to the 410 route is %s, want dead_letter", st)
	}
	env.Hub.WaitNotifies()
	if len(wakeRcv.Hits()) != 1 || len(dlRcv.Hits()) != 1 {
		t.Fatalf("after a dead letter: %d and %d hits, want 1 and 1", len(wakeRcv.Hits()), len(dlRcv.Hits()))
	}
	ev = verifyNotify(t, dlRcv.Hits()[0], routes[dlRcv.srv.URL].Whsec, env.Now.Now())
	var rc envelope.Receipt
	if err := json.Unmarshal(ev.Data, &rc); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "receipt" || rc.State != "dead_letter" || rc.DeliveryID != sr3.Deliveries[0].DeliveryID {
		t.Fatalf("dead_letters notification = %+v / %+v, want the dead_letter receipt of %s", ev, rc, sr3.Deliveries[0].DeliveryID)
	}

	// Replacing the routes keeps the whsec of an unchanged URL.
	again := putNotify(t, env,
		map[string]string{"url": wakeRcv.srv.URL, "filter": "all"})
	if again[wakeRcv.srv.URL].Whsec != routes[wakeRcv.srv.URL].Whsec || again[wakeRcv.srv.URL].Filter != "all" {
		t.Fatalf("re-PUT route %+v, want the same whsec and filter all", again[wakeRcv.srv.URL])
	}
	if n := count(t, env, `SELECT count(*) FROM notify_routes`); n != 1 {
		t.Fatalf("notify_routes rows = %d after replace, want 1", n)
	}

	// An unknown filter is rejected.
	resp, raw := env.Do(env.OperatorSecret, "PUT", "/v1/admin/notify",
		map[string]any{"routes": []map[string]string{{"url": wakeRcv.srv.URL, "filter": "everything"}}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT unknown filter = %d, want 422; %s", resp.StatusCode, raw)
	}
}
