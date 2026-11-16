package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/testhub"
)

func hookUser(t *testing.T, hub, secret string) *user {
	t.Helper()
	u := profileUser(t, hub, secret)
	// Legacy-hook admission canonicalizes the shared per-user authority even
	// for Claude fixtures. Never fall back to the build-box user's HOME.
	if err := cliFixtureMkdirAll(u.env["HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	u.env["XDG_CONFIG_HOME"] = filepath.Join(filepath.Dir(u.conf), "xdg")
	return u
}
func hookE2EContext(t *testing.T, out, event string) string {
	t.Helper()
	var body struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("invalid hook JSON %q: %v", out, err)
	}
	if body.HookSpecificOutput.HookEventName != event || strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("hook output shape %q", out)
	}
	return body.HookSpecificOutput.AdditionalContext
}
func TestHookPrintsDeliveries(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	a, b := hookUser(t, env.URL, aSec), hookUser(t, env.URL, bSec)
	a.mustRun("hook", "--event", "user-prompt")
	sent := b.send(aID, "hello hook", "--wake")
	a.env["XDG_CACHE_HOME"] = t.TempDir()
	out := a.mustRun("hook", "--event", "user-prompt")
	ctx := hookE2EContext(t, out, "UserPromptSubmit")
	for _, s := range []string{sent.MessageID, `text="hello hook"`, `untrusted="true"`} {
		if !strings.Contains(ctx, s) {
			t.Fatalf("context lacks %q: %q", s, ctx)
		}
	}
	if again := a.mustRun("hook", "--event", "user-prompt"); again != "" {
		t.Fatalf("repeat output %q", again)
	}
	a.mustRun("reply", sent.MessageID, "ok", "--hold")
}
func TestHookEventFromStdin(t *testing.T) {
	env := testhub.Start(t)
	id, sec := env.NewPeer("session", "A")
	_, senderSec := env.NewPeer("session", "B")
	a, b := hookUser(t, env.URL, sec), hookUser(t, env.URL, senderSec)
	a.mustRun("hook", "--event", "user-prompt")
	b.send(id, "stdin hook", "--hold")
	// A fresh state avoids the preceding user-prompt rate timestamp and
	// exercises a first-run summary for the stdin PostToolUse event.
	fresh := hookUser(t, env.URL, sec)
	var out, errs bytes.Buffer
	code := cli.RunContext(context.Background(), []string{"hook"}, strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`), &out, &errs, fresh.getenv)
	if code != 0 {
		t.Fatalf("stdin exit=%d stderr=%q", code, errs.String())
	}
	ctx := hookE2EContext(t, out.String(), "PostToolUse")
	if !strings.Contains(ctx, "1 unacked deliveries waiting") {
		t.Fatalf("stdin summary=%q", ctx)
	}
}
func TestHookSilentWhenHubDown(t *testing.T) {
	// Close a freshly allocated loopback listener to obtain an unavailable hub.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	a := hookUser(t, "http://"+addr, "vn_peer_test")
	start := time.Now()
	out := a.mustRun("hook", "--event", "user-prompt")
	if out != "" || time.Since(start) >= 2*time.Second {
		t.Fatalf("hub down elapsed=%s out=%q", time.Since(start), out)
	}
}
func TestHookSkipsNonDeliveries(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a, b := hookUser(t, env.URL, aSec), hookUser(t, env.URL, bSec)
	sent := a.send(bID, "for receipt", "--hold")
	a.mustRun("hook", "--event", "user-prompt")
	b.mustRun("receipt", sent.Deliveries[0].DeliveryID, "read")
	b.mustRun("presence", "set", "working")
	// A subscribes explicitly so presence is part of the polled event set.
	a.mustRun("presence", "subscribe", "all")
	b.mustRun("presence", "set", "idle")
	evs := a.inboxJSON()
	receipt, presence := false, false
	for _, ev := range evs {
		receipt = receipt || ev.Type == "receipt"
		presence = presence || ev.Type == "presence"
	}
	if !receipt || !presence {
		t.Fatalf("non-delivery fixture missing receipt=%t presence=%t", receipt, presence)
	}
	if out := a.mustRun("hook", "--event", "user-prompt"); out != "" {
		t.Fatalf("non-deliveries printed %q", out)
	}
	b.send(aID, "later delivery", "--hold")
	if ctx := hookE2EContext(t, a.mustRun("hook", "--event", "user-prompt"), "UserPromptSubmit"); !strings.Contains(ctx, "later delivery") {
		t.Fatalf("later delivery lost: %q", ctx)
	}
}
func TestRestartReplaysHookShown(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	a, b := hookUser(t, env.URL, aSec), hookUser(t, env.URL, bSec)
	a.mustRun("hook", "--event", "user-prompt")
	b.send(aID, "shown by hook", "--hold")
	if ctx := hookE2EContext(t, a.mustRun("hook", "--event", "user-prompt"), "UserPromptSubmit"); !strings.Contains(ctx, "shown by hook") {
		t.Fatal("hook did not show delivery")
	}
	out, stop := startWatch(t, a, "watch", "--for-monitor")
	waitLine(t, out, regexp.MustCompile(`shown by hook$`), 5*time.Second)
	stop()
	if strings.Count(out.String(), "shown by hook") != 1 {
		t.Fatalf("replay=%q", out.String())
	}
}
func TestHookAndWatchShowOnce(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	target, err := url.Parse(env.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	live := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events" && r.Header.Get("Accept") == "text/event-stream" {
			select {
			case live <- struct{}{}:
			default:
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	a, b := hookUser(t, srv.URL, aSec), hookUser(t, env.URL, bSec)
	a.mustRun("hook", "--event", "user-prompt")
	out, stop := startWatch(t, a, "watch", "--for-monitor")
	defer stop()
	select {
	case <-live:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not finish replay")
	}
	var contexts strings.Builder
	for i := 1; i <= 6; i++ {
		msg := fmt.Sprintf("m%d", i)
		b.send(aID, msg, "--hold")
		raw := a.mustRun("hook", "--event", "user-prompt")
		if raw != "" {
			contexts.WriteString(hookE2EContext(t, raw, "UserPromptSubmit"))
			contexts.WriteByte('\n')
		}
	}
	// This later delivery is consumed only by watch. Ordered SSE handling
	// guarantees the six competing deliveries have passed through live dedupe.
	b.send(aID, "watch completion barrier", "--hold")
	waitLine(t, out, regexp.MustCompile(`watch completion barrier$`), 5*time.Second)
	stop()
	combined := out.String() + contexts.String()
	for i := 1; i <= 6; i++ {
		if n := strings.Count(combined, fmt.Sprintf("m%d", i)); n != 1 {
			t.Errorf("m%d appears %d times across watch and hook: %q", i, n, combined)
		}
	}
}

func TestHookCodexStdinShapes(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	a, b := hookUser(t, env.URL, aSec), hookUser(t, env.URL, bSec)
	a.env["CODEX_HOME"] = t.TempDir()
	a.env["CODEX_THREAD_ID"] = "01234567-89ab-cdef-0123-456789abcdef"
	first := b.send(aID, "codex hello", "--wake")
	second := b.send(aID, "codex second", "--hold")
	run := func(input string) {
		t.Helper()
		for _, args := range [][]string{{"hook", "--auto-ack"}, {"hook", "--client", "codex", "--auto-ack"}} {
			var out, errs bytes.Buffer
			if code := cli.RunContext(context.Background(), args, strings.NewReader(input), &out, &errs, a.getenv); code != 0 || out.Len() != 0 {
				t.Fatalf("Codex hook %v exit=%d output=%q stderr=%q", args, code, out.String(), errs.String())
			}
		}
	}
	run(`{"hook_event_name":"UserPromptSubmit","session_id":"01234567-89ab-cdef-0123-456789abcdef"}`)
	run(`{"session_id":"01234567-89ab-cdef-0123-456789abcdef","transcript_path":"/x","cwd":"/w","hook_event_name":"SessionStart","model":"m","source":"resume","permission_mode":"default"}`)
	raw, err := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": a.env["CODEX_THREAD_ID"], "tool_name": "shell_command", "tool_input": map[string]any{}, "tool_response": strings.Repeat("x", 900*1024)})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 1<<20 {
		t.Fatalf("fixture exceeds stdin cap: %d", len(raw))
	}
	// Parent session IDs are also inherited by ambiguous children: neither
	// SessionStart nor a large post-tool response authorizes peer context.
	run(string(raw))
	run(string(raw))
	statePath := filepath.Join(filepath.Dir(a.conf), "state", "p.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("Codex hook touched delivery state: %v", err)
	}
	events, err := json.Marshal(a.inboxJSON())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.MessageID, second.MessageID} {
		if !bytes.Contains(events, []byte(id)) {
			t.Fatalf("Codex hook acknowledged pending message %s: %s", id, events)
		}
	}
}
