package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type launcherFixture struct {
	env      map[string]string
	paths    launcherPaths
	setup    []string
	original []byte
}

func newLauncherFixture(t *testing.T, kind string) launcherFixture {
	t.Helper()
	dir := t.TempDir()
	// testing.TempDir's numbered child uses 0777 before umask. Our fixture is
	// explicitly a private HOME, independent of the caller's cooperative umask.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer vn_peer_LAUNCHER_TEST" {
			t.Errorf("unexpected launcher request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/peers/me":
			fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_launcher"}`)
		case "/v1/peers":
			fmt.Fprintf(w, `{"peers":[{"peer_id":"peer_launcher","kind":%q}]}`, kind)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"HOME": dir, "VECNET_CONFIG": filepath.Join(dir, "config", "config.toml"), "XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_DATA_HOME": filepath.Join(dir, "data"), "XDG_STATE_HOME": filepath.Join(dir, "state"), "PATH": os.Getenv("PATH")}
	if err := saveConfig(env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{"launcher/test": {Hub: srv.URL, Secret: "vn_peer_LAUNCHER_TEST", PeerID: "peer_launcher"}}}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo with spaces")
	if err = os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"}} {
		if out, e := exec.Command("git", args...).CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	home := filepath.Join(dir, "codex")
	if err = os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte("model_provider = 'existing-route'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{net: "launcher/test", getenv: func(k string) string { return env[k] }}
	paths, err := a.launcherPaths()
	if err != nil {
		t.Fatal(err)
	}
	return launcherFixture{env, paths, []string{"setup", "--project", "project-one", "--repo", repo, "--base-ref", "HEAD", "--codex-home", home}, original}
}

func (f launcherFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), append([]string{"--net", "launcher/test", "launcher"}, args...), strings.NewReader(""), &out, &errs, func(k string) string { return f.env[k] })
	return code, out.String(), errs.String()
}

func TestLauncherSetupPreviewAndWrite(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	code, out, errs := f.run(t, f.setup...)
	if code != 0 {
		t.Fatalf("preview %d %s", code, errs)
	}
	if strings.Contains(out, "vn_peer_LAUNCHER_TEST") {
		t.Fatal("credential leaked in preview")
	}
	if _, err := os.Stat(f.paths.Config); !os.IsNotExist(err) {
		t.Fatal("preview wrote config")
	}
	args := append(append([]string{}, f.setup...), "--model", "existing/model", "--effort", "high", "--write")
	code, out, errs = f.run(t, args...)
	if code != 0 {
		t.Fatalf("setup %d %s", code, errs)
	}
	if strings.Contains(out+errs, "vn_peer_LAUNCHER_TEST") {
		t.Fatal("credential leaked")
	}
	for _, path := range []string{f.paths.Config, f.paths.Credential} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("private file %s: %v %v", path, st, err)
		}
	}
	secret, err := os.ReadFile(f.paths.Credential)
	if err != nil || string(secret) != "vn_peer_LAUNCHER_TEST\n" {
		t.Fatal("wrong credential")
	}
	var config struct {
		Backends []struct {
			ID     string `json:"id"`
			Config struct {
				Home    string                       `json:"codex_home"`
				Presets map[string]map[string]string `json:"presets"`
			} `json:"config"`
		} `json:"backends"`
	}
	if err = launcherReadJSON(f.paths.Config, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Backends) != 1 || config.Backends[0].ID != "codex-app-server" || !reflect.DeepEqual(config.Backends[0].Config.Presets["default"], map[string]string{"model": "existing/model", "effort": "high"}) {
		t.Fatalf("wrong native backend: %+v", config)
	}
	if got, _ := os.ReadFile(f.env["VECNET_CONFIG"]); !bytes.Equal(got, f.original) {
		t.Fatal("setup rewrote enrolled profile")
	}
	if got, _ := os.ReadFile(filepath.Join(config.Backends[0].Config.Home, "config.toml")); string(got) != "model_provider = 'existing-route'\n" {
		t.Fatal("setup rewrote existing route")
	}
	code, _, errs = f.run(t, args...)
	if code == 0 || !strings.Contains(errs, "already exists") {
		t.Fatal("setup replaced existing config without force")
	}
	code, _, errs = f.run(t, append(args, "--force")...)
	if code != 0 {
		t.Fatalf("explicit replacement %s", errs)
	}
}

func TestLauncherSetupRefusesConversationalPeer(t *testing.T) {
	f := newLauncherFixture(t, "codex")
	code, _, errs := f.run(t, append(f.setup, "--write")...)
	if code == 0 || !strings.Contains(errs, "kind launcher") {
		t.Fatalf("%d %s", code, errs)
	}
	if _, err := os.Stat(f.paths.Config); !os.IsNotExist(err) {
		t.Fatal("refusal wrote config")
	}
}

func TestLauncherSetupRejectsInvalidEffort(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	args := append(append([]string{}, f.setup...), "--effort", "typo", "--write")
	code, _, errs := f.run(t, args...)
	if code == 0 || !strings.Contains(errs, "--effort must be") {
		t.Fatalf("%d %s", code, errs)
	}
	if _, err := os.Stat(f.paths.Config); !os.IsNotExist(err) {
		t.Fatal("invalid effort wrote config")
	}
}

func TestLauncherSetupTemporaryProjectKeepsScratchDistinct(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	args := append([]string{}, f.setup...)
	for i, arg := range args {
		if arg == "--project" {
			args[i+1] = "temporary"
		}
	}
	code, out, errs := f.run(t, args...)
	if code != 0 {
		t.Fatal(errs)
	}
	var plan struct {
		Config struct {
			Projects map[string]struct {
				Repo    string `json:"repo_root"`
				Scratch bool   `json:"scratch"`
			} `json:"projects"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	ordinary, ok := plan.Config.Projects["temporary"]
	if !ok || ordinary.Scratch || ordinary.Repo == f.paths.Data {
		t.Fatal("ordinary temporary alias became scratch")
	}
	scratch, ok := plan.Config.Projects["vecnet-scratch"]
	if !ok || !scratch.Scratch || scratch.Repo != f.paths.Data {
		t.Fatal("missing distinct generated scratch alias")
	}
}

func launcherTestSource(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkout")
	template, err := os.ReadFile("../../connectors/launcher/systemd/vecnet-launcher-profile.service.in")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"connectors/launcher/src/main.ts": "export {};\n", "connectors/launcher/src/backends/codex-app-server/index.ts": "export {};\n", "connectors/hub-client/src/client.ts": "export {};\n", "connectors/launcher/schema/test.json": "{}", "connectors/launcher/package.json": `{"type":"module"}`, "connectors/hub-client/package.json": `{"type":"module"}`, "connectors/launcher/systemd/vecnet-launcher-profile.service.in": string(template)}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLauncherInstallSnapshotAndExplicitServiceActions(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	if code, _, errs := f.run(t, append(f.setup, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	source := launcherTestSource(t)
	binDir := filepath.Join(t.TempDir(), "runtime %$ spaces")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "service.log")
	for name, body := range map[string]string{"node": "#!/bin/sh\necho v22.23.3\n", "vecnet": "#!/bin/sh\nexit 0\n", "systemctl": fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> '%s'\n", strings.ReplaceAll(log, "'", "'\\''"))} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.env["PATH"] = binDir
	args := []string{"install", "--source", source, "--vecnet-bin", filepath.Join(binDir, "vecnet")}
	if code, out, errs := f.run(t, args...); code != 0 || strings.Contains(out, "vn_peer_LAUNCHER_TEST") {
		t.Fatalf("preview %d %s", code, errs)
	}
	if _, err := os.Stat(f.paths.Manifest); !os.IsNotExist(err) {
		t.Fatal("preview installed runtime")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("preview touched service manager")
	}
	if code, _, _ := f.run(t, append(args, "--enable")...); code == 0 {
		t.Fatal("enable accepted without write")
	}
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	got, err := os.ReadFile(log)
	if err != nil || string(got) != "--user daemon-reload\n" {
		t.Fatalf("unexpected implicit action %q %v", got, err)
	}
	var first launcherInstall
	if err = launcherReadJSON(f.paths.Manifest, &first); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(f.paths.Unit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unit), "/usr/bin/node") || !strings.Contains(string(unit), "runtime %%$$ spaces/node") || !strings.Contains(string(unit), "VECNET_BIN=") || strings.Contains(string(unit), "delivery-main") {
		t.Fatalf("wrong service:\n%s", unit)
	}
	before, err := os.ReadFile(first.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "connectors/launcher/src/main.ts"), []byte("export const changed = true;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := f.run(t, append(args, "--write", "--enable", "--start")...); code != 0 {
		t.Fatal(errs)
	}
	var next launcherInstall
	if err = launcherReadJSON(f.paths.Manifest, &next); err != nil {
		t.Fatal(err)
	}
	if first.Entry == next.Entry {
		t.Fatal("upgrade reused mutable source location")
	}
	if got, _ := os.ReadFile(first.Entry); !bytes.Equal(got, before) {
		t.Fatal("upgrade mutated old snapshot")
	}
	got, _ = os.ReadFile(log)
	if !strings.Contains(string(got), "--user enable "+filepath.Base(f.paths.Unit)) || !strings.Contains(string(got), "--user restart "+filepath.Base(f.paths.Unit)) {
		t.Fatalf("missing explicit actions %s", got)
	}
	code, out, errs := f.run(t, "check")
	if code != 0 {
		t.Fatalf("check %s %s", out, errs)
	}
}

func TestLauncherStatusAndProfileIsolation(t *testing.T) {
	f := newLauncherFixture(t, "launcher")
	code, out, errs := f.run(t, "status")
	if code != 0 {
		t.Fatal(errs)
	}
	var status struct{ Configured, Installed bool }
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.Configured || status.Installed {
		t.Fatalf("wrong status %s", out)
	}
	a := &app{net: "launcher/test", getenv: func(k string) string { return f.env[k] }}
	f.env["VECNET_CONFIG"] = filepath.Join(t.TempDir(), "other.toml")
	other, err := a.launcherPaths()
	if err != nil {
		t.Fatal(err)
	}
	if other.Unit == f.paths.Unit || other.State == f.paths.State {
		t.Fatal("independent config files collide")
	}
}

func TestLauncherNodeVersionsAndUnitEscaping(t *testing.T) {
	for version, want := range map[string]bool{"v22.18.0": true, "v22.23.3\n": true, "v24.0.0": true, "v23.6.0": true, "v23.0.0": false, "v22.17.0": false, "v20.0.0": false, "invalid": false} {
		if got := launcherNodeVersion(version); got != want {
			t.Errorf("%s = %v", version, got)
		}
	}
	got, err := launcherSystemdQuote(`/tmp/a%$"\b`, true)
	if err != nil || got != `"/tmp/a%%$$\"\\b"` {
		t.Fatalf("quote %q %v", got, err)
	}
	if _, err = launcherSystemdQuote("a\nb", false); err == nil {
		t.Fatal("newline accepted in unit value")
	}
}

// Exercise install itself so unsafe reuse cannot publish a unit/manifest or
// invoke the service manager, even when every source byte still matches.
func TestLauncherInstallRejectsUnsafeSnapshotReuse(t *testing.T) {
	cases := map[string]func(*testing.T, launcherFixture, launcherInstall){
		"snapshot writable": func(t *testing.T, _ launcherFixture, m launcherInstall) { launcherTestChmod(t, m.Snapshot, 0o770) },
		"runtime directory writable": func(t *testing.T, _ launcherFixture, m launcherInstall) {
			launcherTestChmod(t, filepath.Dir(m.Entry), 0o702)
		},
		"runtime file writable":   func(t *testing.T, _ launcherFixture, m launcherInstall) { launcherTestChmod(t, m.Entry, 0o620) },
		"managed parent writable": func(t *testing.T, f launcherFixture, _ launcherInstall) { launcherTestChmod(t, f.paths.Data, 0o770) },
		"data ancestor writable": func(t *testing.T, f launcherFixture, _ launcherInstall) {
			launcherTestChmod(t, f.env["XDG_DATA_HOME"], 0o777)
		},
		"manifest parent writable": func(t *testing.T, f launcherFixture, _ launcherInstall) {
			launcherTestChmod(t, filepath.Dir(f.paths.Manifest), 0o770)
		},
		"unit parent writable": func(t *testing.T, f launcherFixture, _ launcherInstall) {
			launcherTestChmod(t, filepath.Dir(f.paths.Unit), 0o770)
		},
		"manifest file writable": func(t *testing.T, f launcherFixture, _ launcherInstall) {
			launcherTestChmod(t, f.paths.Manifest, 0o620)
		},
		"unit file writable": func(t *testing.T, f launcherFixture, _ launcherInstall) { launcherTestChmod(t, f.paths.Unit, 0o620) },
		"runtime intermediate symlink": func(t *testing.T, _ launcherFixture, m launcherInstall) {
			launcherTestMoveToSymlink(t, filepath.Dir(m.Entry))
		},
		"snapshot parent symlink": func(t *testing.T, _ launcherFixture, m launcherInstall) {
			launcherTestMoveToSymlink(t, filepath.Dir(m.Snapshot))
		},
		"manifest parent symlink": func(t *testing.T, f launcherFixture, _ launcherInstall) {
			launcherTestMoveToSymlink(t, filepath.Dir(f.paths.Manifest))
		},
		"unit parent symlink": func(t *testing.T, f launcherFixture, _ launcherInstall) {
			launcherTestMoveToSymlink(t, filepath.Dir(f.paths.Unit))
		},
		"foreign runtime owner": func(t *testing.T, _ launcherFixture, m launcherInstall) {
			if err := os.Chown(m.Entry, os.Geteuid()+1, -1); err != nil {
				t.Skip("foreign-owner fixture is unavailable without privileges")
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, args, log := launcherInstalledFixture(t)
			var m launcherInstall
			if err := launcherReadJSON(f.paths.Manifest, &m); err != nil {
				t.Fatal(err)
			}
			beforeUnit, err := os.ReadFile(f.paths.Unit)
			if err != nil {
				t.Fatal(err)
			}
			beforeManifest, err := os.ReadFile(f.paths.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			beforeLog, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			beforeUnitInfo, err := os.Lstat(f.paths.Unit)
			if err != nil {
				t.Fatal(err)
			}
			beforeManifestInfo, err := os.Lstat(f.paths.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, f, m)
			if code, _, errs := f.run(t, append(args, "--write", "--enable", "--start")...); code == 0 {
				t.Fatalf("unsafe snapshot accepted: %s", errs)
			}
			for _, item := range []struct {
				path string
				want []byte
			}{{f.paths.Unit, beforeUnit}, {f.paths.Manifest, beforeManifest}, {log, beforeLog}} {
				got, err := os.ReadFile(item.path)
				if err != nil || !bytes.Equal(got, item.want) {
					t.Fatalf("unsafe install changed %s: %v", item.path, err)
				}
			}
			for _, item := range []struct {
				path   string
				before os.FileInfo
			}{{f.paths.Unit, beforeUnitInfo}, {f.paths.Manifest, beforeManifestInfo}} {
				after, err := os.Lstat(item.path)
				if err != nil || !os.SameFile(item.before, after) {
					t.Fatalf("unsafe install republished %s: %v", item.path, err)
				}
			}
		})
	}
}

func launcherTestChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func launcherTestMoveToSymlink(t *testing.T, path string) {
	t.Helper()
	elsewhere := filepath.Join(t.TempDir(), "matching-runtime")
	if err := os.Rename(path, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
}

func launcherInstalledFixture(t *testing.T) (launcherFixture, []string, string) {
	t.Helper()
	f := newLauncherFixture(t, "launcher")
	if code, _, errs := f.run(t, append(f.setup, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	source := launcherTestSource(t)
	binDir := t.TempDir()
	log := filepath.Join(t.TempDir(), "manager.log")
	for name, body := range map[string]string{"node": "#!/bin/sh\necho v22.23.3\n", "vecnet": "#!/bin/sh\nexit 0\n", "systemctl": fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> '%s'\n", strings.ReplaceAll(log, "'", "'\\''"))} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.env["PATH"] = binDir
	args := []string{"install", "--source", source, "--vecnet-bin", filepath.Join(binDir, "vecnet")}
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	return f, args, log
}

func TestLauncherInstallReusesPrivateSnapshot(t *testing.T) {
	f, args, _ := launcherInstalledFixture(t)
	var first launcherInstall
	if err := launcherReadJSON(f.paths.Manifest, &first); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatalf("valid private reuse refused: %s", errs)
	}
	var second launcherInstall
	if err := launcherReadJSON(f.paths.Manifest, &second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("repeat install changed the snapshot")
	}
}
