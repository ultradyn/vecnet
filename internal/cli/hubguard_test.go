package cli_test

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// guardTransport records an attempted command without depending on DNS,
// public services or unreachable LAN hosts. No tests here run in parallel.
// Identity probes may also run, so commandCalls counts only the target path.
type guardTransport struct {
	path                string
	calls, commandCalls atomic.Int32
}

func (g *guardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	g.calls.Add(1)
	if r.URL.Path == g.path {
		g.commandCalls.Add(1)
	}
	return nil, errors.New("guard fixture: network unavailable")
}

func installGuardTransport(t *testing.T, path string) *guardTransport {
	t.Helper()
	g := &guardTransport{path: path}
	old := http.DefaultTransport
	http.DefaultTransport = g
	t.Cleanup(func() { http.DefaultTransport = old })
	return g
}

// A plain http:// hub on a public host is refused before any credential is
// sent (PROTOCOL §20.4); loopback and LAN hosts, https, an explicit opt-in in
// the profile, and VECNET_ALLOW_INSECURE_HTTP=1 are allowed.
func TestInsecureHubRefused(t *testing.T) {
	g := installGuardTransport(t, "/v1/peers")
	refused := []string{"http://vecnet.fly.dev", "http://203.0.113.9:8080", "http://hub.example.com"}
	for _, hub := range refused {
		u := profileUser(t, hub, "vn_peer_x")
		before := g.calls.Load()
		code, _, errs := u.run("peers")
		if code == 0 || !strings.Contains(errs, "plain http on a public host") {
			t.Errorf("%s: exit %d, stderr %q; want refusal", hub, code, errs)
		}
		if g.calls.Load() != before {
			t.Errorf("%s: refusal attempted HTTP", hub)
		}
		u.env["VECNET_ALLOW_INSECURE_HTTP"] = "1"
		before = g.commandCalls.Load()
		if _, _, errs := u.run("peers"); strings.Contains(errs, "plain http on a public host") {
			t.Errorf("%s with VECNET_ALLOW_INSECURE_HTTP=1: still refused", hub)
		}
		if g.commandCalls.Load() != before+1 {
			t.Errorf("%s: explicit opt-in did not attempt the peers request", hub)
		}
	}
	// Allowed hosts get past the guard and attempt the command request.
	// The recording transport makes the intended network failure deterministic.
	for _, hub := range []string{
		"https://vecnet.fly.dev", "http://127.0.0.1:1", "http://[::1]:1", "http://localhost:1", "http://192.168.1.5:1",
		"http://10.0.0.2:1", "http://100.100.1.1:1", "http://xsm:1", "http://box.lan:1", "http://box.local:1",
	} {
		u := profileUser(t, hub, "vn_peer_x")
		before := g.commandCalls.Load()
		if _, _, errs := u.run("peers"); strings.Contains(errs, "plain http on a public host") {
			t.Errorf("%s: refused, want allowed", hub)
		}
		if g.commandCalls.Load() != before+1 {
			t.Errorf("%s: allowed host did not attempt the peers request", hub)
		}
	}
	// allow_insecure_http in the profile opts in.
	u := profileUser(t, "http://hub.example.com:1", "vn_peer_x")
	raw, err := os.ReadFile(u.conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(u.conf, append(raw, []byte("allow_insecure_http = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	before := g.commandCalls.Load()
	if _, _, errs := u.run("peers"); strings.Contains(errs, "plain http on a public host") {
		t.Errorf("profile allow_insecure_http: still refused")
	}
	if g.commandCalls.Load() != before+1 {
		t.Error("profile opt-in did not attempt the peers request")
	}
}

// Validation errors and stderr never repeat a hub URL's userinfo, path, or query.
func TestHubURLSecretsStayOffStderr(t *testing.T) {
	const (
		userSecret  = "pwUSERSECRET"
		pathSecret  = "PATHSECRET"
		querySecret = "QUERYSECRET"
		host        = "hidden-host.example"
	)
	secrets := []string{userSecret, pathSecret, querySecret, "alice", host, "FRAG"}
	cases := []struct {
		name string
		hub  string
		want string
		hide bool
	}{
		{"malformed", "http://[::1" + userSecret, "the hub is not a URL like https://hub.example", true},
		{"scheme", "ftp://" + host + "/" + pathSecret, "scheme must be https", true},
		{"query", "https://" + host + "/" + pathSecret + "?token=" + querySecret, "contains a query", true},
		{"fragment", "https://" + host + "/" + pathSecret + "#FRAG", "contains a fragment", true},
		{"public http", "http://" + host, "plain http on a public host", true},
		{"userinfo", "https://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret, "must not include credentials", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := profileUser(t, tc.hub, "vn_peer_x")
			code, out, errs := u.run("peers")
			if code == 0 {
				t.Fatalf("exit 0, stderr %q", errs)
			}
			if !strings.Contains(errs, tc.want) {
				t.Fatalf("stderr %q, want %q", errs, tc.want)
			}
			banned := []string{userSecret, pathSecret, querySecret}
			if tc.hide {
				banned = secrets
			}
			for _, secret := range banned {
				if strings.Contains(errs, secret) || strings.Contains(out, secret) {
					t.Fatalf("secret %q reached the terminal\nstdout: %s\nstderr: %s", secret, out, errs)
				}
			}
		})
	}
}

// enrol refuses a public http:// hub before sending the token, and
// --allow-insecure-http both permits it and records the opt-in.
func TestEnrolInsecureHub(t *testing.T) {
	g := installGuardTransport(t, "/v1/enrol")
	u := newUser(t)
	code, _, errs := u.run("enrol", "--hub", "http://hub.example.com:1", "--token", "vn_enrol_x", "--profile", "p")
	if code == 0 || !strings.Contains(errs, "--allow-insecure-http") {
		t.Fatalf("enrol over public http: exit %d, stderr %q", code, errs)
	}
	if g.calls.Load() != 0 {
		t.Fatal("refused enrol attempted HTTP")
	}
	_, _, errs = u.run("enrol", "--hub", "http://hub.example.com:1", "--token", "vn_enrol_x", "--profile", "p", "--allow-insecure-http")
	if strings.Contains(errs, "plain http on a public host") {
		t.Fatalf("--allow-insecure-http still refused: %q", errs)
	}
	if g.commandCalls.Load() != 1 {
		t.Fatal("enrol opt-in did not attempt the enrol request")
	}
}
