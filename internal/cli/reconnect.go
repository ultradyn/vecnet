package cli

import (
	"fmt"
	"io"
	"math/rand/v2"
	"time"
)

// reconnectStatus reports one outage, with details only once it has lasted
// long enough to need attention. MCP callers supply sanitized details.
type reconnectStatus struct {
	out    io.Writer
	name   string
	now    func() time.Time
	since  time.Time
	active bool
}

func (s *reconnectStatus) failed(detail string) {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if !s.active {
		s.active, s.since = true, now
		fmt.Fprintf(s.out, "vecnet: %s: hub restarting; reconnecting…\n", s.name)
	} else if now.Sub(s.since) >= time.Minute && detail != "" {
		fmt.Fprintf(s.out, "vecnet: %s: %s; reconnecting\n", s.name, flatten(detail))
	}
}

func (s *reconnectStatus) live() {
	if s.active {
		fmt.Fprintf(s.out, "vecnet: %s: reconnected\n", s.name)
		s.active = false
	}
}

// Keep the first retry near one second even if the SSE preface said 3000.
// ±20% jitter prevents synchronized restarts; the hard cap is 24 seconds.
func reconnectDelay(failures int) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < 20*time.Second; i++ {
		d *= 2
	}
	d = min(d, 20*time.Second)
	return d*4/5 + rand.N(d*2/5+1)
}
