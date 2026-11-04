package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

// openActivity connects to GET /v1/admin/activity and returns the response,
// whatever its status.
func openActivity(t *testing.T, env *testhub.Env, secret string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", env.URL+"/v1/admin/activity", nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return resp
}

func TestActivityFeedOperatorOnly(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "reader")
	_, principal := env.NewPrincipal("reader principal")
	resp := openActivity(t, env, "")
	body, _ := io.ReadAll(resp.Body)
	mustErr(t, resp, body, 401, "unauthenticated")
	for _, bearer := range []string{secret, principal} {
		resp := openActivity(t, env, bearer)
		body, _ := io.ReadAll(resp.Body)
		mustErr(t, resp, body, 403, "forbidden")
	}
}

// The feed carries metadata only: ids, sender, kind and time, never the body
// or the title. A rename is a thread entry, again without the title.
func TestActivityFeedMetadataOnly(t *testing.T) {
	env := testhub.Start(t)
	env.Configure(func(d *httpapi.Deps) { d.Heartbeat = 100 * time.Millisecond })
	a, asec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	resp := openActivity(t, env, env.OperatorSecret)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("activity = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	s := (&sseStream{resp: resp, frames: make(chan sseFrame, 64)}).start()
	if f := s.next(t, 10*time.Second); f.Retry != "3000" {
		t.Fatalf("first frame %+v", f)
	}
	if f := s.next(t, 10*time.Second); f.Event != "ready" || f.Data != "{}" || f.HasID {
		t.Fatalf("ready frame %+v", f)
	}

	const secretText, secretTitle = "SECRET-BODY-7f3a", "SECRET-TITLE-91c2"
	body := sendBody("activity-meta", b)
	body["content"] = map[string]any{"text": secretText}
	body["thread_title"] = secretTitle
	sent := postSend(t, env, asec, body, http.StatusAccepted)
	f := s.nextEvent(t, 10*time.Second)
	if f.Event != "message" || f.HasID {
		t.Fatalf("frame %+v, want event: message with no id", f)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(f.Data), &data); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"at", "kind", "message_id", "sender", "thread_id"}) {
		t.Fatalf("fields %v", keys)
	}
	if data["thread_id"] != sent.ThreadID || data["message_id"] != sent.MessageID || data["sender"] != a || data["kind"] != "message" {
		t.Fatalf("data %v", data)
	}
	if strings.Contains(f.Data, secretText) || strings.Contains(f.Data, secretTitle) {
		t.Fatalf("activity carries message text: %s", f.Data)
	}

	adminDo(t, env, "PATCH", "/v1/admin/threads/"+sent.ThreadID, map[string]any{"title": "SECRET-RENAME"}, 204)
	f = s.nextEvent(t, 10*time.Second)
	if f.Event != "thread" || !strings.Contains(f.Data, sent.ThreadID) || strings.Contains(f.Data, "SECRET") {
		t.Fatalf("rename frame %+v", f)
	}
	for {
		if f := s.next(t, 5*time.Second); f.Comment == "hb" {
			break
		}
	}
}

func TestActivityRestartFrame(t *testing.T) {
	env := testhub.Start(t)
	d := httpapi.NewDrain()
	env.Configure(func(deps *httpapi.Deps) { deps.Drain = d })
	resp := openActivity(t, env, env.OperatorSecret)
	if resp.StatusCode != 200 {
		t.Fatalf("activity = %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	if got := readRawFrame(t, br); got != "retry: 3000\n\n" {
		t.Fatalf("retry = %q", got)
	}
	if got := readRawFrame(t, br); got != "event: ready\ndata: {}\n\n" {
		t.Fatalf("ready = %q", got)
	}
	d.Announce(1500 * time.Millisecond)
	if got := readRawFrame(t, br); got != "event: hub_restarting\ndata: {\"retry_after_ms\":1500}\n\n" {
		t.Fatalf("restart = %q", got)
	}
	expectRawEOF(t, br)
	if !d.Wait(time.Second) {
		t.Fatal("activity did not leave drain")
	}
}
