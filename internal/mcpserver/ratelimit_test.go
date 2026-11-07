package mcpserver_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

// TestSendToolRateLimitMessage: the MCP tool error the model sees names the
// limit, its max, and when to retry, both in the text content and in the
// structured error message (PROTOCOL §16.2).
func TestSendToolRateLimitMessage(t *testing.T) {
	t.Run("sends_per_min", func(t *testing.T) {
		env := testhub.Start(t)
		a, aSec := env.NewPeer("session", "A")
		b, _ := env.NewPeer("session", "B")
		env.SetLimits(a, 1, 100)
		var sent sendResult
		toolOK(t, env, aSec, "vecnet_send", sendArgs("sends-ok", b, "one"), &sent)
		env.Now.Advance(19 * time.Second)

		assertToolRateMessage(t, env, aSec, sendArgs("sends-over", "", "two", sent.ThreadID),
			"per-peer rate limit exceeded: sends_per_min 1 (sends in the last minute); retry in 41s",
			"sends_per_min", 1, 41)
	})

	t.Run("threads_per_hour", func(t *testing.T) {
		env := testhub.Start(t)
		c, cSec := env.NewPeer("session", "C")
		b, _ := env.NewPeer("session", "B")
		env.SetLimits(c, 100, 2)
		toolOK(t, env, cSec, "vecnet_send", sendArgs("thr-1", b, "one"), nil)
		toolOK(t, env, cSec, "vecnet_send", sendArgs("thr-2", b, "two"), nil)
		env.Now.Advance(19 * time.Minute)

		assertToolRateMessage(t, env, cSec, sendArgs("thr-3", b, "three"),
			"per-peer rate limit exceeded: threads_per_hour 2 (new threads in the last hour); retry in 41m",
			"threads_per_hour", 2, 3600-19*60)
	})
}

func sendArgs(key, recipient, text string, thread ...string) map[string]any {
	args := map[string]any{
		"idempotency_key": key,
		"priority":        "hold",
		"content":         map[string]any{"text": text},
	}
	if recipient != "" {
		args["recipients"] = []string{recipient}
	}
	if len(thread) == 1 && thread[0] != "" {
		args["thread_id"] = thread[0]
	}
	return args
}

func assertToolRateMessage(t *testing.T, env *testhub.Env, secret string, args map[string]any, want, limit string, max, retry int) {
	t.Helper()
	res := callTool(t, env, secret, "vecnet_send", args)
	if !res.IsError {
		t.Fatalf("IsError false, structured %s", res.StructuredContent)
	}
	var e toolErrorBody
	if err := json.Unmarshal(res.StructuredContent, &e); err != nil {
		t.Fatalf("structuredContent %s: %v", res.StructuredContent, err)
	}
	if e.Error.Code != "rate_limited" || e.Error.Message != want {
		t.Fatalf("error = %s %q, want rate_limited %q", e.Error.Code, e.Error.Message, want)
	}
	if e.Error.Details["limit"] != limit || e.Error.Details["max"] != float64(max) || e.Error.Details["retry_after_seconds"] != float64(retry) {
		t.Fatalf("details = %v, want limit %s max %d retry %d", e.Error.Details, limit, max, retry)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "rate_limited: "+want {
		t.Fatalf("tool text = %+v, want %q", res.Content, "rate_limited: "+want)
	}
}
