//go:build !unix

package cli

import (
	"context"
	"fmt"
)

func lockLegacySourceWith(context.Context, string, string) (func(), error) {
	return nil, fmt.Errorf("safe legacy source locking unsupported on this platform")
}
