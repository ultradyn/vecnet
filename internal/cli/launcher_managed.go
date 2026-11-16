package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XertroV/vecnet/internal/platform"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// launcherManaged is ownership evidence, separate from the runtime's five-path
// manifest. It contains no credential or mutable job/authority state.
type launcherManaged struct {
	Schema           int               `json:"schema"`
	ConfigPath       string            `json:"config_path"`
	Profile          string            `json:"profile"`
	UnitPath         string            `json:"unit_path"`
	ManifestPath     string            `json:"manifest_path"`
	DataPath         string            `json:"data_path"`
	VecnetConfig     string            `json:"vecnet_config"`
	Home             string            `json:"home"`
	Path             string            `json:"path"`
	Node             string            `json:"node"`
	PublicBin        string            `json:"public_bin"`
	PublicBinDefault bool              `json:"public_bin_default"`
	Source           string            `json:"source"`
	SourceDefault    bool              `json:"source_default"`
	UnitSHA256       string            `json:"unit_sha256"`
	ManifestSHA256   string            `json:"manifest_sha256"`
	ConfigSHA256     string            `json:"config_sha256"`
	Version          string            `json:"version"`
	BinarySHA256     string            `json:"binary_sha256"`
	ProjectRoots     []string          `json:"project_roots"`
	Assets           map[string]string `json:"assets"`
	Runtime          launcherInstall   `json:"runtime"`
}

func launcherHash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func launcherManagedJSON(value any) []byte {
	raw, _ := json.MarshalIndent(value, "", "  ")
	return append(raw, '\n')
}
func launcherDecode(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing launcher metadata")
	}
	// Reject duplicate keys, including nested objects, before any mutation.
	d = json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("duplicate launcher metadata key")
				}
				seen[key] = true
				if e = walk(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid launcher metadata")
		}
		_, err = d.Token()
		return err
	}
	return walk()
}

func launcherManagedRead(root *launcherRoot, name string) ([]byte, error) {
	raw, err := launcherReadOwned(root, name, true)
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if launcherCheckMode(root, name, info, 0o600) != nil || len(raw) > 16<<20 {
		return nil, errors.New("launcher managed file requires mode0600 and bounded size")
	}
	return raw, nil
}
func launcherSyncDir(root *launcherRoot) error {
	f, e := root.Open(".")
	if e != nil {
		return e
	}
	defer f.Close()
	return platform.SyncDir(f)
}

// Every rename that authorizes a later publication is durable before returning.
func launcherDurableWrite(root *launcherRoot, name string, raw []byte, mode fs.FileMode) error {
	if _, err := launcherReadOwned(root, name, true); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := launcherTempName(".managed-")
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
	if err = root.Rename(tmp, name); err != nil {
		return err
	}
	return launcherSyncDir(root)
}

func launcherSnapshotInventory(snapshot string) (map[string]string, error) {
	root, err := launcherOpenDir(snapshot, snapshot, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	hashes := map[string]string{}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, e := root.Lstat(name)
		if e != nil {
			return e
		}
		if e = launcherCheckInfo(root, name, info, entry.IsDir(), true); e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		raw, e := launcherManagedRead(root, name)
		if e != nil {
			return e
		}
		hashes[filepath.ToSlash(name)] = launcherHash(raw)
		return nil
	})
	return hashes, err
}

func (a *app) registerLauncherInstallation(p launcherPaths, m launcherInstall, unit []byte, vecnetConfig, searchPath string, publicBinDefault, sourceDefault bool) error {
	root, err := launcherOpenDir(filepath.Dir(p.Manifest), filepath.Dir(p.Manifest), false)
	if err != nil {
		return err
	}
	defer root.Close()
	manifest, err := launcherManagedRead(root, filepath.Base(p.Manifest))
	if err != nil {
		return err
	}
	config, err := launcherManagedRead(root, filepath.Base(p.Config))
	if err != nil {
		return err
	}
	var cfg struct {
		Projects map[string]struct {
			RepoRoot string `json:"repo_root"`
			Scratch  bool   `json:"scratch"`
		} `json:"projects"`
	}
	if err = json.Unmarshal(config, &cfg); err != nil {
		return err
	}
	roots := []string{}
	seen := map[string]bool{}
	for _, project := range cfg.Projects {
		if project.Scratch {
			continue
		}
		worker := *a
		worker.localProjectRoot = project.RepoRoot
		identity, e := worker.localProjectIdentity()
		if e != nil {
			return e
		}
		if !seen[identity.Root] {
			seen[identity.Root] = true
			roots = append(roots, identity.Root)
		}
	}
	sort.Strings(roots)
	bin, err := os.ReadFile(m.VecnetBin)
	if err != nil {
		return err
	}
	assets, err := launcherSnapshotInventory(m.Snapshot)
	if err != nil {
		return err
	}
	meta := launcherManaged{Schema: 1, ConfigPath: p.Config, Profile: p.Name, UnitPath: p.Unit, ManifestPath: p.Manifest, DataPath: p.Data, VecnetConfig: vecnetConfig, Home: platform.Home(a.getenv), Path: searchPath, Node: m.Node, PublicBin: m.VecnetBin, PublicBinDefault: publicBinDefault, Source: m.Source, SourceDefault: sourceDefault, UnitSHA256: launcherHash(unit), ManifestSHA256: launcherHash(manifest), ConfigSHA256: launcherHash(config), Version: localSoftwareVersion(), BinarySHA256: launcherHash(bin), ProjectRoots: roots, Assets: assets, Runtime: m}
	// Explicit installation is the adoption boundary. Refuse an unsafe sibling;
	// never use runtime state or a service name alone as ownership evidence.
	if err = launcherDurableWrite(root, "managed.json", launcherManagedJSON(meta), 0o600); err != nil {
		return err
	}
	now := time.Now()
	for _, projectRoot := range roots {
		worker := *a
		worker.localProjectRoot = projectRoot
		if _, err = worker.observeLocalProject(meta.Version, now); err != nil {
			return fmt.Errorf("observe launcher project: %w", err)
		}
		if err = worker.registerLocalArtifactForProject(projectRoot, filepath.Join(filepath.Dir(p.Manifest), "managed.json"), "launcher-profile", meta.Version, now); err != nil {
			return err
		}
	}
	return nil
}
