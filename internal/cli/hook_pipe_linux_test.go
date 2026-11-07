//go:build linux

package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// A native stdout pipe that accepts a prefix and then stalls must receive
// either nothing or one complete JSON object, never a truncated prefix.
// Freeing exactly one page leaves room for 4096 bytes: an eight-item CJK
// batch is about twice that, so a plain write would emit one page and block.
func TestHookPartialPipeAtomicOutput(t *testing.T) {
	events := make([]envelope.Event, 8)
	for i := range events {
		events[i] = hookEvent(i+1, strings.Repeat("界", 400))
	}
	if n := len(hookOutput("user-prompt", hookContext(newHookHint("p", "", ""), func() []hookItem {
		items := make([]hookItem, len(events))
		for i := range items {
			items[i] = hookItem{Cursor: events[i].Cursor, Text: strings.Repeat("界", 400)}
		}
		return items
	}(), 0, false))); n <= hookPipeAtomicBytes {
		t.Fatalf("fixture too small to need more than one page: %d bytes", n)
	}
	env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hookPage(w, events, "e1.8")
	})
	seedHook(t, path, "e1.0")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	filled := fillPipe(t, writer)
	page := make([]byte, 4096)
	if _, err := io.ReadFull(reader, page); err != nil {
		t.Fatal(err)
	}
	filled -= len(page)
	cmd := hookProcess(t, env)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = writer, &stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook: %v stderr=%q", err, stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("hook escaped budget: %s", elapsed)
	}
	_ = writer.Close()
	raw, err := io.ReadAll(reader)
	if err != nil || len(raw) < filled || bytes.Count(raw[:filled], []byte("x")) != filled {
		t.Fatalf("prefill: size=%d want>=%d err=%v", len(raw), filled, err)
	}
	out := string(raw[filled:])
	if out == "" {
		t.Fatal("one free page should hold a complete capped output")
	}
	if len(out) > hookPipeAtomicBytes {
		t.Fatalf("output %d bytes exceeds the atomic pipe write size", len(out))
	}
	ctx := contextOf(t, out) // fails on a truncated prefix
	items := strings.Count(ctx, "\ncursor=")
	if items == 0 || items >= len(events) || !strings.Contains(ctx, fmt.Sprintf("(+%d more not shown yet", len(events)-items)) {
		t.Fatalf("items=%d context=%q", items, ctx)
	}
	s := readHookState(t, path)
	if len(s.Shown) != items || s.PollCursor != fmt.Sprintf("e1.%d", items) {
		t.Fatalf("state=%+v after %d shown items", s, items)
	}
}
