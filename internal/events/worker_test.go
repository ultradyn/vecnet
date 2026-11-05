package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/egress"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// subscribe creates a verified subscription for peer at the head of its log,
// principal "vecnet" unless tokenExp is set, the way events/subscribe does.
func subscribe(t *testing.T, env *testhub.Env, cb *testhub.Callback, peer string, args map[string]any, tokenExp time.Time) events.Subscription {
	t.Helper()
	ctx := context.Background()
	cur, err := env.Hub.Log.Current(ctx, []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	kind := "vecnet"
	if !tokenExp.IsZero() {
		kind = "oauth"
		// No login stands behind this subscription, so no oauth_principals
		// row does: bind its principal, the peer itself, here.
		env.Events.ResolveDotPeer = func(_ context.Context, principalID string) (string, error) {
			return principalID, nil
		}
	}
	var types []string
	if v, ok := args["types"].([]string); ok {
		types = v
	}
	s, _, err := env.Events.Store.Upsert(ctx, events.Subscription{
		PrincipalKind: kind, PrincipalID: peer, PeerID: peer, Name: events.Name,
		URL: cb.URL, Arguments: args, Types: types, Cursor: cur,
	}, webhook.NewSecret(), 0, tokenExp)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := env.Events.Store.MarkVerified(ctx, s.ID); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	return get(t, env, s.ID)
}

func send(t *testing.T, env *testhub.Env, secret, recipient, key, priority, text string) string {
	t.Helper()
	resp, out := env.Do(secret, "POST", "/v1/messages", map[string]any{
		"idempotency_key": key,
		"priority":        priority,
		"recipients":      []string{recipient},
		"content":         map[string]any{"text": text},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s: %d %s", key, resp.StatusCode, out)
	}
	var sent struct {
		Deliveries []hub.DeliveryRef `json:"deliveries"`
	}
	if err := json.Unmarshal(out, &sent); err != nil || len(sent.Deliveries) != 1 {
		t.Fatalf("send %s: decode %s: %v", key, out, err)
	}
	return sent.Deliveries[0].DeliveryID
}

func step(t *testing.T, env *testhub.Env) int {
	t.Helper()
	n, err := env.Events.Step(context.Background())
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	return n
}

func wantPosts(t *testing.T, cb *testhub.Callback, n int) []testhub.CallbackPost {
	t.Helper()
	posts := cb.Posts()
	if len(posts) != n {
		t.Fatalf("POSTs = %d, want %d", len(posts), n)
	}
	return posts
}

func get(t *testing.T, env *testhub.Env, id string) events.Subscription {
	t.Helper()
	s, err := env.Events.Store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return s
}

// outbox reads one outbox row's attempts and terminal reason.
func outbox(t *testing.T, env *testhub.Env, sub, event string) (attempts int, done bool, terminal string) {
	t.Helper()
	var doneAt, term *string
	err := env.Hub.DB.QueryRow(`SELECT attempts, done_at, terminal FROM event_outbox WHERE sub_id = ? AND event_id = ?`, sub, event).
		Scan(&attempts, &doneAt, &term)
	if err != nil {
		t.Fatalf("outbox(%s, %s): %v", sub, event, err)
	}
	if term != nil {
		terminal = *term
	}
	return attempts, doneAt != nil, terminal
}

// refresh repeats a subscription's Upsert and a passed challenge, as a
// re-subscribe to a working callback does.
func refresh(t *testing.T, env *testhub.Env, sub events.Subscription, tokenExp time.Time) events.Subscription {
	t.Helper()
	again, created, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
		PrincipalKind: sub.PrincipalKind, PrincipalID: sub.PrincipalID, PeerID: sub.PeerID, Name: sub.Name,
		URL: sub.URL, Arguments: sub.Arguments, Types: sub.Types,
	}, webhook.NewSecret(), 0, tokenExp)
	if err != nil || created || again.ID != sub.ID {
		t.Fatalf("refresh = %+v, created %v, %v", again, created, err)
	}
	if !again.Verified {
		if err := env.Events.Store.MarkVerified(context.Background(), again.ID); err != nil {
			t.Fatalf("MarkVerified: %v", err)
		}
		again = get(t, env, again.ID)
	}
	return again
}

func TestOutageQueuesAndDrains(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(n int, w http.ResponseWriter) {
		if n <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})

	dly := send(t, env, sender, dot, "outage", "wake", "hi")
	if n := step(t, env); n != 0 {
		t.Fatalf("Step sent %d on a 503", n)
	}
	wantPosts(t, cb, 1)
	step(t, env)
	wantPosts(t, cb, 1) // backoff: not due yet

	// Backoff is 2^attempt s plus up to 10 %.
	env.Now.Advance(1900 * time.Millisecond)
	step(t, env)
	wantPosts(t, cb, 1)
	env.Now.Advance(1100 * time.Millisecond)
	step(t, env)
	wantPosts(t, cb, 2)
	env.Now.Advance(5 * time.Second)
	step(t, env)
	wantPosts(t, cb, 3)
	env.Now.Advance(9 * time.Second)
	if n := step(t, env); n != 1 {
		t.Fatalf("Step sent %d on the 200, want 1", n)
	}
	posts := wantPosts(t, cb, 4)
	for _, p := range posts {
		if p.ID != dly || string(p.Body) != string(posts[0].Body) {
			t.Fatalf("attempt = %s %s, want the same eventId %s and bytes", p.ID, p.Body, dly)
		}
	}
	if posts[0].Timestamp == posts[3].Timestamp {
		t.Fatal("a retry reused its timestamp")
	}
	env.Now.Advance(2 * time.Hour)
	step(t, env)
	wantPosts(t, cb, 4)
	if attempts, done, terminal := outbox(t, env, sub.ID, dly); attempts != 3 || !done || terminal != "" {
		t.Fatalf("outbox = %d %v %q, want 3 failures, done, not terminal", attempts, done, terminal)
	}
}

func wantEventsCounters(t *testing.T, env *testhub.Env, failed, paused int) {
	t.Helper()
	counters, err := env.Hub.Counters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"events_push_failed": failed, "events_paused": paused} {
		if got, ok := counters[name]; !ok || got != want {
			t.Errorf("counter %s = %d (present %v), want %d", name, got, ok, want)
		}
	}
}

func TestEventsPushFailureCounted(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusUnauthorized, http.StatusTemporaryRedirect, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			env := testhub.Start(t)
			dot, _ := env.NewPeer("dot", "d")
			sender, secret := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			subscribe(t, env, cb, dot, nil, time.Time{})
			cb.OnPost(func(n int, w http.ResponseWriter) {
				if n != 1 {
					return
				}
				if status != 0 {
					w.WriteHeader(status)
					return
				}
				// Close without a response to exercise a real transport failure.
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			})
			dly := send(t, env, secret, dot, "failure", "wake", "retry me")
			if n := step(t, env); n != 0 {
				t.Fatalf("failed Step sent %d, want 0", n)
			}
			wantEventsCounters(t, env, 1, 0)
			env.Now.Advance(3 * time.Second)
			if n := step(t, env); n != 1 {
				t.Fatalf("successful retry sent %d, want 1", n)
			}
			wantEventsCounters(t, env, 1, 0)
			posts := wantPosts(t, cb, 2)
			if posts[0].ID != dly || posts[1].ID != dly {
				t.Fatalf("retry changed delivery: %+v", posts)
			}
			evs, err := env.Hub.Log.Read(context.Background(), []string{sender}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 10)
			if err != nil {
				t.Fatal(err)
			}
			accepted := 0
			for _, ev := range evs {
				if ev.Type != "receipt" {
					continue
				}
				var receipt venv.Receipt
				if err := json.Unmarshal(ev.Data, &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.DeliveryID == dly && receipt.State == "accepted" && receipt.By == "hub" {
					accepted++
				}
			}
			if accepted != 1 {
				t.Fatalf("accepted receipts = %d, want 1", accepted)
			}
		})
	}
}

func TestEventsPauseCounted(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	send(t, env, sender, dot, "exhaust", "wake", "retry me")
	for attempt := 1; attempt <= hub.PushAttempts; attempt++ {
		if n := step(t, env); n != 0 {
			t.Fatalf("failed Step sent %d, want 0", n)
		}
		wantPosts(t, cb, attempt)
		paused := 0
		wantReason := ""
		if attempt == hub.PushAttempts {
			paused, wantReason = 1, events.PausedDeliveryFailed
		}
		wantEventsCounters(t, env, attempt, paused)
		if got := get(t, env, sub.ID).PausedReason; got != wantReason {
			t.Fatalf("after attempt %d paused = %q, want %q", attempt, got, wantReason)
		}
		env.Now.Advance(5 * time.Minute)
	}
	step(t, env)
	wantPosts(t, cb, hub.PushAttempts)
	wantEventsCounters(t, env, 8, 1)

	expiring := subscribe(t, env, testhub.NewCallback(t), dot, nil, env.Now.Now().Add(time.Hour))
	operator := subscribe(t, env, testhub.NewCallback(t), dot, nil, env.Now.Now().Add(time.Hour))
	if err := env.Events.Store.Pause(ctx, operator.ID); err != nil {
		t.Fatal(err)
	}
	wantEventsCounters(t, env, 8, 1)
	env.Now.Advance(2 * time.Hour)
	step(t, env)
	if got := get(t, env, expiring.ID).PausedReason; got != events.PausedTokenExpired {
		t.Fatalf("expired token paused = %q", got)
	}
	if got := get(t, env, operator.ID).PausedReason; got != events.PausedOperator {
		t.Fatalf("operator pause overwritten: %q", got)
	}
	wantEventsCounters(t, env, 8, 2)
	step(t, env)
	wantEventsCounters(t, env, 8, 2)
}

func TestEventsCountersTerminalResponses(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusRequestEntityTooLarge} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			env := testhub.Start(t)
			dot, _ := env.NewPeer("dot", "d")
			_, sender := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			subscribe(t, env, cb, dot, nil, time.Time{})
			cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(status) })
			send(t, env, sender, dot, "terminal", "wake", "no retry")
			step(t, env)
			env.Now.Advance(time.Hour)
			step(t, env)
			wantPosts(t, cb, 1)
			wantEventsCounters(t, env, 0, 0)
		})
	}
}

func TestEventsCountersAuthPauses(t *testing.T) {
	t.Run("bulk expiry and credential rotation", func(t *testing.T) {
		env := testhub.Start(t)
		dot, _ := env.NewPeer("dot", "d")
		var subs []events.Subscription
		for range 2 {
			subs = append(subs, subscribe(t, env, testhub.NewCallback(t), dot, nil, env.Now.Now().Add(time.Hour)))
		}
		session, _ := env.NewPeer("session", "s")
		rotated := subscribe(t, env, testhub.NewCallback(t), session, nil, time.Time{})
		if _, err := env.Hub.RotatePeer(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		if got := get(t, env, rotated.ID).PausedReason; got != events.PausedCredentialRotated {
			t.Fatalf("rotated subscription pause = %q", got)
		}
		wantEventsCounters(t, env, 0, 0)
		env.Now.Advance(2 * time.Hour)
		step(t, env)
		for _, sub := range subs {
			if got := get(t, env, sub.ID).PausedReason; got != events.PausedTokenExpired {
				t.Fatalf("expired subscription pause = %q", got)
			}
		}
		wantEventsCounters(t, env, 0, 2)
		step(t, env)
		wantEventsCounters(t, env, 0, 2)
	})
	t.Run("unauthorized", func(t *testing.T) {
		env := testhub.Start(t)
		dot, _ := env.NewPeer("dot", "d")
		_, sender := env.NewPeer("session", "s")
		cb := testhub.NewCallback(t)
		sub := subscribe(t, env, cb, dot, nil, time.Time{})
		send(t, env, sender, dot, "unauthorized", "wake", "a")
		if err := env.Hub.SetPeerDisabled(context.Background(), dot, true); err != nil {
			t.Fatal(err)
		}
		step(t, env)
		if got := get(t, env, sub.ID).PausedReason; got != events.PausedUnauthorized {
			t.Fatalf("disabled subscription pause = %q", got)
		}
		wantPosts(t, cb, 0)
		wantEventsCounters(t, env, 0, 1)
		step(t, env)
		wantEventsCounters(t, env, 0, 1)
	})
	for _, operatorPause := range []bool{false, true} {
		t.Run(fmt.Sprintf("mid-drain expiry operator=%v", operatorPause), func(t *testing.T) {
			env := testhub.Start(t)
			dot, _ := env.NewPeer("dot", "d")
			_, sender := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, dot, nil, env.Now.Now().Add(time.Hour))
			cb.OnPost(func(_ int, _ http.ResponseWriter) {
				if operatorPause {
					if err := env.Events.Store.Pause(context.Background(), sub.ID); err != nil {
						t.Error(err)
					}
				}
				env.Now.Advance(2 * time.Hour)
			})
			send(t, env, sender, dot, "one", "wake", "a")
			send(t, env, sender, dot, "two", "wake", "b")
			step(t, env)
			wantPosts(t, cb, 1)
			wantReason, wantPaused := events.PausedTokenExpired, 1
			if operatorPause {
				wantReason, wantPaused = events.PausedOperator, 0
			}
			if got := get(t, env, sub.ID).PausedReason; got != wantReason {
				t.Fatalf("mid-drain pause = %q, want %q", got, wantReason)
			}
			wantEventsCounters(t, env, 0, wantPaused)
			step(t, env)
			wantEventsCounters(t, env, 0, wantPaused)
		})
	}
}

func TestEventsCountersFailureTransactional(t *testing.T) {
	for _, priorAttempts := range []int{0, hub.PushAttempts - 1} {
		t.Run(fmt.Sprint(priorAttempts), func(t *testing.T) {
			env := testhub.Start(t)
			dot, _ := env.NewPeer("dot", "d")
			_, sender := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, dot, nil, time.Time{})
			cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
			dly := send(t, env, sender, dot, "rollback", "wake", "a")
			for range priorAttempts {
				step(t, env)
				env.Now.Advance(5 * time.Minute)
			}
			// Fail either INSERT or UPSERT of the counter; the outbox must roll back too.
			if _, err := env.Hub.DB.Exec(`CREATE TRIGGER reject_counter BEFORE INSERT ON counters
				WHEN NEW.name = 'events_push_failed' BEGIN SELECT RAISE(ABORT, 'counter unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := env.Events.Step(context.Background()); err == nil || !strings.Contains(err.Error(), "counter unavailable") {
				t.Fatalf("Step with unavailable counter = %v", err)
			}
			if attempts, done, _ := outbox(t, env, sub.ID, dly); attempts != priorAttempts || done {
				t.Fatalf("failed transaction persisted outbox: attempts %d, done %v", attempts, done)
			}
			if got := get(t, env, sub.ID).PausedReason; got != "" {
				t.Fatalf("failed transaction paused subscription: %q", got)
			}
			wantEventsCounters(t, env, priorAttempts, 0)
			if _, err := env.Hub.DB.Exec(`DROP TRIGGER reject_counter`); err != nil {
				t.Fatal(err)
			}
			step(t, env)
			paused := 0
			if priorAttempts == hub.PushAttempts-1 {
				paused = 1
			}
			wantEventsCounters(t, env, priorAttempts+1, paused)
		})
	}
}

func TestOutageQueuesWhileNoLiveSubscription(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})

	env.Now.Advance(25 * time.Hour) // the subscription lapses unrefreshed
	dly := send(t, env, sender, dot, "gap", "wake", "while away")
	step(t, env)
	wantPosts(t, cb, 0)

	refresh(t, env, sub, time.Time{})
	step(t, env)
	if posts := wantPosts(t, cb, 1); posts[0].ID != dly {
		t.Fatalf("drained %s, want %s", posts[0].ID, dly)
	}
}

func TestTerminalResponsesNeverRetried(t *testing.T) {
	for _, code := range []int{http.StatusGone, http.StatusRequestEntityTooLarge} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			env := testhub.Start(t)
			dot, _ := env.NewPeer("dot", "d")
			_, sender := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, dot, nil, time.Time{})
			cb.OnPost(func(n int, w http.ResponseWriter) {
				if n == 1 {
					w.WriteHeader(code)
				}
			})
			gone := send(t, env, sender, dot, "gone", "wake", "a")
			next := send(t, env, sender, dot, "next", "wake", "b")
			step(t, env)
			posts := wantPosts(t, cb, 2)
			if posts[0].ID != gone || posts[1].ID != next {
				t.Fatalf("POSTs = %s, %s; a terminal answer must not block the next event", posts[0].ID, posts[1].ID)
			}
			env.Now.Advance(2 * time.Hour)
			step(t, env)
			wantPosts(t, cb, 2)
			_, done, terminal := outbox(t, env, sub.ID, gone)
			if !done || terminal != fmt.Sprintf("http_%d", code) {
				t.Fatalf("outbox done %v terminal %q", done, terminal)
			}
			if got := get(t, env, sub.ID); got.PausedReason != "" {
				t.Fatalf("paused %q on one %d", got.PausedReason, code)
			}
		})
	}
}

func TestExhaustedPausesSubscription(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) })

	dly := send(t, env, sender, dot, "doomed", "wake", "a")
	later := send(t, env, sender, dot, "later", "wake", "b")
	for i := 1; i <= 8; i++ {
		step(t, env)
		wantPosts(t, cb, i)
		if got := get(t, env, sub.ID); (got.PausedReason != "") != (i == 8) {
			t.Fatalf("after attempt %d paused = %q", i, got.PausedReason)
		}
		env.Now.Advance(2 * time.Hour)
		refreshExpiry(t, env, sub.ID)
	}
	if _, done, terminal := outbox(t, env, sub.ID, dly); !done || terminal != "exhausted" {
		t.Fatalf("outbox done %v terminal %q", done, terminal)
	}
	if got := get(t, env, sub.ID); got.PausedReason != "delivery_failed" {
		t.Fatalf("paused = %q", got.PausedReason)
	}
	step(t, env)
	wantPosts(t, cb, 8) // paused: nothing more goes out

	// A refresh resumes it; the queued event was not dropped.
	cb.OnPost(nil)
	refresh(t, env, sub, time.Time{})
	step(t, env)
	if posts := wantPosts(t, cb, 9); posts[8].ID != later {
		t.Fatalf("after resume POST %s, want %s", posts[8].ID, later)
	}
}

// refreshExpiry keeps a subscription live across a long clock jump without
// touching anything else.
func refreshExpiry(t *testing.T, env *testhub.Env, id string) {
	t.Helper()
	if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET expires_at = ? WHERE sub_id = ?`,
		venv.FormatTime(env.Now.Now().Add(24*time.Hour)), id); err != nil {
		t.Fatal(err)
	}
}

func TestTokenExpiryPauses(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, env.Now.Now().Add(time.Hour))
	if sub.Expires == nil || !sub.Expires.Equal(env.Now.Now().Add(time.Hour)) {
		t.Fatalf("expires = %v, want clamped to the token", sub.Expires)
	}

	cb.OnPost(func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	queued := send(t, env, sender, dot, "queued", "wake", "before expiry")
	step(t, env)
	wantPosts(t, cb, 1)
	cursorBefore := get(t, env, sub.ID).Cursor

	env.Now.Advance(2 * time.Hour)
	after := send(t, env, sender, dot, "after", "wake", "while paused")
	step(t, env)
	wantPosts(t, cb, 1)
	got := get(t, env, sub.ID)
	if got.PausedReason != "token_expired" {
		t.Fatalf("paused = %q, want token_expired", got.PausedReason)
	}
	if got.Cursor != cursorBefore {
		t.Fatalf("cursor moved while paused: %v -> %v", cursorBefore, got.Cursor)
	}

	again := refresh(t, env, sub, env.Now.Now().Add(time.Hour))
	if again.PausedReason != "" || !again.Verified {
		t.Fatalf("re-subscribe = %+v", again)
	}
	step(t, env)
	posts := wantPosts(t, cb, 3)
	if posts[1].ID != queued || posts[2].ID != after {
		t.Fatalf("drained %s, %s; want %s, %s", posts[1].ID, posts[2].ID, queued, after)
	}
}

func TestTypesFilter(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "a")
	b, bSecret := env.NewPeer("session", "b")
	plain, withReceipts := testhub.NewCallback(t), testhub.NewCallback(t)
	subscribe(t, env, plain, a, nil, time.Time{})
	subscribe(t, env, withReceipts, a, map[string]any{"types": []string{"delivery", "receipt"}}, time.Time{})

	dly := send(t, env, aSecret, b, "ask", "wake", "to b")
	resp, out := env.Do(bSecret, "POST", "/v1/receipts", map[string]any{"delivery_id": dly, "state": "processed"})
	if resp.StatusCode >= 300 {
		t.Fatalf("receipt: %d %s", resp.StatusCode, out)
	}
	step(t, env)
	wantPosts(t, plain, 0)
	posts := withReceipts.Posts()
	if len(posts) == 0 {
		t.Fatal("no receipt pushed")
	}
	var body struct {
		EventID string     `json:"eventId"`
		Cursor  string     `json:"cursor"`
		Data    venv.Event `json:"data"`
	}
	if err := json.Unmarshal(posts[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Type != "receipt" {
		t.Fatalf("pushed %q, want receipt", body.Data.Type)
	}
	if body.EventID != body.Cursor || posts[0].ID != body.Cursor {
		t.Fatalf("eventId %q, webhook-id %q, cursor %q: a non-delivery event's id is its cursor", body.EventID, posts[0].ID, body.Cursor)
	}

	// Only delivery, receipt, thread_updated and approval_expired ever reach
	// a dot, whatever the row says [R14].
	other := testhub.NewCallback(t)
	subscribe(t, env, other, b, map[string]any{"types": []string{"presence", "directory_updated"}}, time.Time{})
	if _, err := env.Hub.Log.Append(context.Background(), nil, b, "presence", env.Now.Now(), map[string]any{"state": "idle"}); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	wantPosts(t, other, 0)
}

func TestPriorityGate(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})

	idle := send(t, env, sender, dot, "unknown-presence", "wake_idle", "pushed: presence unknown counts as idle")
	send(t, env, sender, dot, "hold", "hold", "never pushed")
	resp, out := env.Do(dotSecret, "POST", "/v1/presence", map[string]any{"state": "working", "at": venv.FormatTime(env.Now.Now())})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("presence: %d %s", resp.StatusCode, out)
	}
	held := send(t, env, sender, dot, "held", "wake_idle", "held while working")
	step(t, env)
	if posts := wantPosts(t, cb, 1); posts[0].ID != idle {
		t.Fatalf("pushed %s, want %s", posts[0].ID, idle)
	}
	ok, err := env.Hub.DeliveryGate(context.Background(), held)
	if err != nil || ok {
		t.Fatalf("DeliveryGate(held) = %v, %v", ok, err)
	}
	ok, err = env.Hub.DeliveryGate(context.Background(), "dly_UNKNOWN")
	if err != nil || ok {
		t.Fatalf("DeliveryGate(unknown) = %v, %v", ok, err)
	}

	// Expired while nothing was tailing: the sender has its expired receipt
	// (PROTOCOL §5.1), so a late drain must not push it after all.
	late := send(t, env, sender, dot, "late", "wake", "expires unread")
	if ok, err := env.Hub.DeliveryGate(context.Background(), late); err != nil || !ok {
		t.Fatalf("DeliveryGate(live) = %v, %v", ok, err)
	}
	if _, err := env.Hub.ExpireDeliveries(context.Background(), env.Now.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := env.Hub.DeliveryGate(context.Background(), late); err != nil || ok {
		t.Fatalf("DeliveryGate(expired) = %v, %v", ok, err)
	}
}

func TestOversizePayloadPreview(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})

	// The largest real Delivery: 16 attachment references, 4 KiB of text,
	// long metadata. It fits and goes out whole.
	atts := make([]any, 16)
	for i := range atts {
		atts[i] = map[string]any{"sha256": fmt.Sprintf("%064x", i), "size": 14, "media_type": "text/plain",
			"name": strings.Repeat("n", 200), "origin_url": "https://example.com/" + strings.Repeat("p", 1000)}
	}
	resp, out := env.Do(sender, "POST", "/v1/messages", map[string]any{
		"idempotency_key": "big-but-legal",
		"priority":        "wake",
		"recipients":      []string{dot},
		"content":         map[string]any{"text": strings.Repeat("t", 4096)},
		"attachments":     atts,
		"metadata":        map[string]any{"k": strings.Repeat("m", 8000)},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: %d %s", resp.StatusCode, out)
	}
	step(t, env)
	posts := wantPosts(t, cb, 1)
	if len(posts[0].Body) > hub.MaxPushBody {
		t.Fatalf("legal delivery body %d bytes", len(posts[0].Body))
	}
	var whole struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(posts[0].Body, &whole); err != nil {
		t.Fatal(err)
	}
	if err := venv.Validate("event", whole.Data); err != nil {
		t.Fatalf("legal delivery is not a whole event: %v", err)
	}

	// A synthetic event over the cap: a spilled message whose logged bytes
	// grew past 256 KiB.
	text := "é" + strings.Repeat("spilled text ", 1000)
	dly := send(t, env, sender, dot, "spill", "wake", text)
	var seq int64
	var data string
	if err := env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE dly_id = ?`, dly).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT data FROM events WHERE seq = ?`, seq).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	d["message"].(map[string]any)["metadata"] = map[string]any{"pad": strings.Repeat("x", 300<<10)}
	grown, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE events SET data = ? WHERE seq = ?`, string(grown), seq); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	posts = wantPosts(t, cb, 2)
	p := posts[1]
	if len(p.Body) > hub.MaxPushBody {
		t.Fatalf("preview body %d bytes", len(p.Body))
	}
	var body struct {
		EventID string `json:"eventId"`
		Cursor  string `json:"cursor"`
		Data    struct {
			DeliveryID string `json:"delivery_id"`
			MessageID  string `json:"message_id"`
			ThreadID   string `json:"thread_id"`
			Preview    string `json:"preview"`
			BlobURL    string `json:"blob_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(p.Body, &body); err != nil {
		t.Fatal(err)
	}
	if p.ID != dly || body.EventID != dly || body.Data.DeliveryID != dly || body.Cursor == "" {
		t.Fatalf("preview ids = %+v", body)
	}
	if !strings.HasPrefix(body.Data.MessageID, "msg_") || !strings.HasPrefix(body.Data.ThreadID, "thr_") {
		t.Fatalf("preview ids = %+v", body.Data)
	}
	if body.Data.BlobURL == "" {
		t.Fatalf("preview has no blob_url: %s", p.Body[:min(len(p.Body), 400)])
	}
	if n := len(body.Data.Preview); n == 0 || n > 1024 || !strings.HasPrefix(text, body.Data.Preview) {
		t.Fatalf("preview is %d bytes of %q...", n, body.Data.Preview[:min(n, 40)])
	}
}

func TestTwoSubscriptionsSamePeer(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	a, b := testhub.NewCallback(t), testhub.NewCallback(t)
	subscribe(t, env, a, dot, nil, time.Time{})
	subscribe(t, env, b, dot, nil, time.Time{})
	dly := send(t, env, sender, dot, "both", "wake", "x")
	step(t, env)
	if pa, pb := wantPosts(t, a, 1), wantPosts(t, b, 1); pa[0].ID != dly || pb[0].ID != dly {
		t.Fatalf("POSTs = %s, %s; want %s twice", pa[0].ID, pb[0].ID, dly)
	}
}

func TestThreadFilter(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	everything := testhub.NewCallback(t)
	subscribe(t, env, everything, dot, nil, time.Time{})

	first := send(t, env, sender, dot, "t1", "wake", "thread one")
	var thr string
	if err := env.Hub.DB.QueryRow(`SELECT thr_id FROM deliveries WHERE dly_id = ?`, first).Scan(&thr); err != nil {
		t.Fatal(err)
	}
	narrow := testhub.NewCallback(t)
	subscribe(t, env, narrow, dot, map[string]any{"thread_id": thr}, time.Time{})

	send(t, env, sender, dot, "t2", "wake", "another thread")
	resp, out := env.Do(sender, "POST", "/v1/messages", map[string]any{
		"idempotency_key": "t1-again", "priority": "wake", "thread_id": thr,
		"content": map[string]any{"text": "thread one again"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: %d %s", resp.StatusCode, out)
	}
	step(t, env)
	wantPosts(t, everything, 3)
	posts := wantPosts(t, narrow, 1)
	var body struct {
		Data venv.Event `json:"data"`
	}
	if err := json.Unmarshal(posts[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body.Data.Data), "thread one again") {
		t.Fatalf("narrow got %s", body.Data.Data)
	}
}

// A delivery that expires with its row in the outbox is not pushed: its sender
// has been told it expired (PROTOCOL §5.1). The row closes and the next one
// goes out.
func TestExpiredRowNotPosted(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	doomed := send(t, env, sender, dot, "doomed", "wake", "a")
	step(t, env)
	wantPosts(t, cb, 1)
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET expires_at = ? WHERE dly_id = ?`,
		venv.FormatTime(env.Now.Now().Add(-time.Hour)), doomed); err != nil {
		t.Fatal(err)
	}
	if n, err := env.Hub.ExpireDeliveries(context.Background(), env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("ExpireDeliveries = %d, %v", n, err)
	}
	next := send(t, env, sender, dot, "next", "wake", "b")
	env.Now.Advance(time.Hour)
	step(t, env)
	if posts := wantPosts(t, cb, 2); posts[1].ID != next {
		t.Fatalf("POST %s, want %s; the expired delivery must not go out", posts[1].ID, next)
	}
	if _, done, terminal := outbox(t, env, sub.ID, doomed); !done || terminal != "gated" {
		t.Fatalf("outbox done %v terminal %q, want gated", done, terminal)
	}
	got, err := env.Hub.DeliveryGate(context.Background(), doomed)
	if err != nil || got {
		t.Fatalf("DeliveryGate(expired) = %v, %v", got, err)
	}
}

// Expiry while a subscription is paused is caught the same way on resume.
func TestExpiredWhilePausedNotPosted(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, env.Now.Now().Add(time.Hour))
	cb.OnPost(func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	doomed := send(t, env, sender, dot, "doomed", "wake", "a")
	step(t, env)
	wantPosts(t, cb, 1)
	env.Now.Advance(2 * time.Hour)
	step(t, env)
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state = 'expired' WHERE dly_id = ?`, doomed); err != nil {
		t.Fatal(err)
	}
	refresh(t, env, sub, env.Now.Now().Add(time.Hour))
	step(t, env)
	wantPosts(t, cb, 1)
}

func TestGateRefusesTerminalStates(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	dly := send(t, env, sender, dot, "one", "wake", "a")
	ctx := context.Background()
	for state, want := range map[string]bool{
		"queued": true, "pushed": true, "accepted": true, "processed": true,
		"expired": false, "dead_letter": false, "failed": false,
	} {
		if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state = ? WHERE dly_id = ?`, state, dly); err != nil {
			t.Fatal(err)
		}
		if got, err := env.Hub.DeliveryGate(ctx, dly); err != nil || got != want {
			t.Fatalf("DeliveryGate(%s) = %v, %v; want %v", state, got, err, want)
		}
	}
}

// The token can expire during a drain: each send re-checks it against the
// current time (PROTOCOL §4.4), and what is left stays queued.
func TestTokenExpiresMidDrain(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, env.Now.Now().Add(time.Hour))
	cb.OnPost(func(n int, _ http.ResponseWriter) {
		if n == 1 {
			env.Now.Advance(2 * time.Hour)
		}
	})
	send(t, env, sender, dot, "one", "wake", "a")
	second := send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, cb, 1)
	if got := get(t, env, sub.ID); got.PausedReason != "token_expired" {
		t.Fatalf("paused = %q, want token_expired", got.PausedReason)
	}
	if _, done, _ := outbox(t, env, sub.ID, second); done {
		t.Fatal("the unsent event was settled")
	}
	refresh(t, env, sub, env.Now.Now().Add(time.Hour))
	step(t, env)
	if posts := wantPosts(t, cb, 2); posts[1].ID != second {
		t.Fatalf("resumed with %s, want %s", posts[1].ID, second)
	}
}

// A refresh during a drain counts: the fresh token is not paused for the old
// one's expiry, and the drain carries on.
func TestRefreshMidDrainKeepsSending(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, env.Now.Now().Add(5*time.Second))
	cb.OnPost(func(n int, _ http.ResponseWriter) {
		if n != 1 {
			return
		}
		env.Now.Advance(10 * time.Second)
		if _, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
			PrincipalKind: sub.PrincipalKind, PrincipalID: sub.PrincipalID, PeerID: sub.PeerID, Name: sub.Name,
			URL: sub.URL, Arguments: sub.Arguments, Types: sub.Types,
		}, webhook.NewSecret(), 0, env.Now.Now().Add(time.Hour)); err != nil {
			t.Error(err)
		}
		// The token had lapsed, so this refresh owes a challenge, and passes it.
		if err := env.Events.Store.MarkVerified(context.Background(), sub.ID); err != nil {
			t.Error(err)
		}
	})
	send(t, env, sender, dot, "one", "wake", "a")
	send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, cb, 2)
	if got := get(t, env, sub.ID); got.PausedReason != "" {
		t.Fatalf("paused = %q, want none", got.PausedReason)
	}
}

// So can the subscription itself lapse: it stops sending, as Active would
// have it, without a pause reason, and a refresh picks up the rest.
func TestSubscriptionLapsesMidDrain(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET expires_at = ? WHERE sub_id = ?`,
		venv.FormatTime(env.Now.Now().Add(5*time.Second)), sub.ID); err != nil {
		t.Fatal(err)
	}
	cb.OnPost(func(n int, _ http.ResponseWriter) {
		if n == 1 {
			env.Now.Advance(10 * time.Second) // inside the drain budget
		}
	})
	send(t, env, sender, dot, "one", "wake", "a")
	second := send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, cb, 1)
	if got := get(t, env, sub.ID); got.PausedReason != "" {
		t.Fatalf("paused = %q, want none", got.PausedReason)
	}
	refresh(t, env, sub, time.Time{})
	step(t, env)
	if posts := wantPosts(t, cb, 2); posts[1].ID != second {
		t.Fatalf("resumed with %s, want %s", posts[1].ID, second)
	}
}

// An unsubscribe during a drain stops it.
func TestUnsubscribeMidDrain(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(n int, _ http.ResponseWriter) {
		if n == 1 {
			if err := env.Events.Store.Delete(context.Background(), sub.PrincipalKind, sub.PrincipalID, sub.URL, sub.Name, sub.Arguments); err != nil {
				t.Error(err)
			}
		}
	})
	send(t, env, sender, dot, "one", "wake", "a")
	send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, cb, 1)
}

// One subscription may not hold a Step for its whole batch: a callback that
// answers slowly yields after the budget, and the rest wait for the next Step.
func TestDrainBudget(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(int, http.ResponseWriter) { env.Now.Advance(events.ClientTimeout) })
	send(t, env, sender, dot, "one", "wake", "a")
	send(t, env, sender, dot, "two", "wake", "b")
	if n := step(t, env); n != 1 {
		t.Fatalf("first Step sent %d, want 1", n)
	}
	wantPosts(t, cb, 1)
	step(t, env)
	wantPosts(t, cb, 2)
}

func TestAuthorisationRecheckedBeforeSend(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})

	ctx := context.Background()
	send(t, env, sender, dot, "one", "wake", "a")
	if err := env.Hub.SetPeerDisabled(ctx, dot, true); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	wantPosts(t, cb, 0)
	if got := get(t, env, sub.ID); got.PausedReason != "unauthorized" {
		t.Fatalf("paused = %q, want unauthorized", got.PausedReason)
	}
	if err := env.Hub.SetPeerDisabled(ctx, dot, false); err != nil {
		t.Fatal(err)
	}
	if err := env.Events.Store.Resume(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	wantPosts(t, cb, 1)

	// An OAuth subscription also needs its principal still bound to the peer.
	oauth := testhub.NewCallback(t)
	osub := subscribe(t, env, oauth, dot, nil, env.Now.Now().Add(time.Hour))
	bound := dot
	env.Events.ResolveDotPeer = func(context.Context, string) (string, error) { return bound, nil }
	send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, oauth, 1)
	bound = ""
	send(t, env, sender, dot, "three", "wake", "c")
	step(t, env)
	wantPosts(t, oauth, 1)
	if got := get(t, env, osub.ID); got.PausedReason != "unauthorized" {
		t.Fatalf("oauth paused = %q, want unauthorized", got.PausedReason)
	}
}

func TestEventPushAcceptsDelivery(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	sender, senderSecret := env.NewPeer("session", "s")
	a, b := testhub.NewCallback(t), testhub.NewCallback(t)
	subscribe(t, env, a, dot, nil, time.Time{})
	subscribe(t, env, b, dot, nil, time.Time{})
	dly := send(t, env, senderSecret, dot, "ok", "wake", "x")
	step(t, env)

	var state string
	if err := env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id = ?`, dly).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("delivery state = %q, want accepted on a 2xx (PROTOCOL §5.1)", state)
	}
	evs, err := env.Hub.Log.Read(context.Background(), []string{sender}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var receipts []venv.Receipt
	for _, e := range evs {
		if e.Type == "receipt" {
			var r venv.Receipt
			if err := json.Unmarshal(e.Data, &r); err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, r)
		}
	}
	if len(receipts) != 1 || receipts[0].DeliveryID != dly || receipts[0].State != "accepted" || receipts[0].By != "hub" {
		t.Fatalf("receipts = %+v, want one accepted by hub", receipts)
	}
}

func TestSecretSealedAtRest(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	cb := testhub.NewCallback(t)
	st := env.Events.Store
	secret := webhook.NewSecret()
	s, created, err := st.Upsert(context.Background(), events.Subscription{
		PrincipalKind: "vecnet", PrincipalID: dot, PeerID: dot, Name: events.Name, URL: cb.URL,
	}, secret, 0, time.Time{})
	if err != nil || !created {
		t.Fatalf("Upsert: %v, created %v", err, created)
	}
	var sealed string
	if err := env.Hub.DB.QueryRow(`SELECT secret_enc FROM event_subscriptions WHERE sub_id = ?`, s.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "whsec_") {
		t.Fatalf("secret stored in the clear: %q", sealed)
	}
	plain, err := env.Hub.Unseal(sealed)
	if err != nil || string(plain) != secret {
		t.Fatalf("Hub.Unseal = %q, %v", plain, err)
	}
}

func TestUpsertKey(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	cb := testhub.NewCallback(t)
	st := env.Events.Store
	ctx := context.Background()
	base := events.Subscription{PrincipalKind: "vecnet", PrincipalID: dot, PeerID: dot, Name: events.Name, URL: cb.URL,
		Arguments: map[string]any{"a": 1, "b": 2}}
	first, created, err := st.Upsert(ctx, base, webhook.NewSecret(), time.Hour, time.Time{})
	if err != nil || !created {
		t.Fatalf("first: %v, %v", created, err)
	}
	if !first.Expires.Equal(env.Now.Now().Add(time.Hour)) {
		t.Fatalf("expires = %v", first.Expires)
	}
	same := base
	same.Arguments = map[string]any{"b": 2, "a": 1}
	second, created, err := st.Upsert(ctx, same, webhook.NewSecret(), 48*time.Hour, time.Time{})
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("same key: %+v, %v, %v", second, created, err)
	}
	if !second.Expires.Equal(env.Now.Now().Add(24 * time.Hour)) {
		t.Fatalf("expires = %v, want the 24 h clamp", second.Expires)
	}
	if got := len(second.Types); got != 1 || second.Types[0] != "delivery" {
		t.Fatalf("default types = %v", second.Types)
	}
	otherArgs := base
	otherArgs.Arguments = map[string]any{"a": 1}
	third, created, err := st.Upsert(ctx, otherArgs, webhook.NewSecret(), 0, time.Time{})
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("other arguments: %+v, %v, %v", third, created, err)
	}
	list, err := st.ListForPrincipal(ctx, "vecnet", dot)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListForPrincipal = %+v, %v", list, err)
	}
	if err := st.Delete(ctx, "vecnet", dot, cb.URL, events.Name, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "vecnet", dot, cb.URL, events.Name, map[string]any{"a": 1}); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := st.Get(ctx, third.ID); err == nil {
		t.Fatal("deleted subscription still found")
	}
	active, err := st.Active(ctx, env.Now.Now().Add(25*time.Hour))
	if err != nil || len(active) != 0 {
		t.Fatalf("Active after expiry = %+v, %v", active, err)
	}
}

func TestChallengeRefusedByGuard(t *testing.T) {
	policy := egress.Policy{Resolver: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.1")}, nil
	}}
	err := events.Challenge(context.Background(), policy.Client(2*time.Second),
		events.Subscription{ID: "sub_X", URL: "https://rebind.example/hook"}, webhook.NewSecret(), time.Now())
	var failed events.ErrChallenge
	if !errors.As(err, &failed) || failed.Reason != "invalid_url" {
		t.Fatalf("Challenge = %v, want invalid_url", err)
	}
}

// A cursor moved forward means the client already has what lies before it:
// unsettled rows there go, and what lies after it is read again.
func TestSetCursorDropsWhatTheClientHas(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	first := send(t, env, sender, dot, "one", "wake", "a")
	second := send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, cb, 1) // the first is in backoff, the second waits behind it

	var seq int64
	if err := env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE dly_id = ?`, first).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := env.Events.Store.SetCursor(context.Background(), sub.ID, eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: seq}, true); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	if posts := wantPosts(t, cb, 2); posts[1].ID != second {
		t.Fatalf("next POST %s, want %s", posts[1].ID, second)
	}
}

func TestPurgedCursorIsReportedOnNextSubscribe(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	env.Now.Advance(25 * time.Hour)
	dly := send(t, env, sender, dot, "lost", "wake", "a")
	var seq int64
	if err := env.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE dly_id = ?`, dly).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`DELETE FROM events WHERE seq <= ?`, seq); err != nil {
		t.Fatal(err)
	}
	refresh(t, env, sub, time.Time{})
	step(t, env)
	wantPosts(t, cb, 0)
	got := get(t, env, sub.ID)
	if !got.Truncated || got.Cursor.Seq < seq {
		t.Fatalf("after a purge past the cursor: %+v, want truncated at or past seq %d", got, seq)
	}
}

func TestOutboxDoneRowsPruned(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	send(t, env, sender, dot, "one", "wake", "x")
	step(t, env)
	count := func() (n int) {
		t.Helper()
		if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM event_outbox WHERE sub_id = ?`, sub.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("outbox rows = %d", n)
	}
	env.Now.Advance(25 * time.Hour)
	step(t, env)
	if n := count(); n != 0 {
		t.Fatalf("outbox rows after a day = %d, want pruned", n)
	}
}

func TestRunDelivers(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})
	got := make(chan struct{}, 1)
	cb.OnPost(func(int, http.ResponseWriter) {
		select {
		case got <- struct{}{}:
		default:
		}
	})
	env.StartWorkers()
	send(t, env, sender, dot, "one", "wake", "x")
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("StartWorkers did not deliver")
	}
}

// A subscription stored while private egress was allowed is never reached by
// a default Worker on a strict hub.
func TestDefaultEventsClientRefusesLoopback(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})
	env.Hub.Cfg.AllowPrivateEgress = false
	w := &events.Worker{Hub: env.Hub, Now: env.Now.Now}
	send(t, env, sender, dot, "one", "wake", "x")
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := cb.Requests(); n != 0 {
		t.Fatalf("strict hub's default events client reached a loopback callback: %d requests", n)
	}
}

// report sets the peer's presence as a client would.
func report(t *testing.T, env *testhub.Env, secret, state string) {
	t.Helper()
	resp, out := env.Do(secret, "POST", "/v1/presence", map[string]any{"state": state, "at": venv.FormatTime(env.Now.Now())})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("presence %s: %d %s", state, resp.StatusCode, out)
	}
}

// PROTOCOL §5.2, R16: a held wake_idle is pushed once when presence later
// becomes idle or stale, however many Steps follow.
func TestHeldPushedOnceOnRelease(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(t *testing.T, env *testhub.Env, dotSecret string)
	}{
		{"idle report", func(t *testing.T, env *testhub.Env, dotSecret string) { report(t, env, dotSecret, "idle") }},
		{"stale presence", func(t *testing.T, env *testhub.Env, _ string) {
			env.Now.Advance(hub.DefaultSessionPresenceTTL + time.Minute)
			env.Hub.Tick(context.Background(), false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			dot, dotSecret := env.NewPeer("session", "d")
			_, sender := env.NewPeer("session", "s")
			cb := testhub.NewCallback(t)
			sub := subscribe(t, env, cb, dot, nil, time.Time{})

			report(t, env, dotSecret, "working")
			held := send(t, env, sender, dot, "held", "wake_idle", "held while working")
			after := send(t, env, sender, dot, "after", "wake", "not waiting behind it")
			step(t, env)
			if posts := wantPosts(t, cb, 1); posts[0].ID != after {
				t.Fatalf("pushed %s, want only %s", posts[0].ID, after)
			}
			step(t, env)
			wantPosts(t, cb, 1)

			tc.release(t, env, dotSecret)
			step(t, env)
			posts := wantPosts(t, cb, 2)
			if posts[1].ID != held {
				t.Fatalf("pushed %s, want the released %s", posts[1].ID, held)
			}
			step(t, env)
			wantPosts(t, cb, 2)
			if _, done, term := outbox(t, env, sub.ID, held); !done || term != "" {
				t.Fatalf("outbox row: done %v, terminal %q", done, term)
			}
		})
	}
}

// A client that names a cursor past a parked row has not received it, and the
// subscribe result never points back at it: parked rows sit outside the cursor.
func TestParkedRowSurvivesClientCursor(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	report(t, env, dotSecret, "working")
	held := send(t, env, sender, dot, "held", "wake_idle", "held")
	send(t, env, sender, dot, "after", "wake", "after")
	step(t, env)
	last := get(t, env, sub.ID).Cursor
	if got, err := env.Events.Store.ReplayCursor(context.Background(), sub.ID, last); err != nil || got != last {
		t.Fatalf("ReplayCursor = %v, %v; a parked row must not hold it back from %v", got, err, last)
	}
	if err := env.Events.Store.SetCursor(context.Background(), sub.ID, last, true); err != nil {
		t.Fatal(err)
	}
	report(t, env, dotSecret, "idle")
	step(t, env)
	if posts := wantPosts(t, cb, 2); posts[1].ID != held {
		t.Fatalf("pushed %s, want %s", posts[1].ID, held)
	}
}

// Held rows take no place in a drain batch, so they cannot starve what follows.
func TestParkedRowsDoNotStarveTheBatch(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	subscribe(t, env, cb, dot, nil, time.Time{})
	report(t, env, dotSecret, "working")
	env.SetLimits(peerOf(t, env, sender), 1000, 1000)
	for i := range 60 {
		send(t, env, sender, dot, fmt.Sprintf("held-%d", i), "wake_idle", "held")
	}
	step(t, env)
	wake := send(t, env, sender, dot, "wake", "wake", "goes now")
	step(t, env)
	step(t, env)
	if posts := wantPosts(t, cb, 1); posts[0].ID != wake {
		t.Fatalf("pushed %s, want %s", posts[0].ID, wake)
	}
}

func peerOf(t *testing.T, env *testhub.Env, secret string) string {
	t.Helper()
	c, err := env.Hub.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return c.PeerID
}

// A held delivery the dot has already acked, or that expired, needs no push.
func TestParkedRowClosesWithoutPush(t *testing.T) {
	env := testhub.Start(t)
	dot, dotSecret := env.NewPeer("session", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	report(t, env, dotSecret, "working")
	acked := send(t, env, sender, dot, "acked", "wake_idle", "read through the inbox")
	lapsed := send(t, env, sender, dot, "lapsed", "wake_idle", "expires unread")
	step(t, env)

	cur, err := env.Hub.Log.Current(context.Background(), []string{dot})
	if err != nil {
		t.Fatal(err)
	}
	first := cur
	first.Seq-- // the acked delivery only
	resp, out := env.Do(dotSecret, "POST", "/v1/ack", map[string]string{"cursor": first.String()})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack: %d %s", resp.StatusCode, out)
	}
	step(t, env)
	wantPosts(t, cb, 0)
	if _, done, term := outbox(t, env, sub.ID, acked); !done || term != "gated" {
		t.Fatalf("acked row: done %v, terminal %q", done, term)
	}

	if _, err := env.Hub.ExpireDeliveries(context.Background(), env.Now.Now().Add(365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	step(t, env)
	wantPosts(t, cb, 0)
	if _, done, term := outbox(t, env, sub.ID, lapsed); !done || term != "gated" {
		t.Fatalf("expired row: done %v, terminal %q", done, term)
	}
}

// A subscription paused while a drain runs stops at the next row, as does one
// whose challenge fell due.
func TestPausedMidDrainStops(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, time.Time{})
	cb.OnPost(func(n int, _ http.ResponseWriter) {
		if n == 1 {
			if err := env.Events.Store.Pause(context.Background(), sub.ID); err != nil {
				t.Error(err)
			}
		}
	})
	send(t, env, sender, dot, "one", "wake", "a")
	send(t, env, sender, dot, "two", "wake", "b")
	step(t, env)
	wantPosts(t, cb, 1)
}

func TestSubscriptionCapPerPrincipal(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	cb := testhub.NewCallback(t)
	upsert := func(path string, ttl time.Duration) error {
		_, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{
			PrincipalKind: "vecnet", PrincipalID: dot, PeerID: dot, Name: events.Name, URL: cb.URL + path,
		}, webhook.NewSecret(), ttl, time.Time{})
		return err
	}
	for i := range events.MaxPerPrincipal {
		if err := upsert(fmt.Sprintf("/%d", i), time.Hour); err != nil {
			t.Fatalf("subscription %d: %v", i, err)
		}
	}
	if err := upsert("/over", time.Hour); !errors.Is(err, events.ErrTooMany) {
		t.Fatalf("one past the cap = %v, want ErrTooMany", err)
	}
	if err := upsert("/0", time.Hour); err != nil { // a refresh adds nothing
		t.Fatalf("refresh at the cap: %v", err)
	}
	env.Now.Advance(2 * time.Hour) // lapsed rows do not count
	if err := upsert("/over", time.Hour); err != nil {
		t.Fatalf("after the others lapsed: %v", err)
	}
}

// An OAuth subscription needs the binding seam: without it the worker cannot
// tell who holds the dot now, so it sends nothing.
func TestOAuthSubscriptionFailsClosedWithoutSeam(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "d")
	_, sender := env.NewPeer("session", "s")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, cb, dot, nil, env.Now.Now().Add(time.Hour))
	env.Events.ResolveDotPeer = nil
	send(t, env, sender, dot, "one", "wake", "a")
	step(t, env)
	wantPosts(t, cb, 0)
	if got := get(t, env, sub.ID); got.PausedReason != events.PausedUnauthorized {
		t.Fatalf("paused = %q, want %q", got.PausedReason, events.PausedUnauthorized)
	}
}

// TestReactionWakeInFlightNotWithdrawn pins the dispatch claim (PROTOCOL
// §17.5): a remove that lands while the wake's callback POST is in flight
// cannot withdraw it, since superseded is never pushed. A failed attempt then
// settles the withdrawal, so no retry sends it; an accepted one stays accepted.
func TestReactionWakeInFlightNotWithdrawn(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			env := testhub.Start(t)
			owner, ownerSecret := env.NewPeer("session", "d")
			reactor, reactorSecret := env.NewPeer("session", "s")
			resp, out := env.Do(ownerSecret, "POST", "/v1/messages", map[string]any{
				"idempotency_key": "target", "priority": "hold",
				"recipients": []string{reactor}, "content": map[string]any{"text": "target"},
			})
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("send: %d %s", resp.StatusCode, out)
			}
			var target struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(out, &target); err != nil {
				t.Fatal(err)
			}
			cb := testhub.NewCallback(t)
			subscribe(t, env, cb, owner, map[string]any{"types": []string{"delivery"}}, time.Time{})
			react := func(op string) hub.ReactResponse {
				resp, out := env.Do(reactorSecret, "POST", "/v1/reactions", map[string]any{
					"message_id": target.MessageID, "emoji": "✅", "op": op, "priority": "wake",
				})
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s: %d %s", op, resp.StatusCode, out)
				}
				var r hub.ReactResponse
				if err := json.Unmarshal(out, &r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			wake := react("add").Wake
			if wake == nil {
				t.Fatal("no wake")
			}
			var during hub.ReactResponse
			cb.OnPost(func(n int, w http.ResponseWriter) {
				if n == 1 {
					during = react("remove")
				}
				w.WriteHeader(status)
			})
			step(t, env)
			wantPosts(t, cb, 1)
			if len(during.Superseded) != 0 {
				t.Fatalf("remove during the POST withdrew %v, want nothing while in flight", during.Superseded)
			}
			want := "superseded"
			if status == http.StatusOK {
				want = "accepted"
			}
			var state string
			if err := env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id = ?`, wake.DeliveryID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != want {
				t.Fatalf("after the attempt state = %s, want %s", state, want)
			}
			env.Now.Advance(time.Hour)
			step(t, env)
			wantPosts(t, cb, 1)
		})
	}
}
