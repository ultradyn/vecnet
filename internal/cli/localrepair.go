package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/buildinfo"
	"github.com/XertroV/vecnet/internal/localruntime"
)

var localVersionOnce sync.Once
var localVersion string
var localBinarySHA256 string

func localSoftwareVersion() string {
	localVersionOnce.Do(func() {
		localVersion = buildinfo.Current().Version
		if exe, err := os.Executable(); err == nil {
			if raw, err := os.ReadFile(exe); err == nil {
				localBinarySHA256 = hookDigest(raw)
				if localVersion == "dev" {
					localVersion += "+" + localBinarySHA256[:16]
				}
			}
		}
	})
	return localVersion
}
func localMaintenanceCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "help", "projects", "repair", "hook", "watch", "mcp":
		return false
	case "codex":
		return false
	}
	return true
}
func localBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "vecnet"
	}
	bin, _ := defaultBin(exe)
	return bin
}
func (a *app) localStartup(args []string) {
	if !localMaintenanceCommand(args) || a.getenv("VECNET_LOCAL_MAINTENANCE") == "0" {
		return
	}
	worker := *a
	ctx, cancel := context.WithTimeout(a.ctx, 200*time.Millisecond)
	defer cancel()
	worker.ctx = ctx
	p, err := worker.observeLocalProject(localSoftwareVersion(), time.Now())
	if err != nil {
		return
	}
	// Inventory is not an instruction to install. Adopt only recognized existing
	// vecnet handlers in the current project and the active user layer.
	paths := []string{filepath.Join(p.Root, ".codex/hooks.json")}
	codexHome := a.getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(a.getenv("HOME"), ".codex")
	}
	paths = append(paths, filepath.Join(codexHome, "hooks.json"))
	for _, path := range paths {
		known := false
		for _, artifact := range p.Artifacts {
			if artifact.Path == path {
				known = true
				break
			}
		}
		if known {
			continue
		}
		if raw, err := readLocalHooks(path); err == nil && hasOwnedCodexHooks(raw, localBinary()) {
			_ = worker.registerLocalArtifact(path, "codex-hooks", "legacy", time.Now())
		}
	}
	projects, err := worker.localProjects()
	if err != nil {
		return
	}
	pending, managed := false, false
	for _, p := range projects {
		if ctx.Err() != nil {
			pending = true
			break
		}
		if len(p.Artifacts) > 0 {
			managed = true
		}
		for _, art := range p.Artifacts {
			if ctx.Err() != nil {
				pending = true
				break
			}
			if art.Version != localSoftwareVersion() {
				pending = true
			}

		}
		if p.Status == "pending" || p.Status == "deferred" {
			pending = true
		}
	}
	records, _ := worker.localEnrollments()
	for _, r := range records {
		if localEnrollmentRetryable(r.State) {
			pending = true
		}
	}
	// A new executable must refresh the service's private copy as well. It is
	// safe to start repair without an owner; it cannot create one absent pending
	// enrollment intent.
	if ctx.Err() == nil && localRuntimePresent(a.getenv("HOME")) {
		managed = true
	}
	// The foreground reads bounded local metadata only. Complete file integrity
	// checks run in repair; a periodic durable checkpoint catches same-version drift.
	if managed {
		raw, err := worker.readLocalState("repair-status.json")
		var last localRepairReport
		if err != nil || json.Unmarshal(raw, &last) != nil || last.State != "ready" || last.Version != localSoftwareVersion() || last.BinarySHA256 != localBinarySHA256 || time.Since(last.AttemptedAt) > 10*time.Minute || time.Since(last.AttemptedAt) < 0 {
			pending = true
		}
	}

	if pending {
		a.scheduleLocalRepair()
	}
}
func (a *app) scheduleLocalRepair() {
	exe, err := os.Executable()
	if err != nil || (strings.HasSuffix(exe, ".test") && a.localRepairStart == nil) {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Millisecond)
	defer cancel()
	unlock, err := a.lockLocalState(ctx, "schedule")
	if err != nil {
		return
	}
	defer unlock()
	if raw, err := a.readLocalState("repair-requested.json"); err == nil {
		var marker struct {
			RequestedAt time.Time `json:"requested_at"`
		}
		if json.Unmarshal(raw, &marker) == nil && time.Since(marker.RequestedAt) >= 0 && time.Since(marker.RequestedAt) < 30*time.Second {
			return
		}
	}
	cmd := exec.Command(exe, "repair", "--background")
	for _, k := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "VECNET_NODE", "CODEX_HOME"} {
		if v := a.getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "VECNET_LOCAL_MAINTENANCE=0")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	start := cmd.Start
	if a.localRepairStart != nil {
		start = func() error { return a.localRepairStart(cmd) }
	}
	if err := start(); err == nil {
		_ = a.writeLocalState("repair-requested.json", mustLocalJSON(map[string]any{"requested_at": time.Now().UTC(), "version": localSoftwareVersion()}))
		if cmd.Process != nil {
			_ = cmd.Process.Release()
		}
	}
}
func hasOwnedCodexHooks(raw []byte, bin string) bool {
	root, err := parseJSONNode(raw)
	if err != nil || root == nil || root.kind != '{' {
		return false
	}
	hooks := root.member("hooks")
	if hooks == nil || hooks.kind != '{' {
		return false
	}
	for _, event := range codexHookEvents {
		groups := hooks.member(event.key)
		if groups == nil || groups.kind != '[' {
			continue
		}
		for _, group := range groups.vals {
			handlers := group.member("hooks")
			if handlers == nil || handlers.kind != '[' || !codexOwnedGroup(group, event.matcher) {
				continue
			}
			for _, handler := range handlers.vals {
				if codexOwnedHandler(handler, bin, event.name) {
					return true
				}
			}
		}
	}
	return false
}
func (a *app) repairLocalProjects() error { return a.repairLocalProjectsAfterRuntime(nil) }
func (a *app) repairLocalProjectsAfterRuntime(runtimeErr error) error {
	projects, err := a.localProjects()
	if err != nil {
		return err
	}
	var failures []error
	for _, p := range projects {
		status, why := "ready", ""
		completed := map[string]localArtifact{}
		if st, e := os.Stat(p.Root); e != nil || !st.IsDir() {
			status = "deferred"
			why = "project is missing or inaccessible"
		} else if resolved, err := filepath.EvalSymlinks(p.Root); err != nil || resolved != p.Root {
			status = "deferred"
			why = "project path moved or became a symlink; inspect or forget the registered root"
		} else {
			for _, art := range p.Artifacts {
				if a.ctx.Err() != nil {
					return a.ctx.Err()
				}
				if art.Kind != "codex-hooks" {
					status = "deferred"
					why = "unsupported managed artifact kind: " + art.Kind
					continue
				}
				raw, e := readLocalHooks(art.Path)
				if e != nil {
					status = "deferred"
					why = "managed hook file is missing or inaccessible"
					continue
				}
				if !hasOwnedCodexHooks(raw, localBinary()) {
					status = "deferred"
					why = "managed hooks were customized or removed; review file"
					continue
				}
				// Refuse symlink redirection on unattended repair. Explicit installer
				// remains the deliberate adoption path for a user-selected new target.
				resolved, e := filepath.EvalSymlinks(art.Path)
				if e != nil || filepath.Clean(resolved) != filepath.Clean(art.Path) {
					status = "deferred"
					why = "managed hook path changed or is a symlink"
					continue
				}
				worker := *a
				worker.localProjectRoot = p.Root
				worker.stdout = io.Discard
				worker.stderr = io.Discard
				if e = worker.installCodexHooksExpected(art.Path, localBinary(), opts{vals: map[string][]string{"write": {}}}, hookDigest(raw)); e != nil {
					status = "deferred"
					why = e.Error()
					continue
				}
				after, e := os.ReadFile(art.Path)
				if e != nil {
					status = "deferred"
					why = e.Error()
					continue
				}
				done := art
				done.Version = localSoftwareVersion()
				done.Hash = hookDigest(after)
				completed[art.Path] = done

			}
		}
		if status == "ready" && runtimeErr != nil && len(p.Artifacts) > 0 {
			status = "pending"
			why = "managed runtime upgrade pending: " + runtimeErr.Error()
		}
		why, err = a.finishLocalProjectRepair(p.Root, completed, status, why, runtimeErr)
		if err != nil {
			return err
		}
		if why != "" {
			failures = append(failures, fmt.Errorf("%s: %s", p.Root, why))
		}
	}
	return errors.Join(failures...)
}
func (a *app) cmdLocalRepair(args []string) error {
	o, err := parseFlags("repair", args, map[string]flagKind{"background": boolFlag, "status": boolFlag})
	if err != nil {
		return err
	}
	if len(o.pos) > 0 {
		return usageErr("repair: unexpected arguments")
	}
	if o.has("status") {
		raw, err := a.readLocalState("repair-status.json")
		if errors.Is(err, os.ErrNotExist) {
			raw = mustLocalJSON(localRepairReport{localSetupResult: localSetupResult{State: "not_run"}})
		} else if err != nil {
			return err
		}
		var report localRepairReport
		if _, err := parseJSONNode(raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			return err
		}
		if a.json {
			return a.emitValue(report)
		}
		fmt.Fprintf(a.stdout, "local repair %s", report.State)
		if report.Reason != "" {
			fmt.Fprintf(a.stdout, ": %s", report.Reason)
		}
		fmt.Fprintln(a.stdout)
		return nil
	}
	// Background workers never queue behind an in-flight repair. All work has
	// a bounded lifetime; unresolved stages remain durable for subsequent use.
	worker := *a
	if o.has("background") {
		ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
		defer cancel()
		worker.ctx = ctx
	}
	lockCtx := worker.ctx
	var cancelLock context.CancelFunc
	if o.has("background") {
		lockCtx, cancelLock = context.WithTimeout(lockCtx, 10*time.Millisecond)
		defer cancelLock()
	}
	unlock, err := worker.lockLocalState(lockCtx, "repair")
	if err != nil {
		if o.has("background") && errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	defer unlock()
	a = &worker
	var failures []error
	var runtimeErr error
	if localRuntimePresent(a.getenv("HOME")) {
		runtimeErr = a.ensureLocalRuntime(a.ctx)
		if runtimeErr == nil {
			if config, err := configPath(a.getenv); err == nil {
				if _, err := os.Stat(config); err == nil {
					runtimeErr = a.retireEmptyLegacySources()
				}
			}
		}
		if runtimeErr != nil {
			failures = append(failures, runtimeErr)
		}
	}
	if err := a.repairLocalProjectsAfterRuntime(runtimeErr); err != nil {
		failures = append(failures, err)
	}
	if err := a.retryLocalEnrollments(); err != nil {
		failures = append(failures, err)
	}
	result := localRepairReport{localSetupResult: localSetupResult{State: "ready"}, Version: localSoftwareVersion(), BinarySHA256: localBinarySHA256, AttemptedAt: time.Now().UTC()}
	if installed, err := localruntime.Read(a.getenv("HOME")); err == nil {
		result.Runtime = &localRepairRuntime{Version: installed.Version, Release: installed.Release, Unit: installed.Unit}
	}
	if len(failures) > 0 {
		result.State = "pending"
		result.Reason = errors.Join(failures...).Error()
	}
	if err := a.writeLocalState("repair-status.json", mustLocalJSON(result)); err != nil {
		failures = append(failures, err)
		result.State = "pending"
		result.Reason = errors.Join(failures...).Error()
	}
	if !o.has("background") {
		if a.json {
			if err := a.emitValue(result); err != nil {
				return err
			}
			return errors.Join(failures...)
		}
		fmt.Fprintf(a.stdout, "local repair %s", result.State)
		if result.Reason != "" {
			fmt.Fprintf(a.stdout, ": %s", result.Reason)
		}
		fmt.Fprintln(a.stdout)
	}
	return errors.Join(failures...)
}
func mustLocalJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }

func (a *app) retireEmptyLegacySources() error {
	config, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	config, err = canonicalCodexPath(config)
	if err != nil {
		return err
	}
	paths, err := a.codexLegacyBindings(config)
	if err != nil {
		return err
	}
	for _, path := range paths {
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			return usageErr("legacy binding is not a regular private file")
		}
		raw, e := readPrivateCodexStatus(path)
		if e != nil {
			return e
		}
		root, e := parseJSONNode(raw)
		if e != nil {
			return e
		}
		if root == nil || root.kind != '{' || len(root.keys) != 2 || root.member("version") == nil || string(root.member("version").raw) != "1" || root.member("bindings") == nil || root.member("bindings").kind != '[' || len(root.member("bindings").vals) != 0 {
			return usageErr("legacy binding has unresolved owners; preserve/review its recovery before migration")
		}
		if err := a.archiveLegacySource(path, raw); err != nil {
			return err
		}
	}
	return nil
}

func localRuntimePresent(home string) bool {
	if localruntime.Installed(home) {
		return true
	}
	_, err := os.Lstat(filepath.Join(home, ".config/systemd/user/vecnet-codex-delivery.service"))
	return err == nil
}
func (a *app) recordLocalCodexHooks(path string) {
	if err := a.registerLocalArtifact(path, "codex-hooks", localSoftwareVersion(), time.Now()); err != nil {
		fmt.Fprintf(a.stderr, "vecnet: hooks saved; local inventory pending: %v (retry: vecnet repair)\n", err)
	}
}

func readLocalHooks(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, usageErr("managed hook target must be a regular file at most 1 MiB")
	}
	return os.ReadFile(path)
}

type localRepairRuntime struct {
	Version string `json:"version"`
	Release string `json:"release"`
	Unit    string `json:"unit"`
}
type localRepairReport struct {
	localSetupResult
	Version      string              `json:"version,omitempty"`
	BinarySHA256 string              `json:"binary_sha256,omitempty"`
	AttemptedAt  time.Time           `json:"attempted_at,omitempty"`
	Runtime      *localRepairRuntime `json:"runtime,omitempty"`
}

// Commit completion only for the artifacts verified by this repair snapshot.
// A concurrent installation/adoption leaves the project pending for another pass.
func (a *app) finishLocalProjectRepair(root string, completed map[string]localArtifact, status, why string, runtimeErr error) (string, error) {
	err := a.updateLocalProject(root, func(current *localProject) error {
		if status == "ready" {
			for _, artifact := range current.Artifacts {
				done, ok := completed[artifact.Path]
				if !ok || done.Kind != artifact.Kind || done.Schema != artifact.Schema || done.Version != artifact.Version || done.Hash != artifact.Hash {
					status = "pending"
					why = "managed artifacts changed during repair; retry maintenance"
					break
				}
			}
			if runtimeErr != nil && len(current.Artifacts) > 0 {
				status = "pending"
				why = "managed runtime upgrade pending: " + runtimeErr.Error()
			}
		}
		current.Status = status
		current.Error = why
		if status == "ready" && len(current.Artifacts) > 0 {
			current.LastMigration = localSoftwareVersion()
			current.AppliedVersion = localSoftwareVersion()
			var digests strings.Builder
			for _, artifact := range current.Artifacts {
				digests.WriteString(artifact.Path + "\x00" + artifact.Hash + "\n")
			}
			current.AppliedDigest = hookDigest([]byte(digests.String()))
		}
		return nil
	})
	return why, err
}
