package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

// inviteFixture is testdata/invite.json, the golden shared with the web UI's
// invite-model.js (internal/ui/jstest/invite.test.mjs).
type inviteFixture struct {
	Slugs []struct {
		Name    string `json:"name"`
		Profile string `json:"profile"`
	} `json:"slugs"`
	Blocks []struct {
		Owner     string `json:"owner"`
		HubURL    string `json:"hub_url"`
		Profile   string `json:"profile"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		Open      bool   `json:"open"`
		Name      string `json:"name"`
		Block     string `json:"block"`
	} `json:"blocks"`
}

func TestInviteBlockGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "invite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f inviteFixture
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Slugs) == 0 || len(f.Blocks) == 0 {
		t.Fatalf("invite.json: %v", err)
	}
	for _, c := range f.Slugs {
		if got := inviteSlug(c.Name); got != c.Profile || !profileRE.MatchString(got) {
			t.Errorf("inviteSlug(%q) = %q, want %q", c.Name, got, c.Profile)
		}
	}
	open := 0
	for _, c := range f.Blocks {
		got := inviteBlock(invite{Owner: c.Owner, HubURL: c.HubURL, Profile: c.Profile, Token: c.Token, Expires: c.ExpiresAt, Open: c.Open, Name: c.Name})
		if got != c.Block {
			t.Errorf("invite block (%q):\n%s\nwant:\n%s", c.Profile, got, c.Block)
		}
		if c.Open {
			open++
		}
		// Short, and agents paste it verbatim: shell guards refuse variables,
		// command chains and substitutions, so nothing like that anywhere.
		if n := strings.Count(got, "\n"); n > 5 {
			t.Errorf("invite block (%q) is %d lines", c.Profile, n)
		}
		if strings.ContainsAny(got, "$;<>&") {
			t.Errorf("invite block (%q) holds a shell metacharacter", c.Profile)
		}
		for _, line := range strings.Split(got, "\n") {
			if cmd, ok := strings.CutPrefix(line, "  "); ok && (strings.ContainsAny(cmd, "`(){}") || strings.Count(cmd, "|") != 1) {
				t.Errorf("command line %q is not one plain pipeline", cmd)
			}
		}
	}
	if open == 0 || open == len(f.Blocks) {
		t.Fatal("invite.json needs fixed and open blocks")
	}
}

// The guide is golden in both forms: placeholders, and filled in for a
// profile the config holds. Run with -update to rewrite the goldens.
func TestGuideGolden(t *testing.T) {
	lookup := func(name string) (Profile, bool) {
		return Profile{Hub: "https://vecnet.example.net/", PeerID: "peer_01TESTGUIDE"}, name == "scout"
	}
	for _, c := range []struct{ file, profile string }{{"guide.golden", ""}, {"guide-profile.golden", "scout"}} {
		got := renderGuide(c.profile, lookup)
		path := filepath.Join("testdata", c.file)
		if *updateHookGolden { // the package's one -update flag (hook_test.go)
			if err := cliFixtureWriteFile(path, []byte(strings.ReplaceAll(got, "\r\n", "\n")), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ReplaceAll(got, "\r\n", "\n") != strings.ReplaceAll(string(want), "\r\n", "\n") {
			t.Errorf("guide for %q differs from %s; rerun with -update and review the diff", c.profile, path)
		}
	}
	filled := renderGuide("scout", lookup)
	for _, s := range []string{"<profile>", "<hub>", "<peer_id>"} {
		if strings.Contains(filled, s) {
			t.Errorf("filled guide keeps %s", s)
		}
	}
	for _, s := range []string{"vecnet --net scout inbox", "vecnet hook install --profile scout --write", "VECNET_PROFILE=scout claude", "https://vecnet.example.net/mcp", "peer_01TESTGUIDE"} {
		if !strings.Contains(filled, s) {
			t.Errorf("filled guide lacks %q", s)
		}
	}
	// A profile the config lacks fills the profile only; an unsafe one nothing.
	if g := renderGuide("other", lookup); !strings.Contains(g, "vecnet --net other inbox") || !strings.Contains(g, "<hub>") {
		t.Error("guide for an unknown profile")
	}
	if renderGuide("-x; rm", lookup) != guideText {
		t.Error("guide filled an unsafe profile")
	}
	hostile := func(string) (Profile, bool) { return Profile{Hub: "https://h/$(id)", PeerID: "peer_x;y"}, true }
	if g := renderGuide("scout", hostile); strings.Contains(g, "$(id)") || strings.Contains(g, "peer_x;y") {
		t.Error("guide filled a hostile hub or peer id")
	}
}

// cliRun runs the CLI with its config under dir, as the cli_test users do.
func cliRun(t *testing.T, dir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	return cliRunEnv(t, dir, stdin, nil, args...)
}

func cliRunEnv(t *testing.T, dir, stdin string, extra map[string]string, args ...string) (int, string, string) {
	t.Helper()
	env := map[string]string{"VECNET_CONFIG": filepath.Join(dir, "config.toml"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"), "HOME": filepath.Join(dir, "home")}
	for k, v := range extra {
		env[k] = v
	}
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func operatorDir(t *testing.T, env *testhub.Env) string {
	t.Helper()
	dir := t.TempDir()
	conf := "[profiles.op]\nhub = \"" + env.URL + "\"\nsecret = \"" + env.OperatorSecret + "\"\n"
	if err := cliFixtureWriteFile(filepath.Join(dir, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func hubOwner(t *testing.T, env *testhub.Env) string {
	t.Helper()
	var owner string
	if err := env.Hub.DB.QueryRow(`SELECT p.name FROM principals p JOIN credentials c ON c.subject_id = p.prn_id WHERE c.kind = 'operator'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

var tokenRE = regexp.MustCompile(`printf %s '(vn_enrol_[A-Za-z0-9_-]+)'`)

func TestPeerInviteEndToEnd(t *testing.T) {
	env := testhub.Start(t)
	opDir := operatorDir(t, env)
	code, out, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "Scout Bot", "--ttl", "2h")
	tok := tokenRE.FindStringSubmatch(out)
	ids := regexp.MustCompile(`invited peer (peer_\S+) \(profile scout-bot\); the token works once and expires (\S+)\n`).FindStringSubmatch(errs)
	if code != 0 || tok == nil || ids == nil {
		t.Fatalf("peer invite: %d\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	exp := envelope.FormatTime(env.Now.Now().Add(2 * time.Hour))
	// stdout is the paste block and nothing else.
	want := inviteBlock(invite{Owner: hubOwner(t, env), HubURL: env.Hub.Cfg.PublicURL, Profile: "scout-bot", Token: tok[1], Expires: exp})
	if out != want || ids[2] != exp {
		t.Fatalf("peer invite stdout:\n%s\nwant:\n%s", out, want)
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
	// The guide it then reads is filled in from the new profile.
	_, out, _ = cliRun(t, agentDir, "", "guide", "--net", "scout-bot")
	if !strings.Contains(out, "vecnet --net scout-bot inbox") || !strings.Contains(out, ids[1]) || !strings.Contains(out, env.URL+"/mcp") {
		t.Fatalf("guide --net after enrol:\n%.400s", out)
	}
	if code, _, errs := cliRun(t, agentDir, "", "--net", "scout-bot", "peers"); code != 0 {
		t.Fatalf("peers as the invited agent: %d %s", code, errs)
	}

	// Refused kinds and bad TTLs never reach the hub.
	for _, kind := range []string{"operator", "dot", "source"} {
		if code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "x", "--kind", kind); code != 1 || !strings.Contains(errs, "cannot be invited") {
			t.Fatalf("invite --kind %s: %d %s", kind, code, errs)
		}
	}
	for _, ttl := range []string{"soon", "1.5s"} {
		if code, _, _ := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "x", "--ttl", ttl); code != 1 {
			t.Fatalf("invite --ttl %s: exit %d", ttl, code)
		}
	}
	if code, _, errs := cliRun(t, opDir, "", "--net", "op", "peer", "invite", "--name", "x", "--ttl", "8d"); code != 2 || !strings.HasPrefix(errs, "invalid_value: ") {
		t.Fatalf("invite --ttl 8d: %d %s", code, errs)
	}

	// --json carries the block and the fields; an expired invite is 410 and
	// re-issue makes a working one.
	code, out, errs = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", "--name", "Late", "--ttl", "1h")
	var j map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || j["profile"] != "late" || !strings.Contains(j["invite"].(string), "--profile late\n") {
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

// --copy pipes the block into the first clipboard tool on PATH and still
// prints it; with none it says so.
func TestPeerInviteCopy(t *testing.T) {
	env := testhub.Start(t)
	opDir := operatorDir(t, env)
	bin := t.TempDir()
	clip := filepath.Join(t.TempDir(), "clip")
	script := "#!/bin/sh\ncat > '" + clip + "'\n"
	tool := "xclip"
	if runtime.GOOS == "windows" {
		tool = "clip.exe"
	}
	cliFixtureProgram(t, bin, tool, script, cliFixtureProgramSpec{Kind: "clipboard", Log: clip})
	code, out, errs := cliRunEnv(t, opDir, "", map[string]string{"PATH": bin}, "--net", "op", "peer", "invite", "--name", "Clip", "--copy")
	got, _ := os.ReadFile(clip)
	if code != 0 || string(got) != out || !strings.Contains(out, "vecnet enrol") || !strings.Contains(errs, "copied to the clipboard ("+tool+")") {
		t.Fatalf("--copy: %d clip=%q out=%q err=%q", code, got, out, errs)
	}
	code, out, errs = cliRunEnv(t, opDir, "", map[string]string{"PATH": t.TempDir()}, "--net", "op", "peer", "invite", "--name", "NoClip", "--copy")
	if code != 0 || !strings.Contains(out, "vecnet enrol") || !strings.Contains(errs, "not copied: no clipboard tool found") {
		t.Fatalf("--copy without a tool: %d %q %q", code, out, errs)
	}
}

// An open invite: the agent names itself at enrol, once; refusals spend
// nothing; the profile in the block is fixed by the invite.
func TestPeerInviteOpenName(t *testing.T) {
	env := testhub.Start(t)
	opDir := operatorDir(t, env)
	code, out, errs := cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite")
	var j struct {
		Name, Profile, Invite string
		NameOpen              bool   `json:"name_open"`
		EnrolToken            string `json:"enrol_token"`
		EnrolExpiresAt        string `json:"enrol_expires_at"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || !j.NameOpen || !strings.HasPrefix(j.Profile, "invited-") ||
		j.Invite != inviteBlock(invite{Owner: hubOwner(t, env), HubURL: env.Hub.Cfg.PublicURL, Profile: j.Profile, Token: j.EnrolToken, Expires: j.EnrolExpiresAt, Open: true}) {
		t.Fatalf("open invite: %d %s %s", code, out, errs)
	}
	agent := t.TempDir()
	enrol := func(tok string, extra ...string) (int, string, string) {
		return cliRun(t, agent, tok, append([]string{"enrol", "--hub", env.URL, "--token", "-"}, extra...)...)
	}
	if code, _, errs := enrol(j.EnrolToken, "--profile", j.Profile); code != 2 || !strings.HasPrefix(errs, "name_required: ") {
		t.Fatalf("enrol without --name: %d %s", code, errs)
	}
	if code, _, errs := enrol(j.EnrolToken, "--profile", j.Profile, "--name", "M a x"); code != 2 || !strings.HasPrefix(errs, "name_reserved: ") {
		t.Fatalf("enrol as max: %d %s", code, errs)
	}
	code, out, errs = enrol(j.EnrolToken, "--profile", j.Profile, "--name", "Web Claude")
	if code != 0 || !strings.Contains(out, "as profile "+j.Profile) || !strings.Contains(out, "display name Web Claude") {
		t.Fatalf("enrol --name: %d %s %s", code, out, errs)
	}
	if code, out, _ := cliRun(t, agent, "", "--net", j.Profile, "--json", "peers"); code != 0 || !strings.Contains(out, `"Web Claude"`) {
		t.Fatalf("directory after naming: %s", out)
	}
	// A suggestion fixes the profile to its slug; a lookalike is refused;
	// without --profile the slug of the chosen name never overwrites one.
	_, out, _ = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite", "--name", "helper", "--open-name")
	if err := json.Unmarshal([]byte(out), &j); err != nil || j.Profile != "helper" || !strings.Contains(j.Invite, "--profile helper --name 'helper'\n") {
		t.Fatalf("suggested invite: %s", out)
	}
	if code, _, errs := enrol(j.EnrolToken, "--profile", "w2", "--name", "WEB  CLAUDE"); code != 2 || !strings.HasPrefix(errs, "name_taken: ") {
		t.Fatalf("lookalike: %d %s", code, errs)
	}
	if code, _, errs := enrol(j.EnrolToken, "--name", "web claude 2"); code != 0 {
		t.Fatalf("enrol with a defaulted profile: %d %s", code, errs)
	}
	_, out, _ = cliRun(t, opDir, "", "--net", "op", "--json", "peer", "invite")
	_ = json.Unmarshal([]byte(out), &j)
	if code, _, errs := enrol(j.EnrolToken, "--name", "Web Claude 2"); code != 1 || !strings.Contains(errs, "already exists") {
		t.Fatalf("defaulted profile overwrite: %d %s", code, errs)
	}
}

func TestGuideCommand(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := cliRun(t, dir, "", "guide")
	if code != 0 || out != guideText {
		t.Fatalf("guide: %d %.200s", code, out)
	}
	if n := strings.Count(out, "\n"); n > 120 {
		t.Fatalf("guide is %d lines; keep it short", n)
	}
	for _, must := range []string{"untrusted data", "--wake", "--wake-idle", "--hold", "hook install --profile", "watch --for-monitor",
		"30 minutes", "vecnet mcp", "VECNET_PROFILE=<profile>", "One agent per profile", "secret", "avatar set", "react", "presence set", "ack <cursor>"} {
		if !strings.Contains(out, must) {
			t.Errorf("guide lacks %q", must)
		}
	}
	// Order: set up, everyday commands, rules.
	if a, b, c := strings.Index(out, "## 1. Set up"), strings.Index(out, "## 2. Everyday"), strings.Index(out, "## 3. Rules"); !(a >= 0 && a < b && b < c) {
		t.Errorf("guide sections out of order: %d %d %d", a, b, c)
	}
	_, out, _ = cliRun(t, dir, "", "--net", "scout", "guide")
	if strings.Contains(out, "<profile>") || !strings.Contains(out, "vecnet --net scout inbox") {
		t.Fatalf("guide --net did not fill the profile")
	}
	_, out, _ = cliRunEnv(t, dir, "", map[string]string{"VECNET_PROFILE": "envprof"}, "guide")
	if !strings.Contains(out, "vecnet --net envprof inbox") {
		t.Fatalf("guide did not honour VECNET_PROFILE")
	}
	if code, _, _ := cliRun(t, dir, "", "guide", "extra"); code != 1 {
		t.Fatalf("guide extra: %d", code)
	}
}

// The guide teaches MCP first, with the CLI as the fallback.
func TestGuideNamesMCPTools(t *testing.T) {
	lookup := func(name string) (Profile, bool) {
		return Profile{Hub: "https://vecnet.example.net/", PeerID: "peer_01TESTGUIDE"}, name == "scout"
	}
	g := renderGuide("scout", lookup)
	for _, s := range []string{"mcp__vecnet-scout__vecnet_reply", "vecnet --net scout mcp", "vecnet hook install --profile scout --mcp --write", "vecnet --net scout inbox"} {
		if !strings.Contains(g, s) {
			t.Errorf("guide lacks %q", s)
		}
	}
	// MCP is named before the everyday CLI commands it replaces.
	if m, c := strings.Index(g, "mcp__vecnet-scout__vecnet_reply"), strings.Index(g, "## 2. Everyday"); m < 0 || m > c {
		t.Errorf("MCP tools are not named before the CLI section: %d %d", m, c)
	}
}
