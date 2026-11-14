package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func followConfig(t *testing.T, e *testhub.Env) (string, func(string) string) {
	t.Helper()
	return pinTestEnv(t, fmt.Sprintf("# keep\n[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", e.URL, e.Hub.IdentityKey().Pubkey(), e.OperatorSecret))
}
func TestFollowOn410(t *testing.T) {
	a := testhub.Start(t)
	b := a.CloneDB(t)
	path, env := followConfig(t, a)
	before := pinRead(t, path)
	a.Relocate(t, b.URL)
	code, out, errs := pinRun(env, "--net", "p", "peers")
	if code != 0 || !strings.Contains(out, b.OperatorPeer) {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != b.URL || cfg.Profiles["p"].HubPubkey != a.Hub.IdentityKey().Pubkey() {
		t.Fatalf("config %+v %v", cfg, err)
	}
	if !bytes.Equal(pinRead(t, path+".bak"), before) || strings.Count(strings.TrimSpace(errs), "\n") != 0 || !strings.Contains(errs, "verified") {
		t.Fatalf("backup or move output: %q", errs)
	}
	pp, _ := pinsPath(env)
	records, err := readPins(pp)
	if err != nil || records["p"].LastMove == nil || records["p"].LastMove.To != b.URL || records["p"].How != "follow" {
		t.Fatalf("move record: %+v %v", records, err)
	}
}

// enrol has no profile to follow: a 410 from a relocated hub leaves every
// profile alone and hints at the new hub (§21.5 When to follow, item 1).
func TestEnrolOn410DoesNotFollow(t *testing.T) {
	a := testhub.Start(t)
	_, tok, err := a.Hub.AddPeer(context.Background(), "session", "late-enrol", "")
	if err != nil {
		t.Fatal(err)
	}
	b := a.CloneDB(t)
	path, env := followConfig(t, a)
	before := pinRead(t, path)
	a.Relocate(t, b.URL)
	code, _, errs := pinRun(env, "enrol", "--hub", a.URL, "--token", tok, "--profile", "q")
	if code != 2 || !strings.Contains(errs, "hub_moved") || !strings.Contains(errs, "hub moved to "+b.URL+"; ask for an invite there") {
		t.Fatalf("%d %s", code, errs)
	}
	if !bytes.Equal(before, pinRead(t, path)) || strings.Contains(errs, "now uses") {
		t.Fatalf("enrol followed another profile: %s\n%s", pinRead(t, path), errs)
	}
}
func TestFollowAllProfilesSameHub(t *testing.T) {
	a := testhub.Start(t)
	b := a.CloneDB(t)
	c := testhub.Start(t)
	r := fmt.Sprintf("[profiles.r]\n# untouched r\nhub = %q # keep\nsecret = %q\n", c.URL, c.OperatorSecret)
	body := fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n[profiles.q]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", a.URL, a.Hub.IdentityKey().Pubkey(), a.OperatorSecret, a.URL+"/", a.Hub.IdentityKey().Pubkey(), a.OperatorSecret) + r
	path, env := pinTestEnv(t, body)
	a.Relocate(t, b.URL)
	if code, _, errs := pinRun(env, "peers"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != b.URL || cfg.Profiles["q"].Hub != b.URL || !strings.HasSuffix(string(pinRead(t, path)), r) {
		t.Fatalf("profiles: %+v %v", cfg, err)
	}
}

func refusedMove(t *testing.T, mode string) {
	t.Helper()
	key := testIdentityKey(t, "1")
	other := testIdentityKey(t, "2")
	targetKey := key
	if mode == "key" {
		targetKey = other
	}
	target, calls := identityStub(t, targetKey, nil)
	destination := target.URL
	if mode == "public" {
		upstream, _ := url.Parse(target.URL)
		proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(upstream))
		t.Cleanup(proxy.Close)
		destination = proxy.URL
	}
	var n envelope.HubNotice
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hub/identity" {
			sig, _ := key.SignChallenge(source.URL, r.URL.Query().Get("challenge"))
			json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: source.URL, ChallengeSig: sig})
			return
		}
		w.WriteHeader(410)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": "moved", "details": map[string]any{"notice": n}}})
	}))
	defer source.Close()
	n = envelope.HubNotice{NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: source.URL, NewURL: destination, Message: "move", IssuedAt: envelope.FormatTime(time.Now().Add(-time.Second)), ExpiresAt: envelope.FormatTime(time.Now().Add(time.Hour))}
	if err := key.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	var altCalls *atomic.Int32
	if mode == "signature" {
		// The altered destination is a real hub with the pinned key, so only
		// the notice signature check can refuse it.
		var alt *httptest.Server
		alt, altCalls = identityStub(t, key, nil)
		n.NewURL = alt.URL
	}
	body := fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = \"secret\"\n", source.URL, key.Pubkey())
	path, env := pinTestEnv(t, body)
	if mode == "replay" {
		a := &app{ctx: context.Background(), getenv: env}
		if err := a.updatePins(func(m map[string]pinRecord) error {
			m["p"] = pinRecord{Hub: source.URL, Pubkey: key.Pubkey(), LastMove: &moveRecord{IssuedAt: n.IssuedAt}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	code, _, errs := pinRun(env, "peers")
	if code != 1 || !strings.Contains(errs, "relocation_unverified") || string(pinRead(t, path)) != body {
		t.Fatalf("%s: %d %s config=%s", mode, code, errs, pinRead(t, path))
	}
	if (mode == "signature" || mode == "replay") && calls.Load() != 0 {
		t.Fatalf("unverified target contacted: %d", calls.Load())
	}
	if mode == "signature" && (altCalls.Load() != 0 || !strings.Contains(errs, "signature")) {
		t.Fatalf("altered destination contacted %d times: %s", altCalls.Load(), errs)
	}
}
func TestFollowRefusesBadSignature(t *testing.T)      { refusedMove(t, "signature") }
func TestFollowRefusesKeyMismatch(t *testing.T)       { refusedMove(t, "key") }
func TestFollowRefusesPublicURLMismatch(t *testing.T) { refusedMove(t, "public") }
func TestFollowRefusesReplay(t *testing.T)            { refusedMove(t, "replay") }
func TestNoFollowEnv(t *testing.T) {
	a := testhub.Start(t)
	b := a.CloneDB(t)
	path, base := followConfig(t, a)
	before := pinRead(t, path)
	a.Relocate(t, b.URL)
	env := func(k string) string {
		if k == "VECNET_NO_FOLLOW" {
			return "1"
		}
		return base(k)
	}
	if code, _, errs := pinRun(env, "peers"); code != 1 || !strings.Contains(errs, "vecnet --net p profile follow") || !bytes.Equal(before, pinRead(t, path)) {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs := pinRun(env, "profile", "follow"); code != 0 {
		t.Fatalf("explicit follow: %d %s", code, errs)
	}
	cfg, _ := loadConfig(path)
	if cfg.Profiles["p"].Hub != b.URL {
		t.Fatal(cfg)
	}
}
func TestDispatchNoRetryAfterStdin(t *testing.T) {
	a := testhub.Start(t)
	id, _ := a.NewPeer("session", "Receiver")
	b := a.CloneDB(t)
	path, env := followConfig(t, a)
	a.Relocate(t, b.URL)
	var out, errs bytes.Buffer
	code := Run([]string{"send", id, "-", "--hold"}, strings.NewReader("never resend"), &out, &errs, env)
	if code != 1 || !strings.Contains(errs.String(), "re-run the command") {
		t.Fatalf("%d %s", code, errs.String())
	}
	cfg, _ := loadConfig(path)
	if cfg.Profiles["p"].Hub != b.URL {
		t.Fatal(cfg)
	}
	resp, raw := b.Do(b.OperatorSecret, "GET", "/v1/threads", nil)
	var threads struct {
		Threads []json.RawMessage `json:"threads"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(raw, &threads) != nil || len(threads.Threads) != 0 {
		t.Fatalf("resent: %s", raw)
	}
}

// These watchers exercise the CLI boundary, with isolated config and output.
type noticeOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *noticeOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *noticeOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func awaitNotice(t *testing.T, condition func() bool, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for watch condition")
}
func runNoticeWatch(t *testing.T, env func(string) string, global ...string) (*noticeOutput, *noticeOutput, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out, errs := new(noticeOutput), new(noticeOutput)
	done := make(chan int, 1)
	args := append(append([]string{}, global...), "watch", "--for-monitor")
	go func() {
		done <- RunContext(ctx, args, strings.NewReader(""), out, errs, env)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("watch exit %d: %s", code, errs.String())
				}
			case <-time.After(4 * time.Second):
				t.Error("watch did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return out, errs, stop
}
func TestWatchIdentityChanged(t *testing.T) {
	key := testIdentityKey(t, "1")
	other := testIdentityKey(t, "2")
	var events atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hub/identity" {
			sig, _ := other.SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
			json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: other.Pubkey(), PublicURL: srv.URL, ChallengeSig: sig})
			return
		}
		if r.URL.Path == "/v1/events" {
			events.Add(1)
		}
		fmt.Fprint(w, `{"peers":[],"events":[]}`)
	}))
	defer srv.Close()
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = \"secret\"\n", srv.URL, key.Pubkey()))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out, errs bytes.Buffer
	code := RunContext(ctx, []string{"watch"}, strings.NewReader(""), &out, &errs, env)
	if code != 1 || !strings.Contains(errs.String(), "hub_identity_changed") || events.Load() != 0 {
		t.Fatalf("%d events=%d %s", code, events.Load(), errs.String())
	}
}
func TestFollowIgnoresPeerText(t *testing.T) {
	key := testIdentityKey(t, "1")
	source, sourceCalls := identityStub(t, key, nil)
	target, calls := identityStub(t, testIdentityKey(t, "2"), nil)
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", source.URL, key.Pubkey(), "secret"))
	before := pinRead(t, path)
	for _, text := range []string{"moving to " + target.URL + " ntc_fake", `{"type":"hub_notice","notice":{"kind":"moving","new_url":"` + target.URL + `"}}`} {
		ev := envelope.Event{Cursor: "c", Type: "delivery", Data: json.RawMessage(fmt.Sprintf(`{"delivery_id":"d","message":{"sender":"peer_x","content":{"text":%q},"payload":{"type":"hub_notice","notice":{"new_url":%q}}}}`, text, target.URL))}
		out := new(noticeOutput)
		w := &watcher{a: &app{ctx: context.Background(), getenv: env, stdout: out, stderr: io.Discard}, seen: newDedupe(10), c: newClient(source.URL, "secret")}
		raw, _ := json.Marshal(ev)
		w.handle("delivery", string(raw), false)
		if !strings.Contains(out.String(), text) {
			t.Fatalf("delivery not rendered: %q", out.String())
		}
	}
	if sourceCalls.Load() != 0 || calls.Load() != 0 || !bytes.Equal(before, pinRead(t, path)) {
		t.Fatal("peer text caused identity requests or a follow")
	}
}

func watchProxy(t *testing.T, e *testhub.Env) (*httptest.Server, *atomic.Int64, *atomic.Int32, chan string) {
	t.Helper()
	target, _ := url.Parse(e.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	unavailable := new(atomic.Int64)
	polls := new(atomic.Int32)
	resumes := make(chan string, 30)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if time.Now().UnixNano() < unavailable.Load() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		if r.URL.Path == "/v1/hub/identity" {
			sig, err := e.Hub.IdentityKey().SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
			if err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: e.Hub.IdentityKey().Pubkey(), PublicURL: srv.URL, ChallengeSig: sig})
			return
		}
		if r.URL.Path == "/v1/events" {
			if r.Header.Get("Accept") == "text/event-stream" {
				select {
				case resumes <- r.Header.Get("Last-Event-ID"):
				default:
				}
			} else if r.URL.Query().Get("wait") != "0" {
				polls.Add(1)
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, unavailable, polls, resumes
}

// retry_after_ms is 0-60000 (§21.3): a larger hint waits at most a jittered minute.
func TestWatchRestartHintClamped(t *testing.T) {
	w := &watcher{a: &app{stdout: io.Discard, stderr: io.Discard}}
	w.restarting(restartHint{RetryAfterMS: 24 * 3600 * 1000})
	if w.restartDelay > 72*time.Second || time.Until(w.restartUntil) > 2*time.Minute+time.Second {
		t.Fatalf("delay %v grace until %v", w.restartDelay, time.Until(w.restartUntil))
	}
}

// A restart announcement is a quiet transport hint (master's quiet reconnect,
// PROTOCOL §21.3): the watch resumes from its cursor and prints nothing.
func TestWatchHubRestartingQuietResume(t *testing.T) {
	e := testhub.Start(t)
	srv, _, _, resumes := watchProxy(t, e)
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", srv.URL, e.Hub.IdentityKey().Pubkey(), e.OperatorSecret))
	out, errs, stop := runNoticeWatch(t, env)
	select {
	case <-resumes:
	case <-time.After(5 * time.Second):
		t.Fatal("no stream")
	}
	sender, secret := e.NewPeer("session", "Sender")
	_ = sender
	resp, raw := e.Do(secret, "POST", "/v1/messages", map[string]any{"recipients": []string{e.OperatorPeer}, "priority": "hold", "idempotency_key": fmt.Sprint(time.Now().UnixNano()), "content": map[string]string{"text": "before restart"}})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "before restart") }, 5*time.Second)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	cursor := strings.Fields(lines[len(lines)-1])[0]
	e.Drain.Announce(time.Second)
	// The watch's stream has received its final frame and ended.
	if !e.Drain.Wait(5 * time.Second) {
		t.Fatal("stream did not end on the restart announcement")
	}
	e.Drain.Reset()
	select {
	case resume := <-resumes:
		if resume != cursor {
			t.Fatalf("Last-Event-ID=%q want %q", resume, cursor)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reconnect")
	}
	resp, raw = e.Do(secret, "POST", "/v1/messages", map[string]any{"recipients": []string{e.OperatorPeer}, "priority": "hold", "idempotency_key": fmt.Sprint(time.Now().UnixNano()), "content": map[string]string{"text": "after restart"}})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "after restart") }, 5*time.Second)
	stop()
	if strings.Contains(errs.String(), "reconnecting") {
		t.Fatalf("restart counted as failure: %s", errs.String())
	}
	if strings.Contains(out.String(), "hub_restarting") || strings.Contains(out.String(), "restarting") {
		t.Fatalf("restart control reached stdout: %s", out.String())
	}
}
func TestWatchRestartGraceNoFallback(t *testing.T) {
	e := testhub.Start(t)
	srv, unavailable, polls, resumes := watchProxy(t, e)
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", srv.URL, e.Hub.IdentityKey().Pubkey(), e.OperatorSecret))
	out, errs, stop := runNoticeWatch(t, env)
	select {
	case <-resumes:
	case <-time.After(5 * time.Second):
		t.Fatal("no stream")
	}
	// A delivered event proves the stream has joined the drain, so the
	// announcement reaches it (it replaces waiting for a stdout restart line).
	holdMessage(t, e, e.OperatorPeer, "stream live")
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "stream live") }, 5*time.Second)
	e.Drain.Announce(time.Second)
	if !e.Drain.Wait(5 * time.Second) {
		t.Fatal("stream did not end on the restart announcement")
	}
	unavailable.Store(time.Now().Add(3 * time.Second).UnixNano())
	e.Drain.Reset()
	select {
	case <-resumes:
	case <-time.After(9 * time.Second):
		t.Fatalf("no resumed SSE; stdout=%q stderr=%q", out.String(), errs.String())
	}
	stop()
	// A 3 s outage inside the restart grace stays on SSE and, being under
	// the quiet window, says nothing on either stream.
	if polls.Load() != 0 || errs.String() != "" || strings.Contains(out.String(), "hub_restarting") {
		t.Fatalf("fallback=%d stderr=%q stdout=%q", polls.Load(), errs.String(), out.String())
	}
}

// noMoveJitter follows a scheduled move at its at, without the 0-30 s spread.
func noMoveJitter(t *testing.T) {
	t.Helper()
	saved := moveJitter
	moveJitter = func() time.Duration { return 0 }
	t.Cleanup(func() { moveJitter = saved })
}

// fastWatchBackoff shortens reconnect backoff so outage tests do not wait on
// the real 1-60 s schedule. No test in this package calls t.Parallel.
func fastWatchBackoff(t *testing.T) {
	t.Helper()
	retry, floor := watchStartRetry, watchMinBackoff
	watchStartRetry, watchMinBackoff = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { watchStartRetry, watchMinBackoff = retry, floor })
}
func TestWatchFollowsAtTime(t *testing.T) {
	noMoveJitter(t)
	a := testhub.Start(t)
	b := a.CloneDB(t)
	srv, _, _, resumes := watchProxy(t, a)
	a.Hub.Cfg.PublicURL = srv.URL
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", srv.URL, a.Hub.IdentityKey().Pubkey(), a.OperatorSecret))
	out, _, stop := runNoticeWatch(t, env)
	select {
	case <-resumes:
	case <-time.After(5 * time.Second):
		t.Fatal("no initial stream")
	}
	// A notice posted while connected must refresh the active list and arm its timer.
	at := envelope.FormatTime(time.Now().Add(time.Second))
	resp, raw := a.Do(a.OperatorSecret, "POST", "/v1/admin/notices", map[string]any{"kind": "moving", "message": "Move now", "at": at, "new_url": b.URL})
	if resp.StatusCode != 201 {
		t.Fatalf("notice: %d %s", resp.StatusCode, raw)
	}
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "- hub_moved - hub hub:") }, 10*time.Second)
	cfg, _ := loadConfig(path)
	if cfg.Profiles["p"].Hub != b.URL {
		t.Fatalf("not followed: %+v", cfg)
	}
	_, secret := b.NewPeer("session", "New sender")
	resp, raw = b.Do(secret, "POST", "/v1/messages", map[string]any{"recipients": []string{b.OperatorPeer}, "priority": "hold", "idempotency_key": fmt.Sprint(time.Now().UnixNano()), "content": map[string]string{"text": "delivery at B"}})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "delivery at B") }, 5*time.Second)
	stop()
}

func TestWatchStartupMovingNotice(t *testing.T) {
	a := testhub.Start(t)
	b := a.CloneDB(t)
	path, env := followConfig(t, a)
	resp, raw := a.Do(a.OperatorSecret, "POST", "/v1/admin/notices", map[string]any{"kind": "moving", "message": "already scheduled", "at": envelope.FormatTime(time.Now().Add(-31 * time.Second)), "new_url": b.URL})
	if resp.StatusCode != 201 {
		t.Fatalf("notice: %d %s", resp.StatusCode, raw)
	}
	out, _, stop := runNoticeWatch(t, env)
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "hub_moved") }, 5*time.Second)
	stop()
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != b.URL {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestWatchLongPollRestartHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" || r.URL.Query().Get("wait") != "25" || r.URL.Query().Get("after") != "previous" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		fmt.Fprint(w, `{"events":[],"next_cursor":"last","hub_restarting":{"retry_after_ms":1000}}`)
	}))
	defer srv.Close()
	out, errs := new(noticeOutput), new(noticeOutput)
	w := &watcher{a: &app{ctx: context.Background(), stdout: out, stderr: errs}, c: newClient(srv.URL, ""), cursor: "previous", forMonitor: true}
	before := time.Now()
	if err := w.longPoll(); err != errRestarting {
		t.Fatalf("restart outcome: %v", err)
	}
	if w.cursor != "last" || w.restartUntil.Before(before.Add(61*time.Second)) || w.restartDelay < 800*time.Millisecond || w.restartDelay > 1200*time.Millisecond || strings.Contains(out.String(), "hub_restarting") || errs.String() != "" {
		t.Fatalf("cursor=%s delay=%s grace=%s stdout=%q stderr=%q", w.cursor, w.restartDelay, w.restartUntil, out.String(), errs.String())
	}
}

func TestWatchFollowOn410(t *testing.T) {
	a := testhub.Start(t)
	b := a.CloneDB(t)
	path, env := followConfig(t, a)
	a.Relocate(t, b.URL)
	out, _, stop := runNoticeWatch(t, env)
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "- hub_moved - hub hub:") }, 5*time.Second)
	stop()
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != b.URL {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestWatchIdentityRotation(t *testing.T) {
	old, next := testIdentityKey(t, "1"), testIdentityKey(t, "2")
	at := envelope.FormatTime(time.Now())
	srv, _ := identityStub(t, next, func(doc *envelope.HubIdentity) {
		doc.Previous = &envelope.HubKeyPrevious{Pubkey: old.Pubkey(), RotatedAt: at, Endorsement: old.Endorse(next.Pubkey(), at)}
	})
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = \"secret\"\n", srv.URL, old.Pubkey()))
	// The fixture's events endpoint answers 404, after successful identity proof.
	code, _, errs := pinRun(env, "watch")
	cfg, err := loadConfig(path)
	if code != 2 || strings.Contains(errs, "hub_identity_changed") || err != nil || cfg.Profiles["p"].HubPubkey != next.Pubkey() {
		t.Fatalf("rotation: %d %+v %v %s", code, cfg, err, errs)
	}
	pp, _ := pinsPath(env)
	pins, err := readPins(pp)
	if err != nil || pins["p"].How != "rotation" || pins["p"].Pubkey != next.Pubkey() {
		t.Fatalf("rotation pin: %+v %v", pins, err)
	}
}

func TestFollowAcceptsEndorsedRotation(t *testing.T) {
	old, next := testIdentityKey(t, "1"), testIdentityKey(t, "2")
	at := envelope.FormatTime(time.Now())
	source, sourceCalls := identityStub(t, old, nil)
	target, _ := identityStub(t, next, func(doc *envelope.HubIdentity) {
		doc.Previous = &envelope.HubKeyPrevious{Pubkey: old.Pubkey(), RotatedAt: at, Endorsement: old.Endorse(next.Pubkey(), at)}
	})
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = \"secret\"\n", source.URL, old.Pubkey()))
	n := envelope.HubNotice{NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: source.URL, NewURL: target.URL, Message: "move", IssuedAt: at, ExpiresAt: envelope.FormatTime(time.Now().Add(time.Hour))}
	if err := old.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), getenv: env, stdout: io.Discard, stderr: io.Discard}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.follow(a.ctx, "p", cfg.Profiles["p"], n)
	if err != nil || got != target.URL {
		t.Fatalf("endorsed destination: %q %v", got, err)
	}
	cfg, err = loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != target.URL || cfg.Profiles["p"].HubPubkey != old.Pubkey() || sourceCalls.Load() != 0 {
		t.Fatalf("move config: %+v %v source probes=%d", cfg, err, sourceCalls.Load())
	}
}

func TestWatchRestartFrameBeforeEOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Buffer enough comments that the reader can reach EOF while complete
		// frame lines are still queued for the SSE dispatcher.
		fmt.Fprint(w, strings.Repeat(": hb\n", 100)+"event: hub_restarting\ndata: {\"retry_after_ms\":1000}\n\n")
	}))
	defer srv.Close()
	for i := 0; i < 32; i++ {
		out := new(noticeOutput)
		w := &watcher{a: &app{ctx: context.Background(), stdout: out, stderr: io.Discard}, c: newClient(srv.URL, ""), forMonitor: true}
		// errRestarting and the hinted delay prove the frame was dispatched;
		// the control itself never reaches stdout.
		if err := w.stream(); err != errRestarting || w.restartDelay < 800*time.Millisecond || w.restartDelay > 1200*time.Millisecond || strings.Contains(out.String(), "hub_restarting") {
			t.Fatalf("frame dropped before EOF on attempt %d: %v delay=%v %q", i, err, w.restartDelay, out.String())
		}
	}
}

// holdMessage queues a hold-priority delivery from a new peer to recipient.
func holdMessage(t *testing.T, e *testhub.Env, recipient, text string) {
	t.Helper()
	_, secret := e.NewPeer("session", "Sender "+text)
	resp, raw := e.Do(secret, "POST", "/v1/messages", map[string]any{"recipients": []string{recipient}, "priority": "hold", "idempotency_key": fmt.Sprint(time.Now().UnixNano()), "content": map[string]string{"text": text}})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
}

// Decision 6: one follow rewrites every profile on the hub, so the other
// watches on that hub adopt the move instead of refusing it as a replay.
func TestTwoWatchesFollowAtTime(t *testing.T) {
	noMoveJitter(t)
	a := testhub.Start(t)
	qID, qSecret := a.NewPeer("session", "Q")
	b := a.CloneDB(t)
	key := a.Hub.IdentityKey().Pubkey()
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n[profiles.q]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", a.URL, key, a.OperatorSecret, a.URL, key, qSecret))
	resp, raw := a.Do(a.OperatorSecret, "POST", "/v1/admin/notices", map[string]any{"kind": "moving", "message": "Move now", "at": envelope.FormatTime(time.Now().Add(2 * time.Second)), "new_url": b.URL})
	if resp.StatusCode != 201 {
		t.Fatalf("notice: %d %s", resp.StatusCode, raw)
	}
	pOut, pErr, pStop := runNoticeWatch(t, env, "--net", "p")
	qOut, qErr, qStop := runNoticeWatch(t, env, "--net", "q")
	for name, out := range map[string]*noticeOutput{"p": pOut, "q": qOut} {
		want := "- hub_moved - hub hub: profile " + name + " now uses " + b.URL
		awaitNotice(t, func() bool { return strings.Contains(out.String(), want) }, 10*time.Second)
	}
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != b.URL || cfg.Profiles["q"].Hub != b.URL {
		t.Fatalf("profiles: %+v %v", cfg, err)
	}
	holdMessage(t, b, b.OperatorPeer, "p at B")
	holdMessage(t, b, qID, "q at B")
	awaitNotice(t, func() bool { return strings.Contains(pOut.String(), "p at B") }, 5*time.Second)
	awaitNotice(t, func() bool { return strings.Contains(qOut.String(), "q at B") }, 5*time.Second)
	pStop()
	qStop()
	if strings.Contains(pErr.String()+qErr.String(), "relocation_unverified") {
		t.Fatalf("refused an adopted move: p=%q q=%q", pErr.String(), qErr.String())
	}
}

// A watch whose profile another command already followed switches to the
// recorded destination on its next reconnect, after verifying it.
func TestWatchAdoptsFollowByOtherCommand(t *testing.T) {
	// Exercise adoption of the other command's committed follow. Keep the
	// watch's independently scheduled follow from racing that command; the
	// drain below explicitly triggers the reconnect this scenario verifies.
	savedJitter := moveJitter
	moveJitter = func() time.Duration { return time.Hour }
	t.Cleanup(func() { moveJitter = savedJitter })
	a := testhub.Start(t)
	_, qSecret := a.NewPeer("session", "Q")
	// B is a copy of A taken after this event, as a real move copies a frozen hub.
	holdMessage(t, a, a.OperatorPeer, "before move")
	b := a.CloneDB(t)
	key := a.Hub.IdentityKey().Pubkey()
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n[profiles.q]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", a.URL, key, a.OperatorSecret, a.URL, key, qSecret))
	out, errs, stop := runNoticeWatch(t, env, "--net", "p")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("stdout=%q stderr=%q", out.String(), errs.String())
		}
	})
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "before move") }, 5*time.Second)
	// The line can be printed during initial replay, before the SSE request
	// exists. Relocating then lets that request follow a 410 before q runs.
	// Wait for the real stream to park on the event log before relocating.
	awaitNotice(t, func() bool { return a.Hub.Log.Waiters(a.OperatorPeer) > 0 }, 5*time.Second)
	a.Relocate(t, b.URL)
	if code, _, e := pinRun(env, "--net", "q", "peers"); code != 0 {
		t.Fatalf("q follow: %d %s", code, e)
	}
	if cfg, err := loadConfig(path); err != nil || cfg.Profiles["p"].Hub != b.URL {
		t.Fatalf("p not rewritten: %+v %v", cfg, err)
	}
	a.Drain.Announce(time.Second)
	awaitNotice(t, func() bool {
		return strings.Contains(out.String(), "- hub_moved - hub hub: profile p now uses "+b.URL)
	}, 10*time.Second)
	holdMessage(t, b, b.OperatorPeer, "after move")
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "after move") }, 5*time.Second)
	stop()
	if strings.Contains(errs.String(), "identity") || strings.Contains(errs.String(), "profile_changed") {
		t.Fatalf("stderr: %q", errs.String())
	}
}

// identityFlakyProxy fronts e and answers /v1/hub/identity with the given
// statuses first, then with a valid proof for the proxy's own URL.
func identityFlakyProxy(t *testing.T, e *testhub.Env, statuses ...int) *httptest.Server {
	t.Helper()
	target, _ := url.Parse(e.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hub/identity" {
			mu.Lock()
			var status int
			if len(statuses) > 0 {
				status, statuses = statuses[0], statuses[1:]
			}
			mu.Unlock()
			if status != 0 {
				http.Error(w, "upstream unavailable", status)
				return
			}
			sig, err := e.Hub.IdentityKey().SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
			if err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: e.Hub.IdentityKey().Pubkey(), PublicURL: srv.URL, ChallengeSig: sig})
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A proxy 503 or limiter 429 on the identity endpoint is an outage, not an
// identity change: the pinned watch backs off and resumes.
func TestWatchIdentityOutageRetries(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			fastWatchBackoff(t)
			e := testhub.Start(t)
			srv := identityFlakyProxy(t, e, status)
			_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", srv.URL, e.Hub.IdentityKey().Pubkey(), e.OperatorSecret))
			holdMessage(t, e, e.OperatorPeer, "after outage")
			out, errs, stop := runNoticeWatch(t, env)
			awaitNotice(t, func() bool { return strings.Contains(out.String(), "after outage") }, 8*time.Second)
			stop()
			if strings.Contains(errs.String(), "hub_identity_changed") {
				t.Fatalf("outage counted as identity change: %q", errs.String())
			}
		})
	}
}

// DEPLOY.md: an unpinned profile on an alias URL (not the hub's public_url)
// keeps working for watch as for one-shot commands, with one warning and no pin.
func TestWatchUnpinnedAliasURL(t *testing.T) {
	e := testhub.Start(t)
	target, _ := url.Parse(e.URL)
	alias := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(alias.Close)
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", alias.URL, e.OperatorSecret))
	if code, _, errs := pinRun(env, "peers"); code != 0 {
		t.Fatalf("peers: %d %s", code, errs)
	}
	holdMessage(t, e, e.OperatorPeer, "via alias")
	out, errs, stop := runNoticeWatch(t, env)
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "via alias") }, 5*time.Second)
	stop()
	if strings.Count(errs.String(), "hub identity not verified") != 1 || strings.Contains(errs.String(), "hub_identity_changed") {
		t.Fatalf("stderr: %q", errs.String())
	}
	pp, _ := pinsPath(env)
	if records, err := readPins(pp); err != nil || records["p"].Pubkey != "" {
		t.Fatalf("alias URL pinned: %+v %v", records, err)
	}
}

// DEPLOY.md: a profile enrolled through an alias URL (not the hub's
// public_url) is pinned from the enrol answer. Its watch proves the pinned key
// with a fresh challenge signed for the served public_url, and connects.
func TestWatchPinnedAliasURL(t *testing.T) {
	e := testhub.Start(t)
	target, _ := url.Parse(e.URL)
	alias := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(alias.Close)
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.op]\nhub = %q\nsecret = %q\n", e.URL, e.OperatorSecret))
	code, out, errs := pinRun(env, "--net", "op", "--json", "peer", "add", "--kind", "session", "--name", "Alias")
	var added struct {
		PeerID     string `json:"peer_id"`
		EnrolToken string `json:"enrol_token"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &added) != nil {
		t.Fatalf("peer add: %d %s %s", code, out, errs)
	}
	if code, _, errs := pinRun(env, "enrol", "--hub", alias.URL, "--token", added.EnrolToken, "--profile", "p"); code != 0 {
		t.Fatalf("enrol: %d %s", code, errs)
	}
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != alias.URL || cfg.Profiles["p"].HubPubkey != e.Hub.IdentityKey().Pubkey() {
		t.Fatalf("enrol profile: %+v %v", cfg.Profiles["p"], err)
	}
	if code, _, errs := pinRun(env, "peers"); code != 0 {
		t.Fatalf("peers: %d %s", code, errs)
	}
	holdMessage(t, e, added.PeerID, "via pinned alias")
	watchOut, watchErrs, stop := runNoticeWatch(t, env)
	awaitNotice(t, func() bool { return strings.Contains(watchOut.String(), "via pinned alias") }, 5*time.Second)
	stop()
	if strings.Contains(watchErrs.String(), "hub_identity") {
		t.Fatalf("stderr: %q", watchErrs.String())
	}
}

// A pinned alias profile cannot follow a move (the notice names the hub's
// public_url): watch prints one relocation_unverified line with the repoint
// hint, keeps running and never follows.
func TestWatchPinnedAliasMovingNoticeHint(t *testing.T) {
	noMoveJitter(t)
	e := testhub.Start(t)
	b := e.CloneDB(t)
	target, _ := url.Parse(e.URL)
	alias := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(alias.Close)
	path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", alias.URL, e.Hub.IdentityKey().Pubkey(), e.OperatorSecret))
	resp, raw := e.Do(e.OperatorSecret, "POST", "/v1/admin/notices", map[string]any{"kind": "moving", "message": "Moving", "at": envelope.FormatTime(time.Now().Add(-31 * time.Second)), "new_url": b.URL})
	if resp.StatusCode != 201 {
		t.Fatalf("notice: %d %s", resp.StatusCode, raw)
	}
	var posted struct{ Notice envelope.HubNotice }
	if err := json.Unmarshal(raw, &posted); err != nil || posted.Notice.Hub == "" {
		t.Fatalf("notice body: %s %v", raw, err)
	}
	own := posted.Notice.Hub
	out, errs, stop := runNoticeWatch(t, env)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("stdout: %s\nstderr: %s", out.String(), errs.String())
		}
	})
	want := "- relocation_unverified - hub hub: not following " + b.URL + ": notice names hub " + own + "\n"
	hint := "profile p uses an alias of " + own + "; run `vecnet --net p profile repoint " + own + "` to follow its moves"
	awaitNotice(t, func() bool { return strings.Contains(out.String(), want) && strings.Contains(errs.String(), hint) }, 5*time.Second)
	// A later moving Event for the same notice does not repeat the line.
	resp, raw = e.Do(e.OperatorSecret, "POST", "/v1/admin/notices", map[string]any{"kind": "maintenance", "message": "unrelated"})
	if resp.StatusCode != 201 {
		t.Fatalf("notice: %d %s", resp.StatusCode, raw)
	}
	holdMessage(t, e, e.OperatorPeer, "still at the alias")
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "still at the alias") }, 5*time.Second)
	stop()
	if n := strings.Count(out.String(), "relocation_unverified"); n != 1 {
		t.Fatalf("relocation_unverified printed %d times: %q", n, out.String())
	}
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != alias.URL || strings.Contains(errs.String(), "relocation_unverified") {
		t.Fatalf("followed or exited: %+v %v %q", cfg.Profiles["p"], err, errs.String())
	}
}

// The alias tolerance covers the URL only: a pinned alias serving another key
// still exits hub_identity_changed before any bearer request.
func TestWatchPinnedAliasKeyMismatch(t *testing.T) {
	e := testhub.Start(t)
	target, _ := url.Parse(e.URL)
	var events atomic.Int32
	proxy := httputil.NewSingleHostReverseProxy(target)
	alias := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events" {
			events.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(alias.Close)
	_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nhub_pubkey = %q\nsecret = %q\n", alias.URL, testIdentityKey(t, "2").Pubkey(), e.OperatorSecret))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out, errs bytes.Buffer
	code := RunContext(ctx, []string{"watch"}, strings.NewReader(""), &out, &errs, env)
	if code != 1 || !strings.Contains(errs.String(), "hub_identity_changed") || events.Load() != 0 {
		t.Fatalf("%d events=%d %s", code, events.Load(), errs.String())
	}
}

// A scheduled follow whose destination is not reachable yet (DNS lag on a
// domain move) is an outage: one relocation_unverified line, the watch stays
// on the old hub, and the follow retries until the destination comes up.
func TestWatchScheduledFollowWaitsForDestination(t *testing.T) {
	noMoveJitter(t)
	fastWatchBackoff(t)
	a := testhub.Start(t)
	// The destination refuses connections until B exists; B is cloned only
	// after the watch has seen A's later events, so its cursor stays valid.
	var b atomic.Pointer[testhub.Env]
	var refused atomic.Int32
	var dest *httptest.Server
	dest = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub := b.Load()
		if hub == nil {
			refused.Add(1)
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		if r.URL.Path == "/v1/hub/identity" {
			sig, err := hub.Hub.IdentityKey().SignChallenge(dest.URL, r.URL.Query().Get("challenge"))
			if err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: hub.Hub.IdentityKey().Pubkey(), PublicURL: dest.URL, ChallengeSig: sig})
			return
		}
		target, _ := url.Parse(hub.URL)
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(dest.Close)
	path, env := followConfig(t, a)
	out, errs, stop := runNoticeWatch(t, env)
	holdMessage(t, a, a.OperatorPeer, "connected at A")
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "connected at A") }, 5*time.Second)
	resp, raw := a.Do(a.OperatorSecret, "POST", "/v1/admin/notices", map[string]any{"kind": "moving", "message": "Move soon", "at": envelope.FormatTime(time.Now().Add(time.Second)), "new_url": dest.URL})
	if resp.StatusCode != 201 {
		t.Fatalf("notice: %d %s", resp.StatusCode, raw)
	}
	awaitNotice(t, func() bool {
		return strings.Contains(out.String(), "- relocation_unverified - hub hub: not following "+dest.URL+": hub identity unavailable")
	}, 5*time.Second)
	holdMessage(t, a, a.OperatorPeer, "still at A")
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "still at A") }, 5*time.Second)
	awaitNotice(t, func() bool { return refused.Load() >= 2 }, 10*time.Second) // at least one retry fails before the destination comes up
	b.Store(a.CloneDB(t))
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "- hub_moved - hub hub:") }, 10*time.Second)
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["p"].Hub != dest.URL {
		t.Fatalf("not followed: %+v %v", cfg.Profiles["p"], err)
	}
	holdMessage(t, b.Load(), a.OperatorPeer, "delivery at B")
	awaitNotice(t, func() bool { return strings.Contains(out.String(), "delivery at B") }, 5*time.Second)
	stop()
	if n := strings.Count(out.String(), "relocation_unverified"); n != 1 {
		t.Fatalf("relocation_unverified printed %d times: %q", n, out.String())
	}
	if strings.Contains(errs.String(), "relocation_unverified") {
		t.Fatalf("stderr: %q", errs.String())
	}
}
