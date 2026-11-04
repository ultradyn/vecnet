package main

// Public-deploy audit (docs/research/2026-11-04-public-deploy-audit.md).
// These tests FAIL on master by design; each pins a deploy-config finding.

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
	if !has("*.db", "**/*.db") {
		t.Error(".dockerignore does not exclude *.db (e.g. .demo-data/, ad-hoc copies)")
	}
}

// Finding S4: without [http_service.concurrency], Fly applies its default
// connection limits (soft 20 / hard 25 per machine). Every peer's SSE
// stream and long-poll, every MCP stream, and every slow anonymous request
// holds one, so a single machine stops taking new connections at ~25.
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
