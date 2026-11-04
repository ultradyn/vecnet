package cli_test

import (
	"os"
	"strings"
	"testing"
)

// A plain http:// hub on a public host is refused before any credential is
// sent (PROTOCOL §19.4); loopback and LAN hosts, https, an explicit opt-in in
// the profile, and VECNET_ALLOW_INSECURE_HTTP=1 are allowed.
func TestInsecureHubRefused(t *testing.T) {
	refused := []string{"http://vecnet.fly.dev", "http://203.0.113.9:8080", "http://hub.example.com"}
	for _, hub := range refused {
		u := profileUser(t, hub, "vn_peer_x")
		code, _, errs := u.run("peers")
		if code == 0 || !strings.Contains(errs, "plain http on a public host") {
			t.Errorf("%s: exit %d, stderr %q; want refusal", hub, code, errs)
		}
		u.env["VECNET_ALLOW_INSECURE_HTTP"] = "1"
		if _, _, errs := u.run("peers"); strings.Contains(errs, "plain http on a public host") {
			t.Errorf("%s with VECNET_ALLOW_INSECURE_HTTP=1: still refused", hub)
		}
	}
	// Allowed hosts get past the guard (the request itself then fails: nothing
	// listens there).
	for _, hub := range []string{
		"https://vecnet.fly.dev", "http://127.0.0.1:1", "http://[::1]:1", "http://localhost:1", "http://192.168.1.5:1",
		"http://10.0.0.2:1", "http://100.100.1.1:1", "http://xsm:1", "http://box.lan:1", "http://box.local:1",
	} {
		u := profileUser(t, hub, "vn_peer_x")
		if _, _, errs := u.run("peers"); strings.Contains(errs, "plain http on a public host") {
			t.Errorf("%s: refused, want allowed", hub)
		}
	}
	// allow_insecure_http in the profile opts in.
	u := profileUser(t, "http://hub.example.com:1", "vn_peer_x")
	raw, err := os.ReadFile(u.conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.conf, append(raw, []byte("allow_insecure_http = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, errs := u.run("peers"); strings.Contains(errs, "plain http on a public host") {
		t.Errorf("profile allow_insecure_http: still refused")
	}
}

// enrol refuses a public http:// hub before sending the token, and
// --allow-insecure-http both permits it and records the opt-in.
func TestEnrolInsecureHub(t *testing.T) {
	u := newUser(t)
	code, _, errs := u.run("enrol", "--hub", "http://hub.example.com:1", "--token", "vn_enrol_x", "--profile", "p")
	if code == 0 || !strings.Contains(errs, "--allow-insecure-http") {
		t.Fatalf("enrol over public http: exit %d, stderr %q", code, errs)
	}
	_, _, errs = u.run("enrol", "--hub", "http://hub.example.com:1", "--token", "vn_enrol_x", "--profile", "p", "--allow-insecure-http")
	if strings.Contains(errs, "plain http on a public host") {
		t.Fatalf("--allow-insecure-http still refused: %q", errs)
	}
}
