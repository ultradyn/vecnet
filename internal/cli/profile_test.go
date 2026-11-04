package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestEnrolPinsHubPubkey(t *testing.T) {
	e := testhub.Start(t)
	_, tok, err := e.Hub.AddPeer(context.Background(), "session", "pin-test", "")
	if err != nil {
		t.Fatal(err)
	}
	path, env := pinTestEnv(t, "")
	if code, _, err := pinRun(env, "enrol", "--hub", e.URL, "--token", tok, "--profile", "p"); code != 0 {
		t.Fatalf("%d %s", code, err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["p"].HubPubkey != e.Hub.IdentityKey().Pubkey() {
		t.Fatalf("enrol did not pin: %+v", cfg)
	}
}
func pinnedUser(t *testing.T, e *testhub.Env) (string, func(string) string) {
	t.Helper()
	return pinTestEnv(t, "[profiles.p]\nhub = \""+e.URL+"\"\nhub_pubkey = \""+e.Hub.IdentityKey().Pubkey()+"\"\nsecret = \""+e.OperatorSecret+"\"\n")
}
func TestProfileRepointVerifies(t *testing.T) {
	e := testhub.Start(t)
	clone := e.CloneDB(t)
	path, env := pinnedUser(t, e)
	if code, _, err := pinRun(env, "profile", "repoint", clone.URL); code != 0 {
		t.Fatalf("%d %s", code, err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["p"]
	if p.Hub != clone.URL || p.HubPubkey != e.Hub.IdentityKey().Pubkey() {
		t.Fatalf("bad repoint %+v", p)
	}
	clone.NewPeer("session", "clone-only")
	if code, out, err := pinRun(env, "--json", "peers"); code != 0 || !strings.Contains(out, "clone-only") {
		t.Fatalf("did not reach clone: %d %s %s", code, out, err)
	}
}
func TestProfileRepointRefusesWithoutForce(t *testing.T) {
	e, other := testhub.Start(t), testhub.Start(t)
	path, env := pinnedUser(t, e)
	original := string(pinRead(t, path))
	if code, _, err := pinRun(env, "profile", "repoint", other.URL); code != 1 || !strings.Contains(err, "relocation_unverified") {
		t.Fatalf("%d %s", code, err)
	}
	if string(pinRead(t, path)) != original {
		t.Fatal("refusal changed config")
	}
	a := &app{ctx: context.Background(), getenv: env}
	if err := a.updatePins(func(m map[string]pinRecord) error {
		m["p"] = pinRecord{Hub: e.URL, Pubkey: e.Hub.IdentityKey().Pubkey(), How: "tofu"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, err := pinRun(env, "profile", "repoint", other.URL, "--force"); code != 0 {
		t.Fatalf("%d %s", code, err)
	}
	cfg, _ := loadConfig(path)
	if cfg.Profiles["p"].Hub != other.URL || cfg.Profiles["p"].HubPubkey != "" {
		t.Fatalf("force %+v", cfg)
	}
	pp, _ := pinsPath(env)
	var m map[string]pinRecord
	if err := json.Unmarshal(pinRead(t, pp), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["p"]; ok {
		t.Fatal("force kept sidecar")
	}
}
func TestProfilePinAccept(t *testing.T) {
	k, old := testIdentityKey(t, "1"), testIdentityKey(t, "2")
	srv, _ := identityStub(t, k, nil)
	path, env := pinTestEnv(t, "[profiles.p]\nhub = \""+srv.URL+"\"\nhub_pubkey = \""+old.Pubkey()+"\"\nsecret = \"s\"\n")
	original := string(pinRead(t, path))
	for _, args := range [][]string{{"profile", "pin"}, {"profile", "pin", "--accept", "SHA256:wrong"}} {
		if code, _, _ := pinRun(env, args...); code != 1 {
			t.Fatalf("mismatch exit %d", code)
		}
		if string(pinRead(t, path)) != original {
			t.Fatal("refusal changed config")
		}
	}
	fp, _ := hubid.Fingerprint(k.Pubkey())
	if code, _, err := pinRun(env, "profile", "pin", "--accept", fp); code != 0 {
		t.Fatalf("%d %s", code, err)
	}
	cfg, _ := loadConfig(path)
	if cfg.Profiles["p"].HubPubkey != k.Pubkey() {
		t.Fatal("accepted key not saved")
	}
	if code, out, err := pinRun(env, "profile", "show", "--json"); code != 0 || !strings.Contains(out, fp) || !strings.Contains(out, `"how":"accept"`) {
		t.Fatalf("show %d %s %s", code, out, err)
	}
	// Unpinned explicit pin is also saved into config.
	path, env = pinTestEnv(t, "[profiles.p]\nhub = \""+srv.URL+"\"\nsecret = \"s\"\n")
	if code, _, err := pinRun(env, "profile", "pin"); code != 0 {
		t.Fatalf("initial pin %d %s", code, err)
	}
	cfg, _ = loadConfig(path)
	if cfg.Profiles["p"].HubPubkey != k.Pubkey() {
		t.Fatal("initial key absent")
	}
}

func TestProfileShowSelectionAndLastMove(t *testing.T) {
	k := testIdentityKey(t, "1")
	fp, _ := hubid.Fingerprint(k.Pubkey())
	original := "operator_profile = \"op\"\n[profiles.p]\nhub = \"https://peer.example\"\nsecret = \"peer\"\n[profiles.op]\nhub = \"https://operator.example\"\nsecret = \"vn_operator_test\"\nhub_pubkey = \"" + k.Pubkey() + "\"\n"
	path, env := pinTestEnv(t, original)
	a := &app{ctx: context.Background(), getenv: env}
	move := &moveRecord{From: "https://old.example", To: "https://peer.example", NoticeID: "ntc_test", IssuedAt: "2026-11-05T07:00:00.000Z"}
	if err := a.updatePins(func(m map[string]pinRecord) error {
		m["p"] = pinRecord{Hub: "https://peer.example", Pubkey: k.Pubkey(), How: "follow", LastMove: move}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, jsonFlag := range []bool{false, true} {
		args := []string{"profile", "show"}
		if jsonFlag {
			args = append(args, "--json")
		}
		code, out, err := pinRun(env, args...)
		if code != 0 || !strings.Contains(out, fp) || !strings.Contains(out, move.NoticeID) || !strings.Contains(out, "follow") {
			t.Fatalf("show %d %s %s", code, out, err)
		}
	}
	if code, out, err := pinRun(env, "--net", "op", "profile", "show", "--json"); code != 0 || !strings.Contains(out, `"profile":"op"`) || strings.Contains(out, "ntc_test") {
		t.Fatalf("--net selection %d %s %s", code, out, err)
	}
	bare := func(k string) string {
		if k == "VECNET_PROFILE" {
			return ""
		}
		return env(k)
	}
	if code, _, err := pinRun(bare, "profile", "show"); code != 1 || !strings.Contains(err, "--net") {
		t.Fatalf("bare agent %d %s", code, err)
	}
	human := func(k string) string {
		if k == "VECNET_HUMAN" {
			return "1"
		}
		return bare(k)
	}
	if code, out, err := pinRun(human, "profile", "show", "--json"); code != 0 || !strings.Contains(out, `"profile":"op"`) {
		t.Fatalf("bare human %d %s %s", code, out, err)
	}
	if string(pinRead(t, path)) != original {
		t.Fatal("show rewrote config")
	}
}

func TestProfileRepointRequiresExistingPin(t *testing.T) {
	k := testIdentityKey(t, "1")
	srv, calls := identityStub(t, k, nil)
	path, env := pinTestEnv(t, "[profiles.p]\nhub = \"https://old.example\"\nsecret = \"s\"\n")
	original := string(pinRead(t, path))
	if code, _, err := pinRun(env, "profile", "repoint", srv.URL); code != 1 || !strings.Contains(err, "relocation_unverified") {
		t.Fatalf("unpinned repoint %d %s", code, err)
	}
	if calls.Load() != 0 || string(pinRead(t, path)) != original {
		t.Fatal("unpinned repoint probed or rewrote config")
	}
}
