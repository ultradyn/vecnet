package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/XertroV/vecnet/internal/tui"
)

func TestOperatorProfileValueSpan(t *testing.T) {
	fly := "[profiles.fly]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_FLY\"\n"
	cases := []struct {
		name    string
		body    string
		profile string
		want    string
	}{
		{
			name:    "inline comment",
			body:    "operator_profile = \"old\" # do not drop\n" + fly,
			profile: "fly",
			want:    "operator_profile = \"fly\" # do not drop\n" + fly,
		},
		{
			name:    "quoted key",
			body:    "\"operator_profile\" = 'old' # keep\n" + fly,
			profile: "fly",
			want:    "\"operator_profile\" = \"fly\" # keep\n" + fly,
		},
		{
			name:    "crlf",
			body:    "operator_profile = \"old\" # c\r\nsecret = \"stay\"\r\n" + strings.ReplaceAll(fly, "\n", "\r\n"),
			profile: "fly",
			want:    "operator_profile = \"fly\" # c\r\nsecret = \"stay\"\r\n" + strings.ReplaceAll(fly, "\n", "\r\n"),
		},
		{
			name:    "multiline string",
			body:    "operator_profile = \"\"\"\nold\nname\n\"\"\" # tail\npeer = 1\n" + fly,
			profile: "fly",
			want:    "operator_profile = \"fly\" # tail\npeer = 1\n" + fly,
		},
		{
			name:    "nested key stays",
			body:    "operator_profile = \"old\"\n[tool]\noperator_profile = \"nested\"\n" + fly,
			profile: "fly",
			want:    "operator_profile = \"fly\"\n[tool]\noperator_profile = \"nested\"\n" + fly,
		},
		{
			name:    "inline table elsewhere",
			body:    "opts = { operator_profile = \"no\", n = 1 } # c\noperator_profile = \"old\"\n" + fly,
			profile: "fly",
			want:    "opts = { operator_profile = \"no\", n = 1 } # c\noperator_profile = \"fly\"\n" + fly,
		},
		{
			name:    "quoted profile name",
			body:    "title = \"keep\"\n[profiles.\"fly.dev\"]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_FLY\"\n",
			profile: "fly.dev",
			want:    "operator_profile = \"fly.dev\"\ntitle = \"keep\"\n[profiles.\"fly.dev\"]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_FLY\"\n",
		},
		{
			name:    "underscore name",
			body:    "[profiles._op]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_operator_X\"\n",
			profile: "_op",
			want:    "operator_profile = \"_op\"\n[profiles._op]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_operator_X\"\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := cliFixtureWriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := setOperatorProfile(path, tc.profile); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("bytes\n got %q\nwant %q", got, tc.want)
			}
			cliFixtureRequireMode(t, path, 0o600)
			var cfg Config
			if _, err := toml.Decode(string(got), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.OperatorProfile != tc.profile {
				t.Fatalf("decoded %q", cfg.OperatorProfile)
			}
		})
	}
}

func TestOperatorProfileRefusesUnsafeForms(t *testing.T) {
	fly := "[profiles.fly]\nhub = \"https://h\"\nsecret = \"vn_operator_FLY\"\n"
	cases := []string{
		"operator_profile = { name = \"old\" } # c\n" + fly,
		"operator_profile = [\"old\"]\n" + fly,
		"operator_profile.name = \"old\"\n" + fly,
	}
	for _, body := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := cliFixtureWriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := setOperatorProfile(path, "fly"); err == nil {
			t.Fatal("unsupported form was written")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Fatalf("file changed\n got %q\nwant %q", got, body)
		}
	}
}

func TestOperatorProfileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.toml")
	body := "operator_profile = \"old\"\n[profiles.fly]\nhub = \"https://h\"\nsecret = \"vn_operator_FLY\"\n"
	if err := cliFixtureWriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := cliFixtureSymlink(t, target, link); err != nil {
		t.Fatal(err)
	}
	err := setOperatorProfile(link, "fly")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("target changed: %q", got)
	}
	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Fatalf("link = %q err %v", dest, err)
	}
}

func TestOperatorProfileConcurrentPreservesEnrolment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "operator_profile = \"old\"\n\n[profiles.fly]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_OLD\"\npeer_id = \"peer_old\"\n"
	if err := cliFixtureWriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	updated := "operator_profile = \"old\"\n\n[profiles.fly]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_NEW\"\npeer_id = \"peer_new\"\n[profiles.extra]\nhub = \"https://enrolled.example\"\nsecret = \"vn_peer_ENROL\"\n"
	errc := make(chan error, 1)
	go func() { errc <- setOperatorProfile(path, "fly") }()
	time.Sleep(30 * time.Millisecond)
	if err := cliFixtureWriteFile(path, []byte(updated), 0o600); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"vn_operator_NEW", "peer_new", "vn_peer_ENROL", "operator_profile = \"fly\""} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Fatalf("missing %s\n%s", keep, got)
		}
	}
	for _, drop := range []string{"vn_operator_OLD", "peer_old"} {
		if bytes.Contains(got, []byte(drop)) {
			t.Fatalf("stale %s\n%s", drop, got)
		}
	}
}

func TestOperatorProfileRefusesMissingProfileUnderLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "operator_profile = \"old\"\n[profiles.fly]\nhub = \"https://h\"\nsecret = \"vn_operator_FLY\"\n"
	if err := cliFixtureWriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	updated := "[profiles.other]\nhub = \"https://h\"\nsecret = \"vn_operator_OTHER\"\n"
	errc := make(chan error, 1)
	go func() { errc <- setOperatorProfile(path, "fly") }()
	time.Sleep(30 * time.Millisecond)
	if err := cliFixtureWriteFile(path, []byte(updated), 0o600); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err := <-errc; err == nil {
		t.Fatal("removed profile was still written")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != updated {
		t.Fatalf("file\n got %q\nwant %q", got, updated)
	}

	// An empty profile map, a zero-byte file, and a removed file used to skip
	// the membership check and install the name the picker still held.
	for _, tc := range []struct {
		name    string
		body    *string
		missing bool
	}{
		{name: "empty map", body: strPtr("title = \"keep\"\n")},
		{name: "zero byte", body: strPtr("")},
		{name: "removed", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			original := "operator_profile = \"old\"\n[profiles.fly]\nhub = \"https://h\"\nsecret = \"vn_operator_FLY\"\n"
			if err := cliFixtureWriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			unlock, err := lockPath(ctx, path+".lock")
			if err != nil {
				t.Fatal(err)
			}
			errc := make(chan error, 1)
			go func() { errc <- setOperatorProfile(path, "fly") }()
			time.Sleep(30 * time.Millisecond)
			if tc.missing {
				if err := os.Remove(path); err != nil {
					unlock()
					t.Fatal(err)
				}
			} else if err := cliFixtureWriteFile(path, []byte(*tc.body), 0o600); err != nil {
				unlock()
				t.Fatal(err)
			}
			unlock()
			err = <-errc
			if err == nil {
				t.Fatal("dangling operator_profile was written")
			}
			if strings.Contains(err.Error(), "vn_operator") || strings.Contains(err.Error(), "secret") {
				t.Fatal("refusal quoted the config")
			}
			if tc.missing {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("removed file came back: %v", err)
				}
				return
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != *tc.body {
				t.Fatal("latest file state was replaced")
			}
			if bytes.Contains(got, []byte("operator_profile")) && *tc.body == "" {
				t.Fatal("zero-byte file gained an operator_profile")
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestCommitOperatorProfileRefusesStaleBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := []byte("operator_profile = \"old\"\nsecret = \"one\"\n")
	if err := cliFixtureWriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("operator_profile = \"old\"\nsecret = \"two\"\n")
	if err := cliFixtureWriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	err = commitOperatorProfile(path, info, raw, []byte("operator_profile = \"fly\"\nsecret = \"one\"\n"))
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("err %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, changed) {
		t.Fatalf("stale write landed: %q", got)
	}
}

func TestChooseQuotedAndUnderscoreProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "# keep\n[profiles._op]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_operator_X\"\n[profiles.\"fly.dev\"]\nhub = \"https://vecnet.fly.dev\"\nsecret = \"vn_operator_Y\"\n"
	if err := cliFixtureWriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
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

	for _, name := range []string{"_op", "fly.dev"} {
		t.Run("n "+name, func(t *testing.T) {
			if err := cliFixtureWriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: getenv}
			pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
				return tui.PickResult{Name: name}, nil
			}
			if err := a.chooseTUIProfile(true); err != nil || a.net != name {
				t.Fatalf("n %s: net %q err %v", name, a.net, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Fatalf("n rewrote the file: %q", got)
			}
		})
		t.Run("y "+name, func(t *testing.T) {
			if err := cliFixtureWriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: getenv}
			pickOperator = func(context.Context, []tui.ProfileChoice, io.Reader, io.Writer) (tui.PickResult, error) {
				return tui.PickResult{Name: name, MakeDefault: true}, nil
			}
			if err := a.chooseTUIProfile(true); err != nil || a.net != name {
				t.Fatalf("y %s: net %q err %v", name, a.net, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "operator_profile = " + strconvQuote(name) + "\n" + original
			if string(got) != want {
				t.Fatalf("y rewrite\n got %q\nwant %q", got, want)
			}
			var cfg Config
			if _, err := toml.Decode(string(got), &cfg); err != nil || cfg.OperatorProfile != name {
				t.Fatalf("decoded %q err %v", cfg.OperatorProfile, err)
			}
		})
	}
}

func strconvQuote(s string) string {
	quoted, err := profileString(s)
	if err != nil {
		return ""
	}
	return quoted
}
