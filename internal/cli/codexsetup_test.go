package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupRun(f *bindFixture, args ...string) (string, string, error) {
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdout: &out, stderr: &errs, getenv: func(k string) string { return f.env[k] }}
	err := a.cmdCodexSetup(args, func() time.Time { return f.now })
	return out.String(), errs.String(), err
}

func TestCodexSetupDryRunDoesNotBind(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.server.Close() // dry-run must not authenticate or infer a thread remotely
	settings := filepath.Join(t.TempDir(), "hooks.json")
	out, _, err := setupRun(f, "--dry-run", "--settings", settings, "--bin", "vecnet")
	if err != nil || !strings.Contains(out, "--client codex") || !strings.Contains(out, bindT1) {
		t.Fatalf("%s: %v", out, err)
	}
	for _, path := range []string{f.path, settings, f.path + ".lock"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run touched %s: %v", path, err)
		}
	}
}

func TestCodexSetupRequiresThreadAndExistingProfile(t *testing.T) {
	f := newBindFixture(t)
	for _, args := range [][]string{{"--dry-run"}, {"--thread", "latest", "--dry-run"}} {
		if _, _, err := setupRun(f, args...); err == nil {
			t.Fatal("accepted missing/invalid thread")
		}
	}
	f.env["VECNET_PROFILE"] = "missing"
	if _, _, err := setupRun(f, "--thread", bindT1, "--dry-run"); err == nil {
		t.Fatal("accepted unknown profile")
	}
}

func TestCodexSetupExplicitThreadAndUnavailable(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT2
	settings := filepath.Join(t.TempDir(), "hooks.json")
	out, errs, err := setupRun(f, "--thread", bindT1, "--settings", settings, "--bin", "vecnet")
	if err != nil || !strings.Contains(out, "unavailable") || strings.Contains(out, "delivery ready") || !strings.Contains(errs, "/hooks") {
		t.Fatalf("out=%s errs=%s err=%v", out, errs, err)
	}
	if got := bindValue[string](t, f.entries()[0], "thread_id"); got != bindT1 {
		t.Fatal(got)
	}
	raw, err := os.ReadFile(settings)
	if err != nil || bytes.Contains(raw, []byte("--profile")) {
		t.Fatalf("%s: %v", raw, err)
	}
}

func TestCodexSetupReadinessRequiresFreshExactOwner(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	var bindings codexBindingsFile
	if err := json.Unmarshal(f.raw(), &bindings); err != nil {
		t.Fatal(err)
	}
	b := bindings.Bindings[0]
	settings := filepath.Join(t.TempDir(), "hooks.json")
	for _, tc := range []struct {
		name, field string
		value       any
		ready       bool
	}{
		{name: "ready", ready: true},
		{name: "boundary", field: "updated_at", value: f.now.Add(-30 * time.Second).Format(time.RFC3339), ready: true},
		{name: "refused_version", field: "state", value: "refused_version"},
		{name: "stale", field: "updated_at", value: f.now.Add(-31 * time.Second).Format(time.RFC3339)},
		{name: "future", field: "updated_at", value: f.now.Add(time.Second).Format(time.RFC3339)},
		{name: "generation", field: "generation", value: strings.Repeat("f", 32)},
		{name: "namespace", field: "config_path", value: filepath.Join(t.TempDir(), "other.toml")},
		{name: "profile", field: "profile", value: "q"},
		{name: "as", field: "as", value: "peer_other"},
		{name: "home", field: "codex_home", value: filepath.Join(t.TempDir(), "other-home")},
		{name: "thread", field: "thread_id", value: bindT2},
		{name: "dead", field: "liveness", value: map[string]any{"live": false}},
		{name: "unsupported", field: "state", value: "unsupported"},
		{name: "paused", field: "state", value: "paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"config_path": b.ConfigPath, "profile": b.Profile, "as": b.As, "codex_home": b.CodexHome, "thread_id": b.ThreadID, "generation": b.Generation, "state": "ready", "liveness": map[string]any{"live": true}}
			status := map[string]any{"version": 1, "updated_at": f.now.Format(time.RFC3339), "bindings": []any{row}}
			if tc.field == "updated_at" {
				status[tc.field] = tc.value
			} else if tc.field != "" {
				row[tc.field] = tc.value
			}
			raw, _ := json.Marshal(status)
			if err := cliFixtureWriteFile(f.path+".status.json", raw, 0600); err != nil {
				t.Fatal(err)
			}
			out, _, err := setupRun(f, "--thread", bindT1, "--settings", settings, "--bin", "vecnet")
			if err != nil || strings.Contains(out, "delivery ready") != tc.ready {
				t.Fatalf("%s: %v", out, err)
			}
			if (tc.name == "unsupported" || tc.name == "refused_version") && !strings.Contains(out, "unsupported") {
				t.Fatal(out)
			}
		})
	}
}

func setupStatus(t *testing.T, f *bindFixture, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	a := &app{ctx: context.Background(), stdout: &out, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
	err := a.cmdCodexStatus(args, func() time.Time { return f.now })
	return out.String(), err
}

func writeSetupStatus(t *testing.T, f *bindFixture) {
	t.Helper()
	var file codexBindingsFile
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		t.Fatal(err)
	}
	b := file.Bindings[0]
	raw, err := json.Marshal(map[string]any{"version": 1, "updated_at": f.now.Format(time.RFC3339), "bindings": []any{map[string]any{
		"config_path": b.ConfigPath, "profile": b.Profile, "as": b.As, "thread_id": b.ThreadID, "codex_home": b.CodexHome, "generation": b.Generation, "state": "ready", "liveness": map[string]bool{"live": true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(f.path+".status.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSetupStatusPrivateAndReadOnly(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	out, err := setupStatus(t, f)
	if err != nil || !strings.Contains(out, "unavailable") {
		t.Fatalf("%s: %v", out, err)
	}
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatal("status created authority", err)
	}
	f.bind("p", bindT1)
	writeSetupStatus(t, f)
	original := f.raw()
	out, err = setupStatus(t, f)
	if err != nil || !strings.Contains(out, "delivery ready") {
		t.Fatalf("%s: %v", out, err)
	}
	for _, path := range []string{f.path, f.path + ".status.json"} {
		if err := cliFixtureChmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		out, err = setupStatus(t, f)
		if err != nil || strings.Contains(out, "delivery ready") {
			t.Fatalf("nonprivate %s: %s %v", path, out, err)
		}
		if err := cliFixtureChmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(original, f.raw()) {
		t.Fatal("status changed authority")
	}
	f.now = f.now.Add(25 * time.Hour)
	out, err = setupStatus(t, f)
	if err != nil || strings.Contains(out, "delivery ready") {
		t.Fatalf("expired: %s %v", out, err)
	}
}

func TestCodexSetupStatusOtherNamespace(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.bind("p", bindT1)
	writeSetupStatus(t, f)
	var file codexBindingsFile
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		t.Fatal(err)
	}
	other := file.Bindings[0]
	other.ConfigPath = filepath.Join(t.TempDir(), "other.toml")
	if err := cliFixtureWriteFile(other.ConfigPath, []byte("# another config namespace\n"), 0600); err != nil {
		t.Fatal(err)
	}
	other.Caller = "peer_other"
	other.ThreadID = bindT2
	other.Generation = strings.Repeat("a", 32)
	file.Bindings = append(file.Bindings, other)
	raw, _ := json.Marshal(file)
	if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := setupStatus(t, f)
	if err != nil || !strings.Contains(out, "delivery ready") {
		t.Fatalf("%s: %v", out, err)
	}
	file.Bindings = file.Bindings[1:]
	raw, _ = json.Marshal(file)
	if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err = setupStatus(t, f)
	if err != nil || strings.Contains(out, "delivery ready") {
		t.Fatalf("foreign namespace: %s: %v", out, err)
	}
}

func TestCodexSetupStatusRejectsInvalidAndDuplicateReports(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.bind("p", bindT1)
	writeSetupStatus(t, f)
	raw, err := os.ReadFile(f.path + ".status.json")
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	rows := status["bindings"].([]any)
	status["bindings"] = append(rows, rows[0])
	duplicate, _ := json.Marshal(status)
	for _, src := range [][]byte{[]byte(`{"version":1,"version":1}`), []byte(`{"version":2}`), []byte(`{"version":1,"updated_at":"bad"}`), duplicate} {
		if err := cliFixtureWriteFile(f.path+".status.json", src, 0600); err != nil {
			t.Fatal(err)
		}
		out, err := setupStatus(t, f)
		if err != nil || strings.Contains(out, "delivery ready") {
			t.Fatalf("%s: %v", out, err)
		}
	}
}

func TestCodexSetupStatusRejectsReplacedIdentity(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.bind("p", bindT1)
	writeSetupStatus(t, f)
	f.peer.Store("peer_replacement")
	out, err := setupStatus(t, f)
	if err != nil || strings.Contains(out, "delivery ready") || !strings.Contains(out, "identity changed") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestCodexSetupDispatch(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	settings := filepath.Join(t.TempDir(), "hooks.json")
	for _, args := range [][]string{
		{"--net", "p", "codex", "setup", "--dry-run", "--settings", settings, "--bin", "vecnet"},
		{"--net", "p", "codex", "status", "--json"},
	} {
		var out, errs bytes.Buffer
		code := Run(args, strings.NewReader(""), &out, &errs, func(k string) string { return f.env[k] })
		if code != 0 {
			t.Fatalf("%v exit %d: %s", args, code, &errs)
		}
		if out.Len() == 0 {
			t.Fatalf("%v produced no result", args)
		}
	}
	for _, path := range []string{f.path, settings} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("read-only command touched %s: %v", path, err)
		}
	}
}

func TestCodexSetupLegacyAuthorityRefusesWritePreservesRows(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	path := filepath.Join(filepath.Dir(f.env["VECNET_CONFIG"]), "codex", "bindings.json")
	legacy := []byte(`{"version":1,"bindings":[{"profile":"p","thread_id":"old"},{"profile":"unrelated","thread_id":"keep"}]}`)
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "hooks.json")
	out, errs, err := setupRun(f, "--dry-run", "--settings", settings, "--bin", "vecnet")
	if err != nil || !strings.Contains(errs, path) || !strings.Contains(strings.ToLower(errs), "stop") || !strings.Contains(out, "--client codex") {
		t.Fatalf("preview out=%s errs=%s err=%v", out, errs, err)
	}
	_, errs, err = setupRun(f, "--settings", settings, "--bin", "vecnet")
	if err == nil || !strings.Contains(err.Error(), "legacy") || !strings.Contains(errs, path) {
		t.Fatalf("errs=%s err=%v", errs, err)
	}
	for _, p := range []string{settings, f.path} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("legacy setup touched %s: %v", p, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, legacy) {
		t.Fatalf("legacy modified %s %v", after, err)
	}
}

func TestCodexSetupLegacyStatusBlocksReady(t *testing.T) {
	for _, kind := range []string{"rows", "empty", "corrupt", "directory", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			f := newBindFixture(t)
			f.env["CODEX_THREAD_ID"] = bindT1
			f.bind("p", bindT1)
			writeSetupStatus(t, f)
			path := filepath.Join(filepath.Dir(f.env["VECNET_CONFIG"]), "codex", "bindings.json")
			if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "directory":
				err = cliFixtureMkdir(path, 0700)
			case "dangling":
				err = cliFixtureSymlink(t, filepath.Join(t.TempDir(), "missing"), path)
			case "corrupt":
				err = cliFixtureWriteFile(path, []byte("bad"), 0600)
			case "empty":
				err = cliFixtureWriteFile(path, []byte(`{"version":1,"bindings":[]}`), 0600)
			default:
				err = cliFixtureWriteFile(path, []byte(`{"version":1,"bindings":[{"profile":"other"}]}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			a := &app{ctx: context.Background(), stdout: &out, stderr: &errs, json: true, getenv: func(k string) string { return f.env[k] }}
			if err := a.cmdCodexStatus(nil, func() time.Time { return f.now }); err != nil {
				t.Fatal(err)
			}
			var report struct {
				Enrolled            bool `json:"enrolled"`
				Delivery, Migration string
				Legacy              []string `json:"legacy_bindings"`
			}
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if !report.Enrolled || report.Delivery == "ready" || report.Migration != "legacy_unresolved" || len(report.Legacy) != 1 || report.Legacy[0] != path {
				t.Fatalf("%s", &out)
			}
			if !strings.Contains(strings.ToLower(errs.String()), "stop") || !strings.Contains(errs.String(), path) {
				t.Fatal(&errs)
			}
		})
	}
}

func TestCodexSetupLegacyDetectsOriginalConfigSymlinkDirectory(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	dir := t.TempDir()
	alias := filepath.Join(dir, "config.toml")
	if err := cliFixtureSymlink(t, f.env["VECNET_CONFIG"], alias); err != nil {
		t.Fatal(err)
	}
	f.env["VECNET_CONFIG"] = alias
	path := filepath.Join(dir, "codex", "bindings.json")
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	_, errs, err := setupRun(f, "--dry-run", "--settings", filepath.Join(t.TempDir(), "hooks.json"))
	if err != nil || !strings.Contains(errs, path) {
		t.Fatalf("%s %v", errs, err)
	}
}

func TestCodexSetupMissingConnectorActionable(t *testing.T) {
	f := newBindFixture(t)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.bind("p", bindT1)
	out, err := setupStatus(t, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"enrollment:", "Enrollment automatically", "vecnet repair", "Node >=22.18", "Linux user systemd", "docs/connectors/codex-appserver.md", "same HOME", "/hooks"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}

func TestCodexSetupLegacyConventionalHomesAndAuthorityAlias(t *testing.T) {
	for _, kind := range []string{"xdg", "home", "authority-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := newBindFixture(t)
			f.env["CODEX_THREAD_ID"] = bindT1
			f.bind("p", bindT1)
			writeSetupStatus(t, f)
			path := filepath.Join(f.env["XDG_CONFIG_HOME"], "vecnet", "codex", "bindings.json")
			if kind == "home" {
				path = filepath.Join(f.env["HOME"], ".config", "vecnet", "codex", "bindings.json")
			}
			if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "authority-alias" {
				err = cliFixtureSymlink(t, f.path, path)
			} else {
				err = cliFixtureWriteFile(path, []byte(`{"version":1,"bindings":[]}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := setupStatus(t, f)
			if err != nil || strings.Contains(out, "delivery ready") || !strings.Contains(out, "legacy connector") {
				t.Fatalf("%s %v", out, err)
			}
			// Simulate the operator's retirement after stopping/upgrading old processes;
			// setup itself never archives or deletes this source.
			if err := os.Rename(path, path+".retired"); err != nil {
				t.Fatal(err)
			}
			out, err = setupStatus(t, f)
			if err != nil || !strings.Contains(out, "delivery ready") {
				t.Fatalf("retired: %s %v", out, err)
			}
		})
	}
}

func TestCodexSetupLivenessReasonWhitelist(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	writeSetupStatus(t, f)
	var file codexBindingsFile
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		t.Fatal(err)
	}
	path := f.path + ".status.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	row := status["bindings"].([]any)[0].(map[string]any)
	for _, reason := range []string{"expired", "archived", "not_loaded", "idle_stale", "approval_policy", "waiting_approval", "waiting_input", "over_limit", "identity_changed", "ownership_changed", "unverified_thread", "identity_unverified", "watch_stopped", "delivery_unresolved", "", "unexpected-private-detail\n\x1b[31m"} {
		row["liveness"] = map[string]any{"live": false, "reason": reason}
		raw, _ := json.Marshal(status)
		if err := cliFixtureWriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		delivery, got := codexSetupReadiness(path, file.Bindings[0], f.now)
		if delivery != "unavailable" {
			t.Fatal(delivery)
		}
		if reason != "" && !strings.HasPrefix(reason, "unexpected") {
			if got != "exact owner is not live: "+reason {
				t.Fatalf("reason %s: %s", reason, got)
			}
		} else if strings.Contains(got, "unexpected") || strings.Contains(got, "\x1b") {
			t.Fatalf("raw reason leaked: %q", got)
		}
	}
}
