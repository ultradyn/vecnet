package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

// operatorSetup is a config in the shape Max runs: an agent profile named
// by the legacy default key, plus an operator profile named by
// operator_profile. No VECNET_PROFILE, so runs without --net are bare.
func operatorSetup(t *testing.T, env *testhub.Env, agentSecret, extra string) *user {
	t.Helper()
	u := newUser(t)
	if err := cliFixtureMkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `default = "agent"
` + extra + `
[profiles.agent]
hub = "` + env.URL + `"
secret = "` + agentSecret + `"

[profiles.op]
hub = "` + env.URL + `"
secret = "` + env.OperatorSecret + `"
`
	if err := cliFixtureWriteFile(u.conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBareRunRefusedForAgents(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	u := operatorSetup(t, env, aSec, `operator_profile = "op"`)

	for name, extra := range map[string]map[string]string{
		"claude code": {"CLAUDECODE": "1"},
		"codex":       {"CODEX_THREAD_ID": "019a"},
		"no terminal": {}, // test stdin/stdout are buffers, not terminals
		"forced":      {"VECNET_AGENT": "1", "VECNET_HUMAN": "1"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range extra {
				u.env[k] = v
			}
			defer func() {
				for k := range extra {
					delete(u.env, k)
				}
			}()
			code, out, errs := u.run("send", b, "hi", "--wake")
			if code != 1 || out != "" {
				t.Fatalf("bare agent send = %d %q %q, want exit 1", code, out, errs)
			}
			for _, want := range []string{"--net <profile>", "VECNET_PROFILE"} {
				if !strings.Contains(errs, want) {
					t.Fatalf("stderr %q lacks %q", errs, want)
				}
			}
		})
	}
	if n := rowCountCLI(t, env, `SELECT count(*) FROM deliveries`); n != 0 {
		t.Fatalf("deliveries = %d, want 0", n)
	}

	// The same agent with --net, or VECNET_PROFILE, still sends as itself.
	u.env["CLAUDECODE"] = "1"
	if r := u.send("--net", "agent", b, "explicit", "--hold"); len(r.Deliveries) != 1 {
		t.Fatalf("--net send = %+v", r)
	}
	u.env["VECNET_PROFILE"] = "agent"
	if r := u.send(b, "via env", "--hold"); len(r.Deliveries) != 1 {
		t.Fatalf("VECNET_PROFILE send = %+v", r)
	}
	var sender string
	if err := env.Hub.DB.QueryRow(`SELECT json_extract(body,'$.sender') FROM messages ORDER BY rowid DESC LIMIT 1`).Scan(&sender); err != nil {
		t.Fatal(err)
	}
	if sender == env.OperatorPeer {
		t.Fatal("explicit agent profile sent as the operator")
	}
	if got := lastClient(t, env); got != nil {
		t.Fatalf("peer send labelled: %v", got)
	}
}

func TestBareHumanSendsAsOperatorCLI(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	u := operatorSetup(t, env, aSec, `operator_profile = "op"`)
	u.env["VECNET_HUMAN"] = "1"
	u.env["SSH_CONNECTION"] = "10.0.0.5 51234 10.0.0.9 22"

	r := u.send(b, "from my terminal", "--wake")
	if len(r.Deliveries) != 1 || r.Deliveries[0].Recipient != b {
		t.Fatalf("deliveries = %+v", r.Deliveries)
	}

	host, _ := os.Hostname()
	wantLabel := "Operator on " + envelope.CleanClientHost(host) + " CLI via ssh"
	reader := profileUser(t, env.URL, bSec)
	evs := reader.mustRun("--json", "inbox")
	var body struct {
		Events []struct {
			Data struct {
				Message struct {
					Sender   string         `json:"sender"`
					Metadata map[string]any `json:"metadata"`
				} `json:"message"`
			} `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(evs), &body); err != nil || len(body.Events) != 1 {
		t.Fatalf("inbox %q: %v", evs, err)
	}
	m := body.Events[0].Data.Message
	if m.Sender != env.OperatorPeer {
		t.Fatalf("sender = %s, want operator %s", m.Sender, env.OperatorPeer)
	}
	client, _ := m.Metadata[envelope.ClientMetadataKey].(map[string]any)
	if client["label"] != wantLabel || client["kind"] != "operator-cli" || client["ssh"] != true || client["agent_suspected"] != nil {
		t.Fatalf("vecnet.client = %v, want label %q", client, wantLabel)
	}
	if _, leaked := client["ssh_from"]; leaked {
		t.Fatalf("vecnet.client records the ssh client address: %v", client)
	}

	// A human naming the operator profile with --net is labelled the same.
	delete(u.env, "SSH_CONNECTION")
	u.send("--net", "op", b, "explicit", "--hold")
	if got := lastClient(t, env); got["label"] != "Operator on "+envelope.CleanClientHost(host)+" CLI" || got["agent_suspected"] != nil {
		t.Fatalf("human --net op label = %v", got)
	}

	// An agent may name the operator profile explicitly; the send goes
	// through as the operator, marked agent_suspected.
	delete(u.env, "VECNET_HUMAN")
	u.env["CLAUDECODE"] = "1"
	u.send("--net", "op", b, "agent as operator", "--hold")
	got := lastClient(t, env)
	if got["agent_suspected"] != true || got["label"] != "Operator on "+envelope.CleanClientHost(host)+" CLI (agent suspected)" {
		t.Fatalf("agent --net op label = %v", got)
	}
	var sender string
	if err := env.Hub.DB.QueryRow(`SELECT json_extract(body,'$.sender') FROM messages ORDER BY rowid DESC LIMIT 1`).Scan(&sender); err != nil || sender != env.OperatorPeer {
		t.Fatalf("agent --net op sender = %q, %v", sender, err)
	}

	// A peer credential never carries the label.
	u.send("--net", "agent", b, "as myself", "--hold")
	if got := lastClient(t, env); got != nil {
		t.Fatalf("peer send labelled: %v", got)
	}
}

// lastClient is metadata["vecnet.client"] of the newest message, or nil.
func lastClient(t *testing.T, env *testhub.Env) map[string]any {
	t.Helper()
	var raw *string
	if err := env.Hub.DB.QueryRow(`SELECT json_extract(body,'$.metadata."vecnet.client"') FROM messages ORDER BY rowid DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*raw), &m); err != nil {
		t.Fatalf("vecnet.client %q: %v", *raw, err)
	}
	return m
}

// A config whose legacy default names an operator profile is refused for
// every hub command, even one naming another profile: that file was written
// for a bare run to speak as the operator by accident.
func TestDefaultOperatorProfileRefused(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	u := operatorSetup(t, env, aSec, "")
	body, err := os.ReadFile(u.conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(u.conf, []byte(strings.Replace(string(body), `default = "agent"`, `default = "op"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--net", "agent", "peers"}, {"--net", "op", "peers"}, {"peers"}} {
		u.env["VECNET_HUMAN"] = "1"
		code, out, errs := u.run(args...)
		if code != 1 || out != "" || !strings.Contains(errs, `operator_profile = "op"`) {
			t.Fatalf("%v = %d %q %q, want exit 1 suggesting operator_profile", args, code, out, errs)
		}
	}
	// A default naming a peer profile is merely ignored.
	if err := cliFixtureWriteFile(u.conf, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := u.mustRun("--net", "agent", "peers"); !strings.Contains(out, aID) {
		t.Fatalf("peers = %q", out)
	}
}

func TestBareHumanNeedsOperatorProfile(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")

	// No operator_profile: the legacy default does not select the agent.
	u := operatorSetup(t, env, aSec, "")
	u.env["VECNET_HUMAN"] = "1"
	code, _, errs := u.run("send", b, "hi", "--wake")
	if code != 1 || !strings.Contains(errs, "operator_profile") {
		t.Fatalf("no operator_profile = %d %q, want exit 1 naming operator_profile", code, errs)
	}

	// operator_profile naming a peer credential is refused before any call.
	u = operatorSetup(t, env, aSec, `operator_profile = "agent"`)
	u.env["VECNET_HUMAN"] = "1"
	code, _, errs = u.run("send", b, "hi", "--wake")
	if code != 1 || !strings.Contains(errs, "operator credential") {
		t.Fatalf("peer as operator_profile = %d %q, want exit 1", code, errs)
	}

	// operator_profile naming a missing profile.
	u = operatorSetup(t, env, aSec, `operator_profile = "nope"`)
	u.env["VECNET_HUMAN"] = "1"
	if code, _, errs = u.run("send", b, "hi", "--wake"); code != 1 || !strings.Contains(errs, `"nope"`) {
		t.Fatalf("missing operator_profile = %d %q", code, errs)
	}
	if n := rowCountCLI(t, env, `SELECT count(*) FROM deliveries`); n != 0 {
		t.Fatalf("deliveries = %d, want 0", n)
	}
}

// A bare human run of a read command uses the operator profile too.
func TestBareHumanReadsAsOperator(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "A")
	u := operatorSetup(t, env, aSec, `operator_profile = "op"`)
	u.env["VECNET_HUMAN"] = "1"
	if out := u.mustRun("peers"); !strings.Contains(out, aID) {
		t.Fatalf("peers = %q", out)
	}
}

func rowCountCLI(t *testing.T, env *testhub.Env, q string) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// A hook is an agent's identity, so neither hook nor hook install ever
// falls back to the operator profile of a bare human run.
func TestHookNeverBare(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	u := operatorSetup(t, env, aSec, `operator_profile = "op"`)
	u.env["VECNET_HUMAN"] = "1"
	u.env["XDG_CONFIG_HOME"] = filepath.Join(filepath.Dir(u.conf), "xdg")

	settings := filepath.Join(t.TempDir(), "settings.local.json")
	code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--write", "--settings", settings)
	if code != 1 || out != "" || !strings.Contains(errs, "--profile") {
		t.Fatalf("bare hook install = %d %q %q, want exit 1 asking for --profile", code, out, errs)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("settings written: %v", err)
	}
	if code, out, _ := u.run("hook", "--event", "user-prompt"); code != 0 || out != "" {
		t.Fatalf("bare hook = %d %q, want a silent no-op", code, out)
	}
}
