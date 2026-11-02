package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

func subsTime(d time.Duration) *string {
	s := envelope.FormatTime(fixedNow.Add(d))
	return &s
}

func subsInt(n int) *int { return &n }

func subsText(s string) *string { return &s }

// subsRow is a healthy row; each test changes what it is about.
func subsRow(id, peer, health, note string) opapi.Subscription {
	return opapi.Subscription{
		SubID: "sub_" + id, Principal: "oauth:prn_" + id, PeerID: peer, URL: id + ".chatgpt.example",
		Name: "vecnet.message", Cursor: "e0.42", Health: health, HealthNote: note,
		ExpiresAt: subsTime(time.Hour), RefreshBefore: subsTime(50 * time.Minute),
		LastRefreshAt: subsTime(-5 * time.Minute), VerifiedAt: subsTime(-time.Hour),
		LastDeliveryAt: subsTime(-time.Minute), LastStatus: subsInt(200),
	}
}

// subsNine is one row of each health value the protocol names, in its order.
func subsNine() []opapi.Subscription {
	rows := []opapi.Subscription{
		subsRow("fine", "peer_fine", opapi.HealthOK, ""),
		subsRow("failing", "peer_failing", opapi.HealthFailing, "Delivery is retrying. Check the callback; repeated failures pause the subscription."),
		subsRow("late", "peer_late", opapi.HealthLate, "Refresh is due. ChatGPT must re-subscribe before expiry to keep delivery flowing."),
		subsRow("expired", "peer_expired", opapi.HealthExpired, "The subscription expired. Ask the dot to subscribe again; queued events resume from its cursor."),
		subsRow("token", "peer_token", opapi.HealthPausedToken, "Waiting for ChatGPT to re-subscribe with a fresh token. Nothing is lost; delivery resumes from the cursor."),
		subsRow("failed", "peer_failed", opapi.HealthPausedFailed, "One event used all 8 attempts. Fix the callback, then resume. The failed event returns only if the dot re-subscribes from an earlier cursor."),
		subsRow("auth", "peer_auth", opapi.HealthPausedAuth, "The peer was disabled or moved, or its login was replaced or revoked. Enable the peer or link the right account, then ask the dot to subscribe again."),
		subsRow("operator", "peer_operator", opapi.HealthPausedOperator, "You paused this subscription. A re-subscribe does not lift it."),
		subsRow("unverified", "peer_unverified", opapi.HealthUnverified, "Waiting for callback verification. No events are sent until a subscribe challenge succeeds."),
	}
	rows[1].LastStatus, rows[1].Attempts = subsInt(503), 3
	rows[2].ExpiresAt = subsTime(4 * time.Minute)
	rows[3].ExpiresAt = subsTime(-3 * time.Minute)
	rows[4].ExpiresAt, rows[4].PausedReason = subsTime(-time.Minute), subsText("token_expired")
	rows[5].PausedReason, rows[5].LastStatus, rows[5].Attempts = subsText("delivery_failed"), subsInt(500), 8
	rows[6].PausedReason, rows[6].LastRefreshAt = subsText("credential_rotated"), nil
	rows[7].PausedReason = subsText("operator")
	rows[8].ExpiresAt, rows[8].LastRefreshAt, rows[8].LastDeliveryAt, rows[8].LastStatus, rows[8].VerifiedAt = nil, nil, nil, nil, nil
	return rows
}

func subsPeers(rows []opapi.Subscription) []opapi.PeerRow {
	out := make([]opapi.PeerRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, opapi.PeerRow{Peer: opapi.Peer{PeerID: row.PeerID, Name: strings.TrimPrefix(row.PeerID, "peer_") + "-dot"}})
	}
	return out
}

func subsFake(rows []opapi.Subscription) *fakeAPI {
	f := seeded()
	f.subs, f.peers = rows, subsPeers(rows)
	return f
}

// subsLoaded is a loaded model with the real subs screen and static neighbours,
// so these frames stay put as Tasks 18–20 land.
func subsLoaded(t *testing.T, f *fakeAPI, clock *testClock) (*Model, *pump) {
	t.Helper()
	m := newTestModel(t, f, clock)
	m.screens = staticScreens()
	m.screens[tabSubs] = newSubsScreen()
	p := newPump(t, m)
	p.run(m.Init())
	p.until("first load", func() bool { return !m.firstLoading() })
	p.until("live", func() bool { return m.Conn == ConnLive })
	p.idle()
	p.press("4")
	return m, p
}

// subsGolden compares a frame with testdata/subs-<name>.golden. renderAt names
// its file after the test, and the plan names these.
func subsGolden(t *testing.T, m *Model, name string, w, h int) string {
	t.Helper()
	got := plainAt(t, m, w, h)
	assertFrame(t, got, w, h)
	path := filepath.Join("testdata", "subs-"+name+".golden")
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

// subsFlat folds a frame's wrapped lines, so a note can be found whole.
func subsFlat(frame string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(frame, "│", " ")), " ")
}

// subsLine is the frame's first line holding text: the table row, because the
// detail pane sits below it.
func subsLine(t *testing.T, frame, text string) string {
	t.Helper()
	for line := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	t.Fatalf("no line holds %q:\n%s", text, frame)
	return ""
}

func TestSubsGolden(t *testing.T) {
	rows := subsNine()
	m, p := subsLoaded(t, subsFake(rows), newClock())

	t.Run("140x42", func(t *testing.T) {
		frame := subsGolden(t, m, "nine-140x42", 140, 42)
		for _, head := range []string{"peer", "host", "health", "expires in", "last refresh", "last status", "attempts", "cursor"} {
			if !strings.Contains(frame, head) {
				t.Errorf("no %q column:\n%s", head, frame)
			}
		}
		for _, row := range rows {
			line := subsLine(t, frame, strings.TrimPrefix(row.PeerID, "peer_")+"-dot")
			if !regexp.MustCompile(`\b` + row.Health + `\b`).MatchString(line) {
				t.Errorf("health %s missing from its row: %q", row.Health, line)
			}
			if !strings.Contains(line, row.URL) || !strings.Contains(line, row.Cursor) {
				t.Errorf("host or cursor missing: %q", line)
			}
		}
		if line := subsLine(t, frame, "failed-dot"); !strings.Contains(line, "500") || !strings.Contains(line, " 8 ") {
			t.Errorf("last status or attempts missing: %q", line)
		}
		if line := subsLine(t, frame, "unverified-dot"); !strings.Contains(line, "never") {
			t.Errorf("a null last refresh must read never: %q", line)
		}
	})

	t.Run("every note shows in the detail pane", func(t *testing.T) {
		p.press("g")
		for i, row := range rows {
			frame := subsFlat(plainAt(t, m, 140, 42))
			if row.HealthNote != "" && !strings.Contains(frame, row.HealthNote) {
				t.Errorf("row %d: note %q not shown:\n%s", i, row.HealthNote, frame)
			}
			for j, other := range rows {
				if j != i && other.HealthNote != "" && strings.Contains(frame, other.HealthNote) {
					t.Errorf("row %d shows row %d's note", i, j)
				}
			}
			p.press("j")
		}
		p.press("j") // stops at the last row
		if frame := subsFlat(plainAt(t, m, 140, 42)); !strings.Contains(frame, rows[8].HealthNote) {
			t.Errorf("j past the end lost the last row:\n%s", frame)
		}
		p.press("g", "j", "k", "k")
		if frame := subsFlat(plainAt(t, m, 140, 42)); !strings.Contains(frame, "no note") {
			t.Errorf("k past the top lost the first row:\n%s", frame)
		}
		p.press("G")
		if frame := subsFlat(plainAt(t, m, 140, 42)); !strings.Contains(frame, rows[8].HealthNote) {
			t.Errorf("G did not reach the last row:\n%s", frame)
		}
		p.press("g")
	})

	t.Run("selected row names its pause reason", func(t *testing.T) {
		p.press("g", "j", "j", "j", "j", "j", "j") // paused_auth, from a rotated bearer
		golden := subsGolden(t, m, "selected-140x42", 140, 42)
		if !strings.Contains(golden, "credential_rotated") {
			t.Errorf("pause reason not shown:\n%s", golden)
		}
		if !strings.Contains(golden, rows[6].SubID) {
			t.Errorf("sub id not shown:\n%s", golden)
		}
	})

	t.Run("80x24", func(t *testing.T) {
		p.press("g", "j", "j", "j", "j", "j")
		frame := subsGolden(t, m, "nine-80x24", 80, 24)
		for _, row := range rows {
			if !strings.Contains(frame, row.Health) {
				t.Errorf("health %s missing at the minimum size:\n%s", row.Health, frame)
			}
		}
		if !strings.Contains(subsFlat(frame), rows[5].HealthNote) {
			t.Errorf("note cut at the minimum size:\n%s", frame)
		}
		// What the narrow table drops stays in the detail pane.
		for _, want := range []string{"failed.chatgpt.example", "e0.42", "attempts 8"} {
			if !strings.Contains(frame, want) {
				t.Errorf("%q gone at the minimum size:\n%s", want, frame)
			}
		}
	})

	t.Run("a long list scrolls to the selection", func(t *testing.T) {
		var many []opapi.Subscription
		for i := range 30 {
			many = append(many, subsRow(fmt.Sprintf("r%02d", i), fmt.Sprintf("peer_r%02d", i), opapi.HealthOK, ""))
		}
		m, p := subsLoaded(t, subsFake(many), newClock())
		p.press("G")
		frame := plainAt(t, m, 80, 24)
		assertFrame(t, frame, 80, 24)
		if !strings.Contains(frame, "r29-dot") || strings.Contains(frame, "r00-dot") {
			t.Fatalf("selection not on screen:\n%s", frame)
		}
	})

	t.Run("empty", func(t *testing.T) {
		m, _ := subsLoaded(t, subsFake(nil), newClock())
		if frame := subsGolden(t, m, "empty-140x42", 140, 42); !strings.Contains(frame, "no subscriptions") {
			t.Errorf("no empty state:\n%s", frame)
		}
	})

	t.Run("a health this client does not know is neither ok nor hidden", func(t *testing.T) {
		row := subsRow("new", "peer_new", "brand_new_state", "Something new.")
		m, _ := subsLoaded(t, subsFake([]opapi.Subscription{row}), newClock())
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(subsLine(t, frame, "new-dot"), "brand_new_state") {
			t.Errorf("unknown health hidden:\n%s", frame)
		}
		if line := strings.Split(frame, "\n")[0]; !strings.Contains(line, "subs 1") {
			t.Errorf("unknown health not counted: %q", line)
		}
	})

	t.Run("peer text is inert", func(t *testing.T) {
		const osc52, csi, bidi = "\x1b]52;c;aGk=\x07", "\x1b[2J", "\u202e"
		row := subsRow("evil", "peer_evil", opapi.HealthFailing, "note"+osc52+csi+bidi+"\nsecond")
		row.URL, row.Cursor, row.Health = "host"+osc52+".example", "e0"+csi+".7", opapi.HealthPausedAuth+bidi
		row.PausedReason = subsText("unauthorized" + osc52)
		row.Name, row.SubID = "name"+csi, "sub_evil"+bidi
		peers := subsPeers([]opapi.Subscription{row})
		peers[0].Peer.Name = "evil" + osc52 + csi + bidi + "-dot"
		f := subsFake([]opapi.Subscription{row})
		f.peers = peers
		m, _ := subsLoaded(t, f, newClock())
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
		content := m.View().Content
		for _, bad := range []string{"\x1b]", "\x1b[2J", "\u202e", "\x07", "aGk="} {
			if strings.Contains(content, bad) {
				t.Errorf("frame holds %q", bad)
			}
		}
		frame := subsFlat(plainAt(t, m, 140, 42))
		for _, want := range []string{"evil-dot", "host.example", "e0.7", "note second", "unauthorized"} {
			if !strings.Contains(frame, want) {
				t.Errorf("cleaned text %q lost:\n%s", want, frame)
			}
		}
	})
}

func TestSubsExpiresHubClock(t *testing.T) {
	for name, skew := range map[string]time.Duration{"client fast": 10 * time.Minute, "client slow": -10 * time.Minute} {
		t.Run(name, func(t *testing.T) {
			soon := subsRow("soon", "peer_soon", opapi.HealthLate, "Refresh is due.")
			soon.ExpiresAt = subsTime(5 * time.Minute)
			past := subsRow("past", "peer_past", opapi.HealthExpired, "The subscription expired.")
			past.ExpiresAt = subsTime(-3 * time.Minute)
			open := subsRow("open", "peer_open", opapi.HealthOK, "")
			open.ExpiresAt = nil
			// The snapshot's now is the hub's: fixedNow, whatever the client's clock says.
			clock := newClock()
			clock.advance(skew)
			m, _ := subsLoaded(t, subsFake([]opapi.Subscription{soon, past, open}), clock)
			if got := m.HubNow(); !got.Equal(fixedNow) {
				t.Fatalf("HubNow = %v, want %v", got, fixedNow)
			}
			frame := plainAt(t, m, 140, 42)
			if line := subsLine(t, frame, "soon-dot"); !regexp.MustCompile(`\s5m\s`).MatchString(line) || strings.Contains(line, "expired") {
				t.Errorf("five minutes left on the hub's clock: %q", line)
			}
			if line := subsLine(t, frame, "past-dot"); !strings.Contains(line, "expired 3m ago") {
				t.Errorf("three minutes past on the hub's clock: %q", line)
			}
			if line := subsLine(t, frame, "open-dot"); strings.Contains(line, "expired") {
				t.Errorf("a null expiry never expires: %q", line)
			}

			// The countdown keeps running on the client's clock plus the offset.
			clock.advance(6 * time.Minute)
			if line := subsLine(t, plainAt(t, m, 140, 42), "soon-dot"); !strings.Contains(line, "expired 1m ago") {
				t.Errorf("past expiry after six minutes: %q", line)
			}
		})
	}
}

// subsKey builds a key message for names keyPress does not cover.
func subsKey(key string) tea.KeyPressMsg {
	codes := map[string]rune{
		"tab": tea.KeyTab, "backspace": tea.KeyBackspace, "delete": tea.KeyDelete, "insert": tea.KeyInsert,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
		"esc": tea.KeyEscape, "enter": tea.KeyEnter, "space": tea.KeySpace,
	}
	if code, ok := codes[key]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	if ctrl, ok := strings.CutPrefix(key, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: []rune(ctrl)[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
}

// subsAllKeys is every key the terminal can send this tab.
func subsAllKeys() []string {
	keys := []string{"tab", "backspace", "delete", "insert", "up", "down", "left", "right", "pgup", "pgdown", "home", "end", "esc", "enter", "space"}
	for r := '!'; r <= '~'; r++ {
		keys = append(keys, string(r))
	}
	for r := 'a'; r <= 'z'; r++ {
		keys = append(keys, "ctrl+"+string(r))
	}
	return keys
}

func TestSubsNoWrites(t *testing.T) {
	writes := []string{"ReadThread", "ResumeSubscription", "RetryDeadLetter", "DiscardDeadLetter", "Send", "Answer", "SetPresence"}
	rows := subsNine()
	f := subsFake(rows)
	m, p := subsLoaded(t, f, newClock())
	screen := m.screens[tabSubs]
	quits := map[string]bool{"q": true, "ctrl+c": true, "ctrl+q": true}

	// Every row, because a resumable row is where a write would be tempting.
	for i := range rows {
		for _, key := range subsAllKeys() {
			p.press("g")
			for range i {
				p.press("j")
			}
			if note := rows[i].HealthNote; note != "" && !strings.Contains(subsFlat(plainAt(t, m, 140, 42)), note) {
				t.Fatalf("row %d is not selected before %q", i, key)
			}
			msg := subsKey(key)
			// A command is the only way this tab could reach the hub. The shell
			// claims some of these keys first, so ask the screen directly too.
			if cmd := screen.Update(msg, m); cmd != nil {
				t.Errorf("row %d key %q started a command", i, key)
			}
			if !quits[key] {
				p.send(msg)
			}
			m.Mode, m.Tab, m.Help, m.Input = ModeNormal, tabSubs, false, ""
		}
	}
	p.idle()
	for _, name := range writes {
		if n := f.count(name); n != 0 {
			t.Errorf("%s called %d times", name, n)
		}
	}
	if len(f.sent) != 0 {
		t.Errorf("sent %d messages", len(f.sent))
	}

	t.Run("the shell's quit keys too", func(t *testing.T) {
		for key := range quits {
			f := subsFake(subsNine())
			m, p := subsLoaded(t, f, newClock())
			p.send(subsKey(key))
			if m.ctx.Err() == nil {
				t.Fatalf("%s did not quit", key)
			}
			for _, name := range writes {
				if n := f.count(name); n != 0 {
					t.Errorf("%s: %s called %d times", key, name, n)
				}
			}
		}
	})
}

func TestTopBarSubsCount(t *testing.T) {
	counted := regexp.MustCompile(`\bsubs \d`)
	top := func(t *testing.T, m *Model, w, h int) string {
		t.Helper()
		return strings.Split(plainAt(t, m, w, h), "\n")[0]
	}

	t.Run("two rows not ok", func(t *testing.T) {
		rows := []opapi.Subscription{
			subsRow("a", "peer_a", opapi.HealthOK, ""),
			subsRow("b", "peer_b", opapi.HealthLate, "Refresh is due."),
			subsRow("c", "peer_c", opapi.HealthPausedOperator, "You paused this subscription."),
		}
		f := subsFake(rows)
		m, p := subsLoaded(t, f, newClock())
		for _, size := range [][2]int{{140, 42}, {80, 24}} {
			if line := top(t, m, size[0], size[1]); !strings.Contains(line, "subs 2") {
				t.Errorf("%dx%d: %q", size[0], size[1], line)
			}
		}
		// It shows from every tab, and follows the data.
		p.press("1")
		if line := top(t, m, 140, 42); !strings.Contains(line, "subs 2") {
			t.Errorf("from the inbox tab: %q", line)
		}
		f.subs = rows[:2]
		f.reset()
		p.press("ctrl+l")
		p.loaded(f, "Subscriptions")
		if line := top(t, m, 140, 42); !strings.Contains(line, "subs 1") {
			t.Errorf("after a reload: %q", line)
		}
		f.subs = rows[:1]
		f.reset()
		p.press("ctrl+l")
		p.loaded(f, "Subscriptions")
		if line := top(t, m, 140, 42); counted.MatchString(line) {
			t.Errorf("all ok after a reload: %q", line)
		}
	})

	t.Run("all ok", func(t *testing.T) {
		rows := []opapi.Subscription{subsRow("a", "peer_a", opapi.HealthOK, ""), subsRow("b", "peer_b", opapi.HealthOK, "")}
		m, _ := subsLoaded(t, subsFake(rows), newClock())
		line := top(t, m, 140, 42)
		if counted.MatchString(line) {
			t.Errorf("count shown: %q", line)
		}
		if !strings.Contains(line, tabBar) {
			t.Errorf("tab bar changed: %q", line)
		}
	})

	t.Run("no rows", func(t *testing.T) {
		m, _ := subsLoaded(t, subsFake(nil), newClock())
		if line := top(t, m, 140, 42); counted.MatchString(line) {
			t.Errorf("count shown: %q", line)
		}
	})
}

func TestSubsLoadStates(t *testing.T) {
	rows := subsNine()
	f := subsFake(rows)
	m, p := subsLoaded(t, f, newClock())
	p.press("g", "j", "j", "j", "j", "j")

	t.Run("a running reload says so and keeps the row", func(t *testing.T) {
		f.reset()
		release := f.hold("Subscriptions")
		p.press("ctrl+l")
		p.until("the reload to reach the hub", func() bool { return f.count("Subscriptions") == 1 })
		frame := plainAt(t, m, 140, 42)
		if !strings.Contains(frame, "reloading") || !strings.Contains(subsFlat(frame), rows[5].HealthNote) {
			t.Fatalf("no reload state:\n%s", frame)
		}
		release()
		p.idle()
		if frame := plainAt(t, m, 140, 42); strings.Contains(frame, "reloading") {
			t.Fatalf("reload state stuck:\n%s", frame)
		}
	})

	t.Run("the selection follows its row across a reload", func(t *testing.T) {
		reversed := make([]opapi.Subscription, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			reversed = append(reversed, rows[i])
		}
		f.subs = reversed
		f.reset()
		p.press("ctrl+l")
		p.loaded(f, "Subscriptions")
		if frame := subsFlat(plainAt(t, m, 140, 42)); !strings.Contains(frame, rows[5].HealthNote) {
			t.Fatalf("selection moved to another row:\n%s", frame)
		}
	})

	t.Run("a failed load says so and keeps the last data", func(t *testing.T) {
		f.fail("Subscriptions", fmt.Errorf("hub said \x1b]52;c;aGk=\x07no"))
		f.reset()
		p.press("ctrl+l")
		p.loaded(f, "Subscriptions")
		content := func() string {
			m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
			return m.View().Content
		}
		if strings.Contains(content(), "\x1b]52") {
			t.Fatal("error text reached the terminal uncleaned")
		}
		frame := subsGolden(t, m, "failed-140x42", 140, 42)
		if !strings.Contains(frame, "load failed") || !strings.Contains(frame, "hub said no") {
			t.Fatalf("no failure state:\n%s", frame)
		}
		for _, row := range rows {
			if !strings.Contains(frame, row.Health) {
				t.Errorf("last data lost %s:\n%s", row.Health, frame)
			}
		}
		f.fail("Subscriptions", nil)
		f.reset()
		p.press("ctrl+l")
		p.loaded(f, "Subscriptions")
		if frame := plainAt(t, m, 140, 42); strings.Contains(frame, "load failed") {
			t.Fatalf("failure stuck after a good load:\n%s", frame)
		}
	})
}
