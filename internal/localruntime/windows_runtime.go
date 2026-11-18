//go:build windows

package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
	"golang.org/x/sys/windows"
)

func readWindows(home string) (Result, error) {
	h, e := windowsHome(home)
	if e != nil {
		return Result{}, e
	}
	m, e := readManifest(h, manifestName)
	if e != nil {
		return Result{}, e
	}
	r := m.result(false)
	if p, e := optionalManifest(h, pendingName); e != nil {
		return r, e
	} else if p != nil {
		return r, ErrPending
	}
	if e = checkPath(h, m.Release); e != nil {
		return r, e
	}
	binary, e := readPrivate(filepath.Join(m.Release, platform.Exe("vecnet")), 128<<20)
	if e != nil {
		return r, e
	}
	files, e := distribution(binary)
	if e != nil {
		return r, e
	}
	if filepath.Base(m.Release) != releaseDigest(m.Version, files) {
		return r, errors.New("managed runtime content digest differs; repair required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = verifyRelease(ctx, m.Release, files); e != nil {
		return r, e
	}
	b, e := windowsReadUnit(h)
	if e != nil {
		return r, e
	}
	if sum(b) != m.UnitHash {
		return r, errors.New("managed delivery task definition missing or modified; repair required")
	}
	r.BinarySHA256 = sum(binary)
	return r, nil
}
func prepareWindows(o Options) (Options, []byte, error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Home == "" {
		o.Home = platform.Home(o.Getenv)
	}
	h, e := windowsHome(o.Home)
	if e != nil {
		return o, nil, e
	}
	o.Home = h
	if o.Version == "" || len(o.Version) > 256 || unsafeString(o.Version) {
		return o, nil, errors.New("runtime version is required and must be a single line")
	}
	if o.Binary == "" {
		o.Binary, e = os.Executable()
		if e != nil {
			return o, nil, errors.New("cannot locate CLI executable")
		}
	}
	o.Binary, e = filepath.Abs(o.Binary)
	if e != nil || unsafeWindowsPath(o.Binary) {
		return o, nil, errors.New("invalid CLI executable path")
	}
	// Source software may belong to SYSTEM/Admin (e.g. Program Files). Never
	// tighten its ACL or trust a writable/reparse executable as a private release.
	binary, e := readWindowsExecutable(o.Binary)
	if e != nil {
		return o, nil, fmt.Errorf("cannot safely read CLI executable: %w", e)
	}
	if len(binary) > 128<<20 {
		return o, nil, errors.New("CLI executable exceeds runtime integrity limit")
	}
	if o.Node == "" {
		o.Node, e = findWindowsExecutable("node", o.Getenv("PATH"))
		if e != nil {
			return o, nil, errors.New("Node >=22.18 is required on the configured PATH")
		}
	}
	if !filepath.IsAbs(o.Node) || unsafeWindowsPath(o.Node) {
		return o, nil, errors.New("Node must be an absolute Windows executable path")
	}
	if o.Run == nil {
		if e = platform.CheckPath(o.Node, false, false); e != nil {
			return o, nil, fmt.Errorf("unsafe Node executable: %w", e)
		}
		o.Run = windowsRunner(o)
	}
	return o, binary, nil
}

// Read the source executable on the same non-reparse, ownership-checked handle.
// Shared-readable system installations are allowed; unrelated-user write access
// and source path swaps must never enter an immutable trusted release.
func readWindowsExecutable(path string) ([]byte, error) {
	release, e := pinWindowsExecutable(path)
	if e != nil {
		return nil, e
	}
	defer release()
	dir, e := platform.OpenDir(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer dir.Close()
	if e = platform.CheckFile(dir, true, false); e != nil {
		return nil, e
	}
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return nil, e
	}
	h, e := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	if e = platform.CheckFile(f, false, false); e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if st.Size() > 128<<20 {
		return nil, errors.New("CLI executable exceeds runtime integrity limit")
	}
	b, e := io.ReadAll(io.LimitReader(f, (128<<20)+1))
	if len(b) > 128<<20 {
		return nil, errors.New("CLI executable exceeds runtime integrity limit")
	}
	return b, e
}
func findWindowsExecutable(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) || unsafeWindowsPath(dir) {
			continue
		}
		p := filepath.Join(dir, platform.Exe(name))
		if platform.CheckPath(p, false, false) == nil {
			return p, nil
		}
	}
	return "", errors.New("executable missing")
}

// Retain the actual native executable and every safe ancestor through its probes
// and activation. A safe file DACL does not prevent replacement through an
// untrusted parent; FILE_SHARE_READ alone also prevents in-place writes.
func pinWindowsExecutable(path string) (func(), error) {
	if !filepath.IsAbs(path) || unsafeWindowsPath(path) || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return nil, errors.New("native runtime executable must be an absolute local .exe")
	}
	volume := filepath.VolumeName(path)
	prefix := volume + string(filepath.Separator)
	var pins []*os.File
	closeAll := func() {
		for i := len(pins) - 1; i >= 0; i-- {
			_ = pins[i].Close()
		}
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(path), prefix), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		prefix = filepath.Join(prefix, part)
		dir, e := platform.OpenDir(prefix)
		if e != nil {
			closeAll()
			return nil, e
		}
		pins = append(pins, dir)
		// The OS profile container permits users to create their own profiles, not
		// replace this user's pinned child. Validate all other existing ancestors.
		systemProfiles := strings.EqualFold(prefix, filepath.Join(volume+string(filepath.Separator), "Users"))
		if !systemProfiles {
			if e = platform.CheckFile(dir, true, false); e != nil {
				closeAll()
				return nil, e
			}
		}
	}
	name, e := windows.UTF16PtrFromString(path)
	if e != nil {
		closeAll()
		return nil, e
	}
	handle, e := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		closeAll()
		return nil, e
	}
	file := os.NewFile(uintptr(handle), path)
	pins = append(pins, file)
	if e = platform.CheckFile(file, false, false); e != nil {
		closeAll()
		return nil, e
	}
	return closeAll, nil
}

// Include preflight: its no-share-delete metadata handles must not overlap
// another goroutine's atomic replacement or removal during activation. Keep the
// filesystem lock below, after all trust checks, for cross-process mutation.
var windowsOperations operationGuard

func ensureWindows(parent context.Context, o Options) (Result, error) {
	if e := parent.Err(); e != nil {
		return Result{}, e
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	releaseOperation, e := windowsOperations.acquire(ctx)
	if e != nil {
		return Result{}, e
	}
	defer releaseOperation()
	native := o.Run == nil
	o, binary, e := prepareWindows(o)
	if e != nil {
		return Result{}, e
	}
	if native {
		release, err := pinWindowsExecutable(o.Node)
		if err != nil {
			return Result{}, fmt.Errorf("unsafe Node executable: %w", err)
		}
		defer release()
	}
	if e = nodeCapabilities(ctx, o); e != nil {
		return Result{}, e
	}
	files, e := distribution(binary)
	if e != nil {
		return Result{}, e
	}
	release := filepath.Join(root(o.Home), releaseDigest(o.Version, files))
	unit, e := windowsDefinition(o.Home, o.Node, release)
	if e != nil {
		return Result{}, e
	}
	var definition windowsTask
	_ = json.Unmarshal(unit, &definition)
	desired := manifest{Schema: 1, Version: o.Version, Release: release, Unit: unitPath(o.Home), Node: o.Node, UnitHash: sum(unit)}
	// Capability/conflict/custom-task checks precede ALL installation writes,
	// including lock creation, staging, or recording pending metadata.
	state, e := inspectTask(ctx, o, definition)
	if e != nil {
		return Result{}, e
	}
	old, pending, current, e := inspectWindowsMetadata(o.Home, state)
	if e != nil {
		return Result{}, e
	}
	valid, e := previewWindowsRelease(ctx, release, files, (old != nil && old.Release == release) || (pending != nil && pending.Release == release))
	if e != nil {
		return Result{}, e
	}
	if o.Preview {
		r := desired.result(!valid || old == nil || *old != desired || pending != nil || string(current) != string(unit) || !state.Exists || state.Definition != definition || state.State != 4 || !state.Enabled)
		r.BinarySHA256 = sum(binary)
		return r, nil
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	if e = mkdirSafe(o.Home, root(o.Home), 0700); e != nil {
		return Result{}, e
	}
	unpin, e := pinRuntimePath(o.Home, root(o.Home))
	if e != nil {
		return Result{}, e
	}
	defer unpin()
	unlock, e := lock(ctx, filepath.Join(root(o.Home), "install.lock"))
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	state, e = inspectTask(ctx, o, definition)
	if e != nil {
		return Result{}, e
	}
	old, pending, current, e = inspectWindowsMetadata(o.Home, state)
	if e != nil {
		return Result{}, e
	}
	repairAllowed := (old != nil && old.Release == release) || (pending != nil && pending.Release == release)
	valid = verifyRelease(ctx, release, files) == nil
	if !valid && !repairAllowed {
		if _, se := os.Lstat(release); se == nil {
			return Result{}, errors.New("unrecognized staged release is modified; refusing replacement")
		} else if !errors.Is(se, os.ErrNotExist) {
			return Result{}, se
		}
	}
	unchanged := valid && old != nil && *old == desired && pending == nil && string(current) == string(unit) && state.Exists && state.Definition == definition
	if unchanged && state.State == 4 && state.Enabled {
		r := desired.result(false)
		r.BinarySHA256 = sum(binary)
		return r, nil
	}
	// Persist intent BEFORE stopping or replacing live paths. In particular a
	// same-version repair must not be mistaken for a successfully activated release.
	if e = writeManifest(o.Home, pendingName, desired); e != nil {
		return Result{}, e
	}
	restart := !unchanged || state.State != 4
	if restart && state.Exists && (state.State == 4 || state.State == 2) {
		state, e = taskCommand(ctx, o, "stop", definition, state.XML)
		if e != nil {
			return Result{}, e
		}
		stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
		state, e = waitTask(stopCtx, o, definition, false)
		stopCancel()
		if e != nil {
			return Result{}, e
		}
	}
	repaired, e := stage(ctx, o.Home, release, files, repairAllowed, func() error { return writeManifest(o.Home, pendingName, desired) })
	if e != nil {
		return Result{}, e
	}
	restart = restart || repaired
	if string(current) != string(unit) {
		if len(current) > 0 {
			backup := filepath.Join(root(o.Home), "unit-backups", sum(current)+".task.json")
			if e = mkdirSafe(o.Home, filepath.Dir(backup), 0700); e != nil {
				return Result{}, e
			}
			if e = atomicWrite(backup, current, 0600); e != nil {
				return Result{}, e
			}
		}
		if e = atomicWrite(desired.Unit, unit, 0600); e != nil {
			return Result{}, e
		}
	}
	// Reinspect immediately before each mutation; PowerShell repeats an XML CAS
	// at the service boundary so an operator's concurrently changed task is custom.
	state, e = inspectTask(ctx, o, definition)
	if e != nil {
		return Result{}, e
	}
	if e = acceptTask(state, o.Home, old, pending); e != nil {
		return Result{}, e
	}
	if !state.Exists || state.Definition != definition {
		state, e = taskCommand(ctx, o, "register", definition, state.XML)
		if e != nil {
			return Result{}, e
		}
	}
	if !state.Enabled {
		state, e = taskCommand(ctx, o, "enable", definition, state.XML)
		if e != nil {
			return Result{}, e
		}
	}
	if restart {
		// A stale activation acknowledgement must never authorize a fresh start.
		ack := filepath.Join(root(o.Home), "delivery-active.json")
		if e = platform.CheckPath(ack, false, true); e == nil {
			e = os.Remove(ack)
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return Result{}, e
		}
		state, e = taskCommand(ctx, o, "start", definition, state.XML)
		if e != nil {
			return Result{}, e
		}
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 20*time.Second)
	defer verifyCancel()
	state, e = waitTask(verifyCtx, o, definition, true)
	if e != nil {
		return Result{}, e
	}
	if e = verifyWindowsActivation(verifyCtx, o, definition); e != nil {
		return Result{}, e
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	if e = writeManifest(o.Home, manifestName, desired); e != nil {
		return Result{}, e
	}
	if e = os.Remove(filepath.Join(root(o.Home), pendingName)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return Result{}, e
	}
	r := desired.result(true)
	r.BinarySHA256 = sum(binary)
	return r, nil
}

// Conflicts in an existing, unrecognized software tree must be visible even in
// preview and before lock/directory writes. Repeat under the installation lock.
func previewWindowsRelease(ctx context.Context, release string, files map[string][]byte, repairAllowed bool) (bool, error) {
	if e := verifyRelease(ctx, release, files); e == nil {
		return true, nil
	}
	if e := ctx.Err(); e != nil {
		return false, e
	}
	if _, e := os.Lstat(release); errors.Is(e, os.ErrNotExist) {
		return false, nil
	} else if e != nil {
		return false, e
	}
	if !repairAllowed {
		return false, errors.New("unrecognized staged release is modified; refusing replacement")
	}
	return false, safeBackupTree(ctx, release)
}
func inspectWindowsMetadata(home string, state taskState) (*manifest, *manifest, []byte, error) {
	old, e := optionalManifest(home, manifestName)
	if e != nil {
		return nil, nil, nil, e
	}
	pending, e := optionalManifest(home, pendingName)
	if e != nil {
		return nil, nil, nil, e
	}
	b, e := windowsReadUnit(home)
	if e != nil {
		return nil, nil, nil, e
	}
	if len(b) > 0 {
		recognized := false
		for _, m := range []*manifest{old, pending} {
			if m != nil && sum(b) == m.UnitHash {
				recognized = true
			}
		}
		if !recognized {
			return nil, nil, nil, errors.New("custom or modified managed delivery task definition; refusing replacement")
		}
	}
	if e = acceptTask(state, home, old, pending); e != nil {
		return nil, nil, nil, e
	}
	return old, pending, b, nil
}
func verifyWindowsActivation(ctx context.Context, o Options, d windowsTask) error {
	// Injected Run remains the complete service boundary for isolated tests. Its
	// scheduler state already has action identity+owner checks. Native execution
	// additionally requires a fresh child acknowledgement and sustained liveness.
	// The query operation below performs acknowledgement verification natively.
	state, e := taskCommand(ctx, o, "verify", d, "")
	if e != nil {
		return e
	}
	if !state.Exists || state.State != 4 || state.Definition != d || !state.Enabled {
		return errors.New("managed delivery is not active after start; setup pending, retry repair")
	}
	return nil
}
