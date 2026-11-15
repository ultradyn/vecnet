package httpapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestThreadArchiveRoutes(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, bs := env.NewPeer("session", "B")
	sent := postSend(t, env, as, sendBody("archive", b), 202)
	path := "/v1/admin/threads/" + sent.ThreadID
	for _, action := range []string{"archive", "unarchive"} {
		r, raw := env.Do(as, "POST", path+"/"+action, nil)
		mustErr(t, r, raw, 403, "forbidden")
		r, raw = env.Do(env.OperatorSecret, "POST", "/v1/admin/threads/"+ids.New(ids.Thr)+"/"+action, nil)
		mustErr(t, r, raw, 404, "unknown_thread")
		r, raw = env.Do(env.OperatorSecret, "POST", path+"/"+action, map[string]any{"extra": true})
		mustErr(t, r, raw, 400, "invalid_body")
	}
	for i := 0; i < 2; i++ {
		adminDo(t, env, "POST", path+"/archive", nil, 204)
	}
	var archivedBy string
	if err := env.Hub.DB.QueryRow(`SELECT archived_by FROM threads WHERE thr_id=?`, sent.ThreadID).Scan(&archivedBy); err != nil || archivedBy != "operator" {
		t.Fatalf("archived_by=%s %v", archivedBy, err)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM audit WHERE action='thread_archive' AND actor='operator'`); n != 1 {
		t.Fatalf("archive audit=%d", n)
	}
	// Reactions mutate the message, not operator archive state.
	r, raw := env.Do(bs, "POST", "/v1/reactions", map[string]any{"message_id": sent.MessageID, "emoji": "✅", "op": "add"})
	if r.StatusCode != 200 {
		t.Fatalf("reaction=%d %s", r.StatusCode, raw)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM threads WHERE archived_at IS NOT NULL`); n != 1 {
		t.Fatal("reaction unarchived thread")
	}
	for i := 0; i < 2; i++ {
		adminDo(t, env, "POST", path+"/unarchive", nil, 204)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM audit WHERE action='thread_unarchive' AND actor='operator'`); n != 1 {
		t.Fatalf("unarchive audit=%d", n)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM threads WHERE archived_at IS NULL AND archived_by IS NULL AND unarchived_at IS NOT NULL`); n != 1 {
		t.Fatal("unarchive state missing")
	}
	adminDo(t, env, "POST", path+"/archive", nil, 204)
	postSend(t, env, as, inThread("revive", sent.ThreadID), 202)
	if n := rowCount(t, env, `SELECT count(*) FROM threads WHERE archived_at IS NULL AND archived_by IS NULL AND unarchived_at IS NOT NULL`); n != 1 {
		t.Fatal("new message did not unarchive thread")
	}
	evs, err := env.Hub.Log.Read(context.Background(), []string{env.OperatorPeer}, mustCursor(t, zeroCursor(env)), 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range evs {
		if ev.Type == "operator_view" {
			var d struct {
				Reason   string `json:"reason"`
				ThreadID string `json:"thread_id"`
			}
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				t.Fatal(err)
			}
			if d.Reason == "archive" && d.ThreadID == sent.ThreadID {
				count++
			}
		}
	}
	if count != 4 {
		t.Fatalf("archive invalidations=%d, want 4 (including new-message unarchive)", count)
	}
}

func TestThreadArchiveFiltersAndStats(t *testing.T) {
	env := testhub.Start(t)
	_, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	var threads []string
	for i, title := range []string{"Alpha first", "ALPHA second", "Alpha archived", "unrelated", ""} {
		sent := postSend(t, env, as, sendBody("filter-"+string(rune('a'+i)), b), 202)
		threads = append(threads, sent.ThreadID)
		if title != "" {
			adminDo(t, env, "PATCH", "/v1/admin/threads/"+sent.ThreadID, map[string]any{"title": title}, 204)
		}
	}
	adminDo(t, env, "POST", "/v1/admin/threads/"+threads[2]+"/archive", nil, 204)
	query := func(q string) opapi.ThreadPage {
		t.Helper()
		r, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/threads?"+q, nil)
		if r.StatusCode != 200 {
			t.Fatalf("threads=%d %s", r.StatusCode, raw)
		}
		var p opapi.ThreadPage
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := query("q=aLpHa&archived=false&limit=1")
	if len(p.Threads) != 1 || p.Next == nil {
		t.Fatalf("first=%+v", p)
	}
	next := query("q=aLpHa&archived=false&limit=1&cursor=" + url.QueryEscape(*p.Next))
	if len(next.Threads) != 1 || next.Next != nil || next.Threads[0].Thread.ThreadID == p.Threads[0].Thread.ThreadID {
		t.Fatalf("next=%+v", next)
	}
	p = query("archived=true&q=alpha")
	if len(p.Threads) != 1 || p.Threads[0].Thread.ThreadID != threads[2] {
		t.Fatalf("archived=%+v", p)
	}
	raw, _ := json.Marshal(p.Threads[0])
	if !strings.Contains(string(raw), `"archived_by":"operator"`) || strings.Contains(string(raw), `"archived_at":null`) {
		t.Fatalf("row=%s", raw)
	}
	if p := query(""); len(p.Threads) != 5 {
		t.Fatalf("all=%d", len(p.Threads))
	}
	for _, q := range []string{"archived=1", "archived=", "archived=True", "q=" + url.QueryEscape(strings.Repeat("界", 129))} {
		r, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/threads?"+q, nil)
		mustErr(t, r, raw, 422, "invalid_value")
	}
	query("q=" + url.QueryEscape(strings.Repeat("界", 128)))
	now := env.Now.Now()
	for i, age := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 0, 7*24*time.Hour + time.Second, 0} {
		if _, err := env.Hub.DB.ExecContext(context.Background(), `UPDATE threads SET last_message_at=? WHERE thr_id=?`, envelope.FormatTime(now.Add(-age)), threads[i]); err != nil {
			t.Fatal(err)
		}
	}
	r, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/thread-stats", nil)
	if r.StatusCode != 200 {
		t.Fatalf("stats=%d %s", r.StatusCode, raw)
	}
	var stats struct {
		Now                   string `json:"now"`
		Total, Open, Archived int
		Active24              int `json:"active_24h"`
		Active7               int `json:"active_7d"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Now != envelope.FormatTime(now) || stats.Total != 5 || stats.Open != 4 || stats.Archived != 1 || stats.Active24 != 2 || stats.Active7 != 3 {
		t.Fatalf("stats=%s", raw)
	}
	r, raw = env.Do(as, "GET", "/v1/admin/thread-stats", nil)
	mustErr(t, r, raw, 403, "forbidden")
}
