package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRegistry serves the dist-tags document and counts requests. Tests
// never reach the real npm registry.
type fakeRegistry struct {
	*httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	body string
	code int
	gate chan struct{} // when non-nil, each request waits for a value
}

func newFakeRegistry(t *testing.T, body string) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{body: body, code: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.gate != nil {
			<-f.gate
		}
		f.mu.Lock()
		body, code := f.body, f.code
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRegistry) set(code int, body string) {
	f.mu.Lock()
	f.code, f.body = code, body
	f.mu.Unlock()
}

func (f *fakeRegistry) checker(current string, interval time.Duration) *Checker {
	return &Checker{URL: f.URL, Client: f.Client(), Current: current, Interval: interval}
}

func TestCheckReadsLatestAndCaches(t *testing.T) {
	reg := newFakeRegistry(t, `{"latest":"0.2.7","next":"0.3.0-rc.1"}`)
	now := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	c := reg.checker("0.2.6", time.Hour)
	c.Now = func() time.Time { return now }
	if got, _ := c.Latest(); got != "" {
		t.Fatalf("Latest before check = %q", got)
	}
	got, err := c.Check(context.Background())
	if err != nil || got != "0.2.7" {
		t.Fatalf("Check = %q, %v", got, err)
	}
	latest, at := c.Latest()
	if latest != "0.2.7" || !at.Equal(now) {
		t.Fatalf("Latest = %q at %v", latest, at)
	}
	// A later failure keeps the last good answer.
	reg.set(500, `oops`)
	if _, err := c.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Check after 500 = %v", err)
	}
	if latest, _ := c.Latest(); latest != "0.2.7" {
		t.Fatalf("cache lost after failure: %q", latest)
	}
}

func TestCheckRefusesBadDocuments(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     `<html>`,
		"array":        `["0.2.7"]`,
		"no latest":    `{"next":"0.3.0"}`,
		"number":       `{"latest":7}`,
		"not semver":   `{"latest":"latest"}`,
		"build meta":   `{"latest":"0.2.7+evil"}`,
		"shell chars":  `{"latest":"0.2.7 && curl x"}`,
		"flag":         `{"latest":"--registry=http://evil"}`,
		"oversize":     `{"latest":"0.2.7","pad":"` + strings.Repeat("x", MaxResponseBytes) + `"}`,
		"spaced value": `{"latest":" 0.2.7"}`,
	} {
		t.Run(name, func(t *testing.T) {
			reg := newFakeRegistry(t, body)
			c := reg.checker("0.2.6", time.Hour)
			if got, err := c.Check(context.Background()); err == nil {
				t.Fatalf("Check accepted %q", got)
			}
			if latest, _ := c.Latest(); latest != "" {
				t.Fatalf("cached %q", latest)
			}
		})
	}
}

func TestCheckErrorsNeverEchoBody(t *testing.T) {
	reg := newFakeRegistry(t, `{"latest":"SECRET-ish"}`)
	reg.set(418, "SECRET-ish body")
	_, err := reg.checker("0.2.6", time.Hour).Check(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), reg.URL) {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckTimesOut(t *testing.T) {
	reg := newFakeRegistry(t, `{"latest":"0.2.7"}`)
	reg.gate = make(chan struct{})
	t.Cleanup(func() { close(reg.gate) })
	c := reg.checker("0.2.6", time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Check(ctx); err == nil {
		t.Fatal("Check succeeded against a stalled registry")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Check ignored its deadline")
	}
}

func TestCheckSingleFlight(t *testing.T) {
	reg := newFakeRegistry(t, `{"latest":"0.2.7"}`)
	reg.gate = make(chan struct{})
	c := reg.checker("0.2.6", time.Hour)
	var wg sync.WaitGroup
	results := make([]string, 5)
	for i := range results {
		wg.Go(func() { results[i], _ = c.Check(context.Background()) })
	}
	// Let the one request through once every caller is waiting on it.
	deadline := time.Now().Add(5 * time.Second)
	for reg.hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(reg.gate)
	wg.Wait()
	if n := reg.hits.Load(); n != 1 {
		t.Fatalf("registry hits = %d, want 1", n)
	}
	for _, r := range results {
		if r != "0.2.7" {
			t.Fatalf("results = %v", results)
		}
	}
}

// Interval 0 and non-release builds make no request at all: Check refuses
// and Run returns at once.
func TestDisabledMakesNoRequests(t *testing.T) {
	reg := newFakeRegistry(t, `{"latest":"0.2.7"}`)
	for name, c := range map[string]*Checker{
		"interval 0":   reg.checker("0.2.6", 0),
		"negative":     reg.checker("0.2.6", -time.Hour),
		"dev build":    reg.checker("dev", time.Hour),
		"fly build":    reg.checker("2026.1106.2049+8877c73", time.Hour),
		"dirty build":  reg.checker("2026.1106.2049+8877c73.dirty", time.Hour),
		"empty":        reg.checker("", time.Hour),
		"nil receiver": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if c.Enabled() {
				t.Fatal("Enabled")
			}
			if _, err := c.Check(context.Background()); err == nil {
				t.Fatal("Check ran")
			}
			done := make(chan struct{})
			go func() {
				if c != nil {
					c.Run(context.Background(), func(string, error) { t.Error("onResult called") })
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not return for a disabled checker")
			}
			svc := NewService(c, System{})
			svc.Detect(context.Background())
			if st := svc.Status(); st.Enabled || st.UpdateAvailable || st.OneClick {
				t.Fatalf("Status = %+v", st)
			}
			if _, _, err := svc.Start(); err != ErrUnavailable {
				t.Fatalf("Start = %v", err)
			}
		})
	}
	if n := reg.hits.Load(); n != 0 {
		t.Fatalf("registry hits = %d, want 0", n)
	}
}

func TestNextClampsToBounds(t *testing.T) {
	c := &Checker{Current: "0.2.6"}
	c.Rand = func() float64 { return 0 }
	for _, iv := range []time.Duration{time.Nanosecond, time.Minute, MinInterval} {
		c.Interval = iv
		if got := c.next(); got < MinInterval {
			t.Fatalf("next(%v) = %v, below the minimum", iv, got)
		}
	}
	c.Rand = func() float64 { return 0.999999 }
	for _, iv := range []time.Duration{MaxInterval, 1 << 62, time.Duration(1<<63 - 1)} {
		c.Interval = iv
		got := c.next()
		if got <= 0 || got > MaxInterval*11/10 {
			t.Fatalf("next(%v) = %v", iv, got)
		}
	}
}

func TestRunJittersAndRepeats(t *testing.T) {
	reg := newFakeRegistry(t, `{"latest":"0.2.7"}`)
	c := reg.checker("0.2.6", time.Hour)
	c.Rand = func() float64 { return 0 }
	if got := c.next(); got != 54*time.Minute {
		t.Fatalf("next at rand 0 = %v", got)
	}
	c.Rand = func() float64 { return 0.999999 }
	if got := c.next(); got < 65*time.Minute || got >= 66*time.Minute {
		t.Fatalf("next at rand ~1 = %v", got)
	}
}

func TestParseInterval(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    time.Duration
		clamped bool
	}{
		{"", DefaultInterval, false},
		{"0", 0, false},
		{"0s", 0, false},
		{"0h", 0, false},
		{"6", 6 * time.Hour, false},
		{"24", 24 * time.Hour, false},
		{"6h", 6 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{" 3h ", 3 * time.Hour, false},
		{"15m", 15 * time.Minute, false},
		{"14m59s", MinInterval, true},
		{"1s", MinInterval, true},
		{"1ns", MinInterval, true},
	} {
		got, clamped, err := ParseInterval(tc.in)
		if err != nil || got != tc.want || clamped != tc.clamped {
			t.Errorf("ParseInterval(%q) = %v, %v, %v", tc.in, got, clamped, err)
		}
	}
	if got, _, err := ParseInterval("720h"); err != nil || got != MaxInterval {
		t.Fatalf("720h = %v, %v", got, err)
	}
	if got, _, err := ParseInterval("720"); err != nil || got != MaxInterval {
		t.Fatalf("720 hours = %v, %v", got, err)
	}
	for _, in := range []string{"-1", "-1h", "abc", "6 hours", "1.5", "h", "99999999999999999999", "4294967295",
		"721", "720h1s", "2562047h", "9223372036854775807ns"} {
		if _, _, err := ParseInterval(in); err == nil {
			t.Errorf("ParseInterval(%q) accepted", in)
		} else if strings.Contains(err.Error(), in) && in != "h" {
			t.Errorf("error echoes input: %v", err)
		}
	}
}
