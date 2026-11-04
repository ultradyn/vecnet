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
	Open      bool   `json:"open"`
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
	open := 0
	for _, c := range cases {
		got := inviteOpenBlock(c.HubURL, c.Name, c.Token, c.ExpiresAt)
		if c.Open {
			open++
		} else {
			if s := inviteSlug(c.Name); s != c.Profile {
				t.Errorf("inviteSlug(%q) = %q, want %q", c.Name, s, c.Profile)
			}
			if !profileRE.MatchString(c.Profile) {
				t.Errorf("profile %q is not a hook-safe profile", c.Profile)
			}
			got = inviteBlock(c.HubURL, c.Profile, c.Token, c.ExpiresAt)
		}
		if got != c.Block {
			t.Errorf("invite block (%q, open %v):\n%s\nwant:\n%s", c.Name, c.Open, got, c.Block)
		}
		// Agents paste these verbatim, and shell guards refuse variables,
		// command chains and substitutions: one plain command per line.
		if strings.ContainsAny(c.Block, "$;`") {
			t.Errorf("invite block (%q) holds $, ; or a backtick", c.Name)
		}
		for _, line := range strings.Split(c.Block, "\n") {
			if cmd, ok := strings.CutPrefix(line, "   "); ok && !strings.HasPrefix(cmd, "It prints") &&
				(strings.ContainsAny(cmd, "<>&(){}") || strings.Count(cmd, "|") > 1) {
				t.Errorf("command line %q is not one plain command", cmd)
			}
		}
	}
	if open == 0 || open == len(cases) {
		t.Fatalf("invite.json needs fixed and open cases")
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
	var j map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || j["profile"] != "late" || j["invite"] != inviteBlock(j["hub_url"].(string), "late", j["enrol_token"].(string), j["enrol_expires_at"].(string)) {
		t.Fatalf("invite --json: %d %s %s", code, out, errs)
	}
	env.Now.Advance(2 * time.Hour)
	if code, _, errs := cliRun(t, t.TempDir(), j["enrol_token"].(string), "enrol", "--hub", env.URL, "--token", "-", "--profile", "late"); code != 2 || !strings.HasPrefix(errs, "enrol_token_expired: ") {
		t.Fatalf("late enrol: %d %s", code, errs)
	}
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", j["peer_id"].(string))
	var re map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &re) != nil || re["enrol_token"] == j["enrol_token"] || re["profile"] != "late" {
		t.Fatalf("re-issue: %d %s %s", code, out, errs)
	}
	if code, _, errs := cliRun(t, t.TempDir(), re["enrol_token"].(string), "enrol", "--hub", env.URL, "--token", "-", "--profile", "late"); code != 0 {
		t.Fatalf("enrol after re-issue: %d %s", code, errs)
	}
	if code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", j["peer_id"].(string)); code != 2 || !strings.HasPrefix(errs, "already_enrolled: ") {
		t.Fatalf("re-issue after enrol: %d %s", code, errs)
	}
}

// An open invite: the agent names itself at enrol, once; the profile is the
// name's slug; refusals spend nothing.
func TestPeerInviteOpenName(t *testing.T) {
	env := testhub.Start(t)
	opDir := t.TempDir()
	conf := "[profiles.op]\nhub = \"" + env.URL + "\"\nsecret = \"" + env.OperatorSecret + "\"\n"
	if err := os.WriteFile(filepath.Join(opDir, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite")
	var j struct {
		PeerID, Name, Profile, Invite, HubURL string
		NameOpen                              bool   `json:"name_open"`
		EnrolToken                            string `json:"enrol_token"`
		EnrolExpiresAt                        string `json:"enrol_expires_at"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || !j.NameOpen || j.Profile != "" ||
		j.Invite != inviteOpenBlock(env.Hub.Cfg.PublicURL, "", j.EnrolToken, j.EnrolExpiresAt) {
		t.Fatalf("open invite: %d %s %s", code, out, errs)
	}
	if code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--open-name", "--profile", "x"); code != 1 {
		t.Fatalf("open invite with --profile: %d %s", code, errs)
	}
	agent := t.TempDir()
	if code, _, errs := cliRun(t, agent, j.EnrolToken, "enrol", "--hub", env.URL, "--token", "-", "--profile", "p"); code != 2 || !strings.HasPrefix(errs, "name_required: ") {
		t.Fatalf("enrol without --name: %d %s", code, errs)
	}
	if code, _, errs := cliRun(t, agent, j.EnrolToken, "enrol", "--hub", env.URL, "--token", "-", "--name", "M a x"); code != 2 || !strings.HasPrefix(errs, "name_reserved: ") {
		t.Fatalf("enrol as max: %d %s", code, errs)
	}
	code, out, errs = cliRun(t, agent, j.EnrolToken, "enrol", "--hub", env.URL, "--token", "-", "--name", "Web Claude")
	if code != 0 || !strings.Contains(out, "as profile web-claude") || !strings.Contains(out, "display name Web Claude") {
		t.Fatalf("enrol --name: %d %s %s", code, out, errs)
	}
	if code, out, _ := cliRun(t, agent, "", "--net", "web-claude", "--json", "peers"); code != 0 || !strings.Contains(out, `"Web Claude"`) {
		t.Fatalf("directory after naming: %s", out)
	}
	// A second open invite cannot take a lookalike, nor overwrite the profile.
	_, out, _ = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", "--name", "helper", "--open-name")
	if err := json.Unmarshal([]byte(out), &j); err != nil || j.Invite != inviteOpenBlock(env.Hub.Cfg.PublicURL, "helper", j.EnrolToken, j.EnrolExpiresAt) {
		t.Fatalf("suggested invite: %s", out)
	}
	if code, _, errs := cliRun(t, agent, j.EnrolToken, "enrol", "--hub", env.URL, "--token", "-", "--name", "web-claude"); code != 1 || !strings.Contains(errs, "already exists") {
		t.Fatalf("defaulted profile overwrite: %d %s", code, errs)
	}
	if code, _, errs := cliRun(t, agent, j.EnrolToken, "enrol", "--hub", env.URL, "--token", "-", "--name", "WEB  CLAUDE", "--profile", "w2"); code != 2 || !strings.HasPrefix(errs, "name_taken: ") {
		t.Fatalf("lookalike: %d %s", code, errs)
	}
	if code, out, errs := cliRun(t, agent, j.EnrolToken, "enrol", "--hub", env.URL, "--token", "-", "--name", "helper"); code != 0 || !strings.Contains(out, "as profile helper") {
		t.Fatalf("keep suggestion: %d %s %s", code, out, errs)
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
