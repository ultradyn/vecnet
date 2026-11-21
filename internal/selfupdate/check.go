package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RegistryURL is npm's dist-tags document for the vecnet package. It is a few
// dozen bytes, unlike the full packument, which grows with every release.
const RegistryURL = "https://registry.npmjs.org/-/package/vecnet/dist-tags"

// Registry is the npm registry the check reads and every install pins
// (--registry overrides any .npmrc), so npm fetches from the registry whose
// answer the hub verified.
const Registry = "https://registry.npmjs.org/"

// PackageName is the npm package the hub ships in.
const PackageName = "vecnet"

// Check limits. The request is one small GET; anything slower or larger is a
// failure, and failures are silent.
const (
	CheckTimeout     = 10 * time.Second
	MaxResponseBytes = 16 << 10
)

// Interval bounds (vecnet-hub --update-check-interval).
const (
	DefaultInterval = 6 * time.Hour
	MinInterval     = 15 * time.Minute
	// MaxInterval keeps the jitter arithmetic far from overflow.
	MaxInterval = 30 * 24 * time.Hour
)

// ParseInterval reads --update-check-interval / VECNET_UPDATE_CHECK_INTERVAL:
// a Go duration ("6h", "90m") or a bare whole number of hours ("6"). "0" (or
// any zero duration) disables the check. A value above zero but under
// MinInterval is raised to MinInterval; clamped reports that. Negative,
// fractional-hour, unparseable or above-MaxInterval (30 days) values are
// errors that never echo the input.
func ParseInterval(raw string) (d time.Duration, clamped bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultInterval, false, nil
	}
	bad := errors.New("update check interval must be 0 (disabled), a whole number of hours, or a Go duration such as 6h or 90m, at most 720h (30 days)")
	if n, perr := strconv.ParseUint(s, 10, 32); perr == nil {
		d = time.Duration(n) * time.Hour
		if n > 0 && d/time.Hour != time.Duration(n) {
			return 0, false, bad
		}
	} else {
		d, perr = time.ParseDuration(s)
		if perr != nil || d < 0 {
			return 0, false, bad
		}
	}
	if d > MaxInterval {
		return 0, false, bad
	}
	if d > 0 && d < MinInterval {
		return MinInterval, true, nil
	}
	return d, false, nil
}

// Checker asks the registry which version is tagged latest, at most once per
// Interval (with jitter), and caches the answer. It never runs for a build
// that is not a release version, and a failure keeps the last good answer.
type Checker struct {
	// URL is the dist-tags document; "" means RegistryURL. Tests point it at
	// an httptest server.
	URL string
	// Client makes the request; production passes egress.Policy{}.Client.
	Client *http.Client
	// Current is the running build's version (buildinfo.Current().Version).
	Current string
	// Interval between checks; zero or negative means the checker never runs.
	Interval time.Duration
	// InitialDelay, when positive, replaces the random 30 s to 3 min wait
	// before the first check (tests).
	InitialDelay time.Duration
	// Now and Rand are seams for tests; nil means time.Now and math/rand.
	Now  func() time.Time
	Rand func() float64

	mu        sync.Mutex
	latest    string
	checkedAt time.Time
	inflight  *call
}

type call struct {
	done   chan struct{}
	latest string
	err    error
}

// Enabled reports whether this checker may make network requests: a
// positive interval and a running release version.
func (c *Checker) Enabled() bool {
	return c != nil && c.Interval > 0 && ReleaseVersion(c.Current)
}

// Latest returns the cached latest version and when it was read; "" until
// the first successful check.
func (c *Checker) Latest() (string, time.Time) {
	if c == nil {
		return "", time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.checkedAt
}

// Check fetches the latest version now and caches it. Concurrent callers
// share one request. A disabled checker returns an error without any
// network access.
func (c *Checker) Check(ctx context.Context) (string, error) {
	if !c.Enabled() {
		return "", errors.New("update check disabled")
	}
	c.mu.Lock()
	if f := c.inflight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.latest, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &call{done: make(chan struct{})}
	c.inflight = f
	c.mu.Unlock()

	f.latest, f.err = c.fetch(ctx)
	c.mu.Lock()
	if f.err == nil {
		c.latest, c.checkedAt = f.latest, c.now()
	}
	c.inflight = nil
	c.mu.Unlock()
	close(f.done)
	return f.latest, f.err
}

// fetch performs one bounded GET of the dist-tags document. Errors are
// fixed texts plus a status code; they never carry the response body.
func (c *Checker) fetch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	url := c.URL
	if url == "" {
		url = RegistryURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", errors.New("registry request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vecnet-hub/"+c.Current)
	client := c.Client
	if client == nil {
		return "", errors.New("no registry client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("registry unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return "", errors.New("registry read failed")
	}
	if len(body) > MaxResponseBytes {
		return "", errors.New("registry response too large")
	}
	var tags map[string]json.RawMessage
	if err := json.Unmarshal(body, &tags); err != nil {
		return "", errors.New("registry response is not a JSON object")
	}
	var latest string
	if raw, ok := tags["latest"]; !ok || json.Unmarshal(raw, &latest) != nil {
		return "", errors.New("registry response has no latest tag")
	}
	if !ReleaseVersion(latest) {
		return "", errors.New("registry latest tag is not a release version")
	}
	return latest, nil
}

// Run checks after a short random start delay, then every Interval ±10%,
// until ctx ends. A disabled checker returns at once without any network
// access. onResult, when non-nil, runs after every attempt.
func (c *Checker) Run(ctx context.Context, onResult func(latest string, err error)) {
	if !c.Enabled() {
		return
	}
	// The first check waits 30 s to 3 min, so a restart loop or a fleet
	// booting together does not hit the registry in step.
	wait := 30*time.Second + time.Duration(c.rand()*float64(150*time.Second))
	if c.InitialDelay > 0 {
		wait = c.InitialDelay
	}
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		latest, err := c.Check(ctx)
		if onResult != nil {
			onResult(latest, err)
		}
		wait = c.next()
	}
}

// next is Interval (capped at MaxInterval) scaled by a factor in
// [0.9, 1.1), never below MinInterval, so no Interval set in code can make
// Run hammer the registry.
func (c *Checker) next() time.Duration {
	d := time.Duration(float64(min(c.Interval, MaxInterval)) * (0.9 + 0.2*c.rand()))
	return max(d, MinInterval)
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) rand() float64 {
	if c.Rand != nil {
		return c.Rand()
	}
	return rand.Float64()
}
