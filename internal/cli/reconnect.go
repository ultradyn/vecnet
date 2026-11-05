package cli

import (
	"fmt"
	"io"
	"math/rand/v2"
	"time"
)

const quietOutage = 5 * time.Second

// reconnectStatus reports outages only once they need attention, with details
// after one minute. MCP callers supply sanitized details.
type reconnectStatus struct {
	out       io.Writer
	name      string
	now       func() time.Time
	since     time.Time
	active    bool
	announced bool
}

func (s *reconnectStatus) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *reconnectStatus) failed(detail string) {
	now := s.clock()
	if !s.active {
		s.active, s.since = true, now
	}
	elapsed := now.Sub(s.since)
	if !s.announced && elapsed >= quietOutage {
		fmt.Fprintf(s.out, "vecnet: %s: hub unreachable for %ds; reconnecting…\n", s.name, int64(elapsed/time.Second))
		s.announced = true
	}
	if elapsed >= time.Minute && detail != "" {
		fmt.Fprintf(s.out, "vecnet: %s: %s; reconnecting\n", s.name, flatten(detail))
	}
}

func (s *reconnectStatus) live() {
	if s.active {
		if s.announced {
			fmt.Fprintf(s.out, "vecnet: %s: reconnected after %ds\n", s.name, int64(s.clock().Sub(s.since)/time.Second))
		}
		s.active, s.announced, s.since = false, false, time.Time{}
	}
}

// Keep the first retry near one second even if the SSE preface said 3000.
// ±20% jitter prevents synchronized restarts; the hard cap is 24 seconds.
func reconnectDelay(failures int) time.Duration { return reconnectDelayFrom(time.Second, failures) }

// reconnectDelayFrom is reconnectDelay starting at base (tests shorten it).
func reconnectDelayFrom(base time.Duration, failures int) time.Duration {
	d := base
	for i := 1; i < failures && d < 20*time.Second; i++ {
		d *= 2
	}
	d = min(d, 20*time.Second)
	return d*4/5 + rand.N(d*2/5+1)
}
