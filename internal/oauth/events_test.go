package oauth_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/mcpserver"
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

// A thief who refreshes a stolen token and subscribes their own URL loses that
// subscription when reuse detection fires, not when the token would expire.
func TestFamilyRevocationStopsSubscription(t *testing.T) {
	env := testhub.Start(t)
	b, peer, tok := login(t, env)
	_, sender := env.NewPeer("session", "sender")
	stolen := b.refresh(tok.RefreshToken)
	if stolen.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", stolen.status, stolen.Error)
	}
	cb := testhub.NewCallback(t)
	id := subscribeDot(t, env, stolen.AccessToken, cb)
	wakeDot(t, env, sender, peer, "before")
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("before: %d posts, want 1", n)
	}

	wantError(t, b.refresh(tok.RefreshToken), "invalid_grant") // the victim's replay
	wakeDot(t, env, sender, peer, "after")
	if n := len(cb.Posts()); n != 1 {
		t.Fatalf("after the family was revoked: %d posts, want 1", n)
	}
	s, err := events.NewStore(env.Hub).Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if s.PausedReason != events.PausedUnauthorized {
		t.Fatalf("paused = %q, want %q", s.PausedReason, events.PausedUnauthorized)
	}
	resp, body := env.Do(env.OperatorSecret, "GET", "/v1/admin/audit?limit=20", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "oauth_token_reuse") {
		t.Fatalf("audit: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), tok.RefreshToken) {
		t.Fatal("audit holds a token")
	}
}

// streamUntilEnd opens one /mcp stream with access and reports every frame
// body on frames, closing it when the hub ends the response.
func streamUntilEnd(t *testing.T, env *testhub.Env, method, access string, headers map[string]string, body any) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, env.URL+"/mcp", r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	frames := make(chan string, 64)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				frames <- data
			}
		}
	}()
	return frames
}

func listenBody(uri string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 7, "method": "subscriptions/listen", "params": map[string]any{
		"_meta":         map[string]any{"io.modelcontextprotocol/protocolVersion": mcpserver.ProtoCurrent},
		"notifications": map[string]any{"resourceSubscriptions": []string{uri}},
	}}
}

var listenHeaders = map[string]string{
	"MCP-Protocol-Version": mcpserver.ProtoCurrent,
	"Accept":               "application/json, text/event-stream",
}

// wantEnds drains frames until the stream closes, failing on a result (the
// client must treat this as a reason to reconnect) or a hint.
func wantEnds(t *testing.T, frames <-chan string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, open := <-frames:
			if !open {
				return
			}
			if strings.Contains(f, `"result"`) || strings.Contains(f, "notifications/resources/updated") {
				t.Fatalf("frame after the credential ended: %s", f)
			}
		case <-deadline:
			t.Fatal("stream still open after its credential ended")
		}
	}
}

// PROTOCOL §4.5: a stream's credential is checked on each wake and heartbeat,
// so a dead token cannot keep one open, whichever way it died.
func TestListenEndsWithItsToken(t *testing.T) {
	for name, end := range map[string]func(t *testing.T, env *testhub.Env, b *browser, tok tokens){
		"expiry": func(_ *testing.T, env *testhub.Env, _ *browser, _ tokens) { env.Now.Advance(2 * time.Hour) },
		"family revoked": func(t *testing.T, _ *testhub.Env, b *browser, tok tokens) {
			if got := b.refresh(tok.RefreshToken); got.status != http.StatusOK {
				t.Fatalf("refresh: %d %s", got.status, got.Error)
			}
			wantError(t, b.refresh(tok.RefreshToken), "invalid_grant")
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 20 * time.Millisecond })
			b, peer, tok := login(t, env)
			frames := streamUntilEnd(t, env, http.MethodPost, tok.AccessToken, listenHeaders, listenBody("vecnet://peer/"+peer+"/inbox"))
			select {
			case f := <-frames:
				if !strings.Contains(f, "notifications/subscriptions/acknowledged") {
					t.Fatalf("first frame = %s", f)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no acknowledgement")
			}
			end(t, env, b, tok)
			wantEnds(t, frames)
		})
	}
}

// The 2025 GET stream ends the same way, and the peer stops showing connected.
func TestGetStreamEndsWithItsToken(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 20 * time.Millisecond })
	_, _, tok := login(t, env)
	resp, _ := mcpInitialize(t, env, tok.AccessToken)
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no session id")
	}
	frames := streamUntilEnd(t, env, http.MethodGet, tok.AccessToken, map[string]string{
		"Mcp-Session-Id": sid, "Accept": "text/event-stream",
	}, nil)
	env.Now.Advance(2 * time.Hour)
	wantEnds(t, frames)
}

func mcpInitialize(t *testing.T, env *testhub.Env, access string) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": mcpserver.ProtoLegacy, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "t", "version": "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize: %d %s", resp.StatusCode, body)
	}
	return resp, body
}
