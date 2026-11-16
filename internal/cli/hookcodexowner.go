package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/XertroV/vecnet/internal/platform"
)

// Legacy hooks cannot identify their host reliably. Reserve registered Codex
// recipients even after expiry, and serialize legacy delivery with first bind.
// The caller holds this lock through publication, state commit and auto-ack.
func (a *app) lockLegacyHookRecipient(c *client) (func(), error) {
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return nil, err
	}
	if err := platform.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unlock()
		}
	}()
	file, err := readCodexAuthority(path)
	if errors.Is(err, os.ErrNotExist) {
		ok = true
		return unlock, nil
	}
	if err != nil {
		return nil, err
	}
	checkIdentity := false
	recipients := map[string]bool{}
	state, err := statePath(a.getenv, a.namedProfile(), a.as)
	if err != nil {
		return nil, err
	}
	for _, b := range file.Bindings {
		reserved, err := statePathForConfig(b.ConfigPath, b.Profile, b.As)
		if err != nil {
			return nil, err
		}
		if reserved == state {
			return nil, usageErr("delivery state reserved for Codex; use codex status")
		}
		r := normalizeHub(b.Hub) + "\n" + codexRecipient(b)
		recipients[r] = true
		if normalizeHub(b.Hub) == normalizeHub(c.base) {
			checkIdentity = true
		}
	}
	if checkIdentity {
		caller, err := a.codexBindCaller(c)
		if err != nil {
			return nil, err
		}
		recipient := caller
		if a.as != "" {
			recipient = a.as
		}
		if recipients[normalizeHub(c.base)+"\n"+recipient] {
			return nil, usageErr("recipient reserved for Codex automatic delivery; use codex status")
		}
	}
	ok = true
	return unlock, nil
}
