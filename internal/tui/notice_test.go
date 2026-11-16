package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui/theme"
)

// noticeAt is a signed-looking notice for the banner tests. The TUI never
// verifies signatures; it only draws what the hub lists. The fields pass the
// wire schema, as opapi.Tags checks it.
func noticeAt(id, kind, message string, at time.Duration, newURL string) envelope.HubNotice {
	n := envelope.HubNotice{
		NoticeID: id, Kind: kind, Message: message, Hub: "https://hub.example:8443",
		IssuedAt:  envelope.FormatTime(fixedNow.Add(-time.Hour)),
		ExpiresAt: envelope.FormatTime(fixedNow.Add(30 * 24 * time.Hour)),
		NewURL:    newURL, Sig: strings.Repeat("A", 86),
	}
	if at != 0 {
		n.At = envelope.FormatTime(fixedNow.Add(at))
	}
	return n
}

// 02:00 the next morning, UTC, from fixedNow (07:41).
const twoAM = 18*time.Hour + 19*time.Minute

func movingNotice() envelope.HubNotice {
	return noticeAt("ntc_01JD3S6G8J0M2P4R6T8V0X0001", "moving", "Moving to Fly tonight", twoAM, "https://vecnet.xk.io")
}

func maintenanceNotice() envelope.HubNotice {
	return noticeAt("ntc_01JD3S6G8J0M2P4R6T8V0X0002", "maintenance", "Database vacuum, expect slow reads", 2*time.Hour, "")
}

// noticeGolden compares a stripped frame with testdata/<name>.golden.
// UPDATE_GOLDEN=1 rewrites it.
func noticeGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	sameAsGolden(t, name, got)
}

// sameAsGolden compares with an existing golden and never rewrites it, so a
// guard cannot paper over a changed frame.
func sameAsGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	want := readGolden(t, path)
	if !bytes.Equal(want, []byte(got)) {
		t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// assertBannerBody checks the banner layout: the top bar, then the banner,
// then the active screen drawn one row shorter than without a banner, then
// the status and command lines.
func assertBannerBody(t *testing.T, m *Model, frame string, w, h int) {
	t.Helper()
	assertFrame(t, frame, w, h)
	lines := strings.Split(frame, "\n")
	if !strings.HasPrefix(lines[1], "! ") {
		t.Fatalf("row 2 is not the banner: %q", lines[1])
	}
	body := strings.Split(ansi.Strip(block(m.active().View(m, w, h-4), w, h-4)), "\n")
	for i, want := range body {
		if lines[2+i] != want {
			t.Fatalf("body row %d = %q, want the screen at height %d: %q", i, lines[2+i], h-4, want)
		}
	}
	if !strings.Contains(lines[h-2], "NORMAL") {
		t.Fatalf("status line moved: %q", lines[h-2])
	}
}

func TestNoticeBanner80x24(t *testing.T) {
	f := inboxSeed()
	f.setNotices(movingNotice())
	m, _ := inboxLoaded(t, f, newClock())
	inboxEvents(t, m)
	if len(m.Data.Notices) != 1 {
		t.Fatalf("notices not loaded: %+v", m.Data.Notices)
	}
	frame := plainAt(t, m, 80, 24)
	assertBannerBody(t, m, frame, 80, 24)
	banner := strings.Split(frame, "\n")[1]
	if want := "! MOVING in 18h Nov 03 02:00 -> vecnet.xk.io  Moving to Fly tonight"; strings.TrimRight(banner, " ") != want {
		t.Fatalf("banner %q, want %q", banner, want)
	}
	if strings.Contains(banner, "more") {
		t.Fatalf("one notice claims more: %q", banner)
	}
	noticeGolden(t, "notice-moving-80x24", frame)
}

func TestNoticeBanner140x42(t *testing.T) {
	f := inboxSeed()
	// Listed in issued_at order: the maintenance notice came first, and the
	// move still wins the row.
	f.setNotices(maintenanceNotice(), movingNotice())
	m, _ := inboxLoaded(t, f, newClock())
	inboxEvents(t, m)
	frame := plainAt(t, m, 140, 42)
	assertBannerBody(t, m, frame, 140, 42)
	banner := strings.Split(frame, "\n")[1]
	if want := "! MOVING in 18h Nov 03 02:00 -> vecnet.xk.io  Moving to Fly tonight  (+1 more)"; strings.TrimRight(banner, " ") != want {
		t.Fatalf("banner %q, want %q", banner, want)
	}
	noticeGolden(t, "notice-two-140x42", frame)

	// Priority is moving > restart > maintenance, whatever the list order.
	restart := noticeAt("ntc_01JD3S6G8J0M2P4R6T8V0X0003", "restart", "Kernel update", 20*time.Minute, "")
	got := ansi.Strip(viewNotices(theme.New(theme.TrueColor, true), []envelope.HubNotice{maintenanceNotice(), restart}, fixedNow, time.UTC, 140))
	if want := "! RESTART in 20m 08:01  Kernel update  (+1 more)"; strings.TrimRight(got, " ") != want {
		t.Fatalf("restart banner %q, want %q", got, want)
	}
	got = ansi.Strip(viewNotices(theme.New(theme.TrueColor, true), []envelope.HubNotice{maintenanceNotice()}, fixedNow, time.UTC, 140))
	if want := "! MAINTENANCE in 2h 09:41  Database vacuum, expect slow reads"; strings.TrimRight(got, " ") != want {
		t.Fatalf("maintenance banner %q, want %q", got, want)
	}
	// A long message gives way before the count does.
	long := movingNotice()
	long.Message = strings.Repeat("word ", 40)
	got = ansi.Strip(viewNotices(theme.New(theme.TrueColor, true), []envelope.HubNotice{maintenanceNotice(), long}, fixedNow, time.UTC, 80))
	if lipgloss.Width(got) != 80 || !strings.HasSuffix(strings.TrimRight(got, " "), "...  (+1 more)") || !strings.HasPrefix(got, "! MOVING in 18h Nov 03 02:00 -> vecnet.xk.io  word word") {
		t.Fatalf("narrow banner %q", got)
	}
}

// sgrOnly matches styled text whose only escape sequences are SGR colours.
var sgrOnly = regexp.MustCompile(`^(?:[^\x1b]|\x1b\[[0-9;:]*m)*$`)

func TestNoticeBannerHostile(t *testing.T) {
	// 400 runes of operator text holding a screen clear, CR/LF, a bidi
	// override and non-ASCII text.
	msg := "\x1b[2Jclear\r\nnext line‮evil " + strings.Repeat("é", 10)
	msg += strings.Repeat("y", 400-utf8.RuneCountInString(msg))
	if utf8.RuneCountInString(msg) != 400 {
		t.Fatal("fixture length")
	}
	hostile := noticeAt("ntc_01JD3S6G8J0M2P4R6T8V0X0004", "moving", msg, twoAM, "https://evil‮.example")
	for _, w := range []int{80, 140, 30} {
		out := viewNotices(theme.New(theme.TrueColor, true), []envelope.HubNotice{hostile}, fixedNow, time.UTC, w)
		if !sgrOnly.MatchString(out) {
			t.Fatalf("w=%d: an escape other than SGR reached the terminal: %q", w, out)
		}
		plain := ansi.Strip(out)
		if strings.ContainsAny(plain, "\x1b\r\n‮") || strings.Contains(plain, "[2J") {
			t.Fatalf("w=%d: control text survived: %q", w, plain)
		}
		for _, r := range plain {
			if r < 0x20 || r > 0x7e {
				t.Fatalf("w=%d: non-ASCII %U in banner %q", w, r, plain)
			}
		}
		if got := lipgloss.Width(out); got != w {
			t.Fatalf("w=%d: banner is %d cells: %q", w, got, plain)
		}
	}
	if plain := ansi.Strip(viewNotices(theme.New(theme.TrueColor, true), []envelope.HubNotice{hostile}, fixedNow, time.UTC, 140)); !strings.Contains(plain, "clear next line") {
		t.Fatalf("line breaks are not spaces: %q", plain)
	}

	// The full frame keeps its shape.
	f := inboxSeed()
	f.setNotices(hostile)
	m, _ := inboxLoaded(t, f, newClock())
	for _, size := range [][2]int{{80, 24}, {140, 42}} {
		frame := plainAt(t, m, size[0], size[1])
		assertBannerBody(t, m, frame, size[0], size[1])
		if strings.Contains(m.View().Content, "\x1b[2J") {
			t.Fatalf("%dx%d: the clear reached the frame", size[0], size[1])
		}
	}
}

// (guard) With no active notice there is no banner row: the inbox frames match
// their goldens byte for byte, including when the hub still lists notices that
// have since expired or been cleared.
func TestNoBannerFramesUnchanged(t *testing.T) {
	expired := maintenanceNotice()
	expired.ExpiresAt = envelope.FormatTime(fixedNow.Add(-time.Minute))
	cleared := movingNotice()
	cleared.ClearedAt, cleared.ClearedHow = envelope.FormatTime(fixedNow.Add(-time.Minute)), "operator"
	for name, notices := range map[string][]envelope.HubNotice{"none": nil, "inactive": {expired, cleared}} {
		t.Run(name, func(t *testing.T) {
			f := inboxSeed()
			f.setNotices(notices...)
			m, _ := inboxLoaded(t, f, newClock())
			inboxEvents(t, m)
			sameAsGolden(t, "inbox-140x42", plainAt(t, m, 140, 42))
			sameAsGolden(t, "inbox-80x24", plainAt(t, m, 80, 24))
		})
	}
}

// tailHandle returns a model whose tail hands its handler to the test, so the
// test can deliver event batches as the hub would.
func tailHandle(t *testing.T, f *fakeAPI) (*Model, *pump, func(opclient.Batch) error) {
	t.Helper()
	handles := make(chan func(opclient.Batch) error, 1)
	f.tail = func(ctx context.Context, handle func(opclient.Batch) error, status func(string)) error {
		status("live")
		handles <- handle
		<-ctx.Done()
		return ctx.Err()
	}
	m, p := loaded(t, f, newClock())
	return m, p, <-handles
}

// deliver runs one batch through the tail handler and waits for its ack.
func deliver(p *pump, handle func(opclient.Batch) error, b opclient.Batch) error {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { done <- handle(b) }()
	var err error
	finished := false
	p.until("batch ack", func() bool {
		select {
		case err = <-done:
			finished = true
		default:
		}
		return finished
	})
	return err
}

func TestNoticeEventsRefresh(t *testing.T) {
	f := seeded()
	m, p, handle := tailHandle(t, f)
	if f.count("Notices") != 1 {
		t.Fatalf("first load made %d Notices calls", f.count("Notices"))
	}
	if line := strings.Split(plainAt(t, m, 140, 42), "\n")[1]; strings.HasPrefix(line, "! ") {
		t.Fatalf("banner with no notices: %q", line)
	}

	moving := movingNotice()
	f.setNotices(moving)
	f.reset()
	posted := event(t, "e1.1", "hub_notice", envelope.HubNoticeEvent{Change: "posted", Notice: moving})
	if err := deliver(p, handle, opclient.Batch{Events: []envelope.Event{posted}, Cursor: "e1.1"}); err != nil {
		t.Fatal(err)
	}
	if f.count("Notices") != 1 || f.count("Inbox") != 0 {
		t.Fatalf("posted: Notices %d Inbox %d, want one Notices call only", f.count("Notices"), f.count("Inbox"))
	}
	if line := strings.Split(plainAt(t, m, 140, 42), "\n")[1]; !strings.HasPrefix(line, "! MOVING in 18h Nov 03 02:00 -> vecnet.xk.io") {
		t.Fatalf("no banner after the post: %q", line)
	}

	moving.ClearedAt, moving.ClearedHow = envelope.FormatTime(fixedNow), "operator"
	f.setNotices()
	f.reset()
	cleared := event(t, "e1.2", "hub_notice", envelope.HubNoticeEvent{Change: "cleared", Notice: moving})
	if err := deliver(p, handle, opclient.Batch{Events: []envelope.Event{cleared}, Cursor: "e1.2"}); err != nil {
		t.Fatal(err)
	}
	if f.count("Notices") != 1 {
		t.Fatalf("cleared: %d Notices calls", f.count("Notices"))
	}
	frame := plainAt(t, m, 140, 42)
	assertFrame(t, frame, 140, 42)
	if strings.Contains(frame, "MOVING") {
		t.Fatalf("banner left after the clear:\n%s", frame)
	}
	if line := strings.Split(frame, "\n")[1]; !strings.Contains(line, "inbox") {
		t.Fatalf("body did not move back up: %q", line)
	}
}

// A failed notice fetch draws no banner and says so; a hub from before notices
// (404) is simply a hub with none.
func TestNoticeFetchFails(t *testing.T) {
	f := seeded()
	m, p, handle := tailHandle(t, f)
	f.setNotices(movingNotice())
	f.fail("Notices", &opclient.Error{Status: 500, Code: "demo_failure", Message: "Demo failure"})
	posted := event(t, "e1.1", "hub_notice", envelope.HubNoticeEvent{Change: "posted", Notice: movingNotice()})
	if err := deliver(p, handle, opclient.Batch{Events: []envelope.Event{posted}, Cursor: "e1.1"}); err == nil {
		t.Fatal("a failed 5xx load must keep the cursor for replay")
	}
	if !strings.Contains(m.toast.text, "reload notices") || !m.toast.bad {
		t.Fatalf("toast %+v", m.toast)
	}
	if strings.Contains(plainAt(t, m, 140, 42), "MOVING") {
		t.Fatal("banner drawn from a failed load")
	}

	old := seeded()
	old.fail("Notices", &opclient.Error{Status: 404, Code: "not_found", Message: "Not Found"})
	m, _ = loaded(t, old, newClock())
	if err := m.Data.Errs["notices"]; err != nil || m.toast.text != "" {
		t.Fatalf("404 from an older hub: err %v toast %q", err, m.toast.text)
	}
}

// The top bar shows the hub restarting as a warning, and coming back from it
// refetches everything, as coming back from offline does.
func TestRestartingConnState(t *testing.T) {
	f := seeded()
	release := make(chan struct{})
	f.tail = func(ctx context.Context, _ func(opclient.Batch) error, status func(string)) error {
		status("live")
		<-release
		status("restarting")
		<-release
		status("live")
		<-ctx.Done()
		return ctx.Err()
	}
	m, p := loaded(t, f, newClock())
	release <- struct{}{}
	p.until("restarting", func() bool { return m.Conn == ConnRestarting })
	if ConnRestarting != "restarting" {
		t.Fatalf("ConnRestarting = %q", ConnRestarting)
	}
	if m.connStyle().GetForeground() != m.th.Fg(theme.Warn).GetForeground() {
		t.Fatal("restarting is not drawn as a warning")
	}
	for _, size := range [][2]int{{140, 42}, {80, 24}} {
		if line := strings.Split(plainAt(t, m, size[0], size[1]), "\n")[0]; !strings.Contains(line, "  restarting  ") {
			t.Errorf("%dx%d top bar: %q", size[0], size[1], line)
		}
	}
	f.reset()
	release <- struct{}{}
	p.until("live", func() bool { return m.Conn == ConnLive })
	p.loaded(f, "Inbox", "Threads", "Peers", "Notices")
}

// The event log humanises hub_notice like every other type, with the
// operator's message cleaned.
func TestNoticeEventLogLine(t *testing.T) {
	n := movingNotice()
	n.Message = "Moving\x1b[2J to Fly‮ tonight"
	got := inboxEventText(event(t, "e1.1", "hub_notice", envelope.HubNoticeEvent{Change: "posted", Notice: n}), inboxPeers{})
	if !strings.HasPrefix(got, "posted moving ntc_") || !strings.Contains(got, " → vecnet.xk.io Moving") || strings.ContainsAny(got, "\x1b‮{") {
		t.Fatalf("hub_notice line %q", got)
	}
	got = inboxEventText(event(t, "e1.2", "hub_notice", envelope.HubNoticeEvent{Change: "cleared", Notice: maintenanceNotice()}), inboxPeers{})
	if !strings.HasPrefix(got, "cleared maintenance ntc_") || strings.Contains(got, "→") || !strings.HasSuffix(got, "Database vacuum, expect slow reads") {
		t.Fatalf("hub_notice line %q", got)
	}
}
