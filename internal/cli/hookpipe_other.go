//go:build !linux

package cli

import (
	"context"
	"io"
)

// hookAtomicPipe is Linux-only: elsewhere PIPE_BUF can be as small as 512
// bytes, below one maximal item, so output keeps the bounded plain write.
func hookAtomicPipe(io.Writer) (func(context.Context, []byte) error, bool) {
	return nil, false
}
