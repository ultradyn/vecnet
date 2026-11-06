package tui

import (
	"strconv"
	"strings"
	"time"
)

// Sidebar list rules. These match internal/ui/static/sidebar-model.js, which
// the web sidebar already uses. docs/OPERATOR.md states the ordering once:
// Your Threads keeps the hub's order (newest activity first); Other Threads
// is sorted the same way here, last_message_at descending, then thread id
// descending. Unread counts are only for Your Threads.

const (
	otherPage = 50
	otherMax  = 200
	unreadMax = 99
)

// sideThread is one thread the sidebar can show. A nil Count is a missing
// message_count (an older summary); zero is a real zero.
type sideThread struct {
	ID           string
	Title        string
	Participants []string
	Count        *int
}

// sideSrc is a ThreadRow: the thread plus its last activity.
type sideSrc struct {
	Thread        sideThread
	LastMessageAt string
}

// sidePage is one GET /v1/admin/threads page.
type sidePage struct {
	Threads []sideSrc
	Next    bool
}

// sideRow is one sidebar row, the same fields the web row carries.
type sideRow struct {
	ID, Href, Title, People, Count, Active, Tip, UnreadText string
	Untitled                                                bool
	Unread                                                  int
	IDs                                                     []string
}

// sideList is yourThreads or otherThreads: state, placeholder note, rows,
// and for Other Threads how to see the rest ("" / "show" / "dash").
type sideList struct {
	State, Note, More string
	Rows              []sideRow
}

type sideYours struct {
	Page      *sidePage
	Summaries *[]sideThread
	Err       error
	Me        string
	NameOf    func(string) string
	Now       int64
	Unread    map[string]any
}

type sideOthers struct {
	Page   *sidePage
	Err    error
	Mine   []string
	Me     string
	NameOf func(string) string
	Now    int64
	Limit  int
}

func nextLimit(limit int) int {
	n := limit + otherPage
	if n > otherMax {
		return otherMax
	}
	return n
}

func peopleLine(ids []string, nameOf func(string) string) string {
	if nameOf == nil {
		nameOf = func(id string) string { return id }
	}
	if len(ids) == 0 {
		return ""
	}
	n := min(len(ids), 3)
	shown := make([]string, n)
	for i := 0; i < n; i++ {
		shown[i] = nameOf(ids[i])
	}
	s := strings.Join(shown, ", ")
	if len(ids) > 3 {
		s += " +" + strconv.Itoa(len(ids)-3)
	}
	return s
}

func unreadOf(by map[string]any, id string) int {
	if by == nil {
		return 0
	}
	v, ok := by[id]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		if n > 0 {
			return n
		}
	case int64:
		if n > 0 {
			return int(n)
		}
	}
	return 0
}

func unreadOfInts(by map[string]int, id string) int {
	if by == nil {
		return 0
	}
	n, ok := by[id]
	if !ok || n <= 0 {
		return 0
	}
	return n
}

func unreadLabel(n int) string {
	switch {
	case n > unreadMax:
		return strconv.Itoa(unreadMax) + "+"
	case n > 0:
		return strconv.Itoa(n)
	default:
		return ""
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// sideShort is the web's short id: head through the underscore, ellipsis, last five.
func sideShort(id string) string {
	if id == "" {
		return "?"
	}
	if len(id) <= 14 {
		return id
	}
	cut := strings.IndexByte(id, '_') + 1
	if cut <= 0 || cut >= len(id)-5 {
		return id
	}
	return id[:cut] + "…" + id[len(id)-5:]
}

func sideName(nameOf func(string) string, id string) string {
	if nameOf == nil {
		return id
	}
	return nameOf(id)
}

func cleanSideName(s string) string {
	return strings.Join(strings.Fields(Clean(s)), " ")
}

func standInTitle(t sideThread, me string, nameOf func(string) string) string {
	var names []string
	for _, id := range t.Participants {
		if id == me {
			continue
		}
		name := cleanSideName(sideName(nameOf, id))
		if name == "" {
			name = sideShort(id)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return sideShort(t.ID)
	}
	if len(names) == 2 && len(names) == len(t.Participants) {
		return names[0] + " ↔ " + names[1]
	}
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return names[0] + ", " + names[1] + " +" + strconv.Itoa(len(names)-2)
}

func ago(iso string, nowMs int64) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return ""
	}
	s := (nowMs - t.UnixMilli()) / 1000
	switch {
	case s < 1:
		return "now"
	case s < 60:
		return strconv.FormatInt(s, 10) + "s ago"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m ago"
	case s < 86400:
		return strconv.FormatInt(s/3600, 10) + "h ago"
	default:
		return strconv.FormatInt(s/86400, 10) + "d ago"
	}
}

func fullWhen(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return ""
	}
	return t.UTC().Format("Mon, 2 Jan 2006, 15:04:05")
}

func threadRow(r sideSrc, me string, nameOf func(string) string, now int64, unread int) sideRow {
	t := r.Thread
	ids := t.Participants
	untitled := t.Title == ""
	title := t.Title
	if untitled {
		title = standInTitle(t, me, nameOf)
	}
	var all []string
	var others []string
	for _, p := range ids {
		if p == me {
			all = append(all, "you")
			continue
		}
		all = append(all, sideName(nameOf, p))
		others = append(others, p)
	}
	people := peopleLine(others, nameOf)
	rowIDs := append([]string(nil), others...)
	if untitled {
		people = plural(len(ids), "participant")
		rowIDs = []string{}
	}
	count := ""
	var facts []string
	if t.Count != nil {
		count = strconv.Itoa(*t.Count)
		facts = append(facts, plural(*t.Count, "message"))
	}
	if unread > 0 {
		facts = append(facts, strconv.Itoa(unread)+" unread")
	}
	if r.LastMessageAt != "" {
		facts = append(facts, "last active "+fullWhen(r.LastMessageAt))
	}
	tipParts := make([]string, 0, 3)
	if title != "" {
		tipParts = append(tipParts, title)
	}
	if joined := strings.Join(all, ", "); joined != "" {
		tipParts = append(tipParts, joined)
	}
	if joined := strings.Join(facts, " · "); joined != "" {
		tipParts = append(tipParts, joined)
	}
	return sideRow{
		ID: t.ID, Href: "#thread/" + t.ID, Title: title, Untitled: untitled,
		People: people, Count: count, Unread: unread, UnreadText: unreadLabel(unread),
		Active: ago(r.LastMessageAt, now), Tip: strings.Join(tipParts, "\n"), IDs: rowIDs,
	}
}

func yourThreads(in sideYours) sideList {
	var src []sideSrc
	switch {
	case in.Page != nil:
		src = in.Page.Threads
	case in.Summaries != nil:
		src = make([]sideSrc, len(*in.Summaries))
		for i, t := range *in.Summaries {
			src[i] = sideSrc{Thread: t}
		}
	default:
		if in.Err != nil {
			return sideList{State: "error", Note: "Threads unavailable"}
		}
		return sideList{State: "loading", Note: "…"}
	}
	rows := make([]sideRow, 0, len(src))
	for _, r := range src {
		if r.Thread.ID == "" && r.Thread.Title == "" && r.Thread.Participants == nil && r.Thread.Count == nil {
			continue
		}
		rows = append(rows, threadRow(r, in.Me, in.NameOf, in.Now, unreadOf(in.Unread, r.Thread.ID)))
	}
	if len(rows) == 0 {
		return sideList{State: "empty", Note: "No threads yet"}
	}
	return sideList{State: "ok", Rows: rows}
}

func byActivity(a, b sideSrc) int {
	if a.LastMessageAt != b.LastMessageAt {
		if a.LastMessageAt < b.LastMessageAt {
			return 1
		}
		return -1
	}
	if a.Thread.ID < b.Thread.ID {
		return 1
	}
	if a.Thread.ID > b.Thread.ID {
		return -1
	}
	return 0
}

func otherThreads(in sideOthers) sideList {
	limit := in.Limit
	if limit <= 0 {
		limit = otherPage
	}
	if in.Page == nil {
		if in.Err != nil {
			return sideList{State: "error", Note: "Other threads unavailable"}
		}
		return sideList{State: "loading", Note: "…"}
	}
	listed := map[string]bool{}
	for _, id := range in.Mine {
		listed[id] = true
	}
	kept := make([]sideSrc, 0, len(in.Page.Threads))
	for _, r := range in.Page.Threads {
		if r.Thread.ID == "" && r.Thread.Title == "" && r.Thread.Participants == nil {
			continue
		}
		if listed[r.Thread.ID] || (in.Me != "" && containsString(r.Thread.Participants, in.Me)) {
			continue
		}
		kept = append(kept, r)
	}
	// Newest activity first, ties by id descending, matching the web's byActivity.
	for i := 1; i < len(kept); i++ {
		j := i
		for j > 0 && byActivity(kept[j], kept[j-1]) < 0 {
			kept[j], kept[j-1] = kept[j-1], kept[j]
			j--
		}
	}
	more := ""
	if len(kept) > limit || in.Page.Next {
		if limit >= otherMax {
			more = "dash"
		} else {
			more = "show"
		}
	}
	if len(kept) > limit {
		kept = kept[:limit]
	}
	rows := make([]sideRow, len(kept))
	for i, r := range kept {
		rows[i] = threadRow(r, in.Me, in.NameOf, in.Now, 0)
	}
	if len(rows) == 0 {
		return sideList{State: "empty", Note: "No other threads", More: more}
	}
	return sideList{State: "ok", Rows: rows, More: more}
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
