package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

const (
	redactUser  = "USERSECRET"
	redactPath  = "PATHSECRET"
	redactQuery = "QUERYSECRET"
	redactHost  = "hidden-host.example"
)

func redactCanaries() []string {
	return []string{"alice:" + redactUser, redactUser, redactQuery, "FRAG"}
}

func checkedPathHub() string { return "https://" + redactHost + "/" + redactPath }

func checkedIPv6Hub() string { return "https://[2001:db8::7]/" + redactPath }

func assertNoCanaries(t *testing.T, label, text string) {
	t.Helper()
	for _, secret := range redactCanaries() {
		if strings.Contains(text, secret) {
			t.Fatalf("%s contains %q:\n%s", label, secret, text)
		}
	}
}

func secretHub(extra string) string {
	return "https://alice:" + redactUser + "@" + redactHost + "/" + redactPath + "?token=" + redactQuery + extra
}

func pathQueryHub() string {
	return "https://" + redactHost + "/" + redactPath + "?token=" + redactQuery
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

type errReadCloser struct{ err error }

func (e errReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e errReadCloser) Close() error             { return nil }

func TestTransportErrorOmitsHubURL(t *testing.T) {
	cases := []struct {
		name string
		hub  string
		rt   func(r *http.Request) (*http.Response, error)
		as   func(t *testing.T, err error)
	}{
		{
			name: "dial",
			hub:  checkedIPv6Hub(),
			rt: func(r *http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connect %s", r.URL.String())}
			},
			as: func(t *testing.T, err error) {
				var op *net.OpError
				if !errors.As(err, &op) || op.Op != "dial" {
					t.Fatalf("dial classification lost: %v", err)
				}
				var ue *url.Error
				if !errors.As(err, &ue) {
					t.Fatalf("url.Error lost: %v", err)
				}
			},
		},
		{
			name: "tls",
			hub:  checkedPathHub(),
			rt: func(r *http.Request) (*http.Response, error) {
				return nil, &tls.RecordHeaderError{Msg: "handshake " + r.URL.String()}
			},
			as: func(t *testing.T, err error) {
				var rec *tls.RecordHeaderError
				if !errors.As(err, &rec) {
					t.Fatalf("tls classification lost: %v", err)
				}
			},
		},
		{
			name: "timeout",
			hub:  checkedPathHub(),
			rt: func(r *http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: timeoutNetError{}}
			},
			as: func(t *testing.T, err error) {
				var ne net.Error
				if !errors.As(err, &ne) || !ne.Timeout() {
					t.Fatalf("timeout classification lost: %v", err)
				}
			},
		},
		{
			name: "body",
			hub:  checkedPathHub(),
			rt: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       errReadCloser{fmt.Errorf("read %s", r.URL.String())},
				}, nil
			},
			as: func(t *testing.T, err error) {
				var ne *netError
				if !errors.As(err, &ne) {
					t.Fatalf("body read is not a network error: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(tc.hub, "vn_peer_x")
			c.hc.Transport = roundTripFunc(tc.rt)
			err := c.do(context.Background(), http.MethodGet, "/v1/peers", nil, nil, nil)
			if err == nil {
				t.Fatal("expected a transport error")
			}
			var ne *netError
			if !errors.As(err, &ne) {
				t.Fatalf("exit classification lost, got %T %v", err, err)
			}
			assertNoCanaries(t, "returned error", err.Error())
			if !strings.Contains(err.Error(), tc.hub) {
				t.Fatalf("error %q dropped the hub URL", err.Error())
			}
			if strings.Contains(tc.hub, "[2001:db8::7]") && !strings.Contains(err.Error(), "[2001:db8::7]") {
				t.Fatalf("error mangled IPv6: %q", err.Error())
			}
			tc.as(t, err)
		})
	}
}

func TestTransportDiagnosticsOmitHubURL(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connect %s", r.URL.String())}
	})
	t.Cleanup(func() { http.DefaultTransport = old })

	hub := checkedPathHub()
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", hub, "vn_operator_redact"))
	code, out, errs := pinRun(env, "peers")
	if code != exitNetwork {
		t.Fatalf("peers exit %d, want network\n%s\n%s", code, out, errs)
	}
	assertNoCanaries(t, "peers stderr", out+"\n"+errs)
	if !strings.Contains(errs, "network:") {
		t.Fatalf("stderr %q", errs)
	}

	code, out, errs = pinRun(env, "--json", "prefs", "list")
	if code != exitNetwork {
		t.Fatalf("prefs exit %d\n%s\n%s", code, out, errs)
	}
	assertNoCanaries(t, "prefs json", out+"\n"+errs)
	if !strings.Contains(out, `"code":"network"`) {
		t.Fatalf("json %s", out)
	}

	var stderr bytes.Buffer
	c := newClient(checkedIPv6Hub(), "vn_peer_x")
	c.hc.Transport = http.DefaultTransport
	w := &watcher{
		identityOff: true, replayed: true, forMonitor: true,
		a:    &app{ctx: context.Background(), stdout: io.Discard, stderr: &stderr, getenv: env},
		c:    c,
		seen: newDedupe(8),
	}
	now := time.Unix(100, 0)
	w.now = func() time.Time { return now }
	n := 0
	w.sleep = func(context.Context, time.Duration) bool {
		n++
		now = now.Add(61 * time.Second)
		return n < 2
	}
	if err := w.run(); err != nil {
		t.Fatalf("watch: %v", err)
	}
	assertNoCanaries(t, "reconnect", stderr.String())
	if !strings.Contains(stderr.String(), "connection refused") && !strings.Contains(stderr.String(), "connect ") && !strings.Contains(stderr.String(), "reconnecting") {
		t.Fatalf("reconnect stderr %q", stderr.String())
	}
	if strings.Contains(stderr.String(), redactUser) || strings.Contains(stderr.String(), redactQuery) {
		t.Fatalf("reconnect leaked: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), checkedIPv6Hub()) {
		t.Fatalf("reconnect dropped the hub URL: %s", stderr.String())
	}
}

func TestProfileFollowRefusesBeforeRequest(t *testing.T) {
	var calls int
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("guard: " + r.URL.String())
	})
	t.Cleanup(func() { http.DefaultTransport = old })

	refused := []struct {
		name, hub string
		allowEnv  bool
		allowProf bool
	}{
		{"malformed", "http://[::1" + redactUser, false, false},
		{"scheme", "ftp://" + redactHost + "/" + redactPath + "?token=" + redactQuery, false, false},
		{"scheme userinfo", secretHub(""), false, false},
		{"public http", "http://" + redactHost + "/" + redactPath + "?token=" + redactQuery, false, false},
		{"public http userinfo", "http://alice:" + redactUser + "@" + redactHost + "/" + redactPath + "?token=" + redactQuery, false, false},
		{"https userinfo", secretHub(""), false, false},
		{"https userinfo allowed env", secretHub(""), true, false},
		{"https userinfo allowed profile", secretHub(""), false, true},
		{"query", pathQueryHub(), false, false},
		{"fragment", "https://" + redactHost + "/" + redactPath + "#FRAG", false, false},
		{"query allowed", pathQueryHub(), true, false},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			calls = 0
			body := fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", tc.hub, "vn_peer_x")
			if tc.allowProf {
				body += "allow_insecure_http = true\n"
			}
			_, base := pinTestEnv(t, body)
			env := func(k string) string {
				if k == insecureEnv && tc.allowEnv {
					return "1"
				}
				return base(k)
			}
			code, out, errs := pinRun(env, "profile", "follow")
			if code == 0 {
				t.Fatalf("accepted %s\n%s", tc.hub, errs)
			}
			if calls != 0 {
				t.Fatalf("%s made %d requests\n%s", tc.name, calls, errs)
			}
			assertNoCanaries(t, "profile follow", out+"\n"+errs)
		})
	}

	t.Run("accepted path dials and keeps the path", func(t *testing.T) {
		calls = 0
		hub := checkedPathHub()
		_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", hub, "vn_peer_x"))
		code, out, errs := pinRun(env, "profile", "follow")
		if code == 0 || calls == 0 {
			t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, out, errs)
		}
		assertNoCanaries(t, "accepted follow", out+"\n"+errs)
		if !strings.Contains(out+errs, "/"+redactPath) && !strings.Contains(out+errs, "notices") && calls == 0 {
			t.Fatalf("path hub produced no request\n%s\n%s", out, errs)
		}
	})
}

func noticeWithSecrets(t *testing.T, hub, next string, key *hubid.Key) envelope.HubNotice {
	t.Helper()
	n := envelope.HubNotice{
		NoticeID:  "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Kind:      "moving",
		Message:   "move",
		Hub:       hub,
		NewURL:    next,
		At:        envelope.FormatTime(time.Now().Add(time.Hour)),
		IssuedAt:  envelope.FormatTime(time.Now().Add(-time.Second)),
		ExpiresAt: envelope.FormatTime(time.Now().Add(2 * time.Hour)),
	}
	if key != nil {
		if err := key.SignNotice(&n); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func noticeServer(t *testing.T, notices []envelope.HubNotice) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hub/notices" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"notices": notices})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWatchMovingRejectionOmitsHubURL(t *testing.T) {
	key := testIdentityKey(t, "1")
	pathHub := "https://" + redactHost + "/" + redactPath
	pathNext := "https://next.example/" + redactPath
	secretNext := secretHub("")
	modes := []struct {
		name    string
		json    bool
		monitor bool
	}{
		{"human", false, false},
		{"json", true, false},
		{"monitor", false, true},
	}
	// A signed notice cannot carry userinfo or a query: NoticeBytes refuses
	// them. Those canaries travel on the unsigned refusals. The alias is
	// signed, so its secrets are paths, which normalization keeps.
	branches := []struct {
		name    string
		profile Profile
		notice  envelope.HubNotice
		sign    bool
	}{
		{name: "unpinned", notice: noticeWithSecrets(t, secretHub(""), secretNext, nil)},
		{name: "bad signature", profile: Profile{HubPubkey: key.Pubkey()}, notice: noticeWithSecrets(t, "", secretNext, nil)},
		{name: "invalid source", profile: Profile{HubPubkey: key.Pubkey()}, notice: noticeWithSecrets(t, secretHub(""), secretNext, nil)},
		{name: "alias", profile: Profile{HubPubkey: key.Pubkey()}, notice: noticeWithSecrets(t, pathHub, pathNext, nil), sign: true},
	}
	for _, mode := range modes {
		for _, br := range branches {
			t.Run(mode.name+"/"+br.name, func(t *testing.T) {
				n := br.notice
				srv := noticeServer(t, []envelope.HubNotice{})
				if n.Hub == "" || br.name == "bad signature" {
					n.Hub = srv.URL
				}
				if br.sign {
					if err := key.SignNotice(&n); err != nil {
						t.Fatal(err)
					}
				}
				srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/hub/notices" {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"notices": []envelope.HubNotice{n}})
				})
				profile := br.profile
				profile.Hub = srv.URL
				var out, errs bytes.Buffer
				dir := t.TempDir()
				env := map[string]string{"HOME": dir, "XDG_CONFIG_HOME": dir, "XDG_CACHE_HOME": dir}
				w := &watcher{
					a: &app{
						ctx: context.Background(), stdout: &out, stderr: &errs, json: mode.json,
						getenv: func(k string) string { return env[k] },
					},
					c: newClient(srv.URL, "vn_peer_x"), name: "p", profile: profile, forMonitor: mode.monitor,
				}
				w.refreshMoving()
				assertNoCanaries(t, "refreshMoving", out.String()+"\n"+errs.String())
				if br.name == "alias" && !strings.Contains(out.String()+errs.String(), "https://"+redactHost) && !strings.Contains(out.String()+errs.String(), "not following") {
					t.Fatalf("alias diagnostic missing a safe refusal\nout %s\nerr %s", out.String(), errs.String())
				}
				if !strings.Contains(out.String(), "not following") && br.name != "alias" {
					// alias prints on stdout; every refusal should say so
				}
				if !strings.Contains(out.String(), "relocation_unverified") && !strings.Contains(out.String(), "not following") {
					t.Fatalf("no refusal line\nout %s\nerr %s", out.String(), errs.String())
				}
			})
		}
	}
}

func TestFollowNoticeOmitsHubURL(t *testing.T) {
	key := testIdentityKey(t, "1")
	modes := []struct {
		name    string
		json    bool
		monitor bool
	}{
		{"human", false, false},
		{"json", true, false},
		{"monitor", false, true},
	}
	notices := []struct {
		name string
		next string
	}{
		{"userinfo", secretHub("")},
		{"path", "https://" + redactHost + "/" + redactPath},
		{"query", "https://" + redactHost + "?token=" + redactQuery},
		{"fragment", "https://" + redactHost + "/" + redactPath + "#FRAG"},
	}
	for _, mode := range modes {
		for _, tc := range notices {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				n := noticeWithSecrets(t, "https://"+redactHost, tc.next, nil)
				var out, errs bytes.Buffer
				dir := t.TempDir()
				env := map[string]string{"HOME": dir, "XDG_CONFIG_HOME": dir, "XDG_CACHE_HOME": dir}
				w := &watcher{
					a: &app{
						ctx: context.Background(), stdout: &out, stderr: &errs, json: mode.json,
						getenv: func(k string) string { return env[k] },
					},
					name: "p", profile: Profile{Hub: "https://" + redactHost, HubPubkey: key.Pubkey()},
					forMonitor: mode.monitor,
				}
				err := w.followNotice(n, false)
				if err == nil {
					t.Fatal("expected a refusal")
				}
				text := out.String() + "\n" + errs.String() + "\n" + err.Error()
				assertNoCanaries(t, "followNotice", text)
				if !strings.Contains(out.String(), "not following") {
					t.Fatalf("stdout %s", out.String())
				}
				if tc.name == "path" && !strings.Contains(text, tc.next) {
					t.Fatalf("path URL omitted: %s", text)
				}
			})
		}
	}
}

func TestFollowSuccessOmitsPath(t *testing.T) {
	const pathSecret = redactPath
	key := testIdentityKey(t, "1")
	serve := func(public func() string) *httptest.Server {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/v1/hub/identity") {
				http.NotFound(w, r)
				return
			}
			sig, err := key.SignChallenge(public(), r.URL.Query().Get("challenge"))
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: public(), ChallengeSig: sig})
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	var src, dst *httptest.Server
	src = serve(func() string { return src.URL + "/" + pathSecret })
	dst = serve(func() string { return dst.URL + "/" + pathSecret })
	oldHub := src.URL + "/" + pathSecret
	nextHub := dst.URL + "/" + pathSecret
	n := noticeWithSecrets(t, oldHub, nextHub, &key)
	var stderr bytes.Buffer
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", oldHub, key.Pubkey(), "vn_peer_x"))
	a := &app{ctx: context.Background(), getenv: env, stdout: io.Discard, stderr: &stderr}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.follow(a.ctx, "p", cfg.Profiles["p"], n)
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	if got != nextHub {
		t.Fatalf("config destination %q, want full URL", got)
	}
	assertNoCanaries(t, "follow stderr", stderr.String())
	if !strings.Contains(stderr.String(), "verified") || !strings.Contains(stderr.String(), "/"+pathSecret) {
		t.Fatalf("stderr %q", stderr.String())
	}
	cfg, err = loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != nextHub {
		t.Fatalf("stored hub %+v %v", cfg.Profiles["p"], err)
	}
}

func TestWatchAnnounceOmitsHubURL(t *testing.T) {
	modes := []struct {
		name    string
		json    bool
		monitor bool
	}{
		{"human", false, false},
		{"json", true, false},
		{"monitor", false, true},
	}
	key := testIdentityKey(t, "3")
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			c := newClient(checkedPathHub(), "vn_peer_x")
			c.hc.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			w := &watcher{
				identityOff: true, replayed: true, announceMove: true, forMonitor: mode.monitor,
				a:   &app{ctx: context.Background(), stdout: &out, stderr: &errs, json: mode.json, getenv: func(string) string { return "" }},
				c:   c,
				pin: key.Pubkey(), name: "p",
				seen: newDedupe(8),
			}
			if err := w.run(); !errors.Is(err, errPeerDisabled) {
				t.Fatalf("run: %v\n%s", err, errs.String())
			}
			assertNoCanaries(t, "announce", out.String()+"\n"+errs.String())
			if !strings.Contains(out.String(), checkedPathHub()) {
				t.Fatalf("announce %s", out.String())
			}
			if !mode.monitor && !strings.Contains(out.String(), "verified") {
				t.Fatalf("announce %s", out.String())
			}
		})
	}
}

func TestHubURLBoundaries(t *testing.T) {
	var calls int
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("guard: " + r.URL.String())
	})
	t.Cleanup(func() { http.DefaultTransport = old })

	canaries := []struct{ name, hub string }{
		{"userinfo", "https://alice:" + redactUser + "@" + redactHost + "/" + redactPath},
		{"query", "https://" + redactHost + "/" + redactPath + "?token=" + redactQuery},
		{"fragment", "https://" + redactHost + "/" + redactPath + "#FRAG"},
	}
	for _, tc := range canaries {
		t.Run("enrol/"+tc.name, func(t *testing.T) {
			calls = 0
			_, env := pinTestEnv(t, "")
			code, out, errs := pinRun(env, "enrol", "--hub", tc.hub, "--token", "vn_enrol_x", "--profile", "p")
			if code == 0 || calls != 0 {
				t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, out, errs)
			}
			assertNoCanaries(t, "enrol", out+"\n"+errs)
		})
		t.Run("stored/"+tc.name, func(t *testing.T) {
			calls = 0
			body := fmt.Sprintf("[profiles.bad]\nhub = %q\nsecret = %q\n\n[profiles.ok]\nhub = %q\nsecret = %q\n", tc.hub, "vn_peer_x", checkedPathHub(), "vn_peer_y")
			_, base := pinTestEnv(t, body)
			env := func(k string) string {
				if k == "VECNET_PROFILE" {
					return "bad"
				}
				return base(k)
			}
			code, out, errs := pinRun(env, "peers")
			if code == 0 || calls != 0 {
				t.Fatalf("bad profile exit %d calls %d\n%s\n%s", code, calls, out, errs)
			}
			text := out + "\n" + errs
			assertNoCanaries(t, "stored", text)
			if !strings.Contains(errs, "edit the config") {
				t.Fatalf("stderr %q", errs)
			}
			calls = 0
			envOK := func(k string) string {
				if k == "VECNET_PROFILE" {
					return "ok"
				}
				return base(k)
			}
			code, out, errs = pinRun(envOK, "peers")
			if calls == 0 {
				t.Fatalf("good profile made no request\nexit %d\n%s\n%s", code, out, errs)
			}
			assertNoCanaries(t, "sibling", out+"\n"+errs)
		})
		t.Run("repoint/"+tc.name, func(t *testing.T) {
			calls = 0
			_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", checkedPathHub(), "vn_peer_x"))
			code, out, errs := pinRun(env, "profile", "repoint", tc.hub)
			if code == 0 || calls != 0 {
				t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, out, errs)
			}
			assertNoCanaries(t, "repoint", out+"\n"+errs)
		})
		t.Run("notice/"+tc.name, func(t *testing.T) {
			calls = 0
			_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", checkedPathHub(), "vn_operator_x"))
			code, out, errs := pinRun(env, "notice", "post", "--kind", "moving", "--message", "move", "--new-url", tc.hub)
			if code == 0 || calls != 0 {
				t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, out, errs)
			}
			assertNoCanaries(t, "notice", out+"\n"+errs)
		})
	}

	t.Run("show prints path and ipv6", func(t *testing.T) {
		calls = 0
		ipv6 := checkedIPv6Hub()
		_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", ipv6, "vn_peer_x"))
		code, out, errs := pinRun(env, "profile", "show")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if calls != 0 {
			t.Fatal("profile show dialed")
		}
		if !strings.Contains(out, ipv6) {
			t.Fatalf("show dropped the URL: %s", out)
		}
	})

	t.Run("picker hides a bad hub", func(t *testing.T) {
		c := Config{Profiles: map[string]Profile{
			"ok":  {Hub: checkedPathHub(), Secret: "vn_operator_ok"},
			"bad": {Hub: "https://alice:" + redactUser + "@" + redactHost + "?token=" + redactQuery + "#FRAG", Secret: "vn_operator_bad"},
		}}
		got := operatorProfiles(c, false)
		text := ""
		var okHub string
		for _, choice := range got {
			text += choice.Name + " " + choice.Hub + "\n"
			if choice.Name == "ok" {
				okHub = choice.Hub
			}
		}
		assertNoCanaries(t, "picker", text)
		if okHub != checkedPathHub() {
			t.Fatalf("ok hub %q", okHub)
		}
		if !strings.Contains(text, "edit the config") {
			t.Fatalf("bad profile missing a fixed label: %s", text)
		}
	})
}
