package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Go accepts case-insensitive struct fields and null scalars. Neither is safe
// for recovery evidence: accepted:true must not be overridden by accepted:null.
func validateCodexStateJSON(raw []byte) error {
	n, err := parseJSONNode(raw)
	if err != nil || n.kind != '{' {
		return usageErr("corrupt Codex recovery state")
	}
	var valid func(*jsonNode, string) bool
	valid = func(n *jsonNode, key string) bool {
		if bytes.Equal(n.raw, []byte("null")) {
			return key == "shown" || key == "claims" || key == "replay" || key == "noticed"
		}
		seen := []string{}
		for i, child := range n.vals {
			name := ""
			if n.kind == '{' {
				if json.Unmarshal(n.keys[i], &name) != nil {
					return false
				}
				for _, previous := range seen {
					if strings.EqualFold(previous, name) {
						return false
					}
				}
				seen = append(seen, name)
				name = strings.ToLower(name)
			}
			if !valid(child, name) {
				return false
			}
		}
		return true
	}
	if !valid(n, "") {
		return usageErr("ambiguous Codex recovery state")
	}
	return nil
}

// Sync the private temporary inode before replacing the previous snapshot,
// then sync the rename before acknowledging a claim, ownership transfer, or send.
// Sync ancestors too because the state directory may have just been created.
func writeCodexDurable(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncCodexDirectories(dir)
}

func syncCodexDirectories(dir string) error {
	for {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func removeCodexPending(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncCodexDirectories(filepath.Dir(path))
}
