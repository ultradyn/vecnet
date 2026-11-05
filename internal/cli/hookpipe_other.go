//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package cli

import (
	"context"
	"errors"
	"io"
	"os"
)

var errHookPipeNotAtomic = errors.New("hook output: this platform has no all-or-nothing pipe write")

// hookAtomicPipe: here (Windows included) nothing reports a pipe's free
// space or makes a large write atomic, so a write could leave a truncated
// JSON prefix on a reader that stopped. A native pipe therefore receives no
// output at all; the deliveries stay unclaimed for `vecnet inbox` and the
// next run (PROTOCOL §13.9 "Never block a turn").
func hookAtomicPipe(w io.Writer) (func(context.Context, []byte) error, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, false
	}
	return func(context.Context, []byte) error { return errHookPipeNotAtomic }, true
}
