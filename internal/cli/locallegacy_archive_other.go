//go:build !linux

package cli

import (
	"context"
	"fmt"
)

func archiveLegacySourceWith(context.Context, string, string, []byte, func(context.Context) error) error {
	return fmt.Errorf("safe legacy source archival unsupported on this platform")
}
