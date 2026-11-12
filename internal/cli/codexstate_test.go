package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexStrictStateRefusesAmbiguousClaims(t *testing.T) {
	for _, raw := range []string{`{"claims":[{"Accepted":true,"accepted":false}]}`, `{"claims":[{"Accepted":null}]}`, `{"caller":"one","Caller":"two"}`, `{"Claims":[],"Claimſ":[]}`} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		err := codexUpdateState(t.Context(), path, false, func(*hookState) error { called = true; return nil })
		after, _ := os.ReadFile(path)
		if err == nil || called || string(after) != raw {
			t.Fatalf("ambiguous state mutated: %s", after)
		}
	}
}

func TestCodexStrictStateMigratesLegacyBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"shown":null,"session":"legacy-root","session_how":"rebind"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := codexUpdateState(t.Context(), path, false, func(s *hookState) error { s.AppCursor = "e1.0"; return nil }); err != nil {
		t.Fatal(err)
	}
	state := readHookState(t, path)
	if state.Session != "legacy-root" || state.SessionHow != "rebind" {
		t.Fatalf("lost legacy binding: %+v", state.hookBinding)
	}
}

func TestCodexStrictStatePreservesMalformedLegacyBinding(t *testing.T) {
	for _, raw := range []string{
		`{"shown":null,"session":17}`,
		`{"shown":null,"session":"root","session_at":"invalid"}`,
		`{"shown":null,"session":"root","claude_proc":{"pid":"invalid"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			err := codexUpdateState(t.Context(), path, false, func(*hookState) error { called = true; return nil })
			after, readErr := os.ReadFile(path)
			if err == nil || called || readErr != nil || string(after) != raw {
				t.Fatalf("malformed legacy binding changed: err=%v called=%v state=%s readErr=%v", err, called, after, readErr)
			}
			if _, err := os.Stat(bindingPath(path)); !os.IsNotExist(err) {
				t.Fatalf("refusal created sidecar: %v", err)
			}
		})
	}
}

func TestCodexStrictStateInitializesMissingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "state.json")
	if err := codexUpdateState(t.Context(), path, true, func(s *hookState) error { s.AppCursor = "e1.0"; return nil }); err != nil {
		t.Fatal(err)
	}
	if state := readHookState(t, path); state.AppCursor != "e1.0" {
		t.Fatalf("missing initial state: %+v", state)
	}
}

func TestCodexStrictStatePreservesUnchangedSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"shown":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"session\": \"S1\", \"operator_annotation\": \"keep\"\n}\n")
	if err := os.WriteFile(bindingPath(path), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := codexUpdateState(t.Context(), path, false, func(s *hookState) error { s.AppCursor = "e1.0"; return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(bindingPath(path))
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("sidecar was rewritten: %s %v", after, err)
	}
}

func TestCodexDurableWriteReplacesPrivateSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexDurable(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "new" {
		t.Fatalf("snapshot not replaced: %s %v", raw, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot not private: %v %v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v %v", entries, err)
	}
}

func TestCodexDurableWriteCleansUpFailedRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexDurable(path, []byte("new")); err == nil {
		t.Fatal("replaced directory with snapshot")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "state.json" || !entries[0].IsDir() {
		t.Fatalf("failed replacement changed destination or left temporary file: %v %v", entries, err)
	}
}
