package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/XertroV/vecnet/internal/platform"
)

// cmdLocalPlatform is the narrow native boundary for the bundled Node runtime.
// Node has no built-in Windows ACL or crash-released file locking APIs. These
// helpers operate only on this user's private files and never carry credentials
// in argv, execute shell code, bind an owner, or acknowledge a delivery.
func (a *app) cmdLocalPlatform(args []string) error {
	if len(args) < 2 || !filepath.IsAbs(args[1]) {
		return usageErr("local-platform: expected operation and absolute path")
	}
	op, path := args[0], args[1]
	if op == "check-private" || op == "check-safe" {
		directory := len(args) == 3 && args[2] == "--directory"
		if len(args) != 2 && !directory {
			return usageErr("local-platform: invalid check arguments")
		}
		return platform.CheckPath(path, directory, op == "check-private")
	}
	if len(args) != 2 {
		return usageErr("local-platform: unexpected arguments")
	}
	switch op {
	case "mkdir-private":
		if e := platform.MkdirAll(path, 0700); e != nil {
			return e
		}
		return platform.CheckPath(path, true, true)
	case "read-private":
		f, e := platform.OpenFile(path, os.O_RDONLY, 0)
		if e != nil {
			return e
		}
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
		if e != nil {
			return e
		}
		if len(raw) > 1<<20 {
			return usageErr("private file exceeds 1 MiB")
		}
		_, e = a.stdout.Write(raw)
		return e
	case "write-private":
		raw, e := io.ReadAll(io.LimitReader(a.stdin, 1<<20+1))
		if e != nil {
			return e
		}
		if len(raw) > 1<<20 {
			return usageErr("private file exceeds 1 MiB")
		}
		if e = platform.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return e
		}
		if e = platform.CheckPath(path, false, true); e != nil && !os.IsNotExist(e) {
			return e
		}
		f, e := platform.CreateTemp(filepath.Dir(path), ".private-*")
		if e != nil {
			return e
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		if _, e = f.Write(raw); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		return platform.Replace(tmp, path)
	case "lock":
		if e := platform.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return e
		}
		f, e := platform.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		ok, e := platform.TryLock(f)
		if e != nil {
			return e
		}
		if !ok {
			return errStateBusy
		}
		defer platform.Unlock(f)
		if _, e = fmt.Fprintln(a.stdout, "locked"); e != nil {
			return e
		}
		_, e = io.Copy(io.Discard, a.stdin)
		return e
	default:
		return usageErr("local-platform: unsupported operation")
	}
}
