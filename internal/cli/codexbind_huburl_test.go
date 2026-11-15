package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoredBindingsReceivedHubURLs(t *testing.T) {
	f := newBindFixture(t)
	rows := []codexBinding{}
	for _, tc := range receivedURLCases("https://binding.example") {
		if value, ok := tc.value.(string); ok {
			rows = append(rows, codexBinding{Hub: value, Profile: tc.name, ThreadID: bindT1, ExpiresAt: f.now.Add(time.Hour)})
		}
	}
	for _, hub := range []string{"https://binding.example/Vecnet/%2Fexact/", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", "http://127.0.0.1:7777/prefix/"} {
		rows = append(rows, codexBinding{Hub: hub, Profile: "safe", ThreadID: bindT2, ExpiresAt: f.now.Add(-time.Hour)})
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(codexBindingsFile{Version: 1, Bindings: rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := f.run("--json", "bindings")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	var got []struct {
		codexBinding
		State string `json:"state"`
	}
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rows) {
		t.Fatal("binding rows dropped")
	}
	for i, row := range got {
		if i < 4 {
			if row.Hub != "(unusable)" || row.State != "active" {
				t.Errorf("invalid binding projected as %+v", row)
			}
		} else if row.Hub != rows[i].Hub || row.State != "expired" {
			t.Errorf("valid hub/TTL changed: %+v", row)
		}
	}
	if strings.Contains(out+errs, "T2_") {
		t.Error("stored canary printed")
	}
	if !bytes.Equal(raw, f.raw()) {
		t.Error("inspection rewrote binding bytes")
	}
}

func TestStoredBindingsInspectionReadOnly(t *testing.T) {
	f := newBindFixture(t)
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\n  \"version\": 1, \"bindings\": []\n}\n")
	if err := os.WriteFile(f.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := f.run("--json", "bindings")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0755 {
		t.Error("inspection changed directory permissions")
	}
	if _, err := os.Stat(f.path + ".lock"); !os.IsNotExist(err) {
		t.Error("inspection created ownership lock")
	}
	if !bytes.Equal(raw, f.raw()) {
		t.Error("inspection rewrote file")
	}
}
