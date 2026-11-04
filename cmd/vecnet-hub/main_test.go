package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestInitPrintsSecretOnce(t *testing.T) {
	dir := t.TempDir()
	var first, second bytes.Buffer
	if err := runInit(dir, "Max", &first); err != nil {
		t.Fatalf("first runInit: %v", err)
	}
	if !strings.Contains(first.String(), "vn_operator_") {
		t.Fatalf("first output lacks the operator secret:\n%s", first.String())
	}
	peer := regexp.MustCompile(`peer_[0-9A-HJKMNP-TV-Z]{26}`).FindString(first.String())
	if peer == "" {
		t.Fatalf("first output lacks the operator peer id:\n%s", first.String())
	}
	if err := runInit(dir, "Max", &second); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	if !strings.Contains(second.String(), "already initialised") {
		t.Fatalf("second output lacks \"already initialised\":\n%s", second.String())
	}
	if strings.Contains(second.String(), "vn_operator_") {
		t.Fatalf("second output repeats a secret:\n%s", second.String())
	}
	if !strings.Contains(second.String(), peer) {
		t.Fatalf("second output does not name the existing peer %s:\n%s", peer, second.String())
	}
}

func TestSIGTERMSendsHubRestarting(t *testing.T) { testRestartShutdown(t, noEnv, nil, 5000) }

func testRestartShutdown(t *testing.T, getenv func(string) string, extra []string, wantMS int) {
	t.Helper()
	dir := t.TempDir()
	h, db, err := openHub(dir, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = h.EnsureOperator(context.Background(), "operator")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, token, err := h.AddPeer(context.Background(), "session", "reader", "")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, secret, err := h.Auth.Enrol(context.Background(), token)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir}, extra...)
	boot := bootHub(t, args, getenv)
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownGrace+2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", boot.base+"/v1/events?after=head", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	read := func() string {
		var frame strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			frame.WriteString(line)
			if line == "\n" {
				return frame.String()
			}
		}
	}
	if frame := read(); frame != "retry: 3000\n\n" {
		t.Fatalf("retry = %q", frame)
	}
	if frame := read(); !strings.HasPrefix(frame, "id: ") || !strings.HasSuffix(frame, "\ndata:\n\n") {
		t.Fatalf("prime = %q", frame)
	}
	started := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- boot.stop() }()
	want := fmt.Sprintf("event: hub_restarting\ndata: {\"retry_after_ms\":%d}\n\n", wantMS)
	if frame := read(); frame != want {
		t.Fatalf("restart = %q, want %q", frame, want)
	}
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("after restart = %q %v", rest, err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ShutdownGrace):
		t.Fatal("run did not finish within ShutdownGrace")
	}
	if elapsed := time.Since(started); elapsed >= ShutdownGrace {
		t.Fatalf("shutdown took %v", elapsed)
	}
}

func TestRestartRetryFlag(t *testing.T) {
	for _, value := range []string{"70000", "-1", "bad"} {
		err := run(context.Background(), []string{"--restart-retry-ms", value}, io.Discard, noEnv)
		if err == nil || !strings.Contains(err.Error(), "restart-retry-ms") {
			t.Fatalf("invalid retry %q = %v", value, err)
		}
	}
	env := func(k string) string {
		if k == "VECNET_RESTART_RETRY_MS" {
			return "2000"
		}
		return ""
	}
	testRestartShutdown(t, env, nil, 2000)
	t.Run("flag overrides env including zero", func(t *testing.T) { testRestartShutdown(t, env, []string{"--restart-retry-ms", "0"}, 0) })
	for _, value := range []string{"-1", "70000", "nope"} {
		err := run(context.Background(), nil, io.Discard, func(k string) string {
			if k == "VECNET_RESTART_RETRY_MS" {
				return value
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "restart-retry-ms") {
			t.Fatalf("invalid env retry %q = %v", value, err)
		}
	}
}
