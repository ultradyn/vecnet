package httpapi

import (
	"bufio"
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
)

// Per-client limits on unauthenticated traffic (PROTOCOL §20.2). A client is
// an IP address (an IPv6 /64), taken from RemoteAddr, or from Fly-Client-IP
// when Deps.TrustFlyClientIP says Fly's proxy sets it. Requests with a
// credential that resolves are never limited here; only anonymous work and
// failed authentication spend a client's tokens.
const (
	DefaultClientBurst = 30              // tokens a client starts with
	DefaultClientEvery = 2 * time.Second // one token back per interval (30 a minute)
	maxLimiterKeys     = 65536           // remembered clients; a flood of addresses costs only this memory
)

// Limiter is a token bucket per client. Its methods are safe for concurrent
// use. A nil *Limiter limits nothing.
type Limiter struct {
	Burst float64
	Every time.Duration
	Now   func() time.Time // nil means time.Now

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewLimiter returns a Limiter of burst tokens refilling one per every.
func NewLimiter(burst int, every time.Duration) *Limiter {
	return &Limiter{Burst: float64(burst), Every: every}
}

// DefaultLimiter is the production Limiter.
func DefaultLimiter() *Limiter { return NewLimiter(DefaultClientBurst, DefaultClientEvery) }

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// refill returns key's bucket brought up to now. Call with mu held.
func (l *Limiter) refill(key string, now time.Time) *bucket {
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxLimiterKeys {
			l.prune(now)
		}
		b = &bucket{tokens: l.Burst, at: now}
		l.buckets[key] = b
		return b
	}
	if el := now.Sub(b.at); el > 0 && l.Every > 0 {
		b.tokens = math.Min(l.Burst, b.tokens+float64(el)/float64(l.Every))
	}
	b.at = now
	return b
}

// prune drops buckets that have refilled, and every bucket if that frees
// nothing. Call with mu held.
func (l *Limiter) prune(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+float64(now.Sub(b.at))/float64(l.Every) >= l.Burst {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= maxLimiterKeys {
		clear(l.buckets)
	}
}

// Take spends one of key's tokens. It reports false, with the wait until a
// token is back, when none is left.
func (l *Limiter) Take(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, l.now())
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, l.wait(b)
}

// Check reports whether key has a token left, spending none.
func (l *Limiter) Check(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, l.now())
	if b.tokens >= 1 {
		return true, 0
	}
	return false, l.wait(b)
}

// Spend takes one token from key, if it has one: the cost of a failure
// found after the fact.
func (l *Limiter) Spend(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, l.now())
	b.tokens = math.Max(0, b.tokens-1)
}

func (l *Limiter) wait(b *bucket) time.Duration {
	return time.Duration((1 - b.tokens) * float64(l.Every))
}

// clientKey names the client behind r: Fly-Client-IP when trustFly and it
// parses as an address, else RemoteAddr's host. IPv6 addresses are cut to
// their /64, the smallest block one subscriber usually holds.
func clientKey(r *http.Request, trustFly bool) string {
	if trustFly {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("Fly-Client-IP"))); err == nil {
			return addrKey(a)
		}
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return addrKey(a)
	}
	return host
}

func addrKey(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// tooMany writes 429 client_rate_limited with Retry-After.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	e := hub.E(http.StatusTooManyRequests, "client_rate_limited", "too many unauthenticated requests from this address; retry later")
	e.Details["retry_after_seconds"] = secs
	writeError(w, e)
}

// throttleKind sorts a request for throttle.
type throttleKind int

const (
	throttleNone    throttleKind = iota // authenticate limits failures; static and metadata are free
	throttleEach                        // anonymous work: every request costs a token
	throttleFailure                     // anonymous, cheap when right: failures cost a token
)

func classify(r *http.Request) throttleKind {
	p := r.URL.Path
	switch {
	case p == "/v1/enrol", p == "/oauth/register", p == "/oauth/authorize", p == "/oauth/token":
		return throttleEach
	case strings.HasPrefix(p, "/v1/ingress/"), p == "/mcp",
		strings.HasPrefix(p, "/v1/blobs/") && r.URL.Query().Has("t"):
		return throttleFailure
	}
	return throttleNone
}

// throttle applies l to the public routes (PROTOCOL §20.2):
//   - enrolment and the OAuth endpoints that do work (register, authorize,
//     token) cost one token per request;
//   - ingress, tokened blob reads and /mcp cost one per failure (401, and for
//     ingress and blobs also 403 and 404), and are refused while the client
//     has none left, except /mcp requests carrying a credential, which may be
//     good.
//
// Every other path is left to authenticate, which charges failed bearers.
func throttle(l *Limiter, trustFly bool, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := classify(r)
		if kind == throttleNone {
			next.ServeHTTP(w, r)
			return
		}
		key := clientKey(r, trustFly)
		if kind == throttleEach {
			if ok, wait := l.Take(key); !ok {
				tooMany(w, wait)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		credentialed := r.URL.Path == "/mcp" && r.Header.Get("Authorization") != ""
		if !credentialed {
			if ok, wait := l.Check(key); !ok {
				tooMany(w, wait)
				return
			}
		}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		switch sw.status {
		case http.StatusUnauthorized:
			l.Spend(key)
		case http.StatusForbidden, http.StatusNotFound:
			if r.URL.Path != "/mcp" {
				l.Spend(key)
			}
		}
	})
}

// statusWriter records the status code. It passes Flush, Hijack and
// ResponseController calls through, so SSE responses behind it still stream.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("httpapi: hijack not supported")
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
