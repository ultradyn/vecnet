package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/XertroV/vecnet/internal/platform"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Only these exact publications may be adopted after an interrupted repair.
// Active intent is recorded before replacing the unit or runtime manifest.
type launcherRepairIntent struct {
	Schema      int    `json:"schema"`
	OldMetadata []byte `json:"old_metadata"`
	NewMetadata []byte `json:"new_metadata"`
	OldManifest []byte `json:"old_manifest"`
	NewManifest []byte `json:"new_manifest"`
	OldUnit     []byte `json:"old_unit"`
	NewUnit     []byte `json:"new_unit"`
	WasActive   bool   `json:"was_active"`
	Enabled     string `json:"enabled"`
	Checksum    string `json:"checksum"`
}

func (p launcherRepairIntent) checksum() string {
	p.Checksum = ""
	return launcherHash(launcherManagedJSON(p))
}

type launcherManagerState struct{ Active, Enabled, Fragment, Dropins, Exec string }

func (a *app) launcherManager(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 15*time.Second)
	defer cancel()
	bin, err := launcherExecutable("systemctl", a.getenv("PATH"))
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--user"}, args...)...)
	cmd.WaitDelay = 100 * time.Millisecond
	raw, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("systemctl %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
func hasLauncherManagerKey(values map[string]string, key string) bool {
	_, ok := values[key]
	return ok
}

func (a *app) launcherManagerInspect(meta launcherManaged, recoveringActive bool) (launcherManagerState, error) {
	if runtime.GOOS == "windows" {
		return a.launcherWindowsManagerInspect(meta, recoveringActive)
	}
	raw, err := a.launcherManager("show", filepath.Base(meta.UnitPath), "--property=LoadState,ActiveState,SubState,UnitFileState,FragmentPath,DropInPaths,ExecStart")
	if err != nil {
		return launcherManagerState{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || hasLauncherManagerKey(values, key) {
			return launcherManagerState{}, errors.New("uncertain service manager response")
		}
		values[key] = value
	}
	for _, key := range []string{"LoadState", "ActiveState", "SubState", "UnitFileState", "FragmentPath", "DropInPaths", "ExecStart"} {
		if _, ok := values[key]; !ok {
			return launcherManagerState{}, fmt.Errorf("service manager omitted %s", key)
		}
	}
	state := launcherManagerState{values["ActiveState"], values["UnitFileState"], values["FragmentPath"], values["DropInPaths"], values["ExecStart"]}
	if values["LoadState"] != "loaded" || state.Fragment != meta.UnitPath || state.Dropins != "" {
		return state, errors.New("service fragment changed or has drop-ins; review launcher unit")
	}
	// A failed start may have stopped the old process. Only the caller's
	// validated durable previously-active intent authorizes retrying that state;
	// initial inspection and final readiness never enable this exception.
	recoveringFailed := recoveringActive && state.Active == "failed" && values["SubState"] == "failed"
	if (state.Active != "active" || values["SubState"] != "running") && (state.Active != "inactive" || values["SubState"] != "dead") && !recoveringFailed {
		return state, errors.New("launcher service state is uncertain; require active/running or inactive/dead")
	}
	if state.Enabled != "enabled" && state.Enabled != "disabled" {
		return state, errors.New("launcher enablement is uncertain; require enabled or disabled")
	}
	return state, nil
}
func launcherManagerExec(state launcherManagerState, m launcherManaged) bool {
	if runtime.GOOS == "windows" {
		return launcherWindowsManagerExec(state, m)
	}
	// systemctl show prints a structured command record. Require one command
	// and exact path/argv, rather than substring-matching an executable name.
	if strings.Count(state.Exec, "argv[]=") != 1 || strings.Count(state.Exec, "path=") != 1 {
		return false
	}
	path, argv := "", ""
	for _, part := range strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(state.Exec, "{"), "}")), " ; ") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "path="); ok {
			path = v
		}
		if v, ok := strings.CutPrefix(part, "argv[]="); ok {
			argv = v
		}
	}
	expected := strings.Join([]string{m.Node, "--disable-warning=ExperimentalWarning", m.Runtime.Entry, "run", "--config", m.ConfigPath}, " ")
	return path == m.Node && argv == expected
}

func launcherValidateMetadata(meta launcherManaged, artifactPath string) error {
	if meta.Schema != 1 || meta.Profile == "" || meta.Version == "" || meta.Path == "" || meta.Node != meta.Runtime.Node || len(meta.Assets) == 0 || len(meta.ProjectRoots) == 0 {
		return errors.New("incomplete managed launcher metadata; reinstall explicitly")
	}
	for _, path := range []string{meta.ConfigPath, meta.UnitPath, meta.ManifestPath, meta.DataPath, meta.VecnetConfig, meta.Home, meta.Node, meta.PublicBin, meta.Runtime.Snapshot, meta.Runtime.Entry, meta.Runtime.VecnetBin} {
		if !localRegistryPath(path) || launcherUncertainManagedPath(path) {
			return errors.New("unsafe or uncertain managed launcher path")
		}
	}
	for _, hash := range []string{meta.UnitSHA256, meta.ManifestSHA256, meta.ConfigSHA256, meta.BinarySHA256} {
		if !localRegistryHash(hash) {
			return errors.New("invalid managed launcher digest")
		}
	}
	idHash := launcherHash([]byte(meta.VecnetConfig + "\x00" + meta.Profile))
	id := "p-" + idHash[:16]
	dir := filepath.Join(filepath.Dir(meta.VecnetConfig), "launcher", id)
	if artifactPath != filepath.Join(dir, "managed.json") || meta.ConfigPath != filepath.Join(dir, "config.json") || meta.ManifestPath != filepath.Join(dir, "install.json") || meta.UnitPath != launcherExpectedUnitPath(meta, id) {
		return errors.New("managed launcher profile paths do not match exact identity")
	}
	digest := filepath.Base(meta.Runtime.Snapshot)
	if !localRegistryHash(digest) || meta.Runtime.Snapshot != filepath.Join(meta.DataPath, "snapshots", digest) || meta.Runtime.Entry != filepath.Join(meta.Runtime.Snapshot, "connectors", "launcher", "src", "main.ts") {
		return errors.New("managed launcher snapshot path changed")
	}
	if meta.Runtime.VecnetBin != meta.PublicBin && meta.Runtime.VecnetBin != filepath.Join(meta.DataPath, "bin", meta.BinarySHA256, platform.Exe("vecnet")) {
		return errors.New("managed launcher binary path changed")
	}
	if meta.Runtime.Source != "embedded" && !localRegistryPath(meta.Runtime.Source) {
		return errors.New("invalid launcher source")
	}
	for name, hash := range meta.Assets {
		if !fs.ValidPath(name) || !strings.HasPrefix(name, "connectors/") || !localRegistryHash(hash) {
			return errors.New("invalid launcher asset inventory")
		}
	}
	for i, root := range meta.ProjectRoots {
		if !localRegistryPath(root) || (i > 0 && meta.ProjectRoots[i-1] >= root) {
			return errors.New("invalid managed project roots")
		}
	}
	return nil
}

func launcherReadPath(path string, private bool) ([]byte, error) {
	root, err := launcherOpenDir(filepath.Dir(path), "", false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if private {
		return launcherManagedRead(root, filepath.Base(path))
	}
	return launcherReadOwned(root, filepath.Base(path), false)
}
func launcherValidateNode(path string) error {
	parent, err := launcherOpenDir(filepath.Dir(path), "", false)
	if err != nil {
		return err
	}
	defer parent.Close()
	info, err := parent.Lstat(filepath.Base(path))
	if err != nil {
		return err
	}
	if err = launcherCheckInfo(parent, filepath.Base(path), info, false, false); err != nil {
		return fmt.Errorf("unsafe launcher Node executable: %w", err)
	}
	if !launcherExecutableInfo(path, info) {
		return errors.New("launcher Node path is no longer executable")
	}
	file, err := parent.Open(filepath.Base(path))
	if err != nil {
		return err
	}
	defer file.Close()
	pinned, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, pinned) {
		return errors.New("launcher Node executable changed during validation")
	}
	return nil
}

func launcherValidateSoftware(meta launcherManaged) error {
	if err := launcherValidateNode(meta.Node); err != nil {
		return err
	}
	hashes, err := launcherSnapshotInventory(meta.Runtime.Snapshot)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(hashes, meta.Assets) {
		return errors.New("launcher snapshot changed; preserve customized runtime")
	}
	// Bind the snapshot name to its complete framed inventory, not just filenames.
	root, err := launcherOpenDir(meta.Runtime.Snapshot, meta.Runtime.Snapshot, false)
	if err != nil {
		return err
	}
	defer root.Close()
	files := map[string][]byte{}
	for name := range hashes {
		raw, e := launcherManagedRead(root, name)
		if e != nil {
			return e
		}
		files[name] = raw
	}
	_, digest, err := launcherAssetDigest(files)
	if err != nil {
		return err
	}
	if filepath.Base(meta.Runtime.Snapshot) != digest {
		return errors.New("launcher snapshot digest changed")
	}
	// A default public location is discovery, not a pinned software artifact:
	// package upgrades may replace/remove it or move between npm and Go roots.
	// Explicit overrides and already-private copies always require exact bytes.
	if !(meta.PublicBinDefault && meta.Runtime.VecnetBin == meta.PublicBin) {
		bin, e := launcherReadPath(meta.Runtime.VecnetBin, false)
		if e != nil {
			return e
		}
		if launcherHash(bin) != meta.BinarySHA256 {
			return errors.New("launcher binary changed; review before repair")
		}
		info, e := os.Lstat(meta.Runtime.VecnetBin)
		if e != nil {
			return e
		}
		if !launcherExecutableInfo(meta.Runtime.VecnetBin, info) {
			return errors.New("launcher binary is not executable")
		}
		if meta.Runtime.VecnetBin != meta.PublicBin && launcherPrivateBinaryMode(meta.Runtime.VecnetBin, info) != nil {
			return errors.New("managed private binary requires mode0500")
		}
	}
	unit, err := launcherUnit(files["connectors/launcher/systemd/vecnet-launcher-profile.service.in"], meta.Runtime, meta.ConfigPath, meta.VecnetConfig, meta.Home, meta.Path)
	if err != nil {
		return err
	}
	if launcherHash([]byte(unit)) != meta.UnitSHA256 {
		return errors.New("launcher service template or environment changed")
	}
	return nil
}

type launcherRepairTarget struct {
	Meta                        launcherManaged
	Raw, Manifest, Unit, Binary []byte
	Files                       map[string][]byte
}

// Pin the executable inode and compare it with the kernel's running image.
// If a package changes the pathname again during this repair, defer rather than
// pairing one binary's embedded assets with another binary's on-disk bytes.
func launcherCurrentBinary() ([]byte, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return nil, errors.New("managed launcher repair requires Linux/systemd or Windows Task Scheduler")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	parent, err := launcherOpenDir(filepath.Dir(executable), "", false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(filepath.Base(executable))
	if err != nil {
		return nil, err
	}
	if err = launcherCheckInfo(parent, filepath.Base(executable), info, false, false); err != nil {
		return nil, err
	}
	file, err := parent.Open(filepath.Base(executable))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	pinned, err := file.Stat()
	if err != nil {
		return nil, err
	}
	running, err := launcherRunningExecutable()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, pinned) || !os.SameFile(pinned, running) {
		return nil, errors.New("running executable pathname changed; retry using the installed CLI")
	}
	return io.ReadAll(file)
}

func launcherDesired(old launcherManaged) (launcherRepairTarget, error) {
	target := launcherRepairTarget{Meta: old}
	files, digest, err := launcherAssets("embedded")
	if err != nil {
		return target, err
	}
	target.Files = files
	target.Binary, err = launcherCurrentBinary()
	if err != nil {
		return target, err
	}
	target.Meta.Version = localSoftwareVersion()
	target.Meta.BinarySHA256 = launcherHash(target.Binary)
	target.Meta.Assets = map[string]string{}
	for name, raw := range files {
		target.Meta.Assets[name] = launcherHash(raw)
	}
	snapshot := filepath.Join(old.DataPath, "snapshots", digest)
	target.Meta.Runtime = launcherInstall{Node: old.Node, Entry: filepath.Join(snapshot, "connectors", "launcher", "src", "main.ts"), VecnetBin: filepath.Join(old.DataPath, "bin", target.Meta.BinarySHA256, platform.Exe("vecnet")), Source: "embedded", Snapshot: snapshot}
	target.Manifest = launcherManagedJSON(target.Meta.Runtime)
	target.Meta.ManifestSHA256 = launcherHash(target.Manifest)
	unit, err := launcherUnit(files["connectors/launcher/systemd/vecnet-launcher-profile.service.in"], target.Meta.Runtime, old.ConfigPath, old.VecnetConfig, old.Home, old.Path)
	if err != nil {
		return target, err
	}
	target.Unit = []byte(unit)
	target.Meta.UnitSHA256 = launcherHash(target.Unit)
	target.Raw = launcherManagedJSON(target.Meta)
	return target, nil
}

// Immutable receipt files are published under the profile lock.
func launcherImmutable(root *launcherRoot, name string, raw []byte, mode fs.FileMode) error {
	validate := func() error {
		got, e := launcherReadOwned(root, name, true)
		if e != nil {
			return e
		}
		info, e := root.Lstat(name)
		if e != nil {
			return e
		}
		if !bytes.Equal(got, raw) || launcherCheckMode(root, name, info, mode) != nil {
			return errors.New("immutable launcher file changed")
		}
		return nil
	}
	if _, err := root.Lstat(name); err == nil {
		return validate()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := launcherTempName(".immutable-")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(raw)
	if err == nil {
		err = launcherSetMode(f, mode)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, e := root.Lstat(name); e == nil {
		return validate()
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err = root.Rename(tmp, name); err != nil {
		return err
	}
	if err = validate(); err != nil {
		return err
	}
	return launcherSyncDir(root)
}
func launcherSyncSnapshot(path string) error {
	root, err := launcherOpenDir(path, path, false)
	if err != nil {
		return err
	}
	defer root.Close()
	dirs := []string{}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, name)
			return nil
		}
		var f *os.File
		var e error
		if runtime.GOOS == "windows" {
			f, e = root.OpenFile(name, os.O_RDWR, 0)
		} else {
			f, e = root.Open(name)
		}
		if e != nil {
			return e
		}
		e = f.Sync()
		f.Close()
		return e
	})
	if err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		f, e := root.Open(dir)
		if e != nil {
			return e
		}
		e = platform.SyncDir(f)
		f.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func launcherStage(target launcherRepairTarget) error {
	snapshots, err := launcherOpenDir(filepath.Dir(target.Meta.Runtime.Snapshot), target.Meta.DataPath, true)
	if err != nil {
		return err
	}
	defer snapshots.Close()
	if err = launcherSnapshot(snapshots, filepath.Base(target.Meta.Runtime.Snapshot), target.Files); err != nil {
		return err
	}
	if err = launcherSyncSnapshot(target.Meta.Runtime.Snapshot); err != nil {
		return err
	}
	if err = launcherSyncDir(snapshots); err != nil {
		return err
	}
	if err = launcherStageBinary(target); err != nil {
		return err
	}
	// Persist newly created directory entries all the way to the data boundary.
	for _, path := range []string{filepath.Dir(filepath.Dir(target.Meta.Runtime.VecnetBin)), target.Meta.DataPath, filepath.Dir(target.Meta.DataPath)} {
		root, e := launcherOpenDir(path, "", false)
		if e != nil {
			return e
		}
		e = launcherSyncDir(root)
		root.Close()
		if e != nil {
			return e
		}
	}
	return launcherValidateSoftware(target.Meta)
}

// Publish the entire digest directory atomically. An interrupted stage leaves
// only an unreferenced temporary directory, never a partial executable target.
func launcherStageBinary(target launcherRepairTarget) error {
	parentPath := filepath.Dir(filepath.Dir(target.Meta.Runtime.VecnetBin))
	parent, err := launcherOpenDir(parentPath, target.Meta.DataPath, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(filepath.Dir(target.Meta.Runtime.VecnetBin))
	validate := func() error {
		dir, e := launcherOpenDir(filepath.Join(parentPath, name), target.Meta.DataPath, false)
		if e != nil {
			return e
		}
		defer dir.Close()
		entries, e := fs.ReadDir(dir.FS(), ".")
		if e != nil {
			return e
		}
		if len(entries) != 1 || entries[0].Name() != platform.Exe("vecnet") {
			return errors.New("unexpected private CLI directory content")
		}
		raw, e := launcherReadOwned(dir, platform.Exe("vecnet"), true)
		if e != nil {
			return e
		}
		info, e := dir.Lstat(platform.Exe("vecnet"))
		if e != nil {
			return e
		}
		if !bytes.Equal(raw, target.Binary) || launcherCheckMode(dir, platform.Exe("vecnet"), info, 0o500) != nil {
			return errors.New("immutable private CLI changed")
		}
		return nil
	}
	if _, e := parent.Lstat(name); e == nil {
		return validate()
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	tmp := launcherTempName(".cli-")
	if err = parent.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	defer parent.RemoveAll(tmp)
	staging, err := parent.OpenRoot(tmp)
	if err != nil {
		return err
	}
	err = launcherDurableWrite(staging, platform.Exe("vecnet"), target.Binary, 0o500)
	staging.Close()
	if err != nil {
		return err
	}
	if err = parent.Rename(tmp, name); err != nil {
		if _, e := parent.Lstat(name); e != nil {
			return err
		}
	}
	if err = validate(); err != nil {
		return err
	}
	return launcherSyncDir(parent)
}

func launcherPublishCAS(root *launcherRoot, name string, old, new []byte) error {
	current, err := launcherManagedRead(root, name)
	if err != nil {
		return err
	}
	if bytes.Equal(current, new) {
		return nil
	}
	if !bytes.Equal(current, old) {
		return errors.New("launcher publication changed; preserve file and inspect pending repair")
	}
	return launcherDurableWrite(root, name, new, 0o600)
}

// repairLauncherArtifact never writes the registry. MAIN owns the per-project
// registry CAS and only receives a refreshed artifact after verification.
func (a *app) repairLauncherArtifact(projectRoot string, artifact localArtifact) (done localArtifact, err error) {
	done = artifact
	defer func() {
		if err != nil {
			err = fmt.Errorf("managed launcher upgrade pending: %w", err)
		}
	}()
	ctx := a.localRegistryContext()
	if err = ctx.Err(); err != nil {
		return done, err
	}
	if artifact.Kind != "launcher-profile" || artifact.Schema != 1 || !localRegistryHash(artifact.Hash) || !localRegistryPath(artifact.Path) || filepath.Base(artifact.Path) != "managed.json" {
		return done, errors.New("missing/legacy launcher ownership; run launcher install --write explicitly")
	}
	root, err := launcherOpenDir(filepath.Dir(artifact.Path), filepath.Dir(artifact.Path), false)
	if err != nil {
		return done, err
	}
	defer root.Close()
	if err = launcherValidateLock(root, "install.json.lock", false); err != nil {
		return done, err
	}
	lock, err := root.OpenFile("install.json.lock", os.O_RDWR, 0)
	if err != nil {
		return done, err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return done, err
	}
	if err = launcherCheckFile(lock, false, true); err != nil {
		return done, err
	}
	pinned, err := root.Lstat("install.json.lock")
	if err != nil || !os.SameFile(info, pinned) {
		return done, errors.New("launcher lock changed")
	}
	for {
		if err = ctx.Err(); err != nil {
			return done, err
		}
		var held bool
		held, err = launcherTryLock(lock)
		if err != nil {
			return done, err
		}
		if held {
			break
		}
		if !sleepCtx(ctx, 10*time.Millisecond) {
			return done, ctx.Err()
		}
	}
	lockNow, e := root.Lstat("install.json.lock")
	if e != nil || !os.SameFile(info, lockNow) {
		return done, errors.New("launcher lock changed while waiting")
	}
	current, err := launcherManagedRead(root, "managed.json")
	if err != nil {
		return done, err
	}
	var intent *launcherRepairIntent
	pending, e := launcherManagedRead(root, "repair-pending.json")
	if e == nil {
		intent = &launcherRepairIntent{}
		if err = launcherDecode(pending, intent); err != nil {
			return done, err
		}
		if intent.Schema != 1 || intent.Checksum != intent.checksum() {
			return done, errors.New("corrupt launcher repair intent; inspect before retry")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return done, e
	}
	baseline := current
	if launcherHash(current) != artifact.Hash {
		if intent != nil && launcherHash(intent.OldMetadata) == artifact.Hash {
			baseline = intent.OldMetadata
		} else {
			history, e := launcherOpenDir(filepath.Join(filepath.Dir(artifact.Path), "repair-history"), filepath.Dir(artifact.Path), false)
			if e != nil {
				return done, errors.New("managed metadata hash changed; review before repair")
			}
			baseline, e = launcherManagedRead(history, artifact.Hash+".json")
			history.Close()
			if e != nil || launcherHash(baseline) != artifact.Hash {
				return done, errors.New("managed metadata hash changed; no exact generation receipt")
			}
		}
	}
	var meta launcherManaged
	if err = launcherDecode(baseline, &meta); err != nil {
		return done, err
	}
	if err = launcherValidateMetadata(meta, artifact.Path); err != nil {
		return done, err
	}
	if !launcherHasProject(meta.ProjectRoots, projectRoot) {
		return done, errors.New("project root is not enrolled in this managed launcher")
	}
	resolved, e := localRegistryCanonical(projectRoot)
	if e != nil || resolved != projectRoot {
		return done, errors.New("managed project root moved or became a symlink")
	}
	target, err := launcherDesired(meta)
	if err != nil {
		return done, err
	}
	// Explicit source selection pins user code. Only an installed inventory
	// identical to this embedded bundle can be adopted without replacing it.
	if !meta.SourceDefault && !reflect.DeepEqual(meta.Assets, target.Meta.Assets) {
		return done, errors.New("explicit launcher source differs from bundled runtime; preserve custom code and reinstall explicitly to change source")
	}

	var old launcherManaged
	var oldRaw []byte
	if intent != nil {
		if !bytes.Equal(intent.NewMetadata, target.Raw) || !bytes.Equal(intent.NewUnit, target.Unit) || !bytes.Equal(intent.NewManifest, target.Manifest) {
			return done, errors.New("pending target differs from current software; inspect or reinstall explicitly")
		}
		if err = launcherDecode(intent.OldMetadata, &old); err != nil {
			return done, err
		}
		if err = launcherValidateMetadata(old, artifact.Path); err != nil {
			return done, err
		}
		regenerated, e := launcherDesired(old)
		if e != nil || !bytes.Equal(regenerated.Raw, target.Raw) {
			return done, errors.New("pending repair changed protected launcher identity")
		}
		if launcherHash(intent.OldUnit) != old.UnitSHA256 || launcherHash(intent.OldManifest) != old.ManifestSHA256 {
			return done, errors.New("corrupt pending original publications")
		}
		oldRaw = intent.OldMetadata
		if !bytes.Equal(current, oldRaw) && !bytes.Equal(current, target.Raw) {
			return done, errors.New("managed metadata changed during pending repair")
		}
	} else {
		old = meta
		oldRaw = baseline
		if !bytes.Equal(current, baseline) && !bytes.Equal(current, target.Raw) {
			return done, errors.New("managed metadata differs from verified target")
		}
	}
	config, err := launcherManagedRead(root, filepath.Base(meta.ConfigPath))
	if err != nil {
		return done, err
	}
	if launcherHash(config) != meta.ConfigSHA256 {
		return done, errors.New("launcher config changed; review and reinstall explicitly")
	}
	unitRoot, err := launcherOpenDir(filepath.Dir(meta.UnitPath), filepath.Dir(meta.UnitPath), false)
	if err != nil {
		return done, err
	}
	defer unitRoot.Close()
	unit, err := launcherManagedRead(unitRoot, filepath.Base(meta.UnitPath))
	if err != nil {
		return done, err
	}
	manifest, err := launcherManagedRead(root, "install.json")
	if err != nil {
		return done, err
	}
	if intent == nil && bytes.Equal(current, target.Raw) {
		if !bytes.Equal(unit, target.Unit) || !bytes.Equal(manifest, target.Manifest) {
			return done, errors.New("installed launcher files changed")
		}
		if err = launcherValidateSoftware(target.Meta); err != nil {
			return done, err
		}
		state, e := a.launcherManagerInspect(target.Meta, false)
		if e != nil {
			return done, e
		}
		if !launcherManagerExec(state, target.Meta) {
			return done, errors.New("service executable differs from verified launcher")
		}
		done.Hash = launcherHash(target.Raw)
		done.Version = target.Meta.Version
		return done, nil
	}
	if intent == nil {
		if launcherHash(unit) != meta.UnitSHA256 || launcherHash(manifest) != meta.ManifestSHA256 {
			return done, errors.New("launcher unit or manifest changed; preserve custom installation")
		}
		var runtime launcherInstall
		if err = launcherDecode(manifest, &runtime); err != nil {
			return done, err
		}
		if runtime != meta.Runtime {
			return done, errors.New("launcher runtime metadata changed")
		}
		if err = launcherValidateSoftware(meta); err != nil {
			return done, err
		}
	} else {
		if (!bytes.Equal(unit, intent.OldUnit) && !bytes.Equal(unit, intent.NewUnit)) || (!bytes.Equal(manifest, intent.OldManifest) && !bytes.Equal(manifest, intent.NewManifest)) {
			return done, errors.New("pending publication drift; preserve files and inspect repair")
		}
		if err = launcherValidateSoftware(old); err != nil {
			return done, err
		}
	}
	// Identity, intent checksum, exact old/new publications and original
	// software have been validated above. Enablement and command checks below
	// remain mandatory before resuming even a formerly-active failed service.
	state, err := a.launcherManagerInspect(meta, intent != nil && intent.WasActive)
	if err != nil {
		return done, err
	}
	if intent == nil {
		if !launcherManagerExec(state, meta) {
			return done, errors.New("service executable differs from managed launcher")
		}
		if err = launcherStage(target); err != nil {
			return done, err
		}
		// Staging may take time; do not capture stale service intent.
		again, e := a.launcherManagerInspect(meta, false)
		if e != nil {
			return done, e
		}
		if again != state {
			return done, errors.New("launcher manager state changed during staging")
		}
		// A config edit or cooperating publication during staging is drift, not
		// permission to regenerate a customized installation.
		configNow, e := launcherManagedRead(root, filepath.Base(meta.ConfigPath))
		if e != nil {
			return done, e
		}
		unitNow, e := launcherManagedRead(unitRoot, filepath.Base(meta.UnitPath))
		if e != nil {
			return done, e
		}
		manifestNow, e := launcherManagedRead(root, "install.json")
		if e != nil {
			return done, e
		}
		metadataNow, e := launcherManagedRead(root, "managed.json")
		if e != nil {
			return done, e
		}
		if !bytes.Equal(configNow, config) || !bytes.Equal(unitNow, unit) || !bytes.Equal(manifestNow, manifest) || !bytes.Equal(metadataNow, baseline) {
			return done, errors.New("launcher files changed during staging; preserve installation")
		}
		if err = ctx.Err(); err != nil {
			return done, err
		}
		intent = &launcherRepairIntent{Schema: 1, OldMetadata: baseline, NewMetadata: target.Raw, OldManifest: manifest, NewManifest: target.Manifest, OldUnit: unit, NewUnit: target.Unit, WasActive: state.Active == "active", Enabled: state.Enabled}
		intent.Checksum = intent.checksum()
		if err = launcherDurableWrite(root, "repair-pending.json", launcherManagedJSON(intent), 0o600); err != nil {
			return done, err
		}
	} else {
		if intent.Enabled != "enabled" && intent.Enabled != "disabled" {
			return done, errors.New("corrupt original enablement intent")
		}
		if state.Enabled != intent.Enabled || (!intent.WasActive && state.Active != "inactive") {
			return done, errors.New("launcher service intent changed; preserve current state")
		}
		if !launcherManagerExec(state, old) && !launcherManagerExec(state, target.Meta) {
			return done, errors.New("pending service executable changed")
		}
		if err = launcherStage(target); err != nil {
			return done, err
		}
	}
	if err = ctx.Err(); err != nil {
		return done, err
	}
	if err = launcherPublishCAS(unitRoot, filepath.Base(meta.UnitPath), intent.OldUnit, intent.NewUnit); err != nil {
		return done, err
	}
	if err = launcherPublishCAS(root, "install.json", intent.OldManifest, intent.NewManifest); err != nil {
		return done, err
	}
	if err = a.launcherManagerReload(meta, intent); err != nil {
		return done, err
	}
	if intent.WasActive {
		if err = a.launcherManagerRestart(target.Meta); err != nil {
			return done, err
		}
	}
	final, err := a.launcherManagerInspect(target.Meta, false)
	if err != nil {
		return done, err
	}
	wantActive := "inactive"
	if intent.WasActive {
		wantActive = "active"
	}
	if final.Active != wantActive || final.Enabled != intent.Enabled || !launcherManagerExec(final, target.Meta) {
		return done, errors.New("launcher final service/software verification failed")
	}
	// Recheck exact files after manager actions, before advancing applied metadata.
	if config, err = launcherManagedRead(root, filepath.Base(meta.ConfigPath)); err != nil {
		return done, err
	}
	if launcherHash(config) != meta.ConfigSHA256 {
		return done, errors.New("launcher config changed during activation")
	}
	if unit, err = launcherManagedRead(unitRoot, filepath.Base(meta.UnitPath)); err != nil {
		return done, err
	}
	if !bytes.Equal(unit, target.Unit) {
		return done, errors.New("launcher unit changed during activation")
	}
	if manifest, err = launcherManagedRead(root, "install.json"); err != nil {
		return done, err
	}
	if !bytes.Equal(manifest, target.Manifest) {
		return done, errors.New("launcher manifest changed during activation")
	}
	if err = launcherValidateSoftware(target.Meta); err != nil {
		return done, err
	}
	history, err := launcherOpenDir(filepath.Join(filepath.Dir(artifact.Path), "repair-history"), filepath.Dir(artifact.Path), true)
	if err != nil {
		return done, err
	}
	err = launcherImmutable(history, launcherHash(oldRaw)+".json", oldRaw, 0o600)
	history.Close()
	if err != nil {
		return done, err
	}
	if err = launcherSyncDir(root); err != nil {
		return done, err
	}
	if err = ctx.Err(); err != nil {
		return done, err
	}
	if err = launcherPublishCAS(root, "managed.json", oldRaw, target.Raw); err != nil {
		return done, err
	}
	if err = root.Remove("repair-pending.json"); err != nil {
		return done, err
	}
	if err = launcherSyncDir(root); err != nil {
		return done, err
	}
	done.Hash = launcherHash(target.Raw)
	done.Version = target.Meta.Version
	return done, nil
}

func launcherHasProject(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
