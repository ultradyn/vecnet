package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCodexAuthorityRejectsUnverifiableRows(t *testing.T) {
	for _, field := range []string{"created_at", "expires_at", "config_path", "codex_home", "generation", "caller", "hub"} {
		t.Run(field, func(t *testing.T) {
			f := newBindFixture(t)
			f.bind("p", bindT1)
			if _, err := readCodexAuthority(f.path); err != nil {
				t.Fatal(err)
			}
			rows := f.entries()
			rows[0][field] = json.RawMessage(`""`)
			raw, _ := json.Marshal(map[string]any{"version": 1, "bindings": rows})
			if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCodexAuthority(f.path); err == nil {
				t.Fatal("accepted invalid", field)
			}
		})
	}
}

func TestCodexAuthorityRejectsDuplicateJSONAndPublicMode(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(raw), `"version": 1`, `"version": 1, "version": 1`, 1)
	if duplicate == string(raw) {
		t.Fatal("fixture missing version")
	}
	if err := cliFixtureWriteFile(f.path, []byte(duplicate), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodexAuthority(f.path); err == nil {
		t.Fatal("duplicate keys accepted")
	}
	if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureChmod(f.path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodexAuthority(f.path); err == nil {
		t.Fatal("public authority accepted")
	}
}

func TestCodexAuthorityRejectsAmbiguousScalarSchema(t *testing.T) {
	for _, field := range []string{"Caller", "as", "attended", "idle_dormant_hours"} {
		t.Run(field, func(t *testing.T) {
			f := newBindFixture(t)
			f.bind("p", bindT1)
			rows := f.entries()
			rows[0][field] = json.RawMessage("null")
			if field == "Caller" {
				rows[0][field] = json.RawMessage(`"peer_other"`)
			}
			raw, _ := json.Marshal(map[string]any{"version": 1, "bindings": rows})
			if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCodexAuthority(f.path); err == nil {
				t.Fatal("accepted ambiguous schema", field)
			}
		})
	}
}
