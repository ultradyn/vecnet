package oauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

// subscribeDot subscribes the access token's dot to cb through /mcp and
// returns the subscription id.
func subscribeDot(t *testing.T, env *testhub.Env, access string, cb *testhub.Callback) string {
	t.Helper()
	resp, body := mcp(t, env, access, "events/subscribe", map[string]any{
		"name":      events.Name,
		"arguments": map[string]any{},
		"delivery":  map[string]any{"mode": "webhook", "url": cb.URL, "secret": webhook.NewSecret()},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events/subscribe: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if out.Error != nil || out.Result.ID == "" {
		t.Fatalf("events/subscribe: %s", body)
	}
	return out.Result.ID
}

// wakeDot sends one wake message to peer and runs the Events worker once.
func wakeDot(t *testing.T, env *testhub.Env, secret, peer, key string) {
	t.Helper()
	resp, body := env.Do(secret, "POST", "/v1/messages", map[string]any{
		"idempotency_key": key,
		"priority":        "wake",
		"recipients":      []string{peer},
		"content":         map[string]any{"text": "hello " + key},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s: %d %s", key, resp.StatusCode, body)
	}
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatalf("events worker Step: %v", err)
	}
}

// A dot subscribes through its OAuth token, and its subscription lasts only
// as long as the binding: losing the peer ends delivery at once, however long
// the access token has left to run (PROTOCOL §4.4, §4.5).
func TestSubscriptionFollowsBinding(t *testing.T) {
	for name, lose := range map[string]func(t *testing.T, env *testhub.Env, peer string){
		"link code starts a new account": func(t *testing.T, env *testhub.Env, peer string) {
			b := newBrowser(t, env)
			if got := b.exchange(b.code(verifierA, linkCode(t, env, peer)), verifierA); got.status != http.StatusOK {
				t.Fatalf("exchange: %d %s", got.status, got.Error)
			}
		},
		"operator moves the peer": func(t *testing.T, env *testhub.Env, peer string) {
			prn, _ := env.NewPrincipal("elsewhere")
			if err := env.Hub.MovePeer(context.Background(), peer, prn); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			_, peer, tok := login(t, env)
			_, sender := env.NewPeer("session", "sender")
			cb := testhub.NewCallback(t)
			id := subscribeDot(t, env, tok.AccessToken, cb)

			wakeDot(t, env, sender, peer, "bound")
			if n := len(cb.Posts()); n != 1 {
				t.Fatalf("bound: %d posts, want 1", n)
			}

			lose(t, env, peer)
			wakeDot(t, env, sender, peer, "lost")
			if n := len(cb.Posts()); n != 1 {
				t.Fatalf("after losing the peer: %d posts, want 1", n)
			}
			s, err := events.NewStore(env.Hub).Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if s.PausedReason != events.PausedUnauthorized {
				t.Fatalf("paused = %q, want %q", s.PausedReason, events.PausedUnauthorized)
			}
		})
	}
}

// Review Focus 3 through a real access token: the subscription pauses when
// the token expires, nothing is lost, and a refreshed token resumes it.
func TestRealTokenExpiryPausesSubscription(t *testing.T) {
	env := testhub.Start(t)
	b, peer, tok := login(t, env)
	_, sender := env.NewPeer("session", "sender")
	cb := testhub.NewCallback(t)
	id := subscribeDot(t, env, tok.AccessToken, cb)
	wakeDot(t, env, sender, peer, "live")
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("live: %d posts, want 1", n)
	}

	env.Now.Advance(2 * time.Hour)
	wakeDot(t, env, sender, peer, "expired")
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("after the token expired: %d posts, want 1", n)
	}
	s, err := events.NewStore(env.Hub).Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if s.PausedReason != events.PausedTokenExpired {
		t.Fatalf("paused = %q, want %q", s.PausedReason, events.PausedTokenExpired)
	}

	fresh := b.refresh(tok.RefreshToken)
	if fresh.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", fresh.status, fresh.Error)
	}
	if got := subscribeDot(t, env, fresh.AccessToken, cb); got != id {
		t.Fatalf("resubscribe id = %s, want %s", got, id)
	}
	if _, err := env.Events.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(cb.Posts()); n != 2 {
		t.Fatalf("after resubscribing: %d posts, want 2", n)
	}
}
