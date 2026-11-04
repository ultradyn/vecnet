package main

// Public-deploy audit (docs/research/2026-11-04-public-deploy-audit.md).
// Each test was red on master (9b62751) and pins one deploy-config fix.

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

func auditLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, strings.TrimSpace(sc.Text()))
	}
	return out
}

// Finding S3: `just fly-backup` writes hub.db copies (which hold meta.hub_key,
// the key that seals source secrets and consent cookies and signs blob
// tokens) to backups/ at the repo root, and `just fly-deploy` uploads the
// repo root as the remote builder's context; the build stage runs COPY . .
// .dockerignore must keep those copies, and any stray database, out.
func TestAuditDockerignoreExcludesDatabases(t *testing.T) {
	lines := auditLines(t, "../../.dockerignore")
	has := func(want ...string) bool {
		for _, l := range lines {
			for _, w := range want {
				if l == w {
					return true
				}
			}
		}
		return false
	}
	if !has("backups/", "backups", "/backups") {
		t.Error(".dockerignore does not exclude backups/: `just fly-deploy` after `just fly-backup` ships hub.db (with meta.hub_key) to Fly's builder")
	}
	if !has("**/*.db") {
		t.Error(".dockerignore does not exclude **/*.db (e.g. .demo-data/, ad-hoc copies; a bare *.db matches the root only)")
	}
	for _, dir := range []string{".data/", ".demo-data/", ".scratch/", ".worktrees/"} {
		if !has(dir, strings.TrimSuffix(dir, "/")) {
			t.Errorf(".dockerignore does not exclude %s", dir)
		}
	}
}

// Finding S4: without [http_service.concurrency], Fly applies its default
// (soft 20, no hard limit). Every peer's SSE stream and long poll, every MCP
// stream and every slow anonymous request holds a connection, so the limit
// must be explicit and sized for streams.
func TestAuditFlyConcurrencyConfigured(t *testing.T) {
	raw, err := os.ReadFile("../../fly.toml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "[http_service.concurrency]") {
		t.Fatal("fly.toml has no [http_service.concurrency]: Fly's default hard_limit (25 connections) caps the single hub machine")
	}
	m := regexp.MustCompile(`(?m)^\s*hard_limit\s*=\s*(\d+)`).FindStringSubmatch(s)
	if m == nil || len(m[1]) < 3 {
		t.Fatalf("fly.toml hard_limit = %v; want an explicit limit of at least 100 for long-lived streams", m)
	}
}

// S2's limiter keys on Fly-Client-IP only when told the hub is behind Fly.
func TestAuditFlyTrustsClientIP(t *testing.T) {
	raw, err := os.ReadFile("../../fly.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s*VECNET_TRUST_FLY_CLIENT_IP\s*=\s*"1"`).Match(raw) {
		t.Fatal(`fly.toml [env] lacks VECNET_TRUST_FLY_CLIENT_IP = "1": behind Fly every client would share the proxy's address`)
	}
}
