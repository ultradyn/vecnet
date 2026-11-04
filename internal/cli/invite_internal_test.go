package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

// inviteCase is one entry of testdata/invite.json, the golden shared with
// the web UI's invite-model.js (internal/ui/jstest/invite.test.mjs).
type inviteCase struct {
	Name      string `json:"name"`
	HubURL    string `json:"hub_url"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Profile   string `json:"profile"`
	Block     string `json:"block"`
}

func TestInviteBlockGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "invite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []inviteCase
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("invite.json: %v", err)
	}
	for _, c := range cases {
		if got := inviteSlug(c.Name); got != c.Profile {
			t.Errorf("inviteSlug(%q) = %q, want %q", c.Name, got, c.Profile)
		}
		if !profileRE.MatchString(c.Profile) {
			t.Errorf("profile %q is not a hook-safe profile", c.Profile)
		}
		if got := inviteBlock(c.HubURL, c.Profile, c.Token, c.ExpiresAt); got != c.Block {
			t.Errorf("inviteBlock(%q):\n%s\nwant:\n%s", c.Name, got, c.Block)
		}
	}
}

// cliRun runs the CLI with its config under dir, as the cli_test users do.
func cliRun(t *testing.T, dir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	env := map[string]string{"VECNET_CONFIG": filepath.Join(dir, "config.toml"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"), "HOME": filepath.Join(dir, "home")}
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func TestPeerInviteEndToEnd(t *testing.T) {
	env := testhub.Start(t)
	opDir := t.TempDir()
	conf := "[profiles.op]\nhub = \"" + env.URL + "\"\nsecret = \"" + env.OperatorSecret + "\"\n"
	if err := os.WriteFile(filepath.Join(opDir, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "Scout Bot", "--ttl", "2h")
	if code != 0 {
		t.Fatalf("peer invite: %d %s", code, errs)
	}
	tok := regexp.MustCompile(`printf %s '(vn_enrol_[A-Za-z0-9_-]+)'`).FindStringSubmatch(out)
	ids := regexp.MustCompile(`(?m)^peer_id (peer_\S+)\nprincipal_id (prn_\S+)\n\z`).FindStringSubmatch(out)
	if tok == nil || ids == nil {
		t.Fatalf("peer invite output:\n%s", out)
	}
	exp := envelope.FormatTime(env.Now.Now().Add(2 * time.Hour))
	want := "--- paste to the agent (shown once; it grants a new identity on your hub) ---\n" +
		inviteBlock(env.Hub.Cfg.PublicURL, "scout-bot", tok[1], exp) + "--- end ---\n" +
		"peer_id " + ids[1] + "\nprincipal_id " + ids[2] + "\n"
	if out != want {
		t.Fatalf("peer invite output:\n%s\nwant:\n%s", out, want)
	}

	// The agent side: the pasted command enrols, with no config beforehand.
	agentDir := t.TempDir()
	if code, _, _ := cliRun(t, agentDir, "", "guide"); code != 0 {
		t.Fatalf("guide without config: exit %d", code)
	}
	code, out, errs = cliRun(t, agentDir, tok[1], "enrol", "--hub", env.Hub.Cfg.PublicURL, "--token", "-", "--profile", "scout-bot")
	if code != 0 || !strings.Contains(out, ids[1]) {
		t.Fatalf("enrol: %d %s %s", code, out, errs)
	}
	if code, _, errs := cliRun(t, agentDir, "", "--net", "scout-bot", "peers"); code != 0 {
		t.Fatalf("peers as the invited agent: %d %s", code, errs)
	}

	// Refused kinds never reach the hub.
	for _, kind := range []string{"operator", "dot"} {
		if code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "x", "--kind", kind); code != 1 || !strings.Contains(errs, "cannot be invited") {
			t.Fatalf("invite --kind %s: %d %s", kind, code, errs)
		}
	}
	for _, ttl := range []string{"8d", "soon", "1.5s"} {
		code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "x", "--ttl", ttl)
		if code == 0 {
			t.Fatalf("invite --ttl %s succeeded", ttl)
		}
		_ = errs
	}

	// --json carries the block and the fields; an expired invite is 410 and
	// re-issue makes a working one.
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", "--name", "Late", "--ttl", "1h")
	var j map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || j["profile"] != "late" || j["invite"] != inviteBlock(j["hub_url"], "late", j["enrol_token"], j["enrol_expires_at"]) {
		t.Fatalf("invite --json: %d %s %s", code, out, errs)
	}
	env.Now.Advance(2 * time.Hour)
	if code, _, errs := cliRun(t, t.TempDir(), j["enrol_token"], "enrol", "--hub", env.URL, "--token", "-", "--profile", "late"); code != 2 || !strings.HasPrefix(errs, "enrol_token_expired: ") {
		t.Fatalf("late enrol: %d %s", code, errs)
	}
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", j["peer_id"], "--profile", "late")
	var re map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &re) != nil || re["enrol_token"] == j["enrol_token"] {
		t.Fatalf("re-issue: %d %s %s", code, out, errs)
	}
	if code, _, errs := cliRun(t, t.TempDir(), re["enrol_token"], "enrol", "--hub", env.URL, "--token", "-", "--profile", "late"); code != 0 {
		t.Fatalf("enrol after re-issue: %d %s", code, errs)
	}
	if code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", j["peer_id"]); code != 1 {
		t.Fatalf("re-issue without --profile: %d %s", code, errs)
	}
}

func TestGuide(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := cliRun(t, dir, "", "guide")
	if code != 0 || !strings.HasPrefix(out, "# vecnet agent guide\n") || !strings.Contains(out, "vecnet --net <profile> inbox") {
		t.Fatalf("guide: %d %.200s", code, out)
	}
	if n := strings.Count(out, "\n"); n > 125 {
		t.Fatalf("guide is %d lines; keep it short", n)
	}
	for _, must := range []string{"untrusted data", "--wake", "--wake-idle", "--hold", "hook install --profile", "watch --for-monitor", "vecnet mcp", "secret"} {
		if !strings.Contains(out, must) {
			t.Errorf("guide lacks %q", must)
		}
	}
	_, out, _ = cliRun(t, dir, "", "--net", "scout", "guide")
	if strings.Contains(out, "<profile>") || !strings.Contains(out, "vecnet --net scout inbox") {
		t.Fatalf("guide --net did not fill the profile")
	}
	_, out, _ = cliRun(t, dir, "", "--net", "-x; rm", "guide")
	if !strings.Contains(out, "<profile>") || strings.Contains(out, "rm inbox") {
		t.Fatalf("guide --net filled an unsafe profile")
	}
	if code, _, _ := cliRun(t, dir, "", "guide", "extra"); code != 1 {
		t.Fatalf("guide extra: %d", code)
	}
}
