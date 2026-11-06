package hub

import "testing"

func TestRateLimitMessage(t *testing.T) {
	cases := []struct {
		what, limit string
		max, retry  int
		want        string
	}{
		{"per-peer", "sends_per_min", 1, 41, "per-peer rate limit exceeded: sends_per_min 1 (sends in the last minute); retry in 41s"},
		{"per-peer", "sends_per_min", 60, 60, "per-peer rate limit exceeded: sends_per_min 60 (sends in the last minute); retry in 1m"},
		{"per-peer", "sends_per_min", 60, 61, "per-peer rate limit exceeded: sends_per_min 60 (sends in the last minute); retry in 2m"},
		{"per-peer", "threads_per_hour", 2, 2460, "per-peer rate limit exceeded: threads_per_hour 2 (new threads in the last hour); retry in 41m"},
		{"per-peer", "threads_per_hour", 20, 3600, "per-peer rate limit exceeded: threads_per_hour 20 (new threads in the last hour); retry in 1h"},
		{"per-peer", "threads_per_hour", 20, 3900, "per-peer rate limit exceeded: threads_per_hour 20 (new threads in the last hour); retry in 1h5m"},
		{"per-peer", "threads_per_hour", 20, 3601, "per-peer rate limit exceeded: threads_per_hour 20 (new threads in the last hour); retry in 1h1m"},
		{"per-source", "sends_per_min", 2, 60, "per-source rate limit exceeded: sends_per_min 2 (sends in the last minute); retry in 1m"},
		{"per-source", "threads_per_hour", 1, 3600, "per-source rate limit exceeded: threads_per_hour 1 (new threads in the last hour); retry in 1h"},
	}
	for _, tc := range cases {
		got := rateLimitMessage(tc.what, tc.limit, tc.max, tc.retry)
		if got != tc.want {
			t.Errorf("rateLimitMessage(%q, %q, %d, %d) = %q, want %q", tc.what, tc.limit, tc.max, tc.retry, got, tc.want)
		}
	}
}
