// Package platform provides the OS-specific primitives used by private client
// state. Windows permissions are ACLs, not the synthetic Unix mode bits in Stat.
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Home honors an explicit HOME (also useful for isolated installations), then the
// native Windows profile. All configuration remains injectable through getenv.
func Home(getenv func(string) string) string {
	if h := getenv("HOME"); h != "" {
		return h
	}
	if runtime.GOOS == "windows" {
		return getenv("USERPROFILE")
	}
	return ""
}

func Exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// SyncDir is separate from File.Sync: Windows cannot FlushFileBuffers on a
// directory. Replace uses MOVEFILE_WRITE_THROUGH there instead.
func PrivateFile(path string) error { return CheckPath(path, false, true) }

// CreateTemp creates an exclusive owner-private inode even when its parent has
// permissive inherited Windows ACLs. The inode is private before any bytes exist.
func CreateTemp(dir, pattern string) (*os.File, error) {
	for range 100 {
		var nonce [12]byte
		if _, e := rand.Read(nonce[:]); e != nil {
			return nil, e
		}
		name := pattern + hex.EncodeToString(nonce[:])
		if i := strings.LastIndexByte(pattern, '*'); i >= 0 {
			name = pattern[:i] + hex.EncodeToString(nonce[:]) + pattern[i+1:]
		}
		f, e := OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			continue
		}
		return f, e
	}
	return nil, os.ErrExist
}
