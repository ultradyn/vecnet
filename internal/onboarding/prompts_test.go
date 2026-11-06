package onboarding_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/onboarding"
)

// peerIDValue is an allocated peer id (peer_ plus a body). The field name
// "peer_id" in whoami instructions is not one.
var peerIDValue = regexp.MustCompile(`\bpeer_[A-Za-z0-9]{8,}\b`)

func TestLegacyPromptCompatibility(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "cli", "testdata", "invite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fix inviteFixture
	if err := json.Unmarshal(raw, &fix); err != nil || len(fix.Slugs) == 0 || len(fix.Blocks) == 0 {
		t.Fatalf("invite.json: %v", err)
	}
	profileRE := regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)
	for _, c := range fix.Slugs {
		got := onboarding.Slug(c.Name)
		if got != c.Profile || !profileRE.MatchString(got) {
			t.Errorf("Slug(%q) = %q, want %q", c.Name, got, c.Profile)
		}
	}
	open := 0
	for _, c := range fix.Blocks {
		if c.Open {
			open++
		}
		values := map[string]string{
			"owner": c.Owner, "hub_url": c.HubURL, "profile": c.Profile,
			"token": c.Token, "expires": c.ExpiresAt, "name": c.Name,
		}
		if c.Open {
			values["open"] = "true"
		} else {
			values["open"] = "false"
		}
		for _, kind := range []string{"session", "launcher", "openclaw", "pagchat-bot"} {
			got, err := onboarding.RenderPrompt(kind, onboarding.PhaseInvite, values)
			if err != nil {
				t.Fatalf("%s invite: %v", kind, err)
			}
			if got != c.Block {
				t.Errorf("%s invite block (%q):\n%s\nwant:\n%s", kind, c.Profile, got, c.Block)
			}
		}
		if n := strings.Count(c.Block, "\n"); n > 5 {
			t.Errorf("invite block (%q) is %d lines", c.Profile, n)
		}
		if strings.ContainsAny(c.Block, "$;<>&") {
			t.Errorf("invite block (%q) holds a shell metacharacter", c.Profile)
		}
		for _, line := range strings.Split(c.Block, "\n") {
			if cmd, ok := strings.CutPrefix(line, "  "); ok && (strings.ContainsAny(cmd, "`(){}") || strings.Count(cmd, "|") != 1) {
				t.Errorf("command line %q is not one plain pipeline", cmd)
			}
		}
	}
	if open == 0 || open == len(fix.Blocks) {
		t.Fatal("invite.json needs fixed and open blocks")
	}
	spec, ok := onboarding.Lookup("session")
	if !ok || !strings.Contains(spec.PromptTemplate, "You're invited to vecnet") || !strings.Contains(spec.PromptTemplate, "untrusted data") {
		t.Fatal("session prompt_template is not the invitation wording")
	}
	if _, err := onboarding.RenderPrompt("dot", onboarding.PhaseInvite, map[string]string{}); err == nil {
		t.Fatal("dot rendered an enrolment invite")
	}
	secret := fakeKey(t)
	_, err = onboarding.RenderPrompt("session", onboarding.PhaseInvite, map[string]string{
		"owner": "Max", "hub_url": "https://hub.example", "profile": "scout",
		"token": "vn_enrol_example", "expires": "2026-11-05T09:30:00.000Z",
		"webhook_key": secret,
	})
	checkFE(t, err, secret, onboarding.CodeUnknownField, "webhook_key")
}

func TestGrokbotPromptIdentityStop(t *testing.T) {
	const (
		hub       = "https://hub.example"
		peer      = "peer_expected"
		connector = "vecnet-helper"
		thread    = "thr_setup"
		profile   = "helper-bot"
	)
	spec, ok := onboarding.Lookup("grokbot")
	if !ok {
		t.Fatal("Lookup grokbot")
	}
	if strings.Contains(spec.PromptTemplate, "{{.PeerID}}") {
		t.Fatal("authorize template binds a peer id before the peer exists")
	}
	if !strings.Contains(spec.PromptTemplate, "{{.ConnectorName}}") {
		t.Fatal("authorize template does not name the connector placeholder")
	}
	for _, bad := range []string{"WebhookURL", "WebhookKey", "LinkCode", "{{.Token}}", "{{.Key}}", "stdio", "mcp-remote", "crsr_", "api2.cursor.sh", "conductor", "vecnet-peer", "user-vecnet"} {
		if strings.Contains(spec.PromptTemplate, bad) {
			t.Fatalf("authorize template contains %q", bad)
		}
	}

	auth, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseAuthorize, map[string]string{
		"hub_url": hub, "connector_name": connector,
	})
	if err != nil {
		t.Fatal(err)
	}
	routine := strings.Index(auth, "vecnet incoming messages")
	who := strings.Index(auth, "vecnet_whoami")
	if routine < 0 || who < 0 || routine > who {
		t.Fatal("authorization prompt is not webhook-first")
	}
	for _, need := range []string{
		"routine page", "deep link", "Never paste", hub, connector,
		"vecnet-<slug>", "few gateway", "Hosted OAuth", "default",
		"setup message", `kind "peer"`, "peer_id", "user's own chat", "not proof",
		"connected", "receipt", "ack",
	} {
		if !strings.Contains(auth, need) {
			t.Fatalf("authorize prompt missing %q", need)
		}
	}
	if strings.Contains(auth, peer) || peerIDValue.MatchString(auth) {
		t.Fatal("authorization prompt names a peer id before submission creates one")
	}
	assertNoForbidden(t, auth)

	oauth, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseSetup, map[string]string{
		"hub_url": hub, "peer_id": peer, "connector_name": connector, "thread_id": thread,
	})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseSetup, map[string]string{
		"hub_url": hub, "peer_id": peer, "connector_name": connector, "thread_id": thread,
		"credential_mode": onboarding.CredentialCLIBearer, "profile": profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if oauth == cli {
		t.Fatal("hosted OAuth and CLI bearer rendered the same text")
	}
	for _, need := range []string{
		"Hosted OAuth", connector, peer, hub + "/mcp", "vecnet peer link",
		"one-time code", "No peer secret", "keep-bearer",
		"advisory", "agent_suspected", "wake_idle", "Priority is still required",
		"vecnet_whoami", `kind "peer"`, "user's own chat", "not proof",
		"vecnet_receipt", "vecnet_ack", "run started", "accepted",
		"A receipt does not ack",
		"200 runs", "300 runs", "not a price", "two runs", "WakeParent",
		"few gateway", "vecnet-<slug>", thread,
	} {
		if !strings.Contains(oauth, need) {
			t.Fatalf("hosted setup missing %q", need)
		}
	}
	if strings.Contains(oauth, "CLI bearer") {
		t.Fatal("hosted setup also names CLI bearer")
	}
	assertNoForbidden(t, oauth)

	for _, need := range []string{
		"CLI bearer", profile, peer, "wake_idle", "vecnet_whoami",
		`kind "peer"`, "user's own chat", "advisory", "200 runs", "300 runs",
		"A receipt does not ack", thread, hub,
	} {
		if !strings.Contains(cli, need) {
			t.Fatalf("CLI bearer setup missing %q", need)
		}
	}
	for _, banned := range []string{"OAuth", "peer link", "vecnet-", "/mcp", connector, "Hosted OAuth"} {
		if strings.Contains(cli, banned) {
			t.Fatalf("CLI bearer setup contains %q", banned)
		}
	}
	assertNoForbidden(t, cli)

	secret := fakeKey(t)
	routineURL := "https://api2.cursor.sh/automations/webhook/01234567-89ab-cdef-0123-456789abcdef?q=" + secret
	_, err = onboarding.RenderPrompt("grokbot", onboarding.PhaseAuthorize, map[string]string{
		"hub_url": hub, "connector_name": connector, "webhook_key": secret,
	})
	checkFE(t, err, secret, onboarding.CodeUnknownField, "webhook_key")
	_, err = onboarding.RenderPrompt("grokbot", onboarding.PhaseSetup, map[string]string{
		"hub_url": routineURL, "peer_id": peer, "connector_name": connector, "thread_id": thread,
	})
	checkFE(t, err, secret, onboarding.CodeInvalidValue, "hub_url")
}

func TestGrokbotAuthorizeBeforePeer(t *testing.T) {
	const (
		hub       = "https://hub.example"
		connector = "vecnet-helper"
		peer      = "peer_expected"
		thread    = "thr_setup"
	)
	auth, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseAuthorize, map[string]string{
		"hub_url": hub, "connector_name": connector,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auth, peer) || peerIDValue.MatchString(auth) {
		t.Fatal("pre-submit authorization embedded a peer id")
	}
	if !strings.Contains(auth, "setup message") || !strings.Contains(auth, "vecnet_whoami") {
		t.Fatal("authorization does not defer the whoami binding to the setup message")
	}
	_, err = onboarding.RenderPrompt("grokbot", onboarding.PhaseAuthorize, map[string]string{
		"hub_url": hub, "connector_name": connector, "peer_id": peer,
	})
	checkFE(t, err, "", onboarding.CodeUnknownField, "peer_id")

	setup, err := onboarding.RenderPrompt("grokbot", onboarding.PhaseSetup, map[string]string{
		"hub_url": hub, "peer_id": peer, "connector_name": connector, "thread_id": thread,
	})
	if err != nil {
		t.Fatal(err)
	}
	who := strings.Index(setup, "vecnet_whoami")
	if who < 0 {
		t.Fatal("setup message has no whoami stop")
	}
	line := setup[who:]
	if n := strings.IndexByte(line, '\n'); n >= 0 {
		line = line[:n]
	}
	if !strings.Contains(line, peer) || !strings.Contains(line, `kind "peer"`) || !strings.Contains(line, "peer_id") {
		t.Fatal("setup whoami stop does not name the created peer")
	}
}

func assertNoForbidden(t *testing.T, text string) {
	t.Helper()
	for _, bad := range []string{
		"stdio", "mcp-remote", "mcp_remote", "crsr_", "api2.cursor.sh", "Bearer",
		"conductor", "vecnet-peer", "user-vecnet", "vn_enrol_", "{{",
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("prompt contains %q", bad)
		}
	}
}

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
