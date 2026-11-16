package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChatSaveWindowsNames(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"report:2026?.txt", "report_2026_.txt"},
		{"NUL", "_NUL"},
		{"con", "_con"},
		{"COM1", "_COM1"},
		{"report.txt:private", "report.txt_private"},
		{"report.txt. ", "report.txt"},
		{"...", "blob-sha-file"},
		{"..\\..\\report.txt", "report.txt"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			name := chatSaveName(tc.remote, "sha-file")
			if name != tc.want || !filepath.IsLocal(name) || filepath.Base(name) != name {
				t.Fatalf("save name = %q, want local component %q", name, tc.want)
			}
			saved, err := chatWrite(name, []byte("contents"))
			if err != nil || saved != name {
				t.Fatalf("save = %q, %v", saved, err)
			}
			if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != "contents" {
				t.Fatalf("saved bytes = %q, %v", data, err)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("save did not create one ordinary file: %v, %v", entries, err)
			}
		})
	}
}
