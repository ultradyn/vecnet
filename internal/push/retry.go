// Package push is the hub's push worker: it claims due deliveries, signs
// and POSTs them to their routes as Standard Webhooks, and feeds each
// result back into the delivery state machine (PROTOCOL §3, §5).
package push

import (
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
)

// MaxRetryAfter caps an honoured Retry-After (PROTOCOL §3.3, as w2m does).
const MaxRetryAfter = hub.MaxPushBackoff

// Backoff is the retry delay after attempt failed (PROTOCOL §3.3): base
// min(1 h, 2^attempt s); a positive retryAfter replaces it as min(1 h,
// retryAfter); plus jitter of up to 10 % of the base drawn from rnd
// (math/rand when nil). The hub's PushResult uses the same function.
func Backoff(attempt int, retryAfter time.Duration, rnd func() float64) time.Duration {
	return hub.PushBackoff(attempt, retryAfter, rnd)
}

// ParseRetryAfter reads a Retry-After header (RFC 9110 §10.2.3): delay
// seconds or an HTTP-date, relative to now. The result is capped at 1 h
// (a huge number is 1 h); zero, a past date, a sign, a fraction or anything
// unparseable is 0, meaning "use the computed backoff" (Review Focus 5).
func ParseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if isDigits(h) {
		// Accumulate with an early cap so no length overflows.
		var secs int64
		for _, c := range h {
			secs = secs*10 + int64(c-'0')
			if secs > int64(MaxRetryAfter/time.Second) {
				return MaxRetryAfter
			}
		}
		return time.Duration(secs) * time.Second
	}
	t, err := http.ParseTime(h)
	if err != nil {
		return 0
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	return min(d, MaxRetryAfter)
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
