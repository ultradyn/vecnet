package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/XertroV/vecnet/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// The profile publication is the complete desired task, not a shell fragment.
// The shared native manager must inspect and round-trip every field, including
// environment and argv, before repair is allowed to register a replacement.
type launcherTaskDefinition struct {
	Schema      int               `json:"schema"`
	ManagedBy   string            `json:"managed_by"`
	Name        string            `json:"name"`
	Executable  string            `json:"executable"`
	Args        []string          `json:"args"`
	Directory   string            `json:"directory"`
	Environment map[string]string `json:"environment"`
}
type launcherTaskState struct {
	Exists, Active, Enabled bool
	Definition              launcherTaskDefinition
}
type launcherTaskController interface {
	Inspect(context.Context, string) (launcherTaskState, error)
	Register(context.Context, launcherTaskDefinition, bool) error
	Restart(context.Context, string) error
}

var newLauncherTaskController = launcherNativeTaskController

func launcherWindowsUnit(m launcherInstall, config, vecnetConfig, home, path string) (string, error) {
	id := filepath.Base(filepath.Dir(config))
	if !strings.HasPrefix(id, "p-") || len(id) != 18 {
		return "", errors.New("invalid launcher task profile identity")
	}
	def := launcherTaskDefinition{Schema: 1, ManagedBy: "vecnet launcher install", Name: "vecnet-launcher-" + id, Executable: m.Node, Args: []string{"--disable-warning=ExperimentalWarning", m.Entry, "run", "--config", config}, Directory: home, Environment: map[string]string{"HOME": home, "USERPROFILE": home, "PATH": path, "VECNET_BIN": m.VecnetBin, "VECNET_CONFIG": vecnetConfig}}
	if err := launcherValidateTask(def); err != nil {
		return "", err
	}
	return string(launcherManagedJSON(def)), nil
}
func launcherValidateTask(def launcherTaskDefinition) error {
	if def.Schema != 1 || def.ManagedBy != "vecnet launcher install" || len(def.Name) != len("vecnet-launcher-p-")+16 || !strings.HasPrefix(def.Name, "vecnet-launcher-p-") || !filepath.IsAbs(def.Executable) || !filepath.IsAbs(def.Directory) || len(def.Args) != 5 || def.Args[0] != "--disable-warning=ExperimentalWarning" || def.Args[2] != "run" || def.Args[3] != "--config" || !filepath.IsAbs(def.Args[1]) || !filepath.IsAbs(def.Args[4]) || len(def.Environment) != 5 {
		return errors.New("invalid managed launcher task definition")
	}
	for _, value := range append([]string{def.Name, def.Executable, def.Directory}, def.Args...) {
		if strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("launcher task values contain control characters")
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE", "PATH", "VECNET_BIN", "VECNET_CONFIG"} {
		value := def.Environment[key]
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid managed launcher task environment")
		}
	}
	if def.Environment["HOME"] != def.Directory || def.Environment["USERPROFILE"] != def.Directory {
		return errors.New("launcher task home changed")
	}
	if !launcherTaskHex(strings.TrimPrefix(def.Name, "vecnet-launcher-p-")) {
		return errors.New("invalid managed launcher task name")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "VECNET_BIN", "VECNET_CONFIG"} {
		if !filepath.IsAbs(def.Environment[key]) {
			return errors.New("launcher task environment path is not absolute")
		}
	}
	return nil
}
func launcherTaskHex(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func launcherDecodeTask(raw []byte) (launcherTaskDefinition, error) {
	var def launcherTaskDefinition
	if err := launcherDecode(raw, &def); err != nil {
		return def, err
	}
	return def, launcherValidateTask(def)
}
func launcherWindowsManagedUnit(raw []byte) bool {
	_, err := launcherDecodeTask(raw)
	return err == nil
}
func launcherExpectedTask(meta launcherManaged) (launcherTaskDefinition, error) {
	raw, err := launcherWindowsUnit(meta.Runtime, meta.ConfigPath, meta.VecnetConfig, meta.Home, meta.Path)
	if err != nil {
		return launcherTaskDefinition{}, err
	}
	return launcherDecodeTask([]byte(raw))
}
func launcherWindowsManagerExec(state launcherManagerState, meta launcherManaged) bool {
	actual, err := launcherDecodeTask([]byte(state.Exec))
	if err != nil {
		return false
	}
	want, err := launcherExpectedTask(meta)
	return err == nil && reflect.DeepEqual(actual, want)
}
func (a *app) launcherWindowsManagerInspect(meta launcherManaged, _ bool) (launcherManagerState, error) {
	expected, err := launcherExpectedTask(meta)
	if err != nil {
		return launcherManagerState{}, err
	}
	ctl, err := newLauncherTaskController(a)
	if err != nil {
		return launcherManagerState{}, err
	}
	state, err := ctl.Inspect(a.localRegistryContext(), expected.Name)
	if err != nil {
		return launcherManagerState{}, err
	}
	if !state.Exists || state.Definition.Name != expected.Name {
		return launcherManagerState{}, errors.New("managed launcher task is missing or changed")
	}
	if err = launcherValidateTask(state.Definition); err != nil {
		return launcherManagerState{}, err
	}
	active, enabled := "inactive", "disabled"
	if state.Active {
		active = "active"
	}
	if state.Enabled {
		enabled = "enabled"
	}
	return launcherManagerState{Active: active, Enabled: enabled, Fragment: meta.UnitPath, Exec: string(launcherManagedJSON(state.Definition))}, nil
}
func (a *app) launcherWindowsPrepareInstall(p launcherPaths, raw []byte, enable, start bool) (func() error, error) {
	def, err := launcherDecodeTask(raw)
	if err != nil {
		return nil, err
	}
	ctl, err := newLauncherTaskController(a)
	if err != nil {
		return nil, err
	}
	before, err := ctl.Inspect(a.localRegistryContext(), def.Name)
	if err != nil {
		return nil, err
	}
	if before.Exists {
		oldRaw, e := launcherReadPath(p.Unit, true)
		if e != nil {
			return nil, errors.New("launcher task already exists without private ownership evidence; preserve task")
		}
		old, e := launcherDecodeTask(oldRaw)
		if e != nil || !reflect.DeepEqual(old, before.Definition) {
			return nil, errors.New("launcher task differs from installed definition; preserve custom task")
		}
	}
	return func() error {
		now, e := ctl.Inspect(a.localRegistryContext(), def.Name)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(before, now) {
			return errors.New("launcher task changed during installation")
		}
		enabled := enable || before.Exists && before.Enabled
		if e = ctl.Register(a.localRegistryContext(), def, enabled); e != nil {
			return e
		}
		if start {
			if e = ctl.Restart(a.localRegistryContext(), def.Name); e != nil {
				return e
			}
		}
		final, e := ctl.Inspect(a.localRegistryContext(), def.Name)
		if e != nil {
			return e
		}
		if !final.Exists || !reflect.DeepEqual(final.Definition, def) || final.Enabled != enabled || (start && !final.Active) || (!start && !before.Active && final.Active) {
			return errors.New("launcher task final installation verification failed")
		}
		return nil
	}, nil
}
func (a *app) launcherWindowsManagerReload(meta launcherManaged, intent *launcherRepairIntent) error {
	old, err := launcherDecodeTask(intent.OldUnit)
	if err != nil {
		return err
	}
	target, err := launcherDecodeTask(intent.NewUnit)
	if err != nil {
		return err
	}
	if old.Name != target.Name {
		return errors.New("pending launcher task identity changed")
	}
	ctl, err := newLauncherTaskController(a)
	if err != nil {
		return err
	}
	state, err := ctl.Inspect(a.localRegistryContext(), target.Name)
	if err != nil {
		return err
	}
	enabled := intent.Enabled == "enabled"
	if !state.Exists || state.Enabled != enabled || (!intent.WasActive && state.Active) || (!reflect.DeepEqual(state.Definition, old) && !reflect.DeepEqual(state.Definition, target)) {
		return errors.New("pending launcher task state or definition changed")
	}
	// Register only a proven old/new generation, and do not activate a stopped task.
	return ctl.Register(a.localRegistryContext(), target, enabled)
}
func (a *app) launcherWindowsManagerRestart(meta launcherManaged) error {
	def, err := launcherExpectedTask(meta)
	if err != nil {
		return err
	}
	ctl, err := newLauncherTaskController(a)
	if err != nil {
		return err
	}
	state, err := ctl.Inspect(a.localRegistryContext(), def.Name)
	if err != nil {
		return err
	}
	if !state.Exists || !reflect.DeepEqual(state.Definition, def) {
		return errors.New("launcher task changed before restart")
	}
	return ctl.Restart(a.localRegistryContext(), def.Name)
}
func (a *app) launcherWindowsStatus(p launcherPaths, configured, installed bool, m launcherInstall) error {
	active, enabled := "unknown", "unknown"
	managerError := ""
	if installed {
		ctl, err := newLauncherTaskController(a)
		if err == nil {
			name := strings.TrimSuffix(filepath.Base(p.Unit), ".task.json")
			var state launcherTaskState
			state, err = ctl.Inspect(a.localRegistryContext(), name)
			if err == nil {
				if !state.Exists {
					active, enabled = "not-found", "not-found"
				} else {
					active, enabled = "inactive", "disabled"
					if state.Active {
						active = "active"
					}
					if state.Enabled {
						enabled = "enabled"
					}
				}
			}
		}
		if err != nil {
			active, enabled = "unavailable", "unavailable"
			managerError = fmt.Sprint(err)
		}
	}
	return a.emitValue(map[string]any{"profile": p.Name, "configured": configured, "installed": installed, "config_path": p.Config, "unit": p.Unit, "manager": "windows-task-scheduler", "active": active, "enabled": enabled, "runtime": m, "manager_error": managerError})
}

func launcherNodeOutput(ctx context.Context, node string) ([]byte, error) {
	release, err := launcherPinExecutable(node)
	if err != nil {
		return nil, err
	}
	defer release()
	cmd := exec.CommandContext(ctx, node, "--version")
	platform.Background(cmd)
	return cmd.Output()
}
func (a *app) launcherWindowsCheck(p launcherPaths, m launcherInstall) error {
	// Check executes only the installed runtime, never an unowned manifest path.
	root, err := launcherOpenDir(filepath.Dir(p.Manifest), filepath.Dir(p.Manifest), false)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := launcherManagedRead(root, "managed.json")
	if err != nil {
		return err
	}
	var meta launcherManaged
	if err = launcherDecode(raw, &meta); err != nil {
		return err
	}
	if err = launcherValidateMetadata(meta, filepath.Join(filepath.Dir(p.Manifest), "managed.json")); err != nil {
		return err
	}
	if meta.Runtime != m {
		return errors.New("launcher check runtime manifest differs from managed installation")
	}
	manifest, err := launcherManagedRead(root, filepath.Base(p.Manifest))
	if err != nil {
		return err
	}
	config, err := launcherManagedRead(root, filepath.Base(p.Config))
	if err != nil {
		return err
	}
	unit, err := launcherManagedRead(root, filepath.Base(p.Unit))
	if err != nil {
		return err
	}
	if launcherHash(manifest) != meta.ManifestSHA256 || launcherHash(config) != meta.ConfigSHA256 || launcherHash(unit) != meta.UnitSHA256 {
		return errors.New("launcher check managed publication changed")
	}
	if err = launcherValidateSoftware(meta); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Node, "--disable-warning=ExperimentalWarning", m.Entry, "check", "--config", p.Config)
	def, err := launcherExpectedTask(meta)
	if err != nil {
		return err
	}
	cmd.Env = launcherTaskEnvironment(def.Environment)
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	platform.Background(cmd)
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("launcher command failed: %w", err)
	}
	return nil
}
func launcherTaskEnvironment(values map[string]string) []string {
	// System process creation needs these Windows directories but no ambient
	// credential, VECNET_PROFILE or service authority is inherited.
	env := []string{}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}
