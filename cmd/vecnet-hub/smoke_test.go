package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/XertroV/vecnet/internal/cli"
)

// lockedBuffer is a bytes.Buffer safe for one writer goroutine and a reader.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// noEnv is a getenv that sees an empty environment.
func noEnv(string) string { return "" }

// cliUser runs the vecnet CLI against one config file, never the real home.
type cliUser struct {
	t   *testing.T
	env map[string]string
}

func newCLIUser(t *testing.T) *cliUser {
	dir := t.TempDir()
	return &cliUser{t: t, env: map[string]string{
		"VECNET_CONFIG":  filepath.Join(dir, "config.toml"),
		"XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"HOME":           filepath.Join(dir, "home"),
	}}
}

func (u *cliUser) mustRun(args ...string) string {
	u.t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := cli.RunContext(ctx, args, strings.NewReader(""), &out, &errb, func(k string) string { return u.env[k] })
	if code != 0 {
		u.t.Fatalf("vecnet %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out.String(), errb.String())
	}
	return out.String()
}

var listenLine = regexp.MustCompile(`listening on (http://127\.0\.0\.[0-9]+:([0-9]+))`)

// bootedHub is a hub run in a goroutine by bootHub.
type bootedHub struct {
	base, port string
	out        *lockedBuffer
	stop       func() error // cancel and wait (≤ 2 s); later calls return the first result
}

// bootHub runs run(ctx, args, getenv) in a goroutine and waits for its
// listening line. A t.Cleanup, registered at once (so it runs before the
// caller's earlier t.TempDir removal), stops the hub and waits for it, so no
// failure path leaks a serving hub that holds the data dir.
func bootHub(t *testing.T, args []string, getenv func(string) string) *bootedHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, out, getenv) }()
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(2 * time.Second):
				stopErr = errors.New("run did not return within 2 s of ctx cancel")
			}
		})
		return stopErr
	}
	t.Cleanup(func() {
		if err := stop(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("hub stop: %v", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := listenLine.FindStringSubmatch(out.String()); m != nil {
			return &bootedHub{base: m[1], port: m[2], out: out, stop: stop}
		}
		select {
		case err := <-done:
			done <- err // let stop see it
			t.Fatalf("run returned before listening: %v\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no listening line within 5 s; output:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSmokeBootSendReceive(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	// Init the operator through run, the way `fly-init` does.
	var initOut bytes.Buffer
	if err := run(context.Background(), []string{"init", "--data-dir", dataDir, "--operator-name", "Max"}, &initOut, noEnv); err != nil {
		t.Fatalf("run init: %v\n%s", err, initOut.String())
	}
	opSecret := regexp.MustCompile(`vn_operator_\S+`).FindString(initOut.String())
	if opSecret == "" {
		t.Fatalf("init output lacks the operator secret:\n%s", initOut.String())
	}

	// Boot the real server on a random loopback port.
	hb := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, noEnv)
	base := hb.base

	// /healthz answers 200 without credentials.
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	// The operator adds two peers via the CLI package; each enrols.
	op := newCLIUser(t)
	conf := "default = \"op\"\n\n[profiles.op]\nhub = \"" + base + "\"\nsecret = \"" + opSecret + "\"\n"
	if err := os.WriteFile(op.env["VECNET_CONFIG"], []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	addAndEnrol := func(name string) (string, *cliUser) {
		var added struct {
			PeerID     string `json:"peer_id"`
			EnrolToken string `json:"enrol_token"`
		}
		out := op.mustRun("--json", "peer", "add", "--kind", "session", "--name", name)
		if err := json.Unmarshal([]byte(out), &added); err != nil {
			t.Fatalf("peer add --json output %q: %v", out, err)
		}
		if !strings.HasPrefix(added.PeerID, "peer_") || !strings.HasPrefix(added.EnrolToken, "vn_enrol_") {
			t.Fatalf("peer add --json = %q; want peer_id and enrol_token", out)
		}
		u := newCLIUser(t)
		u.mustRun("enrol", "--hub", base, "--token", added.EnrolToken, "--profile", "smoke")
		return added.PeerID, u
	}
	aID, a := addAndEnrol("A")
	bID, b := addAndEnrol("B")

	// A sends to B with --hold; B's inbox shows it.
	out := a.mustRun("--json", "send", bID, "hello from the smoke test", "--hold")
	var sent struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id"`
	}
	if err := json.Unmarshal([]byte(out), &sent); err != nil || !strings.HasPrefix(sent.MessageID, "msg_") {
		t.Fatalf("send --json output %q: %v", out, err)
	}
	out = b.mustRun("--json", "inbox")
	var inbox struct {
		Events []struct {
			Type string `json:"type"`
			Data struct {
				Recipient string `json:"recipient"`
				Message   struct {
					MessageID string                `json:"message_id"`
					Sender    string                `json:"sender"`
					Content   struct{ Text string } `json:"content"`
				} `json:"message"`
			} `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &inbox); err != nil {
		t.Fatalf("inbox --json output %q: %v", out, err)
	}
	found := false
	for _, ev := range inbox.Events {
		m := ev.Data.Message
		if m.MessageID == sent.MessageID && m.Sender == aID && ev.Data.Recipient == bID && m.Content.Text == "hello from the smoke test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("B's inbox lacks message %s from %s:\n%s", sent.MessageID, aID, out)
	}

	// An open SSE stream must not hold shutdown: the hub ends it at once.
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	bSecret := regexp.MustCompile(`vn_peer_\S+?"`).FindString(readFile(t, b.env["VECNET_CONFIG"]))
	req, err := http.NewRequestWithContext(streamCtx, "GET", base+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSuffix(bSecret, `"`))
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open /v1/events: %v", err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/events = %d, want 200", stream.StatusCode)
	}
	if _, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil {
		t.Fatalf("read the stream preamble: %v", err)
	}

	// Cancelling ctx shuts the hub down within 2 s, cleanly, stream and all.
	if err := hb.stop(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("run after cancel = %v, want nil", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFlagDefaultsFromEnv(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "envdata")
	listen, wantHost := "127.0.0.1:0", ""
	if ln, err := net.Listen("tcp", "127.0.0.2:0"); err == nil {
		ln.Close()
		listen, wantHost = "127.0.0.2:0", "127.0.0.2"
	}
	env := map[string]string{
		"VECNET_LISTEN":     listen,
		"VECNET_DATA_DIR":   dataDir,
		"VECNET_PUBLIC_URL": "https://vecnet.example",
	}
	getenv := func(k string) string { return env[k] }

	// init with no --data-dir uses VECNET_DATA_DIR.
	var initOut bytes.Buffer
	if err := run(context.Background(), []string{"init", "--operator-name", "Max"}, &initOut, getenv); err != nil {
		t.Fatalf("run init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "hub.db")); err != nil {
		t.Fatalf("init did not create hub.db under VECNET_DATA_DIR: %v", err)
	}

	// VECNET_LISTEN is honoured: on a distinctive loopback address when the
	// host has one, else at least not the default dev port.
	hb := bootHub(t, nil, getenv)
	if wantHost != "" && !strings.Contains(hb.base, "//"+wantHost+":") {
		t.Fatalf("hub listens on %s, want VECNET_LISTEN's host %s", hb.base, wantHost)
	}
	if hb.port == "45752" {
		t.Fatalf("hub listens on the default port %s, want VECNET_LISTEN's :0", hb.port)
	}
	if !strings.Contains(hb.out.String(), "public url https://vecnet.example") {
		t.Fatalf("output %q does not report VECNET_PUBLIC_URL", hb.out.String())
	}
	if err := hb.stop(); err != nil {
		t.Fatalf("run after cancel = %v", err)
	}

	// A flag beats its environment default.
	hb = bootHub(t, []string{"--listen", "127.0.0.1:0", "--public-url", "https://flag.example"}, getenv)
	if !strings.Contains(hb.out.String(), "public url https://flag.example") {
		t.Fatalf("output %q: --public-url did not override VECNET_PUBLIC_URL", hb.out.String())
	}
	if err := hb.stop(); err != nil {
		t.Fatalf("run after cancel = %v", err)
	}
}
