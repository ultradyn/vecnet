package cli

// Public-dispatch regression draft for I1. Execution and the baseline RED
// receipt belong to the parent; this file was authored without running Go.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

const statsMovePrivate = "message-canary\n hint-canary\x1b[31m details-canary " + t3StatsURLCanary
const statsMoveSelector = "selector-canary +&id=second?#/"

type statsMoveReply struct {
	status int
	body   []byte
}

type statsMoveHit struct {
	path   string
	status int
	body   []byte
}

// Keep the real hub handlers and identity challenge proofs. Only negative
// fixtures replace a stats response. Capturing the actual successful DTO
// avoids a second read with a potentially different sample timestamp.
type statsMoveObserver struct {
	server *httptest.Server
	mu     sync.Mutex
	hits   []statsMoveHit
	reply  *statsMoveReply // installed before RunContext; never changed in flight
}

func statsMoveProxy(t *testing.T, e *testhub.Env, id string) *statsMoveObserver {
	t.Helper()
	u, err := url.Parse(e.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	o := new(statsMoveObserver)
	o.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("relocation issued a non-GET request")
		}
		switch r.URL.Path {
		case "/v1/hub/identity":
			q := r.URL.Query()
			if len(q) != 1 || len(q["challenge"]) != 1 || q.Get("challenge") == "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Vecnet-Client") != "" {
				t.Error("identity verification must use a fresh public challenge without credentials")
			}
		case "/v1/admin/route-stats":
			q := r.URL.Query()
			if len(q) != 1 || len(q["id"]) != 1 || q.Get("id") != id || r.URL.RawQuery != (url.Values{"id": {id}}).Encode() || r.ContentLength > 0 {
				t.Error("relocation changed the single encoded stats selector or GET body")
			}
			if r.Header.Get("Authorization") != "Bearer "+e.OperatorSecret || r.Header.Get("Accept") != "application/json" {
				t.Error("stats retry lost the selected operator credential or JSON accept header")
			}
		default:
			t.Error("unexpected request outside stats and public identity verification")
		}
		rec := httptest.NewRecorder()
		if r.URL.Path == "/v1/admin/route-stats" && o.reply != nil {
			rec.Header().Set("Content-Type", "application/json")
			rec.WriteHeader(o.reply.status)
			_, _ = rec.Write(o.reply.body)
		} else {
			proxy.ServeHTTP(rec, r)
		}
		o.mu.Lock()
		o.hits = append(o.hits, statsMoveHit{r.URL.Path, rec.Code, append([]byte(nil), rec.Body.Bytes()...)})
		o.mu.Unlock()
		for k, values := range rec.Header() {
			w.Header()[k] = append([]string(nil), values...)
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(o.server.Close)
	e.Hub.Cfg.PublicURL = o.server.URL
	return o
}

func (o *statsMoveObserver) snapshot() []statsMoveHit {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]statsMoveHit(nil), o.hits...)
}

func statsMoveProblem(t *testing.T, status int, code string, notice any, poison string) *statsMoveReply {
	t.Helper()
	details := map[string]any{"new_url": t3StatsURLCanary, "private": poison}
	if notice != nil {
		details["notice"] = notice
	}
	raw, err := json.Marshal(map[string]any{"error": map[string]any{
		"code": code, "message": poison, "hint": poison,
		"details": details,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &statsMoveReply{status, raw}
}

type statsMoveFixture struct {
	a, b         *testhub.Env
	source, next *statsMoveObserver
	id, path     string
	env          func(string) string
	notice       envelope.HubNotice
	config       []byte
	pins         []byte
	pinsFile     string
	profiles     Config
}

func newStatsMoveFixture(t *testing.T, hostileSelector bool) *statsMoveFixture {
	t.Helper()
	a := testhub.Start(t)
	peer, _ := a.NewPeer("session", "stats-move-synthetic")
	id, _, err := a.Hub.AddRoute(context.Background(), "peer", peer, "https://stats.example/private-path?private-query")
	if err != nil {
		t.Fatal(err)
	}
	b := a.CloneDB(t)
	if a.Hub.IdentityKey().Pubkey() != b.Hub.IdentityKey().Pubkey() {
		t.Fatal("fixture clone did not retain the identity key")
	}
	if hostileSelector {
		id = statsMoveSelector
	}
	next := statsMoveProxy(t, b, id)
	source := statsMoveProxy(t, a, id)
	key := a.Hub.IdentityKey().Pubkey()
	other := testIdentityKey(t, "2").Pubkey()
	body := fmt.Sprintf("# keep relocation comments\n[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\nunknown_key = \"keep me\"\n"+
		"[profiles.q]\n# same hub and pin\nhub = %q\nhub_pubkey = %q\nsecret = %q\n"+
		"[profiles.r]\n# different pin must stay\nhub = %q\nhub_pubkey = %q\nsecret = \"vn_other_synthetic\"\n"+
		"[profiles.u]\n# different hub must stay\nhub = %q\nhub_pubkey = %q\nsecret = \"vn_unrelated_synthetic\"\n",
		source.server.URL, key, a.OperatorSecret, source.server.URL+"/", key, a.OperatorSecret,
		source.server.URL, other, next.server.URL, key)
	path, env := pinTestEnv(t, body)
	seed := &app{ctx: context.Background(), getenv: env}
	if err := seed.updatePins(func(m map[string]pinRecord) error {
		for name, pin := range map[string]string{"p": key, "q": key, "r": other, "u": key} {
			hub := source.server.URL
			if name == "u" {
				hub = next.server.URL
			}
			m[name] = pinRecord{Hub: hub, Pubkey: pin, PinnedAt: "2020-01-01T00:00:00Z", How: "config"}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pp, err := pinsPath(env)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	n := a.Relocate(t, next.server.URL)
	return &statsMoveFixture{a, b, source, next, id, path, env, n, pinRead(t, path), pinRead(t, pp), pp, cfg}
}

func statsMoveRun(f *statsMoveFixture, env func(string) string) (int, string, string) {
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), []string{"--net", "p", "route", "stats", f.id, "--json"}, strings.NewReader(""), &out, &errs, env)
	return code, out.String(), errs.String()
}

func statsMoveNoLeak(t *testing.T, f *statsMoveFixture, text string, extra ...string) {
	t.Helper()
	canaries := append([]string{f.a.OperatorSecret, "message-canary", "hint-canary", "details-canary", "selector-canary",
		"userinfo-canary", "password-canary", "failure.example", "private-path", "query-canary", "fragment-canary", "\x1b"}, extra...)
	for _, canary := range canaries {
		if canary != "" && strings.Contains(text, canary) {
			t.Fatal("route stats relocation output leaked synthetic private text")
		}
	}
}

func statsMoveHits(t *testing.T, o *statsMoveObserver, paths []string, statuses []int) []statsMoveHit {
	t.Helper()
	hits := o.snapshot()
	if len(hits) != len(paths) {
		t.Fatalf("request count=%d want=%d", len(hits), len(paths))
	}
	for i, hit := range hits {
		if hit.path != paths[i] || hit.status != statuses[i] {
			t.Errorf("request %d: path=%s status=%d want path=%s status=%d", i, hit.path, hit.status, paths[i], statuses[i])
		}
	}
	return hits
}

func (f *statsMoveFixture) unchanged(t *testing.T) {
	t.Helper()
	if !bytes.Equal(pinRead(t, f.path), f.config) || !bytes.Equal(pinRead(t, f.pinsFile), f.pins) {
		t.Error("unverified or disabled follow changed config or pins")
	}
	if _, err := os.Stat(f.path + ".bak"); !os.IsNotExist(err) {
		t.Error("unverified or disabled follow created a config backup")
	}
}

func (f *statsMoveFixture) followed(t *testing.T, errs string) {
	t.Helper()
	cfg, err := loadConfig(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "q"} {
		want := f.profiles.Profiles[name]
		want.Hub, want.HubPubkey = f.next.server.URL, f.a.Hub.IdentityKey().Pubkey()
		if cfg.Profiles[name] != want {
			t.Errorf("profile %s did not follow while preserving its other fields", name)
		}
	}
	for _, name := range []string{"r", "u"} {
		if cfg.Profiles[name] != f.profiles.Profiles[name] {
			t.Errorf("unrelated profile %s changed", name)
		}
	}
	if !bytes.Equal(pinRead(t, f.path+".bak"), f.config) {
		t.Error("follow did not back up the exact original config")
	}
	body := string(pinRead(t, f.path))
	for _, text := range []string{"# keep relocation comments", "# same hub and pin", "unknown_key = \"keep me\""} {
		if !strings.Contains(body, text) {
			t.Error("follow lost comments or unknown config keys")
		}
	}
	// The remaining two profile sections must survive byte for byte.
	_, beforeTail, _ := strings.Cut(string(f.config), "[profiles.r]")
	_, afterTail, found := strings.Cut(body, "[profiles.r]")
	if !found || beforeTail != afterTail {
		t.Error("follow rewrote unrelated profile text")
	}
	records, err := readPins(f.pinsFile)
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]pinRecord
	if err := json.Unmarshal(f.pins, &before); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "q"} {
		want := before[name]
		want.Hub, want.How = f.next.server.URL, "follow"
		want.LastMove = &moveRecord{From: f.source.server.URL, To: f.next.server.URL, NoticeID: f.notice.NoticeID, IssuedAt: f.notice.IssuedAt}
		if !reflect.DeepEqual(records[name], want) {
			t.Errorf("profile %s lost its pin or exact verified move record", name)
		}
	}
	for _, name := range []string{"r", "u"} {
		if !reflect.DeepEqual(records[name], before[name]) {
			t.Errorf("unrelated pin %s changed", name)
		}
	}
	if strings.Count(errs, "profile p now uses ") != 1 || !strings.Contains(errs, "verified)") {
		t.Error("expected exactly one verified follow diagnostic")
	}
}

// Expected RED at the assigned base: the helper currently erases hub_moved.
func TestRouteStatsRelocationSignedClone(t *testing.T) {
	for _, hostile := range []bool{false, true} {
		t.Run(fmt.Sprintf("hostile-envelope-%t", hostile), func(t *testing.T) {
			f := newStatsMoveFixture(t, false)
			if hostile {
				n := f.notice
				n.Message = statsMovePrivate
				if err := f.a.Hub.IdentityKey().SignNotice(&n); err != nil {
					t.Fatal(err)
				}
				f.source.reply = statsMoveProblem(t, http.StatusGone, "hub_moved", n, statsMovePrivate)
			}
			code, out, errs := statsMoveRun(f, f.env)
			if code != 0 {
				t.Fatalf("signed same-key move: exit=%d want=0 stderr=%q", code, errs)
			}
			statsMoveHits(t, f.source, []string{"/v1/admin/route-stats"}, []int{410})
			hits := statsMoveHits(t, f.next, []string{"/v1/hub/identity", "/v1/admin/route-stats"}, []int{200, 200})
			if !reflect.DeepEqual(t3StatsJSON(t, out), t3StatsJSON(t, string(hits[1].body))) {
				t.Error("followed JSON lost complete actual successor DTO fields, arrays or nulls")
			}
			if !strings.Contains(out, f.id) || strings.Count(strings.TrimSpace(errs), "\n") != 0 {
				t.Error("followed stats did not return the selected route and one diagnostic")
			}
			f.followed(t, errs)
			statsMoveNoLeak(t, f, out+errs)
		})
	}
}

// These refusal requirements are also expected RED until I1 is repaired.
// Their exit 1 and reasons come from followFailure/follow/verifyHubAt, not
// routeStatsError's HTTP exit 2 or network exit 3.
func TestRouteStatsRelocationRefusals(t *testing.T) {
	for _, mode := range []string{"no-follow", "missing-notice", "malformed-notice", "wrong-kind", "bad-signature", "wrong-source", "unchanged-destination", "expired", "cleared", "secret-url", "replay", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			f := newStatsMoveFixture(t, true)
			n := f.notice
			n.Message = statsMovePrivate
			if err := f.a.Hub.IdentityKey().SignNotice(&n); err != nil {
				t.Fatal(err)
			}
			env := f.env
			var notice any = n
			reason := ""
			var wrongKey *statsMoveObserver
			switch mode {
			case "no-follow":
				env = func(k string) string {
					if k == "VECNET_NO_FOLLOW" {
						return "1"
					}
					return f.env(k)
				}
				reason = "hub moved; run `vecnet --net p profile follow`"
			case "missing-notice":
				notice, reason = nil, "hub returned an invalid moving notice"
			case "malformed-notice":
				notice, reason = statsMovePrivate, "hub returned an invalid moving notice"
			case "wrong-kind":
				n.Kind = "maintenance"
				notice, reason = n, "hub returned an invalid moving notice"
			case "bad-signature":
				n.Message += "tampered after signing"
				notice, reason = n, "invalid moving notice signature"
			case "wrong-source":
				n.Hub = f.next.server.URL
				notice, reason = n, "notice does not describe this hub moving"
			case "unchanged-destination":
				n.NewURL = f.source.server.URL
				notice, reason = n, "invalid or unchanged destination"
			case "expired":
				n.IssuedAt = envelope.FormatTime(time.Now().Add(-2 * time.Hour))
				n.ExpiresAt = envelope.FormatTime(time.Now().Add(-time.Hour))
				if err := f.a.Hub.IdentityKey().SignNotice(&n); err != nil {
					t.Fatal(err)
				}
				notice, reason = n, "moving notice is no longer active"
			case "cleared":
				n.ClearedAt = n.IssuedAt
				notice, reason = n, "moving notice is no longer active"
			case "secret-url":
				n.NewURL = t3StatsURLCanary
				// URL rejection at the receive boundary precedes authenticated follow.
				notice, reason = n, "hub returned an invalid moving notice"
			case "replay":
				seed := &app{ctx: context.Background(), getenv: f.env}
				if err := seed.updatePins(func(m map[string]pinRecord) error {
					r := m["p"]
					r.LastMove = &moveRecord{IssuedAt: n.IssuedAt}
					m["p"] = r
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				f.pins = pinRead(t, f.pinsFile)
				reason = "moving notice was already followed"
			case "wrong-key":
				target := testhub.Start(t)
				if target.Hub.IdentityKey().Pubkey() == f.a.Hub.IdentityKey().Pubkey() {
					t.Fatal("wrong-key fixture unexpectedly shares the source key")
				}
				wrongKey = statsMoveProxy(t, target, f.id)
				n.NewURL = wrongKey.server.URL
				if err := f.a.Hub.IdentityKey().SignNotice(&n); err != nil {
					t.Fatal(err)
				}
				notice, reason = n, "hub key does not match the pin"
			}
			f.source.reply = statsMoveProblem(t, 410, "hub_moved", notice, statsMovePrivate)
			code, out, errs := statsMoveRun(f, env)
			if code != 1 || out != "" || errs != "vecnet: relocation_unverified: "+reason+"\n" {
				t.Errorf("refusal exit=%d want=1 stdout=%q stderr=%q", code, out, errs)
			}
			statsMoveHits(t, f.source, []string{"/v1/admin/route-stats"}, []int{410})
			statsMoveHits(t, f.next, nil, nil)
			if wrongKey != nil {
				statsMoveHits(t, wrongKey, []string{"/v1/hub/identity"}, []int{200})
			}
			f.unchanged(t)
			statsMoveNoLeak(t, f, out+errs)
		})
	}
}

// Already-correct controls: neither the status nor the code alone authorizes
// following. Retain the existing fixed route-stats message and HTTP exit 2.
func TestRouteStatsRelocationStatusCodeControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		want   string
	}{
		{"moved-code-with-conflict", 409, "hub_moved", "http_error: route stats request failed\n"},
		{"moved-code-with-server-error", 500, "hub_moved", "http_error: route stats request failed\n"},
		{"gone-with-other-code", 410, "not_found", "not_found: route stats subject was not found\n"},
		{"gone-with-hostile-code", 410, "remote_private_code_canary", "http_error: route stats request failed\n"},
		{"gone-with-numeric-fallback", 410, "http_410", "http_410: route stats request failed\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStatsMoveFixture(t, true)
			f.source.reply = statsMoveProblem(t, tc.status, tc.code, f.notice, statsMovePrivate)
			code, out, errs := statsMoveRun(f, f.env)
			if code != 2 || out != "" || errs != tc.want {
				t.Errorf("status/code control exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			statsMoveHits(t, f.source, []string{"/v1/admin/route-stats"}, []int{tc.status})
			statsMoveHits(t, f.next, nil, nil)
			f.unchanged(t)
			statsMoveNoLeak(t, f, out+errs, "remote_private_code_canary")
		})
	}
}

// Expected RED at base. dispatch retries only once: a second valid signed
// move is a terminal hub error, with no third identity/stats call or rewrite.
func TestRouteStatsRelocationSecondHopStops(t *testing.T) {
	var terminal string
	for _, poison := range []string{statsMovePrivate, "second-envelope-canary " + statsMovePrivate} {
		t.Run(fmt.Sprintf("envelope-%d", len(poison)), func(t *testing.T) {
			f := newStatsMoveFixture(t, true)
			third := statsMoveProxy(t, f.b.CloneDB(t), f.id)
			n := f.b.Relocate(t, third.server.URL)
			n.Message = poison
			if err := f.b.Hub.IdentityKey().SignNotice(&n); err != nil {
				t.Fatal(err)
			}
			f.next.reply = statsMoveProblem(t, 410, "hub_moved", n, poison)
			code, out, errs := statsMoveRun(f, f.env)
			if code != 2 || out != "" {
				t.Errorf("second relocation exit=%d want=2 stdout=%q", code, out)
			}
			statsMoveHits(t, f.source, []string{"/v1/admin/route-stats"}, []int{410})
			statsMoveHits(t, f.next, []string{"/v1/hub/identity", "/v1/admin/route-stats"}, []int{200, 410})
			statsMoveHits(t, third, nil, nil)
			lines := strings.Split(strings.TrimSuffix(errs, "\n"), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[1], "hub_moved: ") {
				t.Fatalf("expected one verified-follow line and one fixed terminal hub_moved line: %q", errs)
			}
			if terminal != "" && lines[1] != terminal {
				t.Error("terminal output changed with arbitrary remote envelope fields")
			}
			terminal = lines[1]
			f.followed(t, errs)
			statsMoveNoLeak(t, f, out+errs, "second-envelope-canary", third.server.URL, n.NoticeID)
		})
	}
}
