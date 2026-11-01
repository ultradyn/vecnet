// Package egress guards every outbound request the hub makes on behalf of
// configured URLs: push routes, notification routes, MCP Events callbacks,
// Grok Bot routines and CIMD fetches (PROTOCOL §14.7, connectors plan
// Global Constraints).
//
// The rules: https only; no userinfo or fragment; no local-only host names;
// DNS is resolved once per connection and only public unicast addresses
// are dialled, pinned, while TLS SNI and the Host header keep the original
// host name; redirects are never followed. Policy.AllowPrivate is the dev
// override (--allow-private-egress) that admits http and private addresses
// so LAN and loopback receivers work; production leaves it false.
//
// Error texts never contain the URL: route URLs can carry credentials in
// their path or query (PROTOCOL §12 Logging, §14 inv. 6).
package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Errors returned by CheckURL and by the Client's requests.
var (
	// ErrPrivateAddress: the URL's host is, or resolves only to, an address
	// that is not public unicast, or is a local-only name (localhost,
	// .local, .internal).
	ErrPrivateAddress = errors.New("egress: destination is not a public address")
	// ErrScheme: the URL is not https (http is allowed only with
	// AllowPrivate), or is not an absolute URL.
	ErrScheme = errors.New("egress: URL scheme must be https")
	// ErrRedirect: the receiver answered with a redirect; it is never
	// followed. http.Client.Do returns it (inside a *url.Error) together
	// with the 3xx response, whose body is already closed.
	ErrRedirect = errors.New("egress: redirect refused")
	// ErrUserinfo: the URL carries user:password@.
	ErrUserinfo = errors.New("egress: URL must not carry userinfo")
	// ErrInvalidURL: the URL is malformed, has a fragment, has no host, or
	// names a host that cannot be a public DNS name (.onion, numeric
	// spellings of an IP address).
	ErrInvalidURL = errors.New("egress: invalid URL")
)

// MaxResponseHeaderBytes caps the response headers the Client reads.
const MaxResponseHeaderBytes = 64 << 10

// Policy is the egress guard. The zero value is the production policy.
type Policy struct {
	// AllowPrivate is the dev override: http URLs, private and loopback
	// addresses and local-only names are allowed. Never set in production.
	AllowPrivate bool
	// Resolver looks up a host name's addresses; nil means
	// net.DefaultResolver.LookupIP(ctx, "ip", host).
	Resolver func(ctx context.Context, host string) ([]net.IP, error)
}

// blocked are the non-public ranges netip's predicates do not already
// cover (or that are worth stating), IPv4 and IPv6.
var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4
		"0.0.0.0/8",       // "this network"
		"10.0.0.0/8",      // RFC 1918
		"100.64.0.0/10",   // carrier-grade NAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local (cloud metadata)
		"172.16.0.0/12",   // RFC 1918
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"192.88.99.0/24",  // 6to4 relay anycast
		"192.168.0.0/16",  // RFC 1918
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, includes 255.255.255.255 broadcast
		// IPv6
		"::/128",         // unspecified
		"::1/128",        // loopback
		"::/96",          // IPv4-compatible (deprecated)
		"64:ff9b::/96",   // NAT64: can embed a private IPv4
		"64:ff9b:1::/48", // local-use NAT64
		"100::/64",       // discard-only
		"2001::/23",      // IETF protocol assignments, incl. Teredo
		"2001:db8::/32",  // documentation
		"2002::/16",      // 6to4: can embed a private IPv4
		"fc00::/7",       // unique local
		"fe80::/10",      // link-local
		"fec0::/10",      // site-local (deprecated)
		"ff00::/8",       // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsPublic reports whether ip is a public unicast address: not loopback,
// link-local, private (RFC 1918, fc00::/7), multicast, unspecified,
// 100.64/10, 169.254/16, 0.0.0.0/8, 192.0.0.0/24, 198.18/15, broadcast,
// documentation or a tunnelling range that can embed one of those. An
// IPv4-mapped IPv6 address is judged as its IPv4 address. It ignores
// AllowPrivate.
func (Policy) IsPublic(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() ||
		a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// allowed is IsPublic, or any parseable address under AllowPrivate.
func (p Policy) allowed(ip net.IP) bool {
	if p.AllowPrivate {
		return len(ip) == net.IPv4len || len(ip) == net.IPv6len
	}
	return p.IsPublic(ip)
}

// CheckURL parses raw and applies the URL rules: https only (http allowed
// only when AllowPrivate), no userinfo, no fragment, a host that is not
// localhost or under .local, .internal or .onion, and a literal IP host
// that is public. AllowPrivate also admits private literals and the
// local-only names (not .onion). It does no DNS lookup: the Client checks
// resolved addresses at dial time, and Resolve does so ahead of time.
func (p Policy) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's error quotes the URL; never pass it on.
		return nil, fmt.Errorf("%w: unparseable", ErrInvalidURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !p.AllowPrivate {
			return nil, ErrScheme
		}
	default:
		return nil, ErrScheme
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("%w: not a hierarchical URL", ErrInvalidURL)
	}
	if u.User != nil {
		return nil, ErrUserinfo
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, fmt.Errorf("%w: fragment not allowed", ErrInvalidURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrInvalidURL)
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%w: bad port", ErrInvalidURL)
		}
	}
	if strings.HasPrefix(u.Host, "[") || strings.Contains(host, ":") {
		ip := net.ParseIP(host) // rejects zones ("fe80::1%eth0")
		if ip == nil {
			return nil, fmt.Errorf("%w: bad IPv6 literal", ErrInvalidURL)
		}
		if !p.allowed(ip) {
			return nil, ErrPrivateAddress
		}
		return u, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !p.allowed(ip) {
			return nil, ErrPrivateAddress
		}
		return u, nil
	}
	if err := p.checkName(host); err != nil {
		return nil, err
	}
	return u, nil
}

// checkName vets a DNS host name (not an IP literal).
func (p Policy) checkName(host string) error {
	name := strings.TrimSuffix(strings.ToLower(host), ".")
	if name == "" || len(name) > 253 {
		return fmt.Errorf("%w: bad host name", ErrInvalidURL)
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return fmt.Errorf("%w: bad host name", ErrInvalidURL)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return fmt.Errorf("%w: bad host name", ErrInvalidURL)
			}
		}
	}
	// No top-level domain is numeric: "127.1", "2130706433" and
	// "0x7f000001" are IP spellings some resolvers honour.
	last := labels[len(labels)-1]
	if isDigits(last) || (strings.HasPrefix(last, "0x") && isHex(last[2:])) {
		return fmt.Errorf("%w: numeric host name", ErrInvalidURL)
	}
	if last == "onion" {
		return fmt.Errorf("%w: .onion host", ErrInvalidURL)
	}
	if !p.AllowPrivate && (last == "localhost" || last == "local" || last == "internal") {
		return ErrPrivateAddress
	}
	return nil
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func isHex(s string) bool {
	return s != "" && strings.Trim(s, "0123456789abcdef") == ""
}

// Resolve looks host up once and returns the addresses the policy allows,
// public ones first (then, under AllowPrivate, loopback, then the rest),
// each group in resolver order. A literal IP is checked, not looked up.
// It returns ErrPrivateAddress when no address is allowed. The Client dials
// only what Resolve returns; callers that must refuse a URL before any
// connection (events/subscribe) may call it directly.
func (p Policy) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		if err := p.checkName(host); err != nil {
			return nil, err
		}
		look := p.Resolver
		if look == nil {
			look = func(ctx context.Context, host string) ([]net.IP, error) {
				return net.DefaultResolver.LookupIP(ctx, "ip", host)
			}
		}
		var err error
		if ips, err = look(ctx, host); err != nil {
			return nil, fmt.Errorf("egress: resolve: %w", err)
		}
	}
	rank := func(ip net.IP) int {
		switch {
		case p.IsPublic(ip):
			return 0
		case ip.IsLoopback():
			return 1
		default:
			return 2
		}
	}
	var out []net.IP
	for _, ip := range ips {
		if p.allowed(ip) {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, ErrPrivateAddress
	}
	slices.SortStableFunc(out, func(a, b net.IP) int { return rank(a) - rank(b) })
	return out, nil
}

// dialContext resolves addr's host once (Resolve), then dials the allowed
// addresses in order until one connects. The address is pinned for the
// connection; http.Transport still does TLS with ServerName = the URL's
// host and sends Host = the URL's host, because it passes the original
// "host:port" here.
func (p Policy) dialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: bad address", ErrInvalidURL)
		}
		ips, err := p.Resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		var first error
		for _, ip := range ips {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			if first == nil {
				first = err
			}
			if ctx.Err() != nil {
				break
			}
		}
		return nil, first
	}
}

// control re-checks, at socket level, that the address being connected to
// is allowed: a last line behind Resolve.
func (p Policy) control(_ context.Context, _, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrPrivateAddress
	}
	ip := net.ParseIP(host)
	if ip == nil || !p.allowed(ip) {
		return ErrPrivateAddress
	}
	return nil
}

// Client returns an http.Client for guarded egress: every request's URL
// must pass CheckURL; its Transport dials only the pinned, allowed
// addresses of a single lookup (TLS ServerName and Host stay the URL's
// host); it uses no proxy; CheckRedirect returns ErrRedirect; response
// headers are capped at 64 KiB; the whole request, body included, times
// out after timeout.
func (p Policy) Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, ControlContext: p.control}
	tr := &http.Transport{
		Proxy:                  nil, // a proxy would dial on our behalf, unpinned
		DialContext:            p.dialContext(d),
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  time.Second,
		MaxIdleConns:           64,
		IdleConnTimeout:        90 * time.Second,
		MaxResponseHeaderBytes: MaxResponseHeaderBytes,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &guard{p: p, base: tr},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
}

// guard applies CheckURL to every request before the pinned transport.
type guard struct {
	p    Policy
	base *http.Transport
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := g.p.CheckURL(req.URL.String()); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	return g.base.RoundTrip(req)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the
// transport.
func (g *guard) CloseIdleConnections() { g.base.CloseIdleConnections() }
