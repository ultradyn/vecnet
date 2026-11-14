package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Removing the installer registration seam must leave this test failing: an
// explicit complete install must enroll its configured non-scratch project.
func TestLauncherManagedInstallationRegistersProjects(t *testing.T) {
	f, _, _ := launcherInstalledFixture(t)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json"))
	if err != nil {
		t.Fatalf("successful install did not publish managed ownership: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema", "config_path", "profile", "unit_path", "unit_sha256", "manifest_sha256", "config_sha256", "version", "binary_sha256", "project_roots", "home", "path", "node", "public_bin", "source"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("metadata missing %s", key)
		}
	}
	a := &app{getenv: func(k string) string { return f.env[k] }}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	registered := []localProject{}
	for _, p := range projects {
		if len(p.Artifacts) > 0 {
			registered = append(registered, p)
		}
	}
	if len(registered) != 1 || len(registered[0].Artifacts) != 1 || registered[0].Artifacts[0].Kind != "launcher-profile" || registered[0].Artifacts[0].Path != filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json") {
		t.Fatalf("want exactly configured non-scratch project registered: %+v", projects)
	}
	if roots, ok := metadata["project_roots"].([]any); !ok || len(roots) != 1 || roots[0] != registered[0].Root {
		t.Fatalf("wrong project roots: %+v", metadata["project_roots"])
	}
}

func TestLauncherManagedInstallationRegistersAllConfiguredRoots(t *testing.T) {
	f, args, _ := launcherInstalledFixture(t)
	var config map[string]any
	if err := json.Unmarshal(launcherTestRead(t, f.paths.Config), &config); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(f.env["HOME"], "second-project")
	if err := os.Mkdir(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	config["projects"].(map[string]any)["second"] = map[string]any{"repo_root": extra, "base_ref": "HEAD"}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(f.paths.Config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	a := &app{getenv: func(k string) string { return f.env[k] }}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	enrolled := 0
	for _, p := range projects {
		for _, art := range p.Artifacts {
			if art.Kind == "launcher-profile" {
				enrolled++
				if p.Root != extra && !strings.Contains(p.Root, "repo with spaces") {
					t.Fatalf("registered scratch/unconfigured root: %s", p.Root)
				}
			}
		}
	}
	if enrolled != 2 {
		t.Fatalf("want two configured roots, got %+v", projects)
	}
}

func TestLauncherManagedInstallationRecordsDefaultCLISelection(t *testing.T) {
	f, args, _ := launcherInstalledFixture(t)
	var meta launcherManaged
	if err := json.Unmarshal(launcherTestRead(t, filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.PublicBinDefault {
		t.Fatal("explicit --vecnet-bin was classified as default")
	}
	// Fixture arguments end with --vecnet-bin PATH. Omit that explicit override
	// so the installed process executable is the authorized default CLI.
	args = args[:len(args)-2]
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	if err := json.Unmarshal(launcherTestRead(t, filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")), &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.PublicBinDefault {
		t.Fatal("default executable selection was not recorded")
	}
	// An environment override is an explicit pin too, even without the flag.
	custom := filepath.Join(f.env["PATH"], "vecnet")
	f.env["VECNET_BIN"] = custom
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	if err := json.Unmarshal(launcherTestRead(t, filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.PublicBinDefault || meta.PublicBin != custom {
		t.Fatal("VECNET_BIN pin was classified as default")
	}
}

func TestLauncherManagedInstallationRecordsSourceSelection(t *testing.T) {
	f, args, _ := launcherInstalledFixture(t)
	path := filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")
	var meta launcherManaged
	if err := json.Unmarshal(launcherTestRead(t, path), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.SourceDefault {
		t.Fatal("explicit source was classified as default")
	}
	args = append([]string{"install"}, args[3:]...)
	if code, _, errs := f.run(t, append(args, "--write")...); code != 0 {
		t.Fatal(errs)
	}
	if err := json.Unmarshal(launcherTestRead(t, path), &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.SourceDefault {
		t.Fatal("default source discovery intent was not recorded")
	}
}
