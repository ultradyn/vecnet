package tui

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/prefs"
)

func prefsGolden(t *testing.T, m *Model, name string) string {
	t.Helper()
	const w, h = 80, 24
	got := plainAt(t, m, w, h)
	assertFrame(t, got, w, h)
	path := filepath.Join("testdata", "prefs-"+name+"-80x24.golden")
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

func prefsOpen(t *testing.T, f *fakeAPI) (*Model, *pump) {
	t.Helper()
	m, p := loaded(t, f, newClock())
	submit(p, "prefs")
	p.until("preferences", func() bool { return m.Data.PrefsKnown || m.Data.Errs["prefs"] != nil })
	p.idle()
	return m, p
}

func prefsSample(t *testing.T) prefs.Snapshot {
	t.Helper()
	values := map[string]prefs.Effective{
		"retention.ack_events":    {Value: prefs.Duration(604800), Source: prefs.SourceDefault},
		"blobs.expiry":            {Value: prefs.Enum("1d"), Source: prefs.SourceDefault},
		"events.refresh_interval": {Value: prefs.Duration(1800), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL"},
		"presence.default_ttl":    {Value: prefs.Duration(3600), Source: prefs.SourceStored},
		"agents.thread_budget":    {Value: prefs.Int(500), Source: prefs.SourceDefault},
	}
	return prefsSnap(t, values, nil)
}

func prefsSnap(t *testing.T, values map[string]prefs.Effective, edit func(*prefs.Definition)) prefs.Snapshot {
	t.Helper()
	defs := make([]prefs.Definition, 0, len(values))
	for key := range values {
		d, err := prefs.Lookup(key)
		if err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(&d)
		}
		defs = append(defs, d)
	}
	snap, err := prefs.NewSnapshot(3, `"prefs-3-abc"`, defs, values)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestPrefsGoldens(t *testing.T) {
	t.Run("loading", func(t *testing.T) {
		f := seeded()
		release := f.hold("Preferences")
		t.Cleanup(release)
		m, p := loaded(t, f, newClock())
		submit(p, "prefs")
		p.until("prefs request", func() bool { return m.inflight["prefs"] })
		frame := prefsGolden(t, m, "loading")
		if !strings.Contains(frame, "loading preferences") {
			t.Fatalf("loading frame:\n%s", frame)
		}
	})
	t.Run("error", func(t *testing.T) {
		f := seeded()
		f.fail("Preferences", &opclient.Error{Status: 500, Code: "prefs_storage_unavailable", Message: "storage down"})
		m, _ := prefsOpen(t, f)
		frame := prefsGolden(t, m, "error")
		if !strings.Contains(frame, "could not load preferences") || !strings.Contains(frame, "prefs_storage_unavailable") {
			t.Fatalf("error frame:\n%s", frame)
		}
	})
	t.Run("empty", func(t *testing.T) {
		f := seeded()
		f.prefs = prefsSnap(t, map[string]prefs.Effective{}, nil)
		m, _ := prefsOpen(t, f)
		frame := prefsGolden(t, m, "empty")
		if !strings.Contains(frame, "no preferences") {
			t.Fatalf("empty frame:\n%s", frame)
		}
	})
	t.Run("group", func(t *testing.T) {
		f := seeded()
		f.prefs = prefsSample(t)
		m, _ := prefsOpen(t, f)
		frame := prefsGolden(t, m, "group")
		for _, want := range []string{"Retention & storage", "Files", "Notifications/presence", "Agents defaults", "retention.ack_events", "blobs.expiry"} {
			if !strings.Contains(frame, want) {
				t.Errorf("group frame lacks %q\n%s", want, frame)
			}
		}
	})
	t.Run("source", func(t *testing.T) {
		f := seeded()
		f.prefs = prefsSample(t)
		m, p := prefsOpen(t, f)
		p.press("j", "j")
		frame := prefsGolden(t, m, "source")
		if !strings.Contains(frame, "> events.refresh_interval") || !strings.Contains(frame, "flag") || !strings.Contains(frame, "VECNET_EVENTS_REFRESH_INTERVAL") || !strings.Contains(frame, "stored") || !strings.Contains(frame, "default") {
			t.Fatalf("source frame:\n%s", frame)
		}
	})
	t.Run("help", func(t *testing.T) {
		f := seeded()
		f.prefs = prefsSample(t)
		m, p := prefsOpen(t, f)
		p.press("?")
		frame := prefsGolden(t, m, "help")
		for _, want := range []string{"j/k move  r refresh", ":prefs", "show read-only hub preferences", "prefs"} {
			if !strings.Contains(frame, want) {
				t.Errorf("help lacks %q\n%s", want, frame)
			}
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		f := seeded()
		f.fail("Preferences", &opclient.Error{Status: http.StatusNotFound, Code: "not_found", Message: "no such route"})
		m, _ := prefsOpen(t, f)
		frame := prefsGolden(t, m, "unavailable")
		if !strings.Contains(frame, "preferences unavailable") || !strings.Contains(frame, "no preferences API") {
			t.Fatalf("unavailable frame:\n%s", frame)
		}
	})
}

func TestPrefsUnavailableForbidden(t *testing.T) {
	f := seeded()
	f.fail("Preferences", &opclient.Error{Status: http.StatusForbidden, Code: "forbidden", Message: "no"})
	m, _ := prefsOpen(t, f)
	frame := plainAt(t, m, 80, 24)
	if !strings.Contains(frame, "preferences unavailable") || !strings.Contains(frame, "cannot read preferences") {
		t.Fatalf("forbidden frame:\n%s", frame)
	}
	if strings.Contains(frame, "could not load preferences") {
		t.Fatalf("forbidden rendered as an error:\n%s", frame)
	}
}

func TestPrefsHostileText(t *testing.T) {
	f := seeded()
	f.prefs = prefsSnap(t, map[string]prefs.Effective{
		"events.refresh_interval": {Value: prefs.Duration(1800), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL"},
	}, func(d *prefs.Definition) {
		if d.Key != "events.refresh_interval" {
			return
		}
		d.Label = "Events \x1b[31mPWN\x1b[0m"
		d.Description = "cadence \u202ehidden \x1b]52;c;aGk=\x07ok"
		d.UnavailableReason = "locked \x1b[2J"
		d.Unit = "sec\x1b]52;c;SECRET\x07onds"
	})
	m, _ := prefsOpen(t, f)
	raw := m.View().Content
	for _, bad := range []string{"\u202e", "\x1b[31m", "\x1b]52", "aGk=", "\x1b[2J", "SECRET"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("hostile sequence %q reached the terminal", bad)
		}
	}
	frame := prefsGolden(t, m, "hostile")
	for _, want := range []string{"PWN", "hidden", "ok", "locked", "seconds"} {
		if !strings.Contains(frame, want) {
			t.Errorf("cleaned frame lacks %q\n%s", want, frame)
		}
	}
}

func TestPrefsHostileRangeMetadata(t *testing.T) {
	f := seeded()
	f.prefs = prefsSnap(t, map[string]prefs.Effective{
		"events.refresh_interval": {Value: prefs.Duration(1800), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL"},
		"blobs.expiry":            {Value: prefs.Enum("1d"), Source: prefs.SourceDefault},
	}, func(d *prefs.Definition) {
		switch d.Key {
		case "events.refresh_interval":
			d.Unit = "sec\x1b]52;c;SECRET\x07onds\u202e"
		case "blobs.expiry":
			d.AllowedValues = append(d.AllowedValues, "\x1b]52;c;SECRET\x07off\u202e")
		}
	})
	m, p := prefsOpen(t, f)
	rawAllowed := m.View().Content
	p.press("j")
	rawUnit := m.View().Content
	for _, raw := range []string{rawAllowed, rawUnit} {
		for _, bad := range []string{"\u202e", "\x1b]52", "SECRET"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("hostile range text %q reached the terminal", bad)
			}
		}
	}
	unit := plainAt(t, m, 80, 24)
	if !strings.Contains(unit, "seconds") {
		t.Fatalf("cleaned unit missing\n%s", unit)
	}
	p.press("k")
	allowed := plainAt(t, m, 80, 24)
	for _, want := range []string{"1h", "30d", "off"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("allowed value %q missing\n%s", want, allowed)
		}
	}
}

func TestPrefsScrollsFullRegistry(t *testing.T) {
	t.Run("80x24", func(t *testing.T) { assertPrefsLastRow(t, false) })
	t.Run("banner", func(t *testing.T) { assertPrefsLastRow(t, true) })
}

func assertPrefsLastRow(t *testing.T, banner bool) {
	t.Helper()
	f := seeded()
	if banner {
		f.setNotices(movingNotice())
	}
	f.prefs = prefsRegistrySnapshot(t)
	m, p := prefsOpen(t, f)
	rows := prefRows(m.Data.Prefs)
	if len(rows) != len(prefs.Definitions()) {
		t.Fatalf("registry rows = %d", len(rows))
	}
	for i := 0; i < len(rows); i++ {
		frame := plainAt(t, m, 80, 24)
		assertFrame(t, frame, 80, 24)
		assertPrefRowVisible(t, frame, rows[i])
		if i+1 < len(rows) {
			p.press("j")
		}
	}
	frame := plainAt(t, m, 80, 24)
	if banner {
		if line := strings.Split(frame, "\n")[1]; !strings.HasPrefix(line, "! ") {
			t.Fatalf("banner missing: %q", line)
		}
	}
	if strings.Contains(frame, "retention.ack_events") {
		t.Fatalf("list did not scroll the first row off 80x24\n%s", frame)
	}
	p.press("k")
	frame = plainAt(t, m, 80, 24)
	assertPrefRowVisible(t, frame, rows[len(rows)-2])
	// Leave room for the complete registry, group headings, help and banner.
	wide := plainAt(t, m, 120, len(rows)+20)
	assertPrefRowVisible(t, wide, rows[len(rows)-2])
	if !strings.Contains(wide, "retention.ack_events") {
		t.Fatalf("taller frame still scrolled the first row away\n%s", wide)
	}
	back := plainAt(t, m, 80, 24)
	assertPrefRowVisible(t, back, rows[len(rows)-2])
}

func assertPrefRowVisible(t *testing.T, frame string, d prefs.Descriptor) {
	t.Helper()
	line := ""
	for _, row := range strings.Split(frame, "\n") {
		if strings.Contains(row, "> "+d.Key) {
			line = row
			break
		}
	}
	if line == "" || !strings.Contains(line, formatTUIPref(d)) || !strings.Contains(line, string(d.Source)) || !strings.Contains(line, prefLiveLabel(d)) {
		t.Fatalf("selected %s line %q\n%s", d.Key, line, frame)
	}
	if !strings.Contains(frame, d.Label) {
		t.Fatalf("help label for %s missing\n%s", d.Key, frame)
	}
	if d.Description != "" {
		part := d.Description
		if len(part) > 40 {
			part = part[:40]
		}
		if !strings.Contains(frame, part) {
			t.Fatalf("help for %s missing %q\n%s", d.Key, part, frame)
		}
	}
	if text := prefRangeText(d); text != "" && !strings.Contains(frame, text) {
		t.Fatalf("range %q for %s missing\n%s", text, d.Key, frame)
	}
}

func prefsRegistrySnapshot(t *testing.T) prefs.Snapshot {
	t.Helper()
	defs := prefs.Definitions()
	values := make(map[string]prefs.Effective, len(defs))
	for _, d := range defs {
		values[d.Key] = prefs.Effective{Value: d.Default, Source: prefs.SourceDefault}
	}
	snap, err := prefs.NewSnapshot(3, `"prefs-3-abc"`, defs, values)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestPrefsFromChatDoesNotMarkRead(t *testing.T) {
	a, m, p := chatOpen(t)
	p.until("initial read", func() bool { return len(a.reads()) >= 1 && chatOf(m).readTo[chatHark] != "" })
	p.idle()
	before := len(a.reads())
	submit(p, "prefs")
	p.until("prefs", func() bool { return m.prefsOn && (m.Data.PrefsKnown || m.Data.Errs["prefs"] != nil) })
	p.idle()
	if got := len(a.reads()); got != before {
		t.Fatalf("opening prefs marked read: %d -> %d", before, got)
	}
	a.add(chatHark, chatMsg("msg_prefs_hidden", chatCodex, "wake", chatAt(7, 42), "Hidden while preferences is open."))
	p.press("r")
	p.idle()
	p.press("ctrl+l")
	p.until("the hidden message", func() bool {
		st := m.Data.Thread[chatHark]
		return st != nil && len(st.Messages) > 0 && st.Messages[len(st.Messages)-1].MessageID == "msg_prefs_hidden"
	})
	if id := chatHark; chatOf(m).readAsk[id] == "msg_prefs_hidden" || chatOf(m).readTo[id] == "msg_prefs_hidden" || len(a.reads()) != before {
		t.Fatalf("ctrl+l marked a hidden chat read: asks %q to %q reads %d", chatOf(m).readAsk[id], chatOf(m).readTo[id], len(a.reads()))
	}
	p.press("r")
	p.idle()
	p.send(chatTickMsg{})
	p.idle()
	if id := chatHark; chatOf(m).readAsk[id] == "msg_prefs_hidden" || chatOf(m).readTo[id] == "msg_prefs_hidden" || len(a.reads()) != before {
		t.Fatalf("refresh or tick marked a hidden chat read: asks %q to %q reads %d", chatOf(m).readAsk[id], chatOf(m).readTo[id], len(a.reads()))
	}
}

func TestPrefsCommandsOpenTheThreadScreen(t *testing.T) {
	t.Run("thread", func(t *testing.T) {
		_, m, p := chatOpen(t)
		submit(p, "prefs")
		p.until("prefs", func() bool { return m.prefsOn })
		p.idle()
		if m.active().Title() != "prefs" || !strings.Contains(plainAt(t, m, 80, 24), "┏━ preferences") {
			t.Fatalf("visible %s", m.active().Title())
		}
		submit(p, "thread 2")
		p.idle()
		frame := plainAt(t, m, 140, 42)
		if m.active().Title() != "chat" || strings.Contains(frame, "┏━ preferences") || !strings.Contains(frame, "Ticket 18") {
			t.Fatalf("thread left visible %s prefs %v\n%s", m.active().Title(), m.prefsOn, frame)
		}
	})
	t.Run("msg", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.sendBack = opapi.SendResult{MessageID: "msg_x", ThreadID: chatOther}
		submit(p, "prefs")
		p.until("prefs", func() bool { return m.prefsOn })
		p.idle()
		submit(p, "msg claw hello from prefs")
		p.until("opened", func() bool { return m.CurrentThread == chatOther && m.active().Title() == "chat" })
		frame := plainAt(t, m, 140, 42)
		if strings.Contains(frame, "┏━ preferences") || !strings.Contains(frame, "Ticket 18") {
			t.Fatalf("msg left preferences up:\n%s", frame)
		}
	})
	t.Run("msg error", func(t *testing.T) {
		_, m, p := chatOpen(t)
		submit(p, "prefs")
		p.until("prefs", func() bool { return m.prefsOn })
		p.idle()
		submit(p, "msg zzz hello")
		p.until("reason", func() bool { return m.Mode == ModeCommand && strings.Contains(m.Input, "msg zzz") })
		frame := plainAt(t, m, 140, 42)
		if m.active().Title() != "chat" || strings.Contains(frame, "┏━ preferences") || !strings.Contains(frame, "no peer matches zzz") {
			t.Fatalf("error navigation stayed on preferences:\n%s", frame)
		}
	})
}

func TestPrefsNavigationIsReadOnly(t *testing.T) {
	f := seeded()
	f.prefs = prefsSample(t)
	m, p := loaded(t, f, newClock())
	f.reset()
	submit(p, "prefs")
	p.until("open", func() bool { return f.count("Preferences") >= 1 && !m.inflight["prefs"] })
	p.idle()
	if verbs := f.preferenceVerbs(); len(verbs) != 1 || verbs[0] != http.MethodGet {
		t.Fatalf("open verbs = %v", verbs)
	}
	if n := f.count("Preferences"); n != 1 {
		t.Fatalf("open GET count = %d", n)
	}
	assertNoPrefWrites(t, f)
	p.press("r")
	p.until("refresh", func() bool { return f.count("Preferences") >= 2 && !m.inflight["prefs"] })
	p.idle()
	if verbs := f.preferenceVerbs(); len(verbs) != 2 || verbs[0] != http.MethodGet || verbs[1] != http.MethodGet {
		t.Fatalf("refresh verbs = %v", verbs)
	}
	p.press("enter")
	p.idle()
	if f.count("Preferences") != 2 {
		t.Fatalf("enter issued another request: %d", f.count("Preferences"))
	}
	before := f.counts()
	p.press("ctrl+l")
	p.until("reload", func() bool { return f.count("Preferences") >= before["Preferences"]+1 && len(m.inflight) == 0 })
	p.idle()
	if f.count("Preferences") != before["Preferences"]+1 {
		t.Fatalf("ctrl+l preferences = %d, was %d", f.count("Preferences"), before["Preferences"])
	}
	for _, verb := range f.preferenceVerbs() {
		if verb != http.MethodGet {
			t.Fatalf("verb %s", verb)
		}
	}
	assertNoPrefWrites(t, f)
	p.press("1")
	if m.prefsOn || m.Tab != 0 {
		t.Fatalf("key 1 left prefs=%v tab=%d", m.prefsOn, m.Tab)
	}
	submit(p, "prefs")
	p.idle()
	submit(p, "tab inbox")
	if m.prefsOn || m.Tab != tabInbox {
		t.Fatalf("tab inbox left prefs=%v tab=%d", m.prefsOn, m.Tab)
	}
}

func assertNoPrefWrites(t *testing.T, f *fakeAPI) {
	t.Helper()
	for _, name := range []string{"ReadThread", "ResumeSubscription", "RetryDeadLetter", "DiscardDeadLetter", "Send", "Answer", "SetPresence", "React", "Upload"} {
		if f.count(name) != 0 {
			t.Errorf("%s was called", name)
		}
	}
}
