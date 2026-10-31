package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

var listenLine = regexp.MustCompile(`listening on (http://127\.0\.0\.1:[0-9]+)`)

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var serveOut lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, &serveOut, noEnv)
	}()
	var base string
	deadline := time.Now().Add(5 * time.Second)
	for base == "" {
		if m := listenLine.FindStringSubmatch(serveOut.String()); m != nil {
			base = m[1]
			break
		}
		select {
		case err := <-done:
			t.Fatalf("run returned before listening: %v\n%s", err, serveOut.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no listening line within 5 s; output:\n%s", serveOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

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

	// Cancelling ctx shuts the hub down within 2 s, cleanly.
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("run after cancel = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return within 2 s of ctx cancel")
	}
}

func TestFlagDefaultsFromEnv(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "envdata")
	env := map[string]string{
		"VECNET_LISTEN":     "127.0.0.1:0",
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

	ctx, cancel := context.WithCancel(context.Background())
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, &out, getenv) }()
	deadline := time.Now().Add(5 * time.Second)
	for !listenLine.MatchString(out.String()) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no listening line on VECNET_LISTEN within 5 s; output:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "public url https://vecnet.example") {
		t.Fatalf("output %q does not report VECNET_PUBLIC_URL", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run after cancel = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return within 2 s of ctx cancel")
	}
}
