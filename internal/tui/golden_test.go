package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderAt sizes m, renders one frame, strips ANSI and compares the frame with
// testdata/<test name>.golden. UPDATE_GOLDEN=1 rewrites the file. It returns
// the stripped frame.
func renderAt(t *testing.T, m *Model, w, h int) string {
	t.Helper()
	got := plainAt(t, m, w, h)
	name := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 once the frame is right): %v", err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
	return got
}

// plainAt renders a frame without ANSI and without touching a golden.
func plainAt(t *testing.T, m *Model, w, h int) string {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return ansi.Strip(m.View().Content)
}
