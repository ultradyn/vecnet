package egress_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// TestIsPublicTable pins the address classes the guard refuses
// (PROTOCOL §14.7): only public unicast addresses are public.
func TestIsPublicTable(t *testing.T) {
	var p egress.Policy
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"10.1.2.3", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"100.64.0.1", false},
		{"::1", false},
		{"fd00::1", false},
		{"fe80::1", false},
		{"0.0.0.0", false},
		// The rest of the plan's list.
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"::", false},
		{"0.1.2.3", false},
		{"192.0.0.8", false},
		{"198.18.0.1", false},
		{"198.19.255.254", false},
		{"255.255.255.255", false},
		// An IPv4-mapped private address is still private.
		{"::ffff:10.0.0.1", false},
		{"::ffff:127.0.0.1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := p.IsPublic(ip); got != c.want {
			t.Errorf("IsPublic(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
	if p.IsPublic(nil) {
		t.Errorf("IsPublic(nil) = true, want false")
	}
}

// TestCheckURL pins the URL rules: https only (http only with
// AllowPrivate), no userinfo, no fragment, no local-only names, literal
// IPs public.
func TestCheckURL(t *testing.T) {
	strict := egress.Policy{}
	dev := egress.Policy{AllowPrivate: true}

	u, err := strict.CheckURL("https://ok.example/x")
	if err != nil {
		t.Fatalf("CheckURL(https://ok.example/x) = %v, want ok", err)
	}
	if u.Host != "ok.example" || u.Path != "/x" {
		t.Fatalf("CheckURL returned %v, want the parsed URL", u)
	}

	if _, err := strict.CheckURL("http://ok.example/x"); !errors.Is(err, egress.ErrScheme) {
		t.Fatalf("CheckURL(http://…) = %v, want ErrScheme", err)
	}
	if _, err := dev.CheckURL("http://ok.example/x"); err != nil {
		t.Fatalf("AllowPrivate CheckURL(http://…) = %v, want ok", err)
	}
	for _, raw := range []string{"ftp://ok.example/x", "ok.example/x", "//ok.example/x"} {
		if _, err := dev.CheckURL(raw); !errors.Is(err, egress.ErrScheme) {
			t.Errorf("AllowPrivate CheckURL(%q) = %v, want ErrScheme", raw, err)
		}
	}

	if _, err := strict.CheckURL("https://u:p@h.example/"); !errors.Is(err, egress.ErrUserinfo) {
		t.Fatalf("CheckURL(https://u:p@h/) = %v, want ErrUserinfo", err)
	}
	if _, err := strict.CheckURL("https://u@h.example/"); !errors.Is(err, egress.ErrUserinfo) {
		t.Fatalf("CheckURL(https://u@h/) = %v, want ErrUserinfo", err)
	}

	for _, raw := range []string{"https://h.example/#f", "https://h.example/#"} {
		if _, err := strict.CheckURL(raw); err == nil {
			t.Errorf("CheckURL(%q) = ok, want an error", raw)
		}
	}

	for _, raw := range []string{
		"https://foo.local/", "https://FOO.Local./", "https://svc.internal/", "https://x.onion/",
		"https://localhost/", "https://a.localhost/", "https:///nohost", "https://",
	} {
		if _, err := strict.CheckURL(raw); err == nil {
			t.Errorf("CheckURL(%q) = ok, want an error", raw)
		}
	}

	for _, raw := range []string{
		"https://10.0.0.1/", "https://127.0.0.1:8443/", "https://[::1]/", "https://[fd00::1]/",
		"https://169.254.169.254/latest", "https://[::ffff:10.0.0.1]/",
	} {
		if _, err := strict.CheckURL(raw); !errors.Is(err, egress.ErrPrivateAddress) {
			t.Errorf("CheckURL(%q) = %v, want ErrPrivateAddress", raw, err)
		}
	}
	if _, err := strict.CheckURL("https://8.8.8.8/x"); err != nil {
		t.Fatalf("CheckURL(https://8.8.8.8/x) = %v, want ok", err)
	}
	// Numeric host spellings a resolver might read as an IP are refused.
	for _, raw := range []string{"https://2130706433/", "https://0x7f000001/", "https://127.1/"} {
		if _, err := strict.CheckURL(raw); err == nil {
			t.Errorf("CheckURL(%q) = ok, want an error", raw)
		}
	}

	// The dev override admits private literals (LAN receivers).
	if _, err := dev.CheckURL("http://127.0.0.1:9000/hook"); err != nil {
		t.Fatalf("AllowPrivate CheckURL(http://127.0.0.1:9000/hook) = %v, want ok", err)
	}
	// It does not relax userinfo or fragments.
	if _, err := dev.CheckURL("http://u:p@127.0.0.1/"); !errors.Is(err, egress.ErrUserinfo) {
		t.Fatalf("AllowPrivate CheckURL(userinfo) = %v, want ErrUserinfo", err)
	}
	if _, err := dev.CheckURL("http://127.0.0.1/#f"); err == nil {
		t.Fatalf("AllowPrivate CheckURL(fragment) = ok, want an error")
	}
}

// countingListener is a 127.0.0.1 listener that counts accepted
// connections; it serves nothing.
func countingListener(t *testing.T) (port string, conns *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns = new(atomic.Int64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port, conns
}

// TestClientRefusesPrivateResolution: a hostname resolving only to
// private addresses is refused at dial time, before any TCP connection.
func TestClientRefusesPrivateResolution(t *testing.T) {
	port, conns := countingListener(t)
	for _, tc := range []struct {
		name string
		ips  []net.IP
		url  string
	}{
		{"10.0.0.5", []net.IP{net.ParseIP("10.0.0.5")}, "https://evil.example:" + port + "/hook"},
		{"10.0.0.5 and loopback", []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("127.0.0.1")}, "https://evil.example:" + port + "/hook"},
		{"literal loopback", nil, "https://127.0.0.1:" + port + "/hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked atomic.Int64
			p := egress.Policy{Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
				asked.Add(1)
				if host != "evil.example" {
					t.Errorf("resolver asked for %q, want evil.example", host)
				}
				return tc.ips, nil
			}}
			req, err := http.NewRequest(http.MethodPost, tc.url, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := p.Client(5 * time.Second).Do(req)
			if err == nil {
				resp.Body.Close()
				t.Fatalf("Do succeeded with status %d, want ErrPrivateAddress", resp.StatusCode)
			}
			if !errors.Is(err, egress.ErrPrivateAddress) {
				t.Fatalf("Do error = %v, want ErrPrivateAddress", err)
			}
			if tc.ips != nil && asked.Load() == 0 {
				t.Fatalf("the resolver was never asked")
			}
		})
	}
	if n := conns.Load(); n != 0 {
		t.Fatalf("listener on 127.0.0.1 got %d connections, want 0", n)
	}
}

// TestClientRefusesRedirect: a 3xx is never followed; Do returns
// ErrRedirect.
func TestClientRefusesRedirect(t *testing.T) {
	var targetHits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		io.WriteString(w, "followed")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := egress.Policy{AllowPrivate: true}
	resp, err := p.Client(5*time.Second).Post(srv.URL+"/start", "application/json", strings.NewReader("{}"))
	if !errors.Is(err, egress.ErrRedirect) {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("Do error = %v, want ErrRedirect", err)
	}
	if resp == nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("Do response = %+v, want the 302 itself alongside ErrRedirect", resp)
	}
	if n := targetHits.Load(); n != 0 {
		t.Fatalf("redirect target got %d requests, want 0", n)
	}
}

// TestClientPinsIP: the client dials the resolved address the guard
// chose, not a fresh lookup, and keeps the original Host.
func TestClientPinsIP(t *testing.T) {
	var gotHost atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost.Store(r.Host)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	su, _ := url.Parse(srv.URL)

	var lookups atomic.Int32
	p := egress.Policy{AllowPrivate: true, Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
		lookups.Add(1)
		if host != "evil.example" {
			t.Errorf("resolver asked for %q, want evil.example", host)
		}
		return []net.IP{net.ParseIP("10.0.0.9"), net.ParseIP("127.0.0.1")}, nil
	}}
	req, err := http.NewRequest(http.MethodPost, "http://evil.example:"+su.Port()+"/hook", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := p.Client(5 * time.Second).Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 from the 127.0.0.1 server", resp.StatusCode)
	}
	if h, _ := gotHost.Load().(string); h != "evil.example:"+su.Port() {
		t.Fatalf("Host header = %q, want evil.example:%s", h, su.Port())
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("resolved %d times, want once: a second answer could differ from the first", n)
	}
}

// TestClientKeepsTLSServerName: over https the pinned dial still verifies
// the certificate for the original hostname (SNI = host).
func TestClientKeepsTLSServerName(t *testing.T) {
	var gotSNI atomic.Value
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSNI.Store(r.TLS.ServerName)
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.StartTLS()
	defer srv.Close()
	su, _ := url.Parse(srv.URL)

	p := egress.Policy{AllowPrivate: true, Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}}
	c := p.Client(5 * time.Second)
	// httptest's certificate is valid for example.com; trust its root.
	egress.SetRootCAs(t, c, srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs)
	resp, err := c.Post("https://example.com:"+su.Port()+"/hook", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if s, _ := gotSNI.Load().(string); s != "example.com" {
		t.Fatalf("TLS ServerName = %q, want example.com", s)
	}
}

// TestPushAndNotifyUseGuard: route and notify URLs go through CheckURL,
// so a loopback receiver is refused unless the hub allows private egress.
func TestPushAndNotifyUseGuard(t *testing.T) {
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer rcv.Close()

	errCode := func(t *testing.T, raw []byte) string {
		t.Helper()
		var b struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("body is not an error envelope: %v; %s", err, raw)
		}
		return b.Error.Code
	}

	t.Run("strict", func(t *testing.T) {
		env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = false })
		peer, _ := env.NewPeer("session", "b")
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/routes", map[string]any{"owner": peer, "url": rcv.URL})
		if resp.StatusCode != http.StatusUnprocessableEntity || errCode(t, raw) != "invalid_url" {
			t.Fatalf("POST route %s = %d %s, want 422 invalid_url", rcv.URL, resp.StatusCode, raw)
		}
		resp, raw = env.Do(env.OperatorSecret, "PUT", "/v1/admin/notify",
			map[string]any{"routes": []map[string]string{{"url": rcv.URL, "filter": "all"}}})
		if resp.StatusCode != http.StatusUnprocessableEntity || errCode(t, raw) != "invalid_url" {
			t.Fatalf("PUT notify %s = %d %s, want 422 invalid_url", rcv.URL, resp.StatusCode, raw)
		}
		if !strings.Contains(string(raw), "routes/0/url") {
			t.Fatalf("PUT notify error %s does not name routes/0/url", raw)
		}
		// The hub's egress client refuses loopback even for a URL stored
		// before the guard existed.
		resp2, err := env.Hub.EgressPolicy().Client(5*time.Second).Post(rcv.URL, "application/json", strings.NewReader("{}"))
		if err == nil {
			resp2.Body.Close()
		}
		if !errors.Is(err, egress.ErrScheme) && !errors.Is(err, egress.ErrPrivateAddress) {
			t.Fatalf("strict hub client POST %s = %v, want a guard error", rcv.URL, err)
		}
	})

	t.Run("allow private", func(t *testing.T) {
		env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = true })
		peer, _ := env.NewPeer("session", "b")
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/routes", map[string]any{"owner": peer, "url": rcv.URL})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST route %s = %d %s, want 201", rcv.URL, resp.StatusCode, raw)
		}
		resp, raw = env.Do(env.OperatorSecret, "PUT", "/v1/admin/notify",
			map[string]any{"routes": []map[string]string{{"url": rcv.URL, "filter": "all"}}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT notify %s = %d %s, want 200", rcv.URL, resp.StatusCode, raw)
		}
	})

	t.Run("testhub default allows private", func(t *testing.T) {
		env := testhub.Start(t)
		if !env.Hub.Cfg.AllowPrivateEgress {
			t.Fatalf("testhub.Start: AllowPrivateEgress = false, want true so loopback receivers work")
		}
		if hub.DefaultConfig().AllowPrivateEgress {
			t.Fatalf("DefaultConfig().AllowPrivateEgress = true, want false in production")
		}
	})
}
