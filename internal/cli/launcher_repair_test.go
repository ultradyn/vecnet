package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func launcherRepairFixture(t *testing.T, active bool) (launcherFixture, *app, string, localArtifact, string) {
	t.Helper()
	f, _, log := launcherInstalledFixture(t)
	a := &app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }, stdout: io.Discard, stderr: io.Discard}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	var project string
	var art localArtifact
	for _, p := range projects {
		for _, v := range p.Artifacts {
			if v.Kind == "launcher-profile" {
				project = p.Root
				art = v
			}
		}
	}
	if project == "" {
		t.Fatal("fixture not registered")
	}
	// The small fixture source stands in for a previous normally discovered
	// bundled release. Record that normal selection explicitly in ownership
	// evidence; the installer tests separately cover real selector recording.
	var fixtureMeta launcherManaged
	if err := json.Unmarshal(launcherTestRead(t, art.Path), &fixtureMeta); err != nil {
		t.Fatal(err)
	}
	fixtureMeta.SourceDefault = true
	if err := os.WriteFile(art.Path, launcherManagedJSON(fixtureMeta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifactForProject(project, art.Path, "launcher-profile", art.Version, time.Now()); err != nil {
		t.Fatal(err)
	}
	art.Hash = launcherHash(launcherTestRead(t, art.Path))

	state := filepath.Join(filepath.Dir(f.paths.Manifest), "manager-test")
	log = filepath.Join(state, "commands")
	if err = os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	value := "inactive"
	if active {
		value = "active"
	}
	for name, raw := range map[string]string{"active": value, "enabled": "disabled", "dropins": "", "fragment": f.paths.Unit} {
		if err = os.WriteFile(filepath.Join(state, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// This replaces only the external systemd boundary. All ownership, CAS,
	// snapshots, staging and durable publications execute against real files.
	script := fmt.Sprintf(`#!/bin/sh
state=%s
unit=%s
manifest=%s
printf '%%s\n' "$*" >> %s
case "$2" in
 show)
  if [ -f "$state/uncertain" ]; then exit 1; fi
  active=$(/bin/cat "$state/active")
  printf 'LoadState=loaded\nActiveState=%%s\n' "$active"
  if [ "$active" = active ]; then echo SubState=running; else echo SubState=dead; fi
  printf 'UnitFileState=%%s\nFragmentPath=%%s\nDropInPaths=%%s\n' "$(/bin/cat "$state/enabled")" "$(/bin/cat "$state/fragment")" "$(/bin/cat "$state/dropins")"
  command=$(/bin/sed -n 's/^ExecStart=//p' "$unit" | /bin/sed 's/"//g')
  node=${command%%%% *}
  printf 'ExecStart={ path=%%s ; argv[]=%%s ; ignore_errors=no ; }\n' "$node" "$command"
  ;;
 daemon-reload) if [ -f "$state/fail-reload" ]; then exit 1; fi ;;
 restart) if [ -f "$state/fail-restart" ]; then exit 1; fi; printf active > "$state/active" ;;
 *) exit 1 ;;
esac
`, shellLauncherTestQuote(state), shellLauncherTestQuote(f.paths.Unit), shellLauncherTestQuote(f.paths.Manifest), shellLauncherTestQuote(log))
	if err = os.WriteFile(filepath.Join(f.env["PATH"], "systemctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return f, a, project, art, state
}
func shellLauncherTestQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func launcherTestRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

// Starting stopped services or omitting active restarts breaks the user's intent.
func TestLauncherManagedRepairPreservesServiceIntent(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			f, a, project, art, state := launcherRepairFixture(t, active)
			if active {
				if err := os.WriteFile(filepath.Join(state, "enabled"), []byte("enabled"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(f.paths.State, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(f.paths.State, "jobs.json")
			if err := os.WriteFile(sentinel, []byte("existing jobs/authority sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			protected := map[string][]byte{}
			for _, path := range []string{sentinel, f.env["VECNET_CONFIG"], filepath.Join(f.setup[len(f.setup)-1], "config.toml")} {
				protected[path] = launcherTestRead(t, path)
			}
			config := launcherTestRead(t, f.paths.Config)
			credential := launcherTestRead(t, f.paths.Credential)
			registryBefore, err := a.localProjects()
			if err != nil {
				t.Fatal(err)
			}
			done, err := a.repairLauncherArtifact(project, art)
			if err != nil {
				t.Fatal(err)
			}
			if done.Hash == art.Hash || done.Version == "" {
				t.Fatalf("not refreshed: %+v", done)
			}
			var manifest launcherInstall
			if err = json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Source != "embedded" || manifest.VecnetBin == art.Path {
				t.Fatalf("not upgraded: %+v", manifest)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(launcherTestRead(t, manifest.VecnetBin), launcherTestRead(t, executable)) {
				t.Fatal("private CLI differs from repairing executable")
			}
			info, err := os.Stat(manifest.VecnetBin)
			if err != nil || info.Mode().Perm() != 0o500 {
				t.Fatalf("private CLI permissions: %v %v", info, err)
			}
			log := string(launcherTestRead(t, filepath.Join(state, "commands")))
			if strings.Contains(log, "--user restart ") != active {
				t.Fatalf("restart intent mismatch: %s", log)
			}
			if strings.Contains(log, "--user enable ") || strings.Contains(log, "--user disable ") {
				t.Fatal("changed enablement")
			}
			// Repair must not race MAIN's registry CAS by updating it itself.
			after, err := a.localProjects()
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, _ := json.Marshal(registryBefore)
			afterJSON, _ := json.Marshal(after)
			if !bytes.Equal(beforeJSON, afterJSON) {
				t.Fatal("repair mutated registry")
			}
			if !bytes.Equal(config, launcherTestRead(t, f.paths.Config)) || !bytes.Equal(credential, launcherTestRead(t, f.paths.Credential)) {
				t.Fatal("repair mutated config/credential")
			}
			for path, before := range protected {
				if !bytes.Equal(before, launcherTestRead(t, path)) {
					t.Fatalf("mutated unrelated state %s", path)
				}
			}
			// Both current and stale project artifacts must converge without publication.
			os.WriteFile(filepath.Join(state, "commands"), nil, 0o600)
			unitInfo, _ := os.Stat(f.paths.Unit)
			manifestInfo, _ := os.Stat(f.paths.Manifest)
			metaInfo, _ := os.Stat(art.Path)
			before := launcherTestRead(t, f.paths.Manifest)
			again, err := a.repairLauncherArtifact(project, art)
			if err != nil || again.Hash != done.Hash {
				t.Fatalf("stale root convergence: %+v %v", again, err)
			}
			if _, err = a.repairLauncherArtifact(project, done); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, launcherTestRead(t, f.paths.Manifest)) {
				t.Fatal("no-op rewrote manifest")
			}
			for path, info := range map[string]os.FileInfo{f.paths.Unit: unitInfo, f.paths.Manifest: manifestInfo, art.Path: metaInfo} {
				after, e := os.Stat(path)
				if e != nil || !os.SameFile(info, after) {
					t.Fatalf("no-op replaced %s", path)
				}
			}
			if log := string(launcherTestRead(t, filepath.Join(state, "commands"))); strings.Contains(log, "daemon-reload") || strings.Contains(log, "restart") {
				t.Fatalf("no-op changed manager: %s", log)
			}
		})
	}
}

func TestLauncherManagedRepairRefusesDrift(t *testing.T) {
	for _, kind := range []string{"unit", "config", "manifest", "metadata", "snapshot", "dropins", "fragment", "uncertain", "binary", "wrong-root"} {
		t.Run(kind, func(t *testing.T) {
			f, a, project, art, state := launcherRepairFixture(t, true)
			var manifest launcherInstall
			json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &manifest)
			switch kind {
			case "unit":
				os.WriteFile(f.paths.Unit, []byte("custom unit\n"), 0o600)
			case "config":
				file, _ := os.OpenFile(f.paths.Config, os.O_APPEND|os.O_WRONLY, 0)
				file.WriteString(" ")
				file.Close()
			case "manifest":
				os.WriteFile(f.paths.Manifest, []byte("{}"), 0o600)
			case "metadata":
				os.WriteFile(art.Path, []byte("{}"), 0o600)
			case "snapshot":
				os.WriteFile(manifest.Entry, []byte("custom\n"), 0o600)
			case "dropins":
				os.WriteFile(filepath.Join(state, "dropins"), []byte("/custom.conf"), 0o600)
			case "fragment":
				os.WriteFile(filepath.Join(state, "fragment"), []byte("/custom.service"), 0o600)
			case "uncertain":
				os.WriteFile(filepath.Join(state, "uncertain"), nil, 0o600)
			case "binary":
				os.WriteFile(manifest.VecnetBin, []byte("custom binary"), 0o700)
			case "wrong-root":
				project = filepath.Dir(project)
			}
			beforeUnit := launcherTestRead(t, f.paths.Unit)
			beforeManifest := launcherTestRead(t, f.paths.Manifest)
			beforeMeta := launcherTestRead(t, art.Path)
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("accepted drift")
			}
			if !bytes.Equal(beforeUnit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(beforeManifest, launcherTestRead(t, f.paths.Manifest)) || !bytes.Equal(beforeMeta, launcherTestRead(t, art.Path)) {
				t.Fatal("drift refusal mutated publication")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(art.Path), "repair-pending.json")); !os.IsNotExist(err) {
				t.Fatalf("drift created intent: %v", err)
			}
		})
	}
}

func TestLauncherManagedRepairResume(t *testing.T) {
	for _, failure := range []string{"fail-reload", "fail-restart"} {
		t.Run(failure, func(t *testing.T) {
			f, a, project, art, state := launcherRepairFixture(t, true)
			beforeMeta := launcherTestRead(t, art.Path)
			os.WriteFile(filepath.Join(state, failure), nil, 0o600)
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("manager failure reported complete")
			}
			if !bytes.Equal(beforeMeta, launcherTestRead(t, art.Path)) {
				t.Fatal("advanced metadata before verification")
			}
			var intent map[string]any
			if err := json.Unmarshal(launcherTestRead(t, filepath.Join(filepath.Dir(art.Path), "repair-pending.json")), &intent); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(state, failure))
			// A failed restart may leave the service stopped; the durable original
			// intent, not this new observation, must decide whether to restart.
			os.WriteFile(filepath.Join(state, "active"), []byte("inactive"), 0o600)
			if _, err := a.repairLauncherArtifact(project, art); err != nil {
				t.Fatal(err)
			}
			if string(launcherTestRead(t, filepath.Join(state, "active"))) != "active" {
				t.Fatal("lost original active intent")
			}
			_ = f
		})
	}
}

func TestLauncherManagedRepairCancellation(t *testing.T) {
	f, a, project, art, _ := launcherRepairFixture(t, false)
	before := launcherTestRead(t, f.paths.Manifest)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.ctx = ctx
	start := time.Now()
	if _, err := a.repairLauncherArtifact(project, art); err == nil {
		t.Fatal("ignored cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow cancellation")
	}
	if !bytes.Equal(before, launcherTestRead(t, f.paths.Manifest)) {
		t.Fatal("cancelled repair mutated files")
	}
}

// A package upgrade replaces the public executable before repair starts. That
// replacement is expected only at this process's exact executable path; an
// arbitrary replacement of an explicitly selected CLI must still be refused.
func TestLauncherManagedRepairAdoptsCurrentPublicExecutable(t *testing.T) {
	for _, defaultCLI := range []bool{false, true} {
		t.Run(fmt.Sprint(defaultCLI), func(t *testing.T) {
			f, a, project, art, _ := launcherRepairFixture(t, false)
			var meta launcherManaged
			if err := json.Unmarshal(launcherTestRead(t, art.Path), &meta); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			meta.PublicBinDefault = defaultCLI
			meta.PublicBin = exe
			meta.Runtime.VecnetBin = exe
			manifest := launcherManagedJSON(meta.Runtime)
			meta.ManifestSHA256 = launcherHash(manifest)
			template := launcherTestRead(t, filepath.Join(meta.Runtime.Snapshot, "connectors/launcher/systemd/vecnet-launcher-profile.service.in"))
			unit, err := launcherUnit(template, meta.Runtime, meta.ConfigPath, meta.VecnetConfig, meta.Home, meta.Path)
			if err != nil {
				t.Fatal(err)
			}
			meta.UnitSHA256 = launcherHash([]byte(unit))
			// BinarySHA256 still records the old package's bytes, as a real upgrade does.
			raw := launcherManagedJSON(meta)
			art.Hash = launcherHash(raw)
			for path, data := range map[string][]byte{art.Path: raw, f.paths.Manifest: manifest, f.paths.Unit: []byte(unit)} {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = a.repairLauncherArtifact(project, art)
			if defaultCLI && err != nil {
				t.Fatal(err)
			}
			if !defaultCLI && err == nil {
				t.Fatal("adopted changed explicitly selected CLI")
			}
		})
	}
}

func TestLauncherManagedRepairLockContention(t *testing.T) {
	f, a, project, art, _ := launcherRepairFixture(t, false)
	lock, err := os.OpenFile(f.paths.Manifest+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	held, err := launcherTryLock(lock)
	if err != nil || !held {
		t.Fatalf("fixture lock %v %v", held, err)
	}
	before := launcherTestRead(t, art.Path)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	a.ctx = ctx
	if _, err = a.repairLauncherArtifact(project, art); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contention did not respect cancellation: %v", err)
	}
	if !bytes.Equal(before, launcherTestRead(t, art.Path)) {
		t.Fatal("contended repair published metadata")
	}
}

func TestLauncherManagedRepairResumesEveryAuthorizedPublication(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, mask := range []int{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("active=%t/partial=%d", active, mask), func(t *testing.T) {
				f, a, project, art, state := launcherRepairFixture(t, active)
				before := launcherTestRead(t, art.Path)
				os.WriteFile(filepath.Join(state, "fail-reload"), nil, 0o600)
				if _, err := a.repairLauncherArtifact(project, art); err == nil {
					t.Fatal("expected pending reload")
				}
				var intent launcherRepairIntent
				if err := json.Unmarshal(launcherTestRead(t, filepath.Join(filepath.Dir(art.Path), "repair-pending.json")), &intent); err != nil {
					t.Fatal(err)
				}
				unit, manifest := intent.OldUnit, intent.OldManifest
				if mask&1 != 0 {
					unit = intent.NewUnit
				}
				if mask&2 != 0 {
					manifest = intent.NewManifest
				}
				os.WriteFile(f.paths.Unit, unit, 0o600)
				os.WriteFile(f.paths.Manifest, manifest, 0o600)
				if !bytes.Equal(before, launcherTestRead(t, art.Path)) {
					t.Fatal("metadata advanced before activation")
				}
				os.Remove(filepath.Join(state, "fail-reload"))
				done, err := a.repairLauncherArtifact(project, art)
				if err != nil {
					t.Fatal(err)
				}
				if done.Hash == art.Hash {
					t.Fatal("resumed stage not applied")
				}
				if string(launcherTestRead(t, filepath.Join(state, "active"))) == "active" != active {
					t.Fatal("resume changed original stopped intent")
				}
			})
		}
	}
}

func TestLauncherManagedRepairPendingDriftPreserved(t *testing.T) {
	for _, kind := range []string{"unit", "manifest", "intent", "config", "metadata", "private-cli"} {
		t.Run(kind, func(t *testing.T) {
			f, a, project, art, state := launcherRepairFixture(t, true)
			os.WriteFile(filepath.Join(state, "fail-reload"), nil, 0o600)
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("expected pending reload")
			}
			os.Remove(filepath.Join(state, "fail-reload"))
			target := f.paths.Unit
			switch kind {
			case "manifest":
				target = f.paths.Manifest
			case "intent":
				target = filepath.Join(filepath.Dir(art.Path), "repair-pending.json")
			case "config":
				target = f.paths.Config
			case "metadata":
				target = art.Path
			case "private-cli":
				var m launcherInstall
				json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &m)
				target = m.VecnetBin
				os.Chmod(target, 0o700)
			}
			if err := os.WriteFile(target, []byte("customized during repair\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := launcherTestRead(t, target)
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("accepted pending drift")
			}
			if !bytes.Equal(before, launcherTestRead(t, target)) {
				t.Fatal("overwrote pending customization")
			}
		})
	}
}

func TestLauncherManagedRepairUnsafePathsPreserved(t *testing.T) {
	for _, kind := range []string{"unit-mode", "config-mode", "metadata-mode", "snapshot-mode", "unit-symlink", "manifest-symlink", "metadata-symlink", "snapshot-symlink", "config-symlink"} {
		t.Run(kind, func(t *testing.T) {
			f, a, project, art, _ := launcherRepairFixture(t, false)
			target := f.paths.Unit
			switch strings.Split(kind, "-")[0] {
			case "config":
				target = f.paths.Config
			case "metadata":
				target = art.Path
			case "manifest":
				target = f.paths.Manifest
			case "snapshot":
				var m launcherInstall
				json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &m)
				target = m.Entry
			}
			if strings.HasSuffix(kind, "mode") {
				if err := os.Chmod(target, 0o640); err != nil {
					t.Fatal(err)
				}
			} else {
				launcherTestMoveToSymlink(t, target)
			}
			before := launcherTestRead(t, target)
			if _, err := a.repairLauncherArtifact(project, art); err == nil {
				t.Fatal("accepted unsafe ownership path")
			}
			if !bytes.Equal(before, launcherTestRead(t, target)) {
				t.Fatal("modified unsafe path")
			}
		})
	}
}

func TestLauncherManagedRepairDefaultCLIMoves(t *testing.T) {
	for _, oldMissing := range []bool{false, true} {
		t.Run(fmt.Sprint(oldMissing), func(t *testing.T) {
			f, a, project, art, _ := launcherRepairFixture(t, false)
			var meta launcherManaged
			if err := json.Unmarshal(launcherTestRead(t, art.Path), &meta); err != nil {
				t.Fatal(err)
			}
			meta.PublicBinDefault = true
			raw := launcherManagedJSON(meta)
			art.Hash = launcherHash(raw)
			if err := os.WriteFile(art.Path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			// Current executable lives elsewhere. The old package location may already
			// have been removed, or may contain a newer package's bytes.
			if oldMissing {
				if err := os.Remove(meta.PublicBin); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(meta.PublicBin, []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.repairLauncherArtifact(project, art); err != nil {
				t.Fatal(err)
			}
			var manifest launcherInstall
			if err := json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &manifest); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(launcherTestRead(t, manifest.VecnetBin), launcherTestRead(t, executable)) {
				t.Fatal("default path migration did not stage current CLI")
			}
		})
	}
}

func TestLauncherManagedRepairConvergesMultipleRegisteredRoots(t *testing.T) {
	f, a, _, _, _ := launcherRepairFixture(t, false)
	var config map[string]any
	if err := json.Unmarshal(launcherTestRead(t, f.paths.Config), &config); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(f.env["HOME"], "another-project")
	if err := os.Mkdir(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	config["projects"].(map[string]any)["second"] = map[string]any{"repo_root": extra, "base_ref": "HEAD"}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(f.paths.Config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var old launcherInstall
	if err := json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &old); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := f.run(t, "install", "--source", old.Source, "--vecnet-bin", old.VecnetBin, "--write"); code != 0 {
		t.Fatal(errs)
	}
	// This second explicit fixture install likewise models a previous normal
	// release rather than custom code; enroll both roots with that evidence.
	path := filepath.Join(filepath.Dir(f.paths.Manifest), "managed.json")
	var fixtureMeta launcherManaged
	if err := json.Unmarshal(launcherTestRead(t, path), &fixtureMeta); err != nil {
		t.Fatal(err)
	}
	fixtureMeta.SourceDefault = true
	if err := os.WriteFile(path, launcherManagedJSON(fixtureMeta), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range fixtureMeta.ProjectRoots {
		if err := a.registerLocalArtifactForProject(root, path, "launcher-profile", fixtureMeta.Version, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	refreshed := ""
	count := 0
	for _, p := range projects {
		for _, art := range p.Artifacts {
			if art.Kind != "launcher-profile" {
				continue
			}
			done, err := a.repairLauncherArtifact(p.Root, art)
			if err != nil {
				t.Fatal(err)
			}
			if refreshed != "" && done.Hash != refreshed {
				t.Fatal("shared profile generations diverged")
			}
			refreshed = done.Hash
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected two independently registered roots, got %d", count)
	}
}

func TestLauncherManagedRepairPreservesExplicitCustomSource(t *testing.T) {
	f, a, project, art, state := launcherRepairFixture(t, true)
	var meta launcherManaged
	if err := json.Unmarshal(launcherTestRead(t, art.Path), &meta); err != nil {
		t.Fatal(err)
	}
	// This source fixture deliberately differs from the embedded runtime.
	meta.SourceDefault = false
	raw := launcherManagedJSON(meta)
	art.Hash = launcherHash(raw)
	if err := os.WriteFile(art.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeUnit := launcherTestRead(t, f.paths.Unit)
	beforeManifest := launcherTestRead(t, f.paths.Manifest)
	if _, err := a.repairLauncherArtifact(project, art); err == nil {
		t.Fatal("replaced explicitly selected custom source with embedded assets")
	}
	if !bytes.Equal(beforeUnit, launcherTestRead(t, f.paths.Unit)) || !bytes.Equal(beforeManifest, launcherTestRead(t, f.paths.Manifest)) || !bytes.Equal(raw, launcherTestRead(t, art.Path)) {
		t.Fatal("modified explicit custom-source installation")
	}
	if log := string(launcherTestRead(t, filepath.Join(state, "commands"))); strings.Contains(log, "daemon-reload") || strings.Contains(log, "restart") {
		t.Fatal("activated custom-source replacement")
	}
}

func TestLauncherManagedRepairExplicitBundledSource(t *testing.T) {
	f, a, project, _, _ := launcherRepairFixture(t, false)
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var old launcherInstall
	if err := json.Unmarshal(launcherTestRead(t, f.paths.Manifest), &old); err != nil {
		t.Fatal(err)
	}
	// Explicit selection is safe to adopt only while the recorded complete
	// source inventory is byte-identical to this executable's embedded bundle.
	if code, _, errs := f.run(t, "install", "--source", source, "--vecnet-bin", old.VecnetBin, "--write"); code != 0 {
		t.Fatal(errs)
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	var art localArtifact
	for _, p := range projects {
		if p.Root == project {
			for _, v := range p.Artifacts {
				if v.Kind == "launcher-profile" {
					art = v
				}
			}
		}
	}
	if _, err := a.repairLauncherArtifact(project, art); err != nil {
		t.Fatal(err)
	}
}
