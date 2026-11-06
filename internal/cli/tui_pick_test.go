package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/tui"
)

func TestSetOperatorProfileRewritesOneLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "# keep me\n# operator_profile = \"nope\"\noperator_profile = \"operator\"\n\n[profiles.operator]\nhub = \"http://127.0.0.1:45752\"\nsecret = \"vn_operator_LOCALSECRET\"\n\n[profiles.peer]\nhub = \"http://127.0.0.1:45752\"\nsecret = \"vn_peer_SECRET\"\n\n[profiles.fly]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_FLYSECRET\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setOperatorProfile(path, "fly"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, "operator_profile = \"operator\"", "operator_profile = \"fly\"", 1)
	if string(got) != want {
		t.Fatalf("rewrite changed more than the assignment\n got %q\nwant %q", got, want)
	}
	if strings.Contains(string(got), "FLYSECRET") && strings.Count(string(got), "vn_operator_FLYSECRET") != 1 {
		t.Fatal("secret bytes moved")
	}

	bare := filepath.Join(dir, "missing.toml")
	if err := setOperatorProfile(bare, "operator"); err == nil {
		t.Fatal("missing file installed a dangling operator_profile")
	}
	if _, err := os.Lstat(bare); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file was created: %v", err)
	}

	crlf := filepath.Join(dir, "crlf.toml")
	body := "peer = 1\r\n"
	if err := os.WriteFile(crlf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setOperatorProfile(crlf, "operator"); err == nil {
		t.Fatal("a file with no profiles installed a dangling operator_profile")
	}
	got, err = os.ReadFile(crlf)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatal("file with no profiles was rewritten")
	}
	if err := setOperatorProfile(path, "not a name"); err == nil {
		t.Fatal("a bad profile name was written")
	}
}

func TestOperatorProfilesHideSecrets(t *testing.T) {
	c := Config{Profiles: map[string]Profile{
		"peer":     {Hub: "http://10.0.0.1:1", Secret: "vn_peer_SECRET"},
		"operator": {Hub: "http://127.0.0.1:45752", Secret: "  vn_operator_LOCALSECRET  "},
		"fly":      {Hub: "https://vecnet.fly.dev", Secret: "vn_operator_FLYSECRET"},
	}}
	got := operatorProfiles(c, false)
	text := strings.ToLower(sprintfChoices(got))
	if strings.Contains(text, "secret") || strings.Contains(text, "vn_") {
		t.Fatalf("choices leaked a credential: %#v", got)
	}
	if len(got) != 2 || got[0].Name != "fly" || got[1].Name != "operator" {
		t.Fatalf("choices = %#v", got)
	}
	if got[1].Hub != "http://127.0.0.1:45752" {
		t.Fatalf("hub = %q", got[1].Hub)
	}
}

func TestChooseTUIProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "# keep\n\n[profiles.operator]\nhub = \"http://127.0.0.1:45752\"\nsecret = \"vn_operator_LOCALSECRET\"\n\n[profiles.peer]\nsecret = \"vn_peer_SECRET\"\nhub = \"http://127.0.0.1:45752\"\n\n[profiles.fly]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_FLYSECRET\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "VECNET_CONFIG":
			return path
		case "VECNET_HUMAN":
			return "1"
		default:
			return ""
		}
	}
	restore := pickOperator
	t.Cleanup(func() { pickOperator = restore })

	t.Run("cancel writes nothing", func(t *testing.T) {
		a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: getenv}
		pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
			return tui.PickResult{Cancelled: true}, nil
		}
		err := a.chooseTUIProfile(true)
		if !errors.Is(err, errTUICancel) || a.net != "" {
			t.Fatalf("cancel: net %q err %v", a.net, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != original {
			t.Fatal("cancel rewrote the file")
		}
	})
	t.Run("n does not write", func(t *testing.T) {
		a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: getenv}
		pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
			return tui.PickResult{Name: "fly"}, nil
		}
		if err := a.chooseTUIProfile(true); err != nil || a.net != "fly" {
			t.Fatalf("n: net %q err %v", a.net, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != original {
			t.Fatal("n rewrote the file")
		}
	})
	t.Run("y rewrites only operator_profile", func(t *testing.T) {
		a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: getenv}
		pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
			return tui.PickResult{Name: "fly", MakeDefault: true}, nil
		}
		if err := a.chooseTUIProfile(false); err != nil || a.net != "fly" {
			t.Fatalf("y: net %q err %v", a.net, err)
		}
		got, _ := os.ReadFile(path)
		want := "operator_profile = \"fly\"\n" + original
		if string(got) != want {
			t.Fatalf("y rewrite\n got %q\nwant %q", got, want)
		}
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("set operator_profile skips the picker", func(t *testing.T) {
		body := "operator_profile = \"peer\"\n" + original
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: getenv}
		called := false
		pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
			called = true
			return tui.PickResult{}, nil
		}
		if err := a.chooseTUIProfile(false); err != nil || called || a.net != "" {
			t.Fatalf("skip: called %v net %q err %v", called, a.net, err)
		}
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("agent bare run does not pick", func(t *testing.T) {
		agentEnv := func(k string) string {
			if k == "VECNET_CONFIG" {
				return path
			}
			if k == "VECNET_AGENT" {
				return "1"
			}
			return ""
		}
		a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: agentEnv}
		called := false
		pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
			called = true
			return tui.PickResult{}, nil
		}
		err := a.chooseTUIProfile(false)
		if err == nil || called {
			t.Fatalf("agent: called %v err %v", called, err)
		}
		var ue *usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "agent") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("no operator profile", func(t *testing.T) {
		only := filepath.Join(dir, "peer.toml")
		if err := os.WriteFile(only, []byte("secret = \"vn_peer_SECRET\"\n[profiles.peer]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_peer_SECRET\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := func(k string) string {
			if k == "VECNET_CONFIG" {
				return only
			}
			if k == "VECNET_HUMAN" {
				return "1"
			}
			return ""
		}
		a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: env}
		err := a.chooseTUIProfile(true)
		if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "vn_peer_") {
			t.Fatalf("err %v", err)
		}
	})
}

func TestChooseTUIProfileRedactsMalformedConfigError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "operator_profile = \"fly\"\n\n[profiles.fly]\nhub = \"https://hub.example\"\nsecret = \"vn_operator_OK\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "vn_operator_SUPERSECRETCREDENTIAL99"
	// A credential followed by a broken Unicode escape. The pinned TOML
	// decoder quotes the whole string it was lexing, which is this secret.
	malformed := "operator_profile = \"fly\"\n\n[profiles.fly]\nhub = \"https://hub.example\"\nsecret = \"" + secret + "\\uZZZZ\"\n"
	getenv := func(k string) string {
		if k == "VECNET_CONFIG" {
			return path
		}
		if k == "VECNET_HUMAN" {
			return "1"
		}
		return ""
	}
	restore := pickOperator
	t.Cleanup(func() { pickOperator = restore })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: stdout, stderr: stderr, getenv: getenv}
	pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
		if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
			return tui.PickResult{}, err
		}
		return tui.PickResult{Name: "fly", MakeDefault: true}, nil
	}
	err := a.chooseTUIProfile(true)
	if err == nil {
		t.Fatal("malformed config was accepted")
	}
	if a.net != "" {
		t.Fatal("a vanished config still selected a profile")
	}
	a.exit(err)
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != malformed {
		t.Fatal("malformed config was rewritten")
	}
	for _, stream := range []struct {
		name string
		text string
	}{
		{"error", err.Error()},
		{"stdout", stdout.String()},
		{"stderr", stderr.String()},
		{"log", logs.String()},
	} {
		if strings.Contains(stream.text, secret) || strings.Contains(stream.text, "\\uZZZZ") || strings.Contains(stream.text, "SUPERSECRET") {
			t.Fatalf("%s contains credential bytes", stream.name)
		}
	}
	// operator_profile, a blank line, the table, the hub, then the secret.
	if err.Error() != "config.toml: parse error at line 5" {
		t.Fatalf("redacted form = %q", strings.ReplaceAll(err.Error(), secret, "REDACTED"))
	}
}

func sprintfChoices(in []tui.ProfileChoice) string {
	var b strings.Builder
	for _, c := range in {
		b.WriteString(c.Name)
		b.WriteByte(' ')
		b.WriteString(c.Hub)
		b.WriteByte('\n')
	}
	return b.String()
}
