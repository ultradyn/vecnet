package tui

import (
	"errors"
	"regexp"
	"testing"
	"time"
)

// Fixture cases from internal/ui/jstest/sidebar.test.mjs. The numbers and
// names are the web sidebar's; this file does not change that suite.

var sideFixtureNow = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC).UnixMilli()

var sideNames = map[string]string{
	"peer_me": "max", "peer_a": "codex-hark", "peer_b": "grok-reviewer",
	"peer_c": "pag-sleuth", "peer_d": "claw-ops", "peer_e": "dot-research",
}

func sideNameOf(id string) string {
	if n, ok := sideNames[id]; ok {
		return n
	}
	return id
}

func sideBack(ms int64) string {
	return time.UnixMilli(sideFixtureNow - ms).UTC().Format(time.RFC3339Nano)
}

func sideSrcRow(id string, title string, participants []string, count *int, at string) sideSrc {
	if participants == nil {
		participants = []string{"peer_a", "peer_b"}
	}
	if at == "" && count == nil && title == "" {
		// caller asked for defaults, including a time, unless at was explicitly empty via sideSrcAt.
	}
	return sideSrc{Thread: sideThread{ID: id, Title: title, Participants: participants, Count: count}, LastMessageAt: at}
}

func sideCount(n int) *int { return &n }

func sideDefault(id string) sideSrc {
	n := 1
	return sideSrcRow(id, "", nil, &n, sideBack(60_000))
}

func sideView(page *sidePage, extra func(*sideOthers)) sideList {
	in := sideOthers{Page: page, Me: "peer_me", NameOf: sideNameOf, Now: sideFixtureNow, Limit: otherPage}
	if extra != nil {
		extra(&in)
	}
	return otherThreads(in)
}

func TestPeopleLine(t *testing.T) {
	if got := peopleLine(nil, sideNameOf); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := peopleLine([]string{}, sideNameOf); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := peopleLine([]string{"peer_a"}, sideNameOf); got != "codex-hark" {
		t.Fatalf("one = %q", got)
	}
	if got := peopleLine([]string{"peer_a", "peer_b", "peer_c"}, sideNameOf); got != "codex-hark, grok-reviewer, pag-sleuth" {
		t.Fatalf("three = %q", got)
	}
	if got := peopleLine([]string{"peer_a", "peer_b", "peer_c", "peer_d", "peer_e"}, sideNameOf); got != "codex-hark, grok-reviewer, pag-sleuth +2" {
		t.Fatalf("five = %q", got)
	}
	short := func(id string) string {
		if n, ok := sideNames[id]; ok {
			return n
		}
		return "peer_…zz"
	}
	if got := peopleLine([]string{"peer_zz"}, short); got != "peer_…zz" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestOtherThreadsSortsByActivityNewestFirst(t *testing.T) {
	n := 1
	v := sideView(&sidePage{Threads: []sideSrc{
		sideSrcRow("thr_old", "", nil, &n, sideBack(3*3_600_000)),
		sideSrcRow("thr_new", "", nil, &n, sideBack(5_000)),
		sideSrcRow("thr_mid1", "", nil, &n, sideBack(5*60_000)),
		sideSrcRow("thr_mid2", "", nil, &n, sideBack(5*60_000)),
	}}, nil)
	if v.State != "ok" {
		t.Fatalf("state %s", v.State)
	}
	got := make([]string, len(v.Rows))
	active := make([]string, len(v.Rows))
	for i, r := range v.Rows {
		got[i], active[i] = r.ID, r.Active
	}
	want := []string{"thr_new", "thr_mid2", "thr_mid1", "thr_old"}
	wantActive := []string{"5s ago", "5m ago", "5m ago", "3h ago"}
	if stringsJoin(got) != stringsJoin(want) || stringsJoin(active) != stringsJoin(wantActive) {
		t.Fatalf("ids %v active %v", got, active)
	}
}

func stringsJoin(ss []string) string {
	return stringsJoinSep(ss, "|")
}

func stringsJoinSep(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func TestOtherThreadsExcludesYoursAndJoined(t *testing.T) {
	v := sideView(&sidePage{Threads: []sideSrc{
		sideDefault("thr_listed"),
		sideSrcRow("thr_joined", "", []string{"peer_a", "peer_me"}, sideCount(1), sideBack(60_000)),
		sideDefault("thr_other"),
	}}, func(in *sideOthers) { in.Mine = []string{"thr_listed"} })
	if len(v.Rows) != 1 || v.Rows[0].ID != "thr_other" {
		t.Fatalf("rows %+v", v.Rows)
	}
}

func TestOtherThreadsRowFields(t *testing.T) {
	n := 42
	v := sideView(&sidePage{Threads: []sideSrc{
		sideSrcRow("thr_1", "Release plan", []string{"peer_a", "peer_b", "peer_c", "peer_d"}, &n, sideBack(12*60_000)),
	}}, nil)
	r := v.Rows[0]
	if r.ID != "thr_1" || r.Href != "#thread/thr_1" || r.Title != "Release plan" || r.Untitled {
		t.Fatalf("row %+v", r)
	}
	if r.People != "codex-hark, grok-reviewer, pag-sleuth +1" || r.Count != "42" || r.Active != "12m ago" {
		t.Fatalf("row %+v", r)
	}
	if ok, _ := regexp.MatchString(`(?s)^Release plan\ncodex-hark, grok-reviewer, pag-sleuth, claw-ops\n42 messages · last active `, r.Tip); !ok {
		t.Fatalf("tip %q", r.Tip)
	}
}

func TestOtherThreadsMissingCountOrTime(t *testing.T) {
	v := sideView(&sidePage{Threads: []sideSrc{
		sideSrcRow("thr_01JBCDEFGHJKMNPQRS", "Plan", nil, nil, ""),
	}}, nil)
	r := v.Rows[0]
	if r.Count != "" || r.Active != "" || r.Tip != "Plan\ncodex-hark, grok-reviewer" {
		t.Fatalf("row %+v tip %q", r, r.Tip)
	}
}

func TestUntitledOtherThreadNamesParticipants(t *testing.T) {
	n := 1
	v := sideView(&sidePage{Threads: []sideSrc{
		sideSrcRow("thr_2", "", nil, &n, sideBack(1000)),
		sideSrcRow("thr_1", "", []string{"peer_a", "peer_b", "peer_c", "peer_d", "peer_e"}, &n, sideBack(2000)),
	}}, nil)
	pair, group := v.Rows[0], v.Rows[1]
	if pair.Title != "codex-hark ↔ grok-reviewer" || !pair.Untitled || pair.People != "2 participants" {
		t.Fatalf("pair %+v", pair)
	}
	if group.Title != "codex-hark, grok-reviewer +3" || group.People != "5 participants" {
		t.Fatalf("group %+v", group)
	}
	if ok, _ := regexp.MatchString(`(?s)^codex-hark, grok-reviewer \+3\ncodex-hark, grok-reviewer, pag-sleuth, claw-ops, dot-research\n1 message · last active `, group.Tip); !ok {
		t.Fatalf("tip %q", group.Tip)
	}
}

func sideMine(extra func(*sideYours)) sideList {
	in := sideYours{Me: "peer_me", NameOf: sideNameOf, Now: sideFixtureNow}
	if extra != nil {
		extra(&in)
	}
	return yourThreads(in)
}

func sideSummary(id, title string, participants []string, count int) sideThread {
	if participants == nil {
		participants = []string{"peer_me", "peer_a"}
	}
	return sideThread{ID: id, Title: title, Participants: participants, Count: &count}
}

func TestYourThreadsSharedRow(t *testing.T) {
	v := sideMine(func(in *sideYours) {
		in.Page = &sidePage{Threads: []sideSrc{
			sideSrcRow("thr_b", "Release plan", []string{"peer_me", "peer_a", "peer_b", "peer_c", "peer_d"}, sideCount(7), sideBack(3*60_000)),
			sideSrcRow("thr_a", "", []string{"peer_a", "peer_me"}, sideCount(2), sideBack(2*3_600_000)),
		}}
	})
	if v.State != "ok" {
		t.Fatalf("state %s", v.State)
	}
	titled, one := v.Rows[0], v.Rows[1]
	if titled.ID != "thr_b" || titled.Href != "#thread/thr_b" || titled.Title != "Release plan" || titled.Untitled ||
		titled.People != "codex-hark, grok-reviewer, pag-sleuth +1" || titled.Count != "7" || titled.Unread != 0 || titled.UnreadText != "" || titled.Active != "3m ago" {
		t.Fatalf("titled %+v", titled)
	}
	if stringsJoinSep(titled.IDs, ",") != "peer_a,peer_b,peer_c,peer_d" {
		t.Fatalf("ids %v", titled.IDs)
	}
	if ok, _ := regexp.MatchString(`(?s)^Release plan\nyou, codex-hark, grok-reviewer, pag-sleuth, claw-ops\n7 messages · last active `, titled.Tip); !ok {
		t.Fatalf("tip %q", titled.Tip)
	}
	if one.Title != "codex-hark" || !one.Untitled || one.People != "2 participants" || len(one.IDs) != 0 || one.Active != "2h ago" {
		t.Fatalf("one %+v", one)
	}
}

func TestYourThreadsKeepsHubOrder(t *testing.T) {
	v := sideMine(func(in *sideYours) {
		in.Page = &sidePage{Threads: []sideSrc{
			sideSrcRow("thr_a", "", nil, sideCount(1), sideBack(9000)),
			sideSrcRow("thr_z", "", nil, sideCount(1), sideBack(1000)),
		}}
	})
	if v.Rows[0].ID != "thr_a" || v.Rows[1].ID != "thr_z" {
		t.Fatalf("order %s %s", v.Rows[0].ID, v.Rows[1].ID)
	}
}

func TestYourThreadsSummariesUntilPage(t *testing.T) {
	sums := []sideThread{
		sideSummary("thr_1", "Hosting", nil, 4),
		sideSummary("thr_2", "", []string{"peer_me", "peer_a", "peer_b"}, 1),
	}
	v := sideMine(func(in *sideYours) { in.Summaries = &sums })
	if v.State != "ok" {
		t.Fatalf("state %s", v.State)
	}
	if v.Rows[0].Title != "Hosting" || v.Rows[0].People != "codex-hark" || v.Rows[0].Count != "4" || v.Rows[0].Active != "" {
		t.Fatalf("first %+v", v.Rows[0])
	}
	if v.Rows[1].Title != "codex-hark, grok-reviewer" || v.Rows[1].People != "3 participants" || v.Rows[1].Count != "1" || v.Rows[1].Active != "" {
		t.Fatalf("second %+v", v.Rows[1])
	}
	both := sideMine(func(in *sideYours) {
		in.Summaries = &sums
		in.Page = &sidePage{Threads: []sideSrc{sideSrcRow("thr_9", "", []string{"peer_me", "peer_b"}, sideCount(1), sideBack(1000))}}
	})
	if len(both.Rows) != 1 || both.Rows[0].ID != "thr_9" {
		t.Fatalf("page should win: %+v", both.Rows)
	}
}

func TestYourThreadsStates(t *testing.T) {
	if got := sideMine(nil); got.State != "loading" || got.Note != "…" || len(got.Rows) != 0 {
		t.Fatalf("loading %+v", got)
	}
	if got := sideMine(func(in *sideYours) { in.Err = errors.New("boom") }); got.State != "error" || got.Note != "Threads unavailable" {
		t.Fatalf("error %+v", got)
	}
	empty := []sideThread{}
	if got := sideMine(func(in *sideYours) { in.Summaries = &empty }); got.State != "empty" || got.Note != "No threads yet" {
		t.Fatalf("empty summaries %+v", got)
	}
	if got := sideMine(func(in *sideYours) { in.Page = &sidePage{} }); got.State != "empty" || got.Note != "No threads yet" {
		t.Fatalf("empty page %+v", got)
	}
	one := []sideThread{sideSummary("thr_1", "Hosting", nil, 1)}
	if got := sideMine(func(in *sideYours) { in.Summaries = &one; in.Err = errors.New("boom") }); got.State != "ok" {
		t.Fatalf("stale %+v", got)
	}
}

func TestOtherThreadsStates(t *testing.T) {
	if got := sideView(nil, nil); got.State != "loading" || got.Note != "…" {
		t.Fatalf("loading %+v", got)
	}
	failed := sideView(nil, func(in *sideOthers) { in.Err = errors.New("boom") })
	if failed.State != "error" || failed.Note != "Other threads unavailable" || len(failed.Rows) != 0 {
		t.Fatalf("error %+v", failed)
	}
	empty := sideView(&sidePage{}, nil)
	if empty.State != "empty" || empty.Note != "No other threads" {
		t.Fatalf("empty %+v", empty)
	}
	if got := sideView(&sidePage{Threads: []sideSrc{sideSrcRow("thr_x", "", []string{"peer_me"}, sideCount(1), sideBack(1000))}}, nil); got.State != "empty" {
		t.Fatalf("excluded %+v", got)
	}
	stale := sideView(&sidePage{Threads: []sideSrc{sideDefault("thr_y")}}, func(in *sideOthers) { in.Err = errors.New("boom") })
	if stale.State != "ok" || len(stale.Rows) != 1 {
		t.Fatalf("stale %+v", stale)
	}
}

func TestOtherThreadsBounds(t *testing.T) {
	many := make([]sideSrc, 60)
	for i := range many {
		id := "thr_" + leftPad(i, 3)
		many[i] = sideSrcRow(id, "", nil, sideCount(1), sideBack(int64(i)*1000))
	}
	first := sideView(&sidePage{Threads: many[:otherPage], Next: true}, nil)
	if len(first.Rows) != otherPage || first.More != "show" {
		t.Fatalf("first page %d %s", len(first.Rows), first.More)
	}
	long := sideView(&sidePage{Threads: many}, nil)
	if len(long.Rows) != otherPage || long.More != "show" {
		t.Fatalf("cut %d %s", len(long.Rows), long.More)
	}
	if got := sideView(&sidePage{Threads: many[:10]}, nil); got.More != "" {
		t.Fatalf("short more %q", got.More)
	}
	capped := sideView(&sidePage{Threads: many[:10], Next: true}, func(in *sideOthers) { in.Limit = otherMax })
	if capped.More != "dash" {
		t.Fatalf("cap more %q", capped.More)
	}
}

func leftPad(n, width int) string {
	s := strconvI(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestNextLimit(t *testing.T) {
	if nextLimit(otherPage) != otherPage*2 || nextLimit(otherMax-10) != otherMax || nextLimit(otherMax) != otherMax {
		t.Fatalf("nextLimit %d %d %d", nextLimit(otherPage), nextLimit(otherMax-10), nextLimit(otherMax))
	}
}

func TestUnreadOf(t *testing.T) {
	m := map[string]any{"thr_a": 3, "thr_z": 0, "thr_neg": -1, "thr_f": 1.5, "thr_s": "2"}
	if unreadOf(m, "thr_a") != 3 {
		t.Fatal(unreadOf(m, "thr_a"))
	}
	for _, id := range []string{"thr_z", "thr_neg", "thr_f", "thr_s", "thr_missing", "toString", "__proto__"} {
		if unreadOf(m, id) != 0 {
			t.Fatalf("%s = %d", id, unreadOf(m, id))
		}
	}
	if unreadOf(nil, "thr_a") != 0 {
		t.Fatal("nil map")
	}
}

func TestUnreadLabel(t *testing.T) {
	if unreadLabel(0) != "" || unreadLabel(1) != "1" || unreadLabel(unreadMax) != "99" || unreadLabel(unreadMax+1) != "99+" || unreadLabel(12345) != "99+" {
		t.Fatalf("%q %q %q %q %q", unreadLabel(0), unreadLabel(1), unreadLabel(unreadMax), unreadLabel(unreadMax+1), unreadLabel(12345))
	}
}

func TestYourThreadsUnreadFromInbox(t *testing.T) {
	page := &sidePage{Threads: []sideSrc{
		sideSrcRow("thr_c", "Release plan", []string{"peer_me", "peer_a"}, sideCount(12), sideBack(60_000)),
		sideSrcRow("thr_b", "", []string{"peer_me", "peer_b"}, sideCount(4), sideBack(60_000)),
		sideSrcRow("thr_a", "Quiet", []string{"peer_me", "peer_a"}, sideCount(2), sideBack(60_000)),
	}}
	head := map[string]any{"thr_c": 5, "thr_b": 120, "thr_gone": 1}
	v := sideMine(func(in *sideYours) { in.Page = page; in.Unread = head })
	want := [][3]string{{"thr_c", "5", "5"}, {"thr_b", "120", "99+"}, {"thr_a", "0", ""}}
	for i, r := range v.Rows {
		if r.ID != want[i][0] || itoa(r.Unread) != want[i][1] || r.UnreadText != want[i][2] {
			t.Fatalf("row %d %+v", i, r)
		}
	}
	if v.Rows[0].Count != "12" {
		t.Fatal(v.Rows[0].Count)
	}
	if ok, _ := regexp.MatchString(`12 messages · 5 unread · last active `, v.Rows[0].Tip); !ok {
		t.Fatalf("tip %q", v.Rows[0].Tip)
	}
	if regexp.MustCompile(`unread`).MatchString(v.Rows[2].Tip) {
		t.Fatalf("quiet tip %q", v.Rows[2].Tip)
	}
	listed := 0
	for _, r := range v.Rows {
		listed += r.Unread
	}
	sum := 5 + 120 + 1
	if listed != sum-1 {
		t.Fatalf("listed %d", listed)
	}
	sums := []sideThread{sideSummary("thr_c", "", nil, 1), sideSummary("thr_a", "Quiet", nil, 1)}
	early := sideMine(func(in *sideYours) { in.Summaries = &sums; in.Unread = head })
	if early.Rows[0].Unread != 5 || early.Rows[1].Unread != 0 {
		t.Fatalf("early %+v", early.Rows)
	}
	none := sideMine(func(in *sideYours) { in.Page = page })
	for _, r := range none.Rows {
		if r.UnreadText != "" {
			t.Fatalf("no map: %+v", r)
		}
	}
}

func itoa(n int) string { return strconvI(n) }

func TestYourThreadsUnreadFollowsHead(t *testing.T) {
	page := &sidePage{Threads: []sideSrc{
		sideSrcRow("thr_a", "A", []string{"peer_me", "peer_a"}, sideCount(1), sideBack(1000)),
		sideSrcRow("thr_b", "B", []string{"peer_me", "peer_b"}, sideCount(1), sideBack(1000)),
	}}
	counts := func(by map[string]any) []int {
		v := sideMine(func(in *sideYours) { in.Page = page; in.Unread = by })
		out := make([]int, len(v.Rows))
		for i, r := range v.Rows {
			out[i] = r.Unread
		}
		return out
	}
	if a := counts(map[string]any{"thr_a": 2, "thr_b": 1}); a[0] != 2 || a[1] != 1 {
		t.Fatal(a)
	}
	if a := counts(map[string]any{"thr_b": 1}); a[0] != 0 || a[1] != 1 {
		t.Fatal(a)
	}
	if a := counts(map[string]any{"thr_a": 1, "thr_b": 1}); a[0] != 1 || a[1] != 1 {
		t.Fatal(a)
	}
}

func TestOtherThreadsShowNoUnread(t *testing.T) {
	v := sideView(&sidePage{Threads: []sideSrc{sideDefault("thr_o")}}, nil)
	if v.Rows[0].Unread != 0 || v.Rows[0].UnreadText != "" {
		t.Fatalf("%+v", v.Rows[0])
	}
}
