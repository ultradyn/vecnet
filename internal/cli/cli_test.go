package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/testhub"
)

// user is one CLI user: its own config file and cache dir under t.TempDir(),
// resolved only through getenv, so no test touches the real home.
type user struct {
	t    *testing.T
	env  map[string]string
	conf string
}

// newUser returns a user with no config file yet.
func newUser(t *testing.T) *user {
	t.Helper()
	dir := t.TempDir()
	u := &user{t: t, conf: filepath.Join(dir, "config", "vecnet.toml")}
	u.env = map[string]string{
		"VECNET_CONFIG":  u.conf,
		"XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"HOME":           filepath.Join(dir, "home"),
	}
	return u
}

// profileUser returns a user whose config holds one default profile "p"
// for hubURL with secret.
func profileUser(t *testing.T, hubURL, secret string) *user {
	t.Helper()
	u := newUser(t)
	if err := os.MkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "default = \"p\"\n\n[profiles.p]\nhub = \"" + hubURL + "\"\nsecret = \"" + secret + "\"\n"
	if err := os.WriteFile(u.conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return u
}

func (u *user) getenv(k string) string { return u.env[k] }

// run runs the CLI with args and returns its exit code, stdout and stderr.
func (u *user) run(args ...string) (int, string, string) {
	u.t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(args, strings.NewReader(""), &out, &errb, u.getenv)
	return code, out.String(), errb.String()
}

// mustRun runs args and fails the test unless the exit code is 0.
func (u *user) mustRun(args ...string) string {
	u.t.Helper()
	code, out, errs := u.run(args...)
	if code != 0 {
		u.t.Fatalf("vecnet %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

// sendResp is the --json output of send / reply.
type sendResp struct {
	MessageID  string `json:"message_id"`
	ThreadID   string `json:"thread_id"`
	Deliveries []struct {
		DeliveryID string `json:"delivery_id"`
		Recipient  string `json:"recipient"`
	} `json:"deliveries"`
}

func (u *user) send(args ...string) sendResp {
	u.t.Helper()
	out := u.mustRun(append([]string{"--json", "send"}, args...)...)
	var r sendResp
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		u.t.Fatalf("send --json output %q: %v", out, err)
	}
	if !strings.HasPrefix(r.MessageID, "msg_") || !strings.HasPrefix(r.ThreadID, "thr_") {
		u.t.Fatalf("send --json output %q lacks ids", out)
	}
	return r
}

// inboxEvent is one event of `inbox --json`.
type inboxEvent struct {
	Cursor string `json:"cursor"`
	Type   string `json:"type"`
	Data   struct {
		DeliveryID string `json:"delivery_id"`
		Recipient  string `json:"recipient"`
		State      string `json:"state"`
		Message    struct {
			MessageID   string                `json:"message_id"`
			ThreadID    string                `json:"thread_id"`
			InReplyTo   *string               `json:"in_reply_to"`
			Sender      string                `json:"sender"`
			Content     struct{ Text string } `json:"content"`
			Attachments []struct {
				SHA256    string `json:"sha256"`
				Size      int64  `json:"size"`
				MediaType string `json:"media_type"`
				Name      string `json:"name"`
				BlobURL   string `json:"blob_url"`
			} `json:"attachments"`
		} `json:"message"`
	} `json:"data"`
}

func (u *user) inboxJSON(args ...string) []inboxEvent {
	u.t.Helper()
	out := u.mustRun(append([]string{"--json", "inbox"}, args...)...)
	var body struct {
		Events     []inboxEvent `json:"events"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		u.t.Fatalf("inbox --json output %q: %v", out, err)
	}
	return body.Events
}

// nonEmptyLines splits s into its non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestEnrolWritesProfile(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	out := op.mustRun("--json", "peer", "add", "--kind", "session", "--name", "C")
	var added struct {
		PeerID     string `json:"peer_id"`
		EnrolToken string `json:"enrol_token"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("peer add output %q: %v", out, err)
	}

	u := newUser(t)
	out = u.mustRun("enrol", "--hub", env.URL, "--token", added.EnrolToken, "--profile", "p")
	if !strings.Contains(out, added.PeerID) {
		t.Fatalf("enrol output %q does not name %s", out, added.PeerID)
	}
	if strings.Contains(out, "vn_peer_") {
		t.Fatalf("enrol printed the secret: %q", out)
	}
	var cfg struct {
		Default  string `toml:"default"`
		Profiles map[string]struct {
			Hub    string `toml:"hub"`
			Secret string `toml:"secret"`
		} `toml:"profiles"`
	}
	if _, err := toml.DecodeFile(u.conf, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	p, ok := cfg.Profiles["p"]
	if !ok {
		t.Fatalf("config has no profiles.p: %+v", cfg)
	}
	if !strings.HasPrefix(p.Secret, "vn_peer_") {
		t.Fatalf("profiles.p.secret = %q, want vn_peer_…", p.Secret)
	}
	if p.Hub != env.URL {
		t.Fatalf("profiles.p.hub = %q, want %q", p.Hub, env.URL)
	}
	if cfg.Default != "p" {
		t.Fatalf("default = %q, want p (first profile)", cfg.Default)
	}
	if fi, err := os.Stat(u.conf); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	// The new profile works: peers lists C.
	if out := u.mustRun("peers"); !strings.Contains(out, added.PeerID) {
		t.Fatalf("peers via enrolled profile = %q, want %s", out, added.PeerID)
	}
	// The token is one-time.
	u2 := newUser(t)
	if code, _, errs := u2.run("enrol", "--hub", env.URL, "--token", added.EnrolToken, "--profile", "p"); code != 2 || !strings.HasPrefix(errs, "unauthenticated: ") {
		t.Fatalf("re-enrol = %d %q, want 2 unauthenticated", code, errs)
	}
}

func TestSendRequiresPriority(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)

	code, out, errs := a.run("send", b, "hi")
	if code != 1 {
		t.Fatalf("send without priority: exit %d, want 1 (stdout %q)", code, out)
	}
	if !strings.Contains(errs, "--wake|--wake-idle|--hold") {
		t.Fatalf("stderr %q does not mention --wake|--wake-idle|--hold", errs)
	}
	// Two priorities are a usage error too.
	if code, _, errs := a.run("send", b, "hi", "--wake", "--hold"); code != 1 || !strings.Contains(errs, "--wake|--wake-idle|--hold") {
		t.Fatalf("send with two priorities: exit %d %q, want 1", code, errs)
	}
	// Nothing was sent.
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM deliveries`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deliveries = %d, %v; want 0", n, err)
	}
}

func TestSendInboxAckRoundTrip(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	sent := a.send(bID, "hello B", "--hold")
	if len(sent.Deliveries) != 1 || sent.Deliveries[0].Recipient != bID {
		t.Fatalf("deliveries = %+v", sent.Deliveries)
	}

	lines := nonEmptyLines(b.mustRun("inbox"))
	if len(lines) != 2 || lines[0] != "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT" {
		t.Fatalf("inbox lines = %q, want header and one event", lines)
	}
	for _, want := range []string{sent.ThreadID, aID, sent.MessageID, "hello B"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("inbox line %q lacks %q", lines[1], want)
		}
	}
	// Reading is not acking: the inbox shows the same event again.
	if again := nonEmptyLines(b.mustRun("inbox")); len(again) != 2 || again[1] != lines[1] {
		t.Fatalf("second inbox = %q, want %q", again, lines)
	}

	ackOut := nonEmptyLines(b.mustRun("inbox", "--ack"))
	if len(ackOut) != 2 || ackOut[1] != lines[1] {
		t.Fatalf("inbox --ack = %q, want the same line", ackOut)
	}
	var state string
	if err := env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id = ?`, sent.Deliveries[0].DeliveryID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("delivery state after inbox --ack = %q, want accepted", state)
	}
	if out := b.mustRun("inbox"); strings.TrimSpace(out) != "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT" {
		t.Fatalf("inbox after ack = %q, want header only", out)
	}
	// The ack reached the hub: A got the accepted receipt.
	var receipt bool
	for _, ev := range a.inboxJSON() {
		if ev.Type == "receipt" && ev.Data.State == "accepted" && ev.Data.DeliveryID == sent.Deliveries[0].DeliveryID {
			receipt = true
		}
	}
	if !receipt {
		t.Fatal("A's inbox lacks the accepted receipt")
	}
}

func TestReplyUsesCache(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	q := a.send(bID, "question?", "--hold")
	// Before B has seen it, the cache misses.
	code, _, errs := b.run("reply", q.MessageID, "answer", "--wake")
	if code != 1 || !strings.Contains(errs, "unknown message id; pass --thread") {
		t.Fatalf("reply before inbox = %d %q, want 1 and the cache-miss error", code, errs)
	}

	b.mustRun("inbox") // fills the message → thread cache
	r := b.send2("reply", q.MessageID, "answer", "--wake")
	if r.ThreadID != q.ThreadID {
		t.Fatalf("reply thread = %s, want %s", r.ThreadID, q.ThreadID)
	}

	var found bool
	for _, ev := range a.inboxJSON() {
		if ev.Type != "delivery" {
			continue
		}
		m := ev.Data.Message
		if m.MessageID != r.MessageID {
			continue
		}
		found = true
		if m.ThreadID != q.ThreadID || m.InReplyTo == nil || *m.InReplyTo != q.MessageID || m.Sender != bID || m.Content.Text != "answer" {
			t.Fatalf("reply delivery = %+v; want thread %s, in_reply_to %s, sender %s", m, q.ThreadID, q.MessageID, bID)
		}
		if ev.Data.Recipient != aID {
			t.Fatalf("reply recipient = %s, want %s", ev.Data.Recipient, aID)
		}
	}
	if !found {
		t.Fatalf("A's inbox lacks the reply %s", r.MessageID)
	}

	// An explicit --thread bypasses the cache.
	c := profileUser(t, env.URL, bSec) // same peer, empty cache
	r2 := c.send2("reply", q.MessageID, "again", "--hold", "--thread", q.ThreadID)
	if r2.ThreadID != q.ThreadID {
		t.Fatalf("reply --thread thread = %s, want %s", r2.ThreadID, q.ThreadID)
	}
}

// send2 runs `--json <args…>` (a reply) and decodes the send response.
func (u *user) send2(args ...string) sendResp {
	u.t.Helper()
	out := u.mustRun(append([]string{"--json"}, args...)...)
	var r sendResp
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.MessageID == "" {
		u.t.Fatalf("%s --json output %q: %v", args[0], out, err)
	}
	return r
}

// syncBuffer is a goroutine-safe stdout for a running watch.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitLine waits until some line of buf matches re, failing after d.
func waitLine(t *testing.T, buf *syncBuffer, re *regexp.Regexp, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, l := range nonEmptyLines(buf.String()) {
			if re.MatchString(l) {
				return l
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no line matching %s within %v; stdout:\n%s", re, d, buf.String())
	return ""
}

func TestWatchForMonitor(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.RunContext(ctx, []string{"watch", "--for-monitor"}, strings.NewReader(""), &out, &errb, a.getenv)
	}()

	sent := b.send(aID, "hello watcher", "--wake")
	re := regexp.MustCompile(`^e\d+\.\d+ delivery ` + regexp.QuoteMeta(sent.ThreadID) + ` ` + regexp.QuoteMeta(bID) + ` B: hello watcher$`)
	waitLine(t, &out, re, 10*time.Second)

	// Text is cut to its first 120 characters and kept on one line.
	long := strings.Repeat("é", 100) + "\n" + strings.Repeat("x", 100)
	sent2 := b.send(aID, long, "--wake")
	want := "B: " + strings.Repeat("é", 100) + " " + strings.Repeat("x", 16)
	re2 := regexp.MustCompile(`^e\d+\.\d+ delivery ` + regexp.QuoteMeta(sent2.ThreadID) + ` ` + regexp.QuoteMeta(bID) + ` ` + regexp.QuoteMeta(want) + `$`)
	waitLine(t, &out, re2, 10*time.Second)

	// A receipt for A's own message is printed too; the agent decides.
	mine := a.send(bID, "for B", "--hold")
	b.mustRun("inbox", "--ack")
	re3 := regexp.MustCompile(`^e\d+\.\d+ receipt ` + regexp.QuoteMeta(mine.ThreadID) + ` ` + regexp.QuoteMeta(bID) + ` B: accepted$`)
	waitLine(t, &out, re3, 10*time.Second)

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("watch exit = %d, want 0; stderr %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not return after its context was cancelled")
	}
	// Each delivery is printed once.
	if n := strings.Count(out.String(), " hello watcher"); n != 1 {
		t.Fatalf("delivery printed %d times:\n%s", n, out.String())
	}
	// The hub's priming event (id, empty data) is not an event: nothing is
	// reported for it (PROTOCOL §13.1).
	if s := errb.String(); s != "" {
		t.Fatalf("watch stderr = %q, want empty", s)
	}
}

// TestFreshCacheUsesHubAckedCursor: the acked cursor lives on the hub and is
// shared by every consumer (PROTOCOL §2.3), so a second install of the same
// peer with an empty cache sees nothing already acked elsewhere.
func TestFreshCacheUsesHubAckedCursor(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	a.send(bID, "one", "--hold")
	if lines := nonEmptyLines(b.mustRun("inbox", "--ack")); len(lines) != 2 {
		t.Fatalf("inbox --ack = %q, want header and one event", lines)
	}

	b2 := profileUser(t, env.URL, bSec) // same peer, another machine
	if out := b2.mustRun("inbox"); strings.TrimSpace(out) != "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT" {
		t.Fatalf("fresh-cache inbox after an ack elsewhere = %q, want header only", out)
	}

	// watch on a fresh cache resumes at the hub's acked cursor too: the
	// first line is the new message, not the acked one.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out, errb syncBuffer
	done := make(chan int, 1)
	b3 := profileUser(t, env.URL, bSec) // built here: it may t.Fatal
	go func() {
		done <- cli.RunContext(ctx, []string{"watch", "--for-monitor"}, strings.NewReader(""), &out, &errb, b3.getenv)
	}()
	sent := a.send(bID, "two", "--wake")
	re := regexp.MustCompile(`^e\d+\.\d+ delivery ` + regexp.QuoteMeta(sent.ThreadID) + ` ` + regexp.QuoteMeta(aID) + ` A: two$`)
	waitLine(t, &out, re, 10*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not return after its context was cancelled")
	}
	if strings.Contains(out.String(), " one") {
		t.Fatalf("watch replayed an acked delivery:\n%s", out.String())
	}
}

// TestPresenceSetAsPrincipal: a principal names the peer it sets presence for
// with --as (?as= on POST /v1/presence).
func TestPresenceSetAsPrincipal(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	prn, pSec := env.NewPrincipal("P")
	x, _, err := env.Hub.AddPeer(context.Background(), "session", "X", prn)
	if err != nil {
		t.Fatal(err)
	}
	p := profileUser(t, env.URL, pSec)
	p.mustRun("--as", x, "presence", "set", "working", "--note", "busy")

	a := profileUser(t, env.URL, aSec)
	var peers []struct {
		PeerID   string `json:"peer_id"`
		Presence string `json:"presence"`
	}
	out := a.mustRun("--json", "peers")
	if err := json.Unmarshal([]byte(out), &peers); err != nil {
		t.Fatalf("peers --json %q: %v", out, err)
	}
	for _, pr := range peers {
		if pr.PeerID == x {
			if pr.Presence != "working" {
				t.Fatalf("presence of %s = %q, want working", x, pr.Presence)
			}
			return
		}
	}
	t.Fatalf("directory lacks %s: %s", x, out)
}

func TestAttachUploads(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	content := "attached bytes\n"
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	a.send(bID, "see file", "--hold", "--attach", path)

	evs := b.inboxJSON()
	if len(evs) != 1 || evs[0].Type != "delivery" {
		t.Fatalf("B's inbox = %+v, want one delivery", evs)
	}
	atts := evs[0].Data.Message.Attachments
	if len(atts) != 1 {
		t.Fatalf("attachments = %+v, want 1", atts)
	}
	at := atts[0]
	if at.MediaType != "text/plain" || at.Name != "file.txt" || at.Size != int64(len(content)) || len(at.SHA256) != 64 {
		t.Fatalf("attachment = %+v; want text/plain file.txt size %d", at, len(content))
	}
	if at.BlobURL == "" {
		t.Fatal("attachment has no blob_url")
	}
	resp, err := http.Get(at.BlobURL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(got) != content {
		t.Fatalf("GET blob_url = %d %q, %v; want 200 %q", resp.StatusCode, got, err, content)
	}

	// A missing file is a usage error and sends nothing.
	if code, _, errs := a.run("send", bID, "x", "--hold", "--attach", filepath.Join(t.TempDir(), "nope.txt")); code != 1 || errs == "" {
		t.Fatalf("send --attach missing file = %d %q, want 1", code, errs)
	}
}

func TestAdminCommandsJSON(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)

	out := op.mustRun("--json", "peer", "add", "--kind", "session", "--name", "C")
	var added map[string]any
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("peer add --json output %q: %v", out, err)
	}
	peer, _ := added["peer_id"].(string)
	tok, _ := added["enrol_token"].(string)
	if !strings.HasPrefix(peer, "peer_") || !strings.HasPrefix(tok, "vn_enrol_") {
		t.Fatalf("peer add --json = %v; want peer_id and enrol_token", added)
	}

	out = op.mustRun("--json", "dead-letters", "list")
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("dead-letters list --json = %q, want a JSON array", out)
	}
	var arr []any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("dead-letters list --json = %q: %v", out, err)
	}
	if len(arr) != 0 {
		t.Fatalf("dead-letters = %v, want none", arr)
	}

	// The flag works after the command too, and principal add prints its id.
	out = op.mustRun("principal", "add", "--name", "P", "--json")
	var prn map[string]any
	if err := json.Unmarshal([]byte(out), &prn); err != nil || !strings.HasPrefix(prn["principal_id"].(string), "prn_") {
		t.Fatalf("principal add --json = %q, %v", out, err)
	}

	// A peer credential is refused on admin commands with exit 2.
	_, sec := env.NewPeer("session", "D")
	d := profileUser(t, env.URL, sec)
	if code, _, errs := d.run("--json", "dead-letters", "list"); code != 2 || !strings.HasPrefix(errs, "forbidden: ") {
		t.Fatalf("dead-letters list as a peer = %d %q, want 2 forbidden", code, errs)
	}
}

func TestHubErrorExitCode(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	a := profileUser(t, env.URL, aSec)

	code, out, errs := a.run("send", "Max", "hi", "--wake")
	if code != 2 {
		t.Fatalf("send to Max: exit %d, want 2 (stdout %q stderr %q)", code, out, errs)
	}
	if !strings.HasPrefix(errs, "name_not_an_address: ") {
		t.Fatalf("stderr = %q, want name_not_an_address: …", errs)
	}
}

func TestNetworkErrorExitCode(t *testing.T) {
	// A listener bound to a random loopback port and closed again refuses
	// connections; nothing leaves the host.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String()
	ln.Close()
	a := profileUser(t, dead, "vn_peer_x")
	if code, _, errs := a.run("peers"); code != 3 || errs == "" {
		t.Fatalf("peers against a dead hub = %d %q, want 3", code, errs)
	}
	// No profile at all is a usage error.
	u := newUser(t)
	if code, _, errs := u.run("peers"); code != 1 || errs == "" {
		t.Fatalf("peers without a config = %d %q, want 1", code, errs)
	}
}

// TestInboxStripsTerminalControls: untrusted message text cannot inject
// ANSI/OSC escapes or fake lines into the terminal (PROTOCOL §14 inv. 2).
func TestInboxStripsTerminalControls(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)
	a.send(bID, "hi\x1b]52;c;cHduZWQ=\x07there\x1b[2K fake\x9bline", "--hold")
	out := b.mustRun("inbox")
	for _, bad := range []string{"\x1b", "\x07", " ", "\x9b"} {
		if strings.Contains(out, bad) {
			t.Fatalf("inbox output %q carries control %q", out, bad)
		}
	}
	if lines := nonEmptyLines(out); len(lines) != 2 || !strings.Contains(lines[1], "there") {
		t.Fatalf("inbox lines = %q, want header and one event with the text", lines)
	}
}

// TestDoubleDashSendsFlagLikeText: "--" ends flag parsing, so "-h" after it
// is message text, not a help request.
func TestDoubleDashSendsFlagLikeText(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	bID, bSec := env.NewPeer("session", "B")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)
	a.send(bID, "--hold", "--", "-h")
	if lines := nonEmptyLines(b.mustRun("inbox")); len(lines) != 2 || !strings.HasSuffix(lines[1], "-h") {
		t.Fatalf("inbox = %q, want header and one message whose text is -h", lines)
	}
}

// TestEnrolTokenFromStdinOrEnv: the one-time token can stay off the command
// line, read from stdin ("--token -") or from VECNET_ENROL_TOKEN.
func TestEnrolTokenFromStdinOrEnv(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	add := func(name string) (peer, tok string) {
		t.Helper()
		var added struct {
			PeerID     string `json:"peer_id"`
			EnrolToken string `json:"enrol_token"`
		}
		if err := json.Unmarshal([]byte(op.mustRun("--json", "peer", "add", "--kind", "session", "--name", name)), &added); err != nil {
			t.Fatal(err)
		}
		return added.PeerID, added.EnrolToken
	}

	p1, tok1 := add("S")
	u := newUser(t)
	var out, errb bytes.Buffer
	if code := cli.Run([]string{"enrol", "--hub", env.URL, "--token", "-", "--profile", "p"},
		strings.NewReader(tok1+"\n"), &out, &errb, u.getenv); code != 0 || !strings.Contains(out.String(), p1) {
		t.Fatalf("enrol --token - = %d %q %q, want 0 naming %s", code, out.String(), errb.String(), p1)
	}

	p2, tok2 := add("E")
	u2 := newUser(t)
	u2.env["VECNET_ENROL_TOKEN"] = tok2
	if out := u2.mustRun("enrol", "--hub", env.URL, "--profile", "p"); !strings.Contains(out, p2) {
		t.Fatalf("enrol via VECNET_ENROL_TOKEN = %q, want %s", out, p2)
	}
}
