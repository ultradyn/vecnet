package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestReconnectStatusQuietWindow(t *testing.T) {
	for _, name := range []string{"watch", "mcp"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			// A zero-valued injected clock must be authoritative too.
			now := time.Time{}
			s := reconnectStatus{out: &out, name: name, now: func() time.Time { return now }}
			s.live()
			s.failed("unexpected EOF")
			now = now.Add(5*time.Second - time.Nanosecond)
			s.failed("connection refused")
			s.live()
			s.live()
			if out.Len() != 0 || s.active {
				t.Fatalf("short outage: active=%t output=%q", s.active, out.String())
			}

			s.failed("unexpected EOF")
			now = now.Add(5 * time.Second)
			s.failed("connection refused")
			want := "vecnet: " + name + ": hub unreachable for 5s; reconnecting…\n"
			if out.String() != want {
				t.Fatalf("at threshold: got %q, want %q", out.String(), want)
			}
			now = now.Add(1900 * time.Millisecond)
			s.failed("unexpected EOF")
			s.live()
			s.live()
			want += "vecnet: " + name + ": reconnected after 6s\n"
			if out.String() != want || s.active {
				t.Fatalf("announced outage: active=%t got %q, want %q", s.active, out.String(), want)
			}

			// Reset both the start time and announcement for later outages.
			s.failed("connection refused")
			now = now.Add(time.Second)
			s.live()
			if out.String() != want {
				t.Fatalf("later short outage: %q", out.String())
			}
			s.failed("connection refused")
			now = now.Add(7200 * time.Millisecond)
			s.failed("")
			s.live()
			want += "vecnet: " + name + ": hub unreachable for 7s; reconnecting…\n" +
				"vecnet: " + name + ": reconnected after 7s\n"
			if out.String() != want {
				t.Fatalf("later long outage: got %q, want %q", out.String(), want)
			}
		})
	}
}

func TestReconnectStatusSustainedDetails(t *testing.T) {
	for _, firstRetry := range []time.Duration{59 * time.Second, time.Minute} {
		t.Run(firstRetry.String(), func(t *testing.T) {
			var out bytes.Buffer
			now := time.Unix(100, 0)
			s := reconnectStatus{out: &out, name: "watch", now: func() time.Time { return now }}
			s.failed("unexpected EOF")
			now = now.Add(firstRetry)
			s.failed("connection\nrefused")
			if firstRetry < time.Minute {
				if strings.Contains(out.String(), "refused") {
					t.Fatalf("early detail: %q", out.String())
				}
				now = now.Add(time.Second)
				s.failed("connection\nrefused")
			}
			// Keep the existing per-failure details after one minute.
			s.failed("")
			s.failed("unexpected EOF")
			s.live()
			if strings.Count(out.String(), "hub unreachable for ") != 1 ||
				strings.Count(out.String(), "connection refused; reconnecting\n") != 1 ||
				strings.Count(out.String(), "unexpected EOF; reconnecting\n") != 1 ||
				!strings.HasSuffix(out.String(), "vecnet: watch: reconnected after 60s\n") {
				t.Fatalf("sustained outage: %q", out.String())
			}
		})
	}
}
