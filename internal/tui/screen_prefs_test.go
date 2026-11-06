package tui

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	})
	m, _ := prefsOpen(t, f)
	raw := m.View().Content
	for _, bad := range []string{"\u202e", "\x1b[31m", "\x1b]52", "aGk=", "\x1b[2J"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("hostile sequence %q reached the terminal", bad)
		}
	}
	frame := prefsGolden(t, m, "hostile")
	for _, want := range []string{"PWN", "hidden", "ok", "locked"} {
		if !strings.Contains(frame, want) {
			t.Errorf("cleaned frame lacks %q\n%s", want, frame)
		}
	}
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
