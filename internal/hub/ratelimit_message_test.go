package hub_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

// TestRateLimitErrorMessageNamesTheLimit: a 429 names the limit that tripped,
// its max, and when to retry, in the message itself (PROTOCOL §16.2). Agents
// read that text and miss details.limit.
func TestRateLimitErrorMessageNamesTheLimit(t *testing.T) {
	t.Run("sends_per_min", func(t *testing.T) {
		env := testhub.Start(t)
		a, aSec := env.NewPeer("session", "A")
		b, _ := env.NewPeer("session", "B")
		env.SetLimits(a, 1, 100)
		first := mustSend(t, env, aSec, req("sends-ok", b))
		env.Now.Advance(19 * time.Second)

		resp, raw := env.Do(aSec, "POST", "/v1/messages", replyReq("sends-over", first.ThreadID, ""))
		assertRateMessage(t, resp, raw,
			"per-peer rate limit exceeded: sends_per_min 1 (sends in the last minute); retry in 41s",
			"sends_per_min", 1, 41)
	})

	t.Run("threads_per_hour", func(t *testing.T) {
		env := testhub.Start(t)
		c, cSec := env.NewPeer("session", "C")
		b, _ := env.NewPeer("session", "B")
		env.SetLimits(c, 100, 2)
		mustSend(t, env, cSec, req("thr-1", b))
		mustSend(t, env, cSec, req("thr-2", b))
		env.Now.Advance(19 * time.Minute)

		resp, raw := env.Do(cSec, "POST", "/v1/messages", req("thr-3", b))
		assertRateMessage(t, resp, raw,
			"per-peer rate limit exceeded: threads_per_hour 2 (new threads in the last hour); retry in 41m",
			"threads_per_hour", 2, 3600-19*60)
	})
}

func assertRateMessage(t *testing.T, resp *http.Response, raw []byte, want, limit string, max, retry int) {
	t.Helper()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Retry-After"); got != strconv.Itoa(retry) {
		t.Fatalf("Retry-After = %q, want %d", got, retry)
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	if body.Error.Code != "rate_limited" {
		t.Fatalf("code = %q; body %s", body.Error.Code, raw)
	}
	if body.Error.Message != want {
		t.Fatalf("message = %q, want %q", body.Error.Message, want)
	}
	if body.Error.Details["limit"] != limit || body.Error.Details["max"] != float64(max) || body.Error.Details["retry_after_seconds"] != float64(retry) {
		t.Fatalf("details = %v, want limit %s max %d retry_after_seconds %d", body.Error.Details, limit, max, retry)
	}
}
