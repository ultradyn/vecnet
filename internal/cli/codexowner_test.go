package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCodexAppServerRequiresOwnership(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake", "private", "sender")
	out, _ := runHook(t, f.env, "--event", "app-server", "--claim", "unowned", "--priorities", "wake", "--expect-hub", f.hub, "--expect-caller", "peer_test")
	if strings.Contains(out, "private") || len(f.state(t).Claims) != 0 {
		t.Fatalf("unowned delivery: %s", out)
	}
}

func TestCodexAppServerTransferDuringPollFailsClosed(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake", "private", "")
	before := f.bytes(t)
	old := hookAfterPoll
	defer func() { hookAfterPoll = old }()
	hookAfterPoll = func() { f.updateOwner(t, func(b *codexBinding) { b.Generation = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }) }
	_, out, _ := f.run(t, "c1", "wake")
	if strings.Contains(out, "private") || !bytes.Equal(before, f.bytes(t)) {
		t.Fatalf("stale poll committed: %s", out)
	}
}

func TestCodexAppServerCorruptStateFailsClosed(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake", "private", "")
	f.seed(t, "{corrupt")
	_, out, _ := f.run(t, "c1", "wake")
	if strings.Contains(out, "private") || string(f.bytes(t)) != "{corrupt" {
		t.Fatalf("corrupt state overwritten: %s", out)
	}
}

func TestCodexGuardChecksGeneration(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	f.seedGuardClaim("claim1")
	generation := bindValue[string](t, f.entries()[0], "generation")
	code, out, _ := f.run("guard", "--thread", bindT1, "--codex-home", f.home, "--expect-generation", generation, "--expect-hub", f.hub, "--expect-caller", "peer_A", "--claim", "claim1", "--attempt", guardAttemptA)
	var got struct {
		Ready bool `json:"ready"`
	}
	_ = json.Unmarshal([]byte(out), &got)
	if code != 0 || !got.Ready {
		t.Fatalf("guard failed: %d %s", code, out)
	}
	f.rejected("unresolved", "bind", "--thread", bindT2, "--expect-generation", generation)
}

func TestCodexHookNeverConsumesAmbiguousContext(t *testing.T) {
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse"} {
		for _, session := range []string{"", bindT1, bindT2} {
			t.Run(event+session, func(t *testing.T) {
				env, path, polls := sessionHub(t)
				seedHook(t, path, "e1.0")
				env["CODEX_THREAD_ID"] = bindT1
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				input := `{"hook_event_name":"` + event + `","session_id":"` + session + `"}`
				out, _ := runHookStdin(t, env, input, "hook", "--profile", "p")
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if out != "" || polls.Load() != 0 || !bytes.Equal(before, after) {
					t.Fatalf("ambiguous Codex hook consumed state: output=%q polls=%d", out, polls.Load())
				}
			})
		}
	}
}

func TestCodexRebindDoesNotOpenTakeoverWindow(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	var out, errs bytes.Buffer
	code := Run([]string{"hook", "rebind", "--profile", "p"}, bytes.NewReader(nil), &out, &errs, func(k string) string { return f.env[k] })
	if code == 0 {
		t.Fatal("Codex rebind allowed a next-hook-wins window")
	}
}

func TestCodexOwnershipRejectsRecipientAlias(t *testing.T) {
	f := newBindFixture(t)
	cfg, err := loadConfig(f.env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles["q"] = cfg.Profiles["p"]
	if err := saveConfig(f.env["VECNET_CONFIG"], cfg); err != nil {
		t.Fatal(err)
	}
	f.bind("p", bindT1)
	f.rejected("recipient", "--net", "q", "bind", "--thread", bindT2)
}

func TestCodexOwnershipGenerationAndAddressedHandoff(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	original := f.entries()[0]
	generation := bindValue[string](t, original, "generation")
	if len(generation) != 32 {
		t.Fatalf("invalid generation %q", generation)
	}
	if bindValue[string](t, original, "config_path") != f.env["VECNET_CONFIG"] {
		t.Fatal("missing config namespace")
	}
	f.now = f.now.Add(time.Hour)
	f.bind("p", bindT1, "--renew")
	if bindValue[string](t, f.entries()[0], "generation") != generation {
		t.Fatal("renewal changed ownership")
	}
	f.rejected("generation", "bind", "--thread", bindT2, "--expect-generation", "00000000000000000000000000000000")
	f.bind("p", bindT2, "--expect-generation", generation)
	next := f.entries()[0]
	if bindValue[string](t, next, "thread_id") != bindT2 || bindValue[string](t, next, "generation") == generation {
		t.Fatal("handoff did not replace ownership")
	}
	f.rejected("generation", "bind", "--thread", bindT1, "--expect-generation", generation)
}

func TestCodexOwnershipUsesInvokingThread(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.ok("bind")
	if bindValue[string](t, f.entries()[0], "thread_id") != bindT1 {
		t.Fatal("wrong invoking thread")
	}
}

func TestCodexOwnershipRejectsSameThreadOtherHome(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	f.rejected("home", "bind", "--thread", bindT1, "--codex-home", f.env["HOME"])
}
