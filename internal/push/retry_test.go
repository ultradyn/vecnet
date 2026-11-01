package push_test

import (
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/push"
)

func fixed(v float64) func() float64 { return func() float64 { return v } }

// TestBackoffTable pins the backoff of PROTOCOL §3.3: base min(1 h,
// 2^attempt s), a positive Retry-After replacing it (capped at 1 h), plus
// jitter of up to 10 % of the base.
func TestBackoffTable(t *testing.T) {
	cases := []struct {
		name       string
		attempt    int
		retryAfter time.Duration
		base       time.Duration
	}{
		{"attempt 1", 1, 0, 2 * time.Second},
		{"attempt 7", 7, 0, 128 * time.Second},
		// 2^11 s is 2048 s, still under the cap; 2^12 s is the first over it.
		{"attempt 11", 11, 0, 2048 * time.Second},
		{"attempt 12 capped", 12, 0, time.Hour},
		{"attempt 63 capped, no overflow", 63, 0, time.Hour},
		{"attempt 1000 capped, no overflow", 1000, 0, time.Hour},
		{"retry-after 30s", 1, 30 * time.Second, 30 * time.Second},
		{"retry-after replaces a larger backoff", 10, 30 * time.Second, 30 * time.Second},
		{"retry-after capped", 1, 5 * time.Hour, time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := push.Backoff(c.attempt, c.retryAfter, fixed(0)); got != c.base {
				t.Fatalf("Backoff(%d, %v) without jitter = %v, want %v", c.attempt, c.retryAfter, got, c.base)
			}
			hi := push.Backoff(c.attempt, c.retryAfter, fixed(0.999999))
			if hi <= c.base || hi > c.base+c.base/10 {
				t.Fatalf("Backoff(%d, %v) with max jitter = %v, want within (%v, %v]", c.attempt, c.retryAfter, hi, c.base, c.base+c.base/10)
			}
			// The default jitter source stays within the same bounds.
			for range 50 {
				got := push.Backoff(c.attempt, c.retryAfter, nil)
				if got < c.base || got > c.base+c.base/10 {
					t.Fatalf("Backoff(%d, %v) = %v, want within [%v, %v]", c.attempt, c.retryAfter, got, c.base, c.base+c.base/10)
				}
			}
		})
	}
}

// TestParseRetryAfter covers Review Focus 5: seconds and HTTP-date are
// parsed, capped at 1 h; garbage, zero and past dates are 0, so the caller
// falls back to the computed backoff.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"120", 120 * time.Second},
		{"  7 ", 7 * time.Second},
		{"3600", time.Hour},
		{"99999", time.Hour},
		{"999999999999999999999999999", time.Hour},
		{now.Add(5 * time.Minute).Format(http1123), 5 * time.Minute},
		{now.Add(5 * time.Minute).Format(time.RFC850), 5 * time.Minute},
		{now.Add(48 * time.Hour).Format(http1123), time.Hour},
		{now.Add(-time.Minute).Format(http1123), 0},
		{"0", 0},
		{"-5", 0},
		{"+5", 0},
		{"1.5", 0},
		{"garbage", 0},
		{"", 0},
		{"12abc", 0},
	}
	for _, c := range cases {
		if got := push.ParseRetryAfter(c.in, now); got != c.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// http1123 is the IMF-fixdate layout of RFC 9110 (http.TimeFormat).
const http1123 = "Mon, 02 Jan 2006 15:04:05 GMT"
