//go:build windows

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/XertroV/vecnet/internal/platform"
)

func localRegistryName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 128 && !strings.ContainsAny(name, "/\\:\x00\r\n") && !strings.ContainsFunc(name, func(c rune) bool { return c < 32 || c > 126 }) && !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}
func localRegistryOpen(home string, create bool) (*os.File, error) {
	if create {
		if e := platform.MkdirAll(home, 0700); e != nil {
			return nil, e
		}
	}
	if e := platform.CheckPath(home, true, false); e != nil {
		return nil, e
	}
	path := filepath.Join(home, ".local", "state", "vecnet", "projects")
	// Every ancestor below HOME must remain non-writable by other users, not
	// just the leaf. OpenDir pins them while native relative opens reject
	// junctions and reparse points; checking the parent ACL prevents rename via
	// an otherwise private-looking child of a public namespace.
	if create {
		if e := platform.MkdirAll(path, 0700); e != nil {
			return nil, e
		}
	}
	ancestor := home
	for _, part := range []string{".local", "state", "vecnet", "projects"} {
		ancestor = filepath.Join(ancestor, part)
		if e := platform.CheckPath(ancestor, true, part == "projects"); e != nil {
			return nil, e
		}
	}
	dir, e := platform.OpenDir(path)
	if e != nil {
		return nil, e
	}
	if e = platform.CheckFile(dir, true, true); e != nil {
		dir.Close()
		return nil, e
	}
	return dir, nil
}
func localRegistryFile(dir *os.File, name string, flags int) (*os.File, error) {
	if !localRegistryName(name) {
		return nil, fmt.Errorf("invalid local state file name")
	}
	return platform.OpenAt(dir, name, flags, 0600)
}
func localRegistryLock(ctx context.Context, dir *os.File) (func(), error) {
	return localRegistryNamedLock(ctx, dir, "inventory.lock")
}
func localRegistryNamedLock(ctx context.Context, dir *os.File, name string) (func(), error) {
	f, e := localRegistryFile(dir, name, os.O_RDWR|os.O_CREATE)
	if e != nil {
		return nil, e
	}
	release, e := platform.Lock(ctx, f)
	if e != nil {
		f.Close()
		return nil, e
	}
	return func() { release(); f.Close() }, nil
}
func localRegistryAtomic(dir *os.File, name string, raw []byte) error {
	if !localRegistryName(name) {
		return fmt.Errorf("invalid local state file name")
	}
	f, e := localRegistryFile(dir, name, os.O_RDONLY)
	if e == nil {
		f.Close()
	} else if !os.IsNotExist(e) {
		return e
	}
	var nonce [12]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	tmp := ".tmp-" + hex.EncodeToString(nonce[:])
	f, e = localRegistryFile(dir, tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if e != nil {
		return e
	}
	path := filepath.Join(dir.Name(), tmp)
	defer os.Remove(path)
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return platform.Replace(path, filepath.Join(dir.Name(), name))
}
func localRegistryArtifact(path string) (*os.File, error) {
	// Artifacts may be intentionally shared-readable; require a safe regular
	// object without turning its content into delivery authority.
	return os.Open(path)
}
