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

// readGolden ignores checkout line endings, not rendering differences. Keep
// every other byte (including trailing spaces and lone CRs) significant, and
// never normalize the rendered frame or rewrite the fixture just to compare it.
func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 once the frame is right): %v", err)
	}
	return bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
}

func TestReadGoldenLineEndings(t *testing.T) {
	for _, input := range []string{"first  \nsecond\r!\n", "first  \r\nsecond\r!\r\n"} {
		path := filepath.Join(t.TempDir(), "frame.golden")
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, want := string(readGolden(t, path)), "first  \nsecond\r!\n"; got != want {
			t.Errorf("readGolden = %q, want %q", got, want)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != input {
			t.Fatalf("reading golden changed it: %q, %v", data, err)
		}
	}
}

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
	want := readGolden(t, path)
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
