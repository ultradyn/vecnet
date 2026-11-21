//go:build unix

package platform

import (
	"errors"
	"testing"
)

func TestOpenWindowsDirAtUnsupported(t *testing.T) {
	if f, err := OpenWindowsDirAt(nil, "child"); f != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Windows-only directory open did not fail closed: %v, %v", f, err)
	}
}
