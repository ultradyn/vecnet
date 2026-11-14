//go:build linux

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Keep the parent descriptors and exact source lock alive throughout the
// operation. Verification is read-only; schema validation belongs to the caller.
func archiveLegacySourceWith(parent context.Context, home, path string, expected []byte, verify func(context.Context) error) error {
	if verify == nil {
		return fmt.Errorf("legacy retirement verifier unavailable")
	}
	if len(expected) > localLegacyProbeLimit {
		return fmt.Errorf("legacy source exceeds archive bound")
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	held, err := acquireLegacySourceWith(ctx, home, path)
	if err != nil {
		return err
	}
	defer held.close()
	name := filepath.Base(path)
	source, err := localRegistryFile(held.dir, name, unix.O_RDONLY)
	if err != nil {
		return fmt.Errorf("cannot safely read legacy source: %w", err)
	}
	defer source.Close()
	original, err := source.Stat()
	if err != nil {
		return err
	}
	if err := localLegacyExpectedSource(source, expected); err != nil {
		return err
	}
	if err := verify(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("legacy archive verification deadline: %w", err)
	}
	if err := held.check(); err != nil {
		return fmt.Errorf("legacy archive parent/lock changed: %w", err)
	}
	// Reopen the name relative to the pinned parent, checking ownership, inode and
	// bytes again after the service/process proof. Never follow absolute parents.
	current, err := localRegistryFile(held.dir, name, unix.O_RDONLY)
	if err != nil {
		return fmt.Errorf("legacy source changed during retirement: %w", err)
	}
	defer current.Close()
	st, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(original, st) {
		return fmt.Errorf("legacy source inode changed during retirement")
	}
	if err := localLegacyExpectedSource(current, expected); err != nil {
		return err
	}
	if err := held.check(); err != nil {
		return fmt.Errorf("legacy archive parent/lock changed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("legacy archive deadline: %w", err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	archive := name + ".retired-" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(nonce[:])
	// RENAME_NOREPLACE preserves any existing recovery file. Unsupported kernels
	// or filesystems fail closed instead of falling back to an overwriting rename.
	fd := int(held.dir.Fd())
	if err := unix.Renameat2(fd, name, fd, archive, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("cannot archive legacy source without replacement: %w", err)
	}
	if err := held.dir.Sync(); err != nil {
		return fmt.Errorf("legacy source archived but parent sync failed: %w", err)
	}
	return nil
}

func localLegacyExpectedSource(f *os.File, expected []byte) error {
	raw, err := io.ReadAll(io.LimitReader(f, localLegacyProbeLimit+1))
	if err != nil {
		return err
	}
	if len(raw) > localLegacyProbeLimit || !bytes.Equal(raw, expected) {
		return fmt.Errorf("legacy bindings changed during retirement")
	}
	return nil
}
