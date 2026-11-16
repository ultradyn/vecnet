package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/platform"
)

type localProject struct {
	Root            string          `json:"root"`
	Directory       string          `json:"directory"`
	Repository      string          `json:"repository,omitempty"`
	Worktree        string          `json:"worktree,omitempty"`
	FirstSeen       time.Time       `json:"first_seen"`
	LastSeen        time.Time       `json:"last_seen"`
	ObservedVersion string          `json:"observed_version"`
	AppliedVersion  string          `json:"applied_version,omitempty"`
	AppliedDigest   string          `json:"applied_digest,omitempty"`
	Artifacts       []localArtifact `json:"artifacts"`
	Status          string          `json:"status,omitempty"`
	Error           string          `json:"error,omitempty"`
	LastMigration   string          `json:"last_migration,omitempty"`
}
type localArtifact struct {
	Path        string    `json:"path"`
	Kind        string    `json:"kind"`
	Version     string    `json:"version"`
	Hash        string    `json:"hash"`
	Schema      int       `json:"schema"`
	InstalledAt time.Time `json:"installed_at"`
}

const localRegistryLimit = 4 << 20

type localRegistryDocument struct {
	Version  int            `json:"version"`
	Projects []localProject `json:"projects"`
}

func localRegistryText(s string) bool {
	return len(s) <= 8192 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func localRegistryPath(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

// Resolve existing ancestors without creating a missing HOME or project. Missing
// project paths remain valid inventory targets for deferred maintenance/forget.
func localRegistryCanonical(path string) (string, error) {
	if !localRegistryPath(path) {
		return "", usageErr("local inventory requires a clean absolute path")
	}
	probe := path
	var tail []string
	for {
		real, err := platform.Canonical(probe)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			if !localRegistryPath(real) {
				return "", usageErr("invalid canonical inventory path")
			}
			return real, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(probe) == probe {
			return "", err
		}
		tail = append(tail, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}
func (a *app) localRegistryDirectory(create bool) (*os.File, error) {
	if a.getenv == nil {
		return nil, usageErr("local inventory requires an absolute HOME")
	}
	home, err := localRegistryCanonical(platform.Home(a.getenv))
	if err != nil {
		return nil, err
	}
	return localRegistryOpen(home, create)
}
func (a *app) localRegistryContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// Validate exact JSON keys, duplicate keys, scalar types and required fields
// before decoding. encoding/json alone accepts case-folded keys and null scalars.
func localRegistryJSON(d *json.Decoder, t reflect.Type) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if t == reflect.TypeFor[time.Time]() {
		if _, ok := token.(string); !ok {
			return usageErr("invalid inventory timestamp")
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return usageErr("invalid inventory object")
		}
		fields := map[string]reflect.StructField{}
		required := map[string]bool{}
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			fields[name] = f
			if !strings.Contains(opts, "omitempty") {
				required[name] = true
			}
		}
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return usageErr("invalid inventory key")
			}
			f, ok := fields[name]
			if !ok || seen[name] {
				return usageErr("unknown or duplicate inventory field: " + name)
			}
			seen[name] = true
			delete(required, name)
			if err := localRegistryJSON(d, f.Type); err != nil {
				return err
			}
		}
		if len(required) != 0 {
			return usageErr("missing required inventory field")
		}
		_, err = d.Token()
		return err
	case reflect.Slice:
		if token != json.Delim('[') {
			return usageErr("invalid inventory array")
		}
		n := 0
		for d.More() {
			n++
			if n > 4096 {
				return usageErr("too many inventory entries")
			}
			if err := localRegistryJSON(d, t.Elem()); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case reflect.String:
		if _, ok := token.(string); !ok {
			return usageErr("invalid inventory string")
		}
	case reflect.Int:
		n, ok := token.(json.Number)
		if !ok {
			return usageErr("invalid inventory integer")
		}
		if _, err := n.Int64(); err != nil {
			return err
		}
	default:
		return usageErr("unsupported inventory field")
	}
	return nil
}
func localRegistryValidate(doc localRegistryDocument) error {
	if doc.Version != 1 || doc.Projects == nil || len(doc.Projects) > 4096 {
		return usageErr("unsupported or invalid local inventory version/schema")
	}
	roots := map[string]bool{}
	for _, p := range doc.Projects {
		if !localRegistryPath(p.Root) || !localRegistryPath(p.Directory) || (p.Directory != p.Root && !strings.HasPrefix(p.Directory, p.Root+string(filepath.Separator))) || roots[p.Root] || p.FirstSeen.IsZero() || p.LastSeen.Before(p.FirstSeen) || p.Artifacts == nil || len(p.Artifacts) > 128 {
			return usageErr("invalid local project identity or metadata")
		}
		roots[p.Root] = true
		if (p.Repository == "") != (p.Worktree == "") || p.Repository != "" && (!localRegistryPath(p.Repository) || !localRegistryPath(p.Worktree)) {
			return usageErr("invalid Git inventory identity")
		}
		for _, s := range []string{p.ObservedVersion, p.AppliedVersion, p.Status, p.Error, p.LastMigration} {
			if !localRegistryText(s) {
				return usageErr("invalid project metadata")
			}
		}
		if p.AppliedDigest != "" && !localRegistryHash(p.AppliedDigest) {
			return usageErr("invalid applied digest")
		}
		paths := map[string]bool{}
		for _, f := range p.Artifacts {
			if !localRegistryPath(f.Path) || paths[f.Path] || f.Kind == "" || !localRegistryText(f.Kind) || !localRegistryText(f.Version) || f.Schema != 1 || !localRegistryHash(f.Hash) || f.InstalledAt.IsZero() {
				return usageErr("invalid managed artifact")
			}
			paths[f.Path] = true
		}
	}
	return nil
}
func localRegistryHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func localRegistryRead(dir *os.File) (localRegistryDocument, error) {
	empty := localRegistryDocument{Version: 1, Projects: []localProject{}}
	f, err := localRegistryFile(dir, "inventory.json", os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, localRegistryLimit+1))
	if err != nil {
		return empty, err
	}
	if len(raw) > localRegistryLimit {
		return empty, usageErr("local inventory exceeds 4 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := localRegistryJSON(d, reflect.TypeFor[localRegistryDocument]()); err != nil {
		return empty, err
	}
	if _, err := d.Token(); err != io.EOF {
		return empty, usageErr("trailing data in local inventory")
	}
	var doc localRegistryDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return empty, err
	}
	if err := localRegistryValidate(doc); err != nil {
		return empty, err
	}
	return doc, nil
}
func (a *app) localProjects() ([]localProject, error) {
	dir, err := a.localRegistryDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return []localProject{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	doc, err := localRegistryRead(dir)
	return doc.Projects, err
}

// Every mutation reads and validates under one stable inode lock, writes a
// fresh owner-only inode, syncs it, renames it, then syncs its directory.
func (a *app) mutateLocalRegistry(create bool, fn func(*localRegistryDocument) error) error {
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 2*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := a.localRegistryDirectory(create)
	if !create && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	unlock, err := localRegistryLock(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	doc, err := localRegistryRead(dir)
	if err != nil {
		return err
	}
	before, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := fn(&doc); err != nil {
		return err
	}
	if err := localRegistryValidate(doc); err != nil {
		return err
	}
	slices.SortFunc(doc.Projects, func(a, b localProject) int { return strings.Compare(a.Root, b.Root) })
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(raw) > localRegistryLimit {
		return usageErr("local inventory exceeds 4 MiB")
	}
	if bytes.Equal(raw, before) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return localRegistryAtomic(dir, "inventory.json", append(raw, '\n'))
}
func (a *app) localProjectIdentity() (localProject, error) {
	dir := a.localProjectRoot
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return localProject{}, err
		}
	}
	dir, err := localRegistryCanonical(dir)
	if err != nil {
		return localProject{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return localProject{}, err
	}
	if !info.IsDir() {
		return localProject{}, usageErr("inventory project must be an existing directory")
	}
	p := localProject{Root: dir, Directory: dir, Artifacts: []localArtifact{}}
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--absolute-git-dir")
	cmd.WaitDelay = 100 * time.Millisecond
	// A caller's GIT_DIR/GIT_WORK_TREE must not redirect inventory discovery.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	out, gitErr := cmd.Output()
	if gitErr == nil {
		paths := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(paths) != 3 {
			return p, usageErr("invalid Git project identity")
		}
		p.Root, err = localRegistryCanonical(filepath.FromSlash(strings.TrimSuffix(paths[0], "\r")))
		if err != nil {
			return p, err
		}
		p.Repository, err = localRegistryCanonical(filepath.FromSlash(strings.TrimSuffix(paths[1], "\r")))
		if err != nil {
			return p, err
		}
		p.Worktree, err = localRegistryCanonical(filepath.FromSlash(strings.TrimSuffix(paths[2], "\r")))
		if err != nil {
			return p, err
		}
	} else if ctx.Err() != nil {
		return p, ctx.Err()
	} // Non-Git directories also work without git installed.
	return p, nil
}
func (a *app) observeLocalProject(version string, now time.Time) (localProject, error) {
	var result localProject
	if now.IsZero() || !localRegistryText(version) {
		return result, usageErr("invalid local observation metadata")
	}
	identity, err := a.localProjectIdentity()
	if err != nil {
		return result, err
	}
	now = now.UTC()
	err = a.mutateLocalRegistry(true, func(doc *localRegistryDocument) error {
		for i := range doc.Projects {
			p := &doc.Projects[i]
			if p.Root == identity.Root {
				p.Directory = identity.Directory
				p.Repository = identity.Repository
				p.Worktree = identity.Worktree
				if now.Before(p.FirstSeen) {
					p.FirstSeen = now
				}
				if now.After(p.LastSeen) {
					p.LastSeen = now
				}
				p.ObservedVersion = version
				result = *p
				return nil
			}
		}
		identity.FirstSeen = now
		identity.LastSeen = now
		identity.ObservedVersion = version
		doc.Projects = append(doc.Projects, identity)
		result = identity
		return nil
	})
	return result, err
}

// Inventory keys outlive the filesystem identities they originally described.
// Exact stored roots must win before consulting today's symlinks/permissions.
func localRegistryProjectIndex(doc *localRegistryDocument, root string) (int, error) {
	if !localRegistryPath(root) {
		return -1, usageErr("local inventory requires a clean absolute project root")
	}
	if i := slices.IndexFunc(doc.Projects, func(p localProject) bool { return p.Root == root }); i >= 0 {
		return i, nil
	}
	canonical, err := localRegistryCanonical(root)
	if err != nil {
		return -1, err
	}
	return slices.IndexFunc(doc.Projects, func(p localProject) bool { return p.Root == canonical }), nil
}

func (a *app) updateLocalProject(root string, fn func(*localProject) error) error {
	if !localRegistryPath(root) {
		return usageErr("local inventory requires a clean absolute project root")
	}
	if fn == nil {
		return usageErr("missing local project update")
	}
	return a.mutateLocalRegistry(true, func(doc *localRegistryDocument) error {
		i, err := localRegistryProjectIndex(doc, root)
		if err != nil {
			return err
		}
		if i < 0 {
			return usageErr("local project is not registered")
		}
		storedRoot := doc.Projects[i].Root
		if err := fn(&doc.Projects[i]); err != nil {
			return err
		}
		if doc.Projects[i].Root != storedRoot {
			return usageErr("local project update cannot change its root")
		}
		return nil
	})
}
func (a *app) registerLocalArtifact(path, kind, version string, now time.Time) error {
	identity, err := a.localProjectIdentity()
	if err != nil {
		return err
	}
	// Installation can be called directly before ordinary startup observation.
	rows, err := a.localProjects()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(rows, func(p localProject) bool { return p.Root == identity.Root }) {
		if _, err := a.observeLocalProject(version, now); err != nil {
			return err
		}
	}
	return a.registerLocalArtifactForProject(identity.Root, path, kind, version, now)
}
func (a *app) registerLocalArtifactForProject(root, path, kind, version string, now time.Time) error {
	if now.IsZero() || kind == "" || !localRegistryText(kind) || !localRegistryText(version) {
		return usageErr("invalid local artifact metadata")
	}
	path, err := localRegistryCanonical(path)
	if err != nil {
		return err
	}
	f, err := localRegistryArtifact(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return usageErr("managed artifact must be a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 16<<20+1))
	if err != nil {
		return err
	}
	if n > 16<<20 {
		return usageErr("managed artifact exceeds 16 MiB")
	}
	artifact := localArtifact{Path: path, Kind: kind, Version: version, Hash: hex.EncodeToString(h.Sum(nil)), Schema: 1, InstalledAt: now.UTC()}
	return a.updateLocalProject(root, func(p *localProject) error {
		for i, old := range p.Artifacts {
			if old.Path == path {
				if old.Kind != kind {
					return usageErr("artifact path already registered with another kind")
				}
				// Adoption is observation, not a successful installation. Preserve
				// the previous hash as drift evidence as well as its applied version.
				if version == "legacy" || old.Hash == artifact.Hash && old.Version == version && old.Schema == artifact.Schema {
					return nil
				}
				p.Artifacts[i] = artifact
				return nil
			}
		}
		p.Artifacts = append(p.Artifacts, artifact)
		return nil
	})
}
func (a *app) cmdProjects(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		jsonOutput := a.json
		for _, arg := range args[1:] {
			if arg != "--json" {
				return usageErr("projects list: unexpected arguments")
			}
			jsonOutput = true
		}
		rows, err := a.localProjects()
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(a.stdout).Encode(rows)
		}
		for _, p := range rows {
			if _, err := fmt.Fprintf(a.stdout, "%q\t%s\t%s\n", p.Root, p.ObservedVersion, p.Status); err != nil {
				return err
			}
		}
		return nil
	case "forget":
		if len(args) != 2 || strings.HasPrefix(args[1], "--") {
			return usageErr("projects forget: expected one absolute project root")
		}
		root := args[1]
		if !localRegistryPath(root) {
			return usageErr("projects forget: expected one absolute project root")
		}
		return a.mutateLocalRegistry(false, func(doc *localRegistryDocument) error {
			i, err := localRegistryProjectIndex(doc, root)
			if err != nil {
				return err
			}
			if i >= 0 {
				doc.Projects = slices.Delete(doc.Projects, i, i+1)
			}
			return nil
		})
	default:
		return usageErr("projects: expected list or forget")
	}
}

// Named state shares the inventory's private directory and validation. Callers
// must hold lockLocalState across read/modify/write; writes are atomic but do
// not acquire a second lock, so enrollment and repair can hold longer locks.
func localStateName(name string) bool {
	if name == "" || len(name) > 100 || strings.HasPrefix(strings.ToLower(name), "inventory") || strings.HasSuffix(strings.ToLower(name), ".lock") {
		return false
	}
	for i, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '.' || c == '_' || c == '-')) {
			return false
		}
	}
	return true
}
func (a *app) readLocalState(name string) ([]byte, error) {
	if !localStateName(name) {
		return nil, usageErr("invalid or reserved local state name")
	}
	dir, err := a.localRegistryDirectory(false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := localRegistryFile(dir, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, localRegistryLimit+1))
	if len(raw) > localRegistryLimit {
		return nil, usageErr("local state exceeds 4 MiB")
	}
	return raw, err
}
func (a *app) writeLocalState(name string, raw []byte) error {
	if !localStateName(name) {
		return usageErr("invalid or reserved local state name")
	}
	if len(raw) > localRegistryLimit {
		return usageErr("local state exceeds 4 MiB")
	}
	dir, err := a.localRegistryDirectory(true)
	if err != nil {
		return err
	}
	defer dir.Close()
	return localRegistryAtomic(dir, name, raw)
}
func (a *app) lockLocalState(ctx context.Context, name string) (func(), error) {
	base := strings.TrimSuffix(name, ".lock")
	if !localStateName(base) {
		return nil, usageErr("invalid or reserved local state lock name")
	}
	if !strings.HasSuffix(name, ".lock") {
		name += ".lock"
	}
	if ctx == nil {
		ctx = a.localRegistryContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := a.localRegistryDirectory(true)
	if err != nil {
		return nil, err
	}
	unlock, err := localRegistryNamedLock(ctx, dir, name)
	if err != nil {
		dir.Close()
		return nil, err
	}
	return func() { unlock(); dir.Close() }, nil
}
