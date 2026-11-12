//go:build unix

package cli

import (
	"io"
	"os"
	"syscall"
)

// codexBindingsEUID is the owner a bindings file must have; tests replace it.
var codexBindingsEUID = os.Geteuid

// readCodexBindings reads the bindings file through one descriptor and checks
// that inode before any row is decoded: a regular file, owned by us, with no
// group or world write bit. Rows another user could have edited are never
// turned into trusted opt-ins by an unrelated bind/unbind preserving them in
// a fresh 0600 file. An owner-owned 0644 file passes and is rewritten 0600.
// O_NONBLOCK keeps a FIFO at the path from blocking the open.
func readCodexBindings(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.Mode().IsRegular():
		return nil, unsafeCodexBindings(path, "not a regular file")
	case !ok || int(st.Uid) != codexBindingsEUID():
		return nil, unsafeCodexBindings(path, "owned by another user")
	case info.Mode().Perm()&0o022 != 0:
		return nil, unsafeCodexBindings(path, "group or world writable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(raw) > 1<<20 {
		return nil, unsafeCodexBindings(path, "exceeds 1 MiB")
	}
	return raw, err
}
