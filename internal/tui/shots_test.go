package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/opapi"
)

// TestWriteShots renders the picker, the loopback header and the grouped chat
// list. It does nothing unless VECNET_SHOTS names a directory.
func TestWriteShots(t *testing.T) {
	dir := os.Getenv("VECNET_SHOTS")
	if dir == "" {
		t.Skip("set VECNET_SHOTS to write frames")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	pick := newPick([]ProfileChoice{
		{Name: "operator", Hub: "http://127.0.0.1:45752"},
		{Name: "fly-operator", Hub: "https://vecnet.fly.dev"},
	})
	pick.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	picker := ansi.Strip(pick.View().Content)
	if !strings.Contains(picker, "operator") || !strings.Contains(picker, "127.0.0.1:45752 local") || strings.Contains(picker, "vn_operator_") {
		t.Fatalf("picker frame:\n%s", picker)
	}
	writeShot(t, dir, "tui-picker.txt", picker)

	header, _ := inboxLoaded(t, inboxSeed(), newClock())
	header.Profile = "operator"
	header.Hub = "http://127.0.0.1:45752"
	headerFrame := plainAt(t, header, 140, 42)
	if !strings.Contains(headerFrame, "operator · hub 127.0.0.1:45752 local") || !strings.Contains(headerFrame, "approval_request") {
		t.Fatalf("header frame:\n%s", headerFrame)
	}
	writeShot(t, dir, "tui-header-local.txt", headerFrame)

	list, _ := chatModel(t, chatSeed(), newClock())
	list.Profile = "operator"
	list.Hub = "http://127.0.0.1:45752"
	list.Data.OthersKnown = true
	list.Data.Others.Threads = []opapi.ThreadRow{
		sideThreadRow("thr_other_old", "staging notes", []string{chatDot, chatClaw}, "2026-11-04T07:00:00Z"),
		sideThreadRow("thr_other_new", "quota review", []string{chatDot}, "2026-11-04T12:00:00Z"),
	}
	grouped := plainAt(t, list, 140, 42)
	if !strings.Contains(grouped, "Your Threads") || !strings.Contains(grouped, "Other Threads ▾") || !strings.Contains(grouped, "quota review") || !strings.Contains(grouped, "hark B196") {
		t.Fatalf("grouped frame:\n%s", grouped)
	}
	if strings.Index(grouped, "quota review") > strings.Index(grouped, "staging notes") {
		t.Fatalf("Other Threads is not newest first:\n%s", grouped)
	}
	writeShot(t, dir, "tui-threads.txt", grouped)
}

func writeShot(t *testing.T, dir, name, frame string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
}
