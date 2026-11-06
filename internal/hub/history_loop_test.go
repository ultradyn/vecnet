package hub_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func historyEraseLoopBodies(t *testing.T, e *testhub.Env, ids ...string) {
	t.Helper()
	for _, id := range ids {
		historySettle(t, e, id)
	}
	e.Now.Advance(48 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historySweep(t, e)
	for _, id := range ids {
		if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, id) != 0 {
			t.Fatal("loop fixture body was not swept")
		}
	}
}

func TestHistoryGCImplicitLoopParent(t *testing.T) {
	for _, keepOlder := range []bool{false, true} {
		for _, limit := range []string{"expired", "max_depth_exceeded", "continuity"} {
			t.Run(fmt.Sprintf("%s/older_retained=%v", limit, keepOlder), func(t *testing.T) {
				e := historyEnv(t, func(c *hub.Config) {
					if limit != "expired" {
						c.DefaultTTL = 7 * 24 * time.Hour
					}
				})
				a, as := historyPeer(t, e, "a")
				b, bs := historyPeer(t, e, "b")
				request := req("first", b)
				request["max_depth"] = 2
				first := mustSend(t, e, as, request)
				second := historySend(t, e, bs, "second", first.ThreadID, a)
				latest := second
				ids := []string{second.MessageID}
				if limit == "max_depth_exceeded" {
					latest = historySend(t, e, as, "third", first.ThreadID, b)
					ids = append(ids, latest.MessageID)
				}
				before := storedLoop(t, e, latest.MessageID)
				if !keepOlder {
					ids = append(ids, first.MessageID)
				}
				historyEraseLoopBodies(t, e, ids...)
				// The compact state must survive export/restore, even if an older
				// retained body still exists and GC visited candidates out of order.
				e = e.CloneDB(t)
				if count(t, e, `SELECT count(*) FROM thread_history_loop_parents WHERE thr_id=?`, first.ThreadID) != 1 {
					t.Fatal("loop metadata must occupy one compact row per swept thread")
				}
				if limit != "continuity" {
					request = replyReq("cut", first.ThreadID, "")
					request["max_depth"] = 64
					request["expires_at"] = envelope.FormatTime(e.Now.Now().Add(7 * 24 * time.Hour))
					response, raw := e.Do(as, "POST", "/v1/messages", request)
					if response.StatusCode != http.StatusUnprocessableEntity {
						t.Fatalf("post-GC implicit send status=%d want 422 %s", response.StatusCode, limit)
					}
					code, _ := apiError(t, raw)
					if code != limit {
						t.Fatalf("post-GC rejection=%s want %s", code, limit)
					}
				} else {
					next := historySend(t, e, as, "continued", first.ThreadID, b)
					loop := storedLoop(t, e, next.MessageID)
					if loop.Depth != before.Depth+1 || loop.MaxDepth != before.MaxDepth || loop.ExpiresAt != before.ExpiresAt || !chainEq(loop.SenderChain, []string{a, b, a}) {
						t.Fatal("post-GC implicit send lost loop continuity")
					}
				}
				if keepOlder && limit != "continuity" {
					// Sweep the older pinned body in a later transaction: it must
					// not replace the newest erased parent's loop state.
					historySettle(t, e, first.MessageID)
					historySweep(t, e)
					historySweep(t, e)
					response, raw := e.Do(as, "POST", "/v1/messages", replyReq("cut-after-older-sweep", first.ThreadID, ""))
					if response.StatusCode != http.StatusUnprocessableEntity {
						t.Fatalf("older sweep reset loop: status=%d", response.StatusCode)
					}
					if code, _ := apiError(t, raw); code != limit {
						t.Fatalf("older sweep changed rejection=%s want %s", code, limit)
					}
				}
				// Operator revival still resets depth and TTL, preserving the chain.
				revived := historySend(t, e, e.OperatorSecret, "revive", first.ThreadID, b)
				loop := storedLoop(t, e, revived.MessageID)
				if loop.Depth != 0 || loop.MaxDepth != before.MaxDepth || loop.ExpiresAt != envelope.FormatTime(e.Now.Now().Add(e.Hub.Cfg.DefaultTTL)) || loop.SenderChain[0] != a || loop.SenderChain[len(loop.SenderChain)-1] != e.OperatorPeer {
					t.Fatal("post-GC operator revival lost loop semantics")
				}
				next := historySend(t, e, bs, "after-revival", first.ThreadID, a)
				if got := storedLoop(t, e, next.MessageID); got.Depth != 1 || got.ExpiresAt != loop.ExpiresAt || !chainEq(got.SenderChain, append(loop.SenderChain, b)) {
					t.Fatal("retained operator revival did not replace erased implicit parent")
				}
			})
		}
	}
}

func TestHistoryGCSourceStartsFreshLoop(t *testing.T) {
	e := historyEnv(t, nil)
	a, _ := historyPeer(t, e, "a")
	b, bs := historyPeer(t, e, "b")
	ctx := context.Background()
	src, _, _, err := e.Hub.AddSource(ctx, "github", "synthetic-history-source", b, nil, "hold")
	if err != nil {
		t.Fatal("synthetic source creation failed")
	}
	ingest := func(key string) string {
		t.Helper()
		id, dropped, err := e.Hub.IngestSource(ctx, hub.SourceMessage{
			SrcID: src, ProviderDeliveryID: key, OriginThreadID: "issue/1",
			OriginEventID: key, EventName: "issues.opened", Summary: "synthetic source",
			Raw: []byte(`{}`), MediaType: "application/json",
		})
		if err != nil || dropped {
			t.Fatal("synthetic source ingestion failed")
		}
		return id
	}
	first := ingest("first")
	thread := storedMessage(t, e, first).ThreadID
	last := historySend(t, e, bs, "source-reply", thread, a)
	historyEraseLoopBodies(t, e, last.MessageID)
	e = e.CloneDB(t)
	next := ingest("next")
	m := storedMessage(t, e, next)
	if m.ThreadID != thread || m.Loop.Depth != 0 || m.Loop.ExpiresAt != envelope.FormatTime(e.Now.Now().Add(24*time.Hour)) || !chainEq(m.Loop.SenderChain, []string{src}) {
		t.Fatal("post-GC source event did not start a fresh loop in its mapped thread")
	}
	continued := historySend(t, e, bs, "new-source-reply", thread, a)
	if loop := storedLoop(t, e, continued.MessageID); loop.Depth != 1 || loop.ExpiresAt != m.Loop.ExpiresAt || !chainEq(loop.SenderChain, []string{src, b}) {
		t.Fatal("peer did not inherit the fresh source loop after GC")
	}
}

func TestHistoryGCLoopParentCommitsWithBodyDeletion(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := historySend(t, e, as, "atomic-loop", "", b)
	historySettle(t, e, m.MessageID)
	e.Now.Advance(48 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historyExec(t, e, `CREATE TRIGGER synthetic_history_delete_failure BEFORE DELETE ON messages BEGIN SELECT RAISE(ABORT,'synthetic_delete_failure'); END`)
	status, err := e.Hub.PruneHistorySlice(context.Background())
	if err == nil || status.Deleted != 0 {
		t.Fatal("synthetic deletion failure did not roll back sweep")
	}
	if count(t, e, `SELECT count(*) FROM thread_history_loop_parents`) != 0 || count(t, e, `SELECT count(*) FROM message_history_locators`) != 0 || count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 1 {
		t.Fatal("failed deletion committed partial loop or locator state")
	}
	historyExec(t, e, `DROP TRIGGER synthetic_history_delete_failure`)
	historySweep(t, e)
	if count(t, e, `SELECT count(*) FROM thread_history_loop_parents WHERE thr_id=? AND msg_id=?`, m.ThreadID, m.MessageID) != 1 || count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 0 {
		t.Fatal("retry did not commit compact loop state with body deletion")
	}
}
