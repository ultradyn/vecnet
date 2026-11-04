package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/testhub"
)

// sharedDir is one project directory used by two Claude Code sessions: one
// CLI config holding profiles p1 and p2, and one settings file whose hooks
// run both profiles' hooks in every session (issue 38).
type sharedDir struct {
	t   *testing.T
	env map[string]string
}

func newSharedDir(t *testing.T, hub, p1Secret, p2Secret string) *sharedDir {
	t.Helper()
	u := hookUser(t, hub, p1Secret)
	delete(u.env, "VECNET_PROFILE")
	body := "[profiles.p1]\nhub = \"" + hub + "\"\nsecret = \"" + p1Secret + "\"\n[profiles.p2]\nhub = \"" + hub + "\"\nsecret = \"" + p2Secret + "\"\n"
	if err := os.WriteFile(u.conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return &sharedDir{t: t, env: u.env}
}

// turn runs every installed hook (p1's and p2's) as session would for one
// prompt, with extra environment, and returns the combined context.
func (d *sharedDir) turn(session string, extra map[string]string) string {
	d.t.Helper()
	var all strings.Builder
	for _, profile := range []string{"p1", "p2"} {
		var out, errs bytes.Buffer
		stdin := `{"hook_event_name":"UserPromptSubmit","session_id":"` + session + `","cwd":"/proj"}`
		code := cli.RunContext(context.Background(), []string{"hook", "--profile", profile, "--event", "user-prompt"}, strings.NewReader(stdin), &out, &errs, d.getenv(extra))
		if code != 0 {
			d.t.Fatalf("hook %s in %s: exit %d stderr=%q", profile, session, code, errs.String())
		}
		if out.Len() > 0 {
			all.WriteString(hookE2EContext(d.t, out.String(), "UserPromptSubmit"))
			all.WriteByte('\n')
		}
	}
	return all.String()
}
func (d *sharedDir) getenv(extra map[string]string) func(string) string {
	return func(k string) string {
		if v, ok := extra[k]; ok {
			return v
		}
		return d.env[k]
	}
}
func (d *sharedDir) rebind(session, profile string) string {
	d.t.Helper()
	var out, errs bytes.Buffer
	code := cli.RunContext(context.Background(), []string{"hook", "rebind", "--profile", profile}, strings.NewReader(""), &out, &errs, d.getenv(map[string]string{"CLAUDE_CODE_SESSION_ID": session}))
	if code != 0 {
		d.t.Fatalf("rebind %s in %s: exit %d stderr=%q", profile, session, code, errs.String())
	}
	return out.String()
}

// The issue 38 repro: sessions A (agent p1) and B (agent p2) share one
// directory, so each runs both profiles' hooks. Neither session may show
// the other's deliveries; p2's delivery reaches B, and A never sees it.
func TestHookTwoSessionsOneDirectory(t *testing.T) {
	env := testhub.Start(t)
	p1ID, p1Sec := env.NewPeer("session", "P1")
	p2ID, p2Sec := env.NewPeer("session", "P2")
	_, sSec := env.NewPeer("session", "Sender")
	sender := hookUser(t, env.URL, sSec)
	d := newSharedDir(t, env.URL, p1Sec, p2Sec)

	var seenA, seenB strings.Builder
	// A starts first: its first turn must not take p2 for itself.
	seenA.WriteString(d.turn("A", nil))
	seenB.WriteString(d.turn("B", nil))
	early := sender.send(p2ID, "early for p2", "--hold")
	seenA.WriteString(d.turn("A", nil))
	seenB.WriteString(d.turn("B", nil))
	if strings.Contains(seenA.String()+seenB.String(), "early for p2") {
		t.Fatalf("an ambiguous session showed p2's delivery:\nA=%q\nB=%q", seenA.String(), seenB.String())
	}
	if !strings.Contains(seenB.String(), "vecnet hook rebind --profile p2") {
		t.Fatalf("B was not told how to bind p2: %q", seenB.String())
	}
	// Each agent binds its own profile, as the notice tells it to.
	d.rebind("A", "p1")
	if out := d.rebind("B", "p2"); !strings.Contains(out, "now bound to this Claude Code session (B)") {
		t.Fatalf("rebind: %q", out)
	}
	seenB.WriteString(d.turn("B", nil)) // p2's first run: summary of the early backlog
	if !strings.Contains(seenB.String(), "1 unacked deliveries waiting") {
		t.Fatalf("B lost the early delivery: %q", seenB.String())
	}
	seenA.WriteString(d.turn("A", nil))

	toP2 := sender.send(p2ID, "only for p2", "--hold")
	toP1 := sender.send(p1ID, "only for p1", "--hold")
	for range 2 {
		seenA.WriteString(d.turn("A", nil))
		seenB.WriteString(d.turn("B", nil))
	}
	a, b := seenA.String(), seenB.String()
	if strings.Contains(a, "only for p2") || strings.Contains(a, toP2.MessageID) || strings.Contains(a, early.MessageID) {
		t.Fatalf("A saw p2's delivery: %q", a)
	}
	if strings.Contains(b, "only for p1") || strings.Contains(b, toP1.MessageID) {
		t.Fatalf("B saw p1's delivery: %q", b)
	}
	if strings.Count(b, toP2.MessageID) != 1 || strings.Count(a, toP1.MessageID) != 1 {
		t.Fatalf("each delivery must reach its session once:\nA=%q\nB=%q", a, b)
	}
}

// With VECNET_PROFILE set per session, the other profile's hook is silent,
// so no rebind is needed.
func TestHookTwoSessionsVecnetProfile(t *testing.T) {
	env := testhub.Start(t)
	_, p1Sec := env.NewPeer("session", "P1")
	p2ID, p2Sec := env.NewPeer("session", "P2")
	_, sSec := env.NewPeer("session", "Sender")
	sender := hookUser(t, env.URL, sSec)
	d := newSharedDir(t, env.URL, p1Sec, p2Sec)
	asA := map[string]string{"VECNET_PROFILE": "p1"}
	asB := map[string]string{"VECNET_PROFILE": "p2"}
	d.turn("A", asA)
	d.turn("B", asB)
	sent := sender.send(p2ID, "p2 by env", "--hold")
	a := d.turn("A", asA)
	b := d.turn("B", asB)
	if a != "" || !strings.Contains(b, sent.MessageID) || strings.Contains(b, "rebind") {
		t.Fatalf("A=%q B=%q", a, b)
	}
}

// A Monitor watch shares shown-state with the hook but has no session id:
// it must neither unbind nor rebind the profile, and dedupe still holds.
func TestWatchKeepsHookBinding(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	a, b := hookUser(t, env.URL, aSec), hookUser(t, env.URL, bSec)
	hook := func(session string) string {
		t.Helper()
		var out, errs bytes.Buffer
		stdin := `{"hook_event_name":"UserPromptSubmit","session_id":"` + session + `"}`
		if code := cli.RunContext(context.Background(), []string{"hook", "--profile", "p", "--event", "user-prompt"}, strings.NewReader(stdin), &out, &errs, a.getenv); code != 0 {
			t.Fatalf("hook exit %d: %s", code, errs.String())
		}
		return out.String()
	}
	hook("S1") // binds (declared by VECNET_PROFILE=p) and takes the log head
	out, stop := startWatch(t, a, "watch", "--for-monitor")
	sent := b.send(aID, "seen by watch", "--hold")
	waitLine(t, out, regexp.MustCompile(`seen by watch$`), 5*time.Second)
	stop()
	statePath := filepath.Join(filepath.Dir(a.conf), "state", "p.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	braw, err := os.ReadFile(filepath.Join(filepath.Dir(a.conf), "state", "p+session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Session string   `json:"session"`
		Shown   []string `json:"shown"`
	}
	if err := json.Unmarshal(braw, &s); err != nil || s.Session != "S1" {
		t.Fatalf("binding after watch: %s (%v)", braw, err)
	}
	if err := json.Unmarshal(raw, &s); err != nil || len(s.Shown) != 1 || s.Shown[0] != sent.Deliveries[0].DeliveryID {
		t.Fatalf("state after watch: %s (%v)", raw, err)
	}
	if again := hook("S1"); again != "" {
		t.Fatalf("hook repeated a delivery watch showed: %q", again)
	}
	if foreign := hook("S2"); !strings.Contains(foreign, "belongs to another Claude Code session") {
		t.Fatalf("watch changed the binding: %q", foreign)
	}
	b.send(aID, "after watch", "--hold")
	if ctx := hookE2EContext(t, hook("S1"), "UserPromptSubmit"); !strings.Contains(ctx, "after watch") {
		t.Fatalf("owner missed later delivery: %q", ctx)
	}
}
