package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Check the public response, including omission for existing clients and the
// absence of history details a sender might not have permission to read.
func warningGroups(t *testing.T, sr hub.SendResponse, want ...string) {
	t.Helper()
	raw, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	warnings, present := response["warnings"]
	if len(want) == 0 {
		if present {
			t.Fatalf("unexpected warnings in successful send: %s", raw)
		}
		return
	}
	var entries []map[string]any
	if err := json.Unmarshal(warnings, &entries); err != nil {
		t.Fatalf("missing warning array: %s (%v)", raw, err)
	}
	var got []string
	for _, w := range entries {
		if len(w) != 5 || w["code"] != "rapid_thread_creation" || w["window_seconds"] != float64(60) || w["threshold"] != float64(2) {
			t.Fatalf("warning shape: %v", w)
		}
		message, _ := w["message"].(string)
		lower := strings.ToLower(message)
		if !strings.Contains(lower, "succeeded") || !strings.Contains(lower, "reply") || !strings.Contains(lower, "related") || !strings.Contains(lower, "distinct") || !strings.Contains(lower, "batch") {
			t.Fatalf("warning must explain success and gentle thread reuse: %q", message)
		}
		if strings.Contains(message, "thr_") || strings.Contains(message, "msg_") || strings.Contains(message, "hello ") {
			t.Fatalf("warning leaks thread history: %q", message)
		}
		group, _ := w["group_id"].(string)
		got = append(got, group)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("warning groups = %v, want %v; response %s", got, want, raw)
	}
}

func TestRapidThreadWarningGroupWideAndOrdered(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	g1 := addGroup(t, env, a, a, b, c)
	g2 := addGroup(t, env, a, a, b, c)
	g3 := addGroup(t, env, a, a, b, c)
	warningGroups(t, mustSend(t, env, aSec, req("one", g1, g2)))
	warningGroups(t, mustSend(t, env, bSec, req("two", c, g2, g1, g2)))
	warningGroups(t, mustSend(t, env, aSec, req("three", c, g2, g1, g2)), g2, g1)
	warningGroups(t, mustSend(t, env, bSec, req("four", g1)), g1)
	warningGroups(t, mustSend(t, env, aSec, req("isolated", g3)))
	if n := count(t, env, `SELECT count(*) FROM group_thread_creations`); n != 8 {
		t.Fatalf("addressed-group creation rows = %d, want 8", n)
	}
}

func TestRapidThreadWarningRollingBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
		warn    bool
	}{
		{"inside", 60*time.Second - time.Millisecond, true},
		{"exactly60", 60 * time.Second, false},
		{"expired", 60*time.Second + time.Millisecond, false},
		{"future_excluded", -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			a, sec := env.NewPeer("session", "A")
			b, _ := env.NewPeer("session", "B")
			g := addGroup(t, env, a, a, b)
			mustSend(t, env, sec, req("first", g))
			mustSend(t, env, sec, req("second", g))
			env.Now.Advance(tc.advance)
			sr := mustSend(t, env, sec, req("boundary", g))
			if tc.warn {
				warningGroups(t, sr, g)
			} else {
				warningGroups(t, sr)
			}
		})
	}
}

func TestRapidThreadWarningRepliesAndDirectSendsDoNotCount(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := addGroup(t, env, a, a, b)
	for i := range 2 {
		warningGroups(t, mustSend(t, env, sec, req(fmt.Sprint("direct", i), b)))
	}
	first := mustSend(t, env, sec, req("first", g))
	for i := range 3 {
		body := req(fmt.Sprint("reply", i), g)
		body["thread_id"] = first.ThreadID
		if i == 0 {
			body["in_reply_to"] = first.MessageID
		}
		warningGroups(t, mustSend(t, env, sec, body))
	}
	warningGroups(t, mustSend(t, env, sec, req("second", g)))
	warningGroups(t, mustSend(t, env, sec, req("third", g)), g)
	if n := count(t, env, `SELECT count(*) FROM group_thread_creations WHERE group_id=?`, g); n != 3 {
		t.Fatalf("creations = %d, want 3", n)
	}
}

func TestRapidThreadWarningReplayFreezesSnapshot(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := addGroup(t, env, a, a, b)
	first := req("first", g)
	third := req("third", g)
	_, _, firstRaw := sendHTTP(t, env, sec, first)
	mustSend(t, env, sec, req("second", g))
	status, sr, thirdRaw := sendHTTP(t, env, sec, third)
	if status != http.StatusAccepted {
		t.Fatalf("third = %d: %s", status, thirdRaw)
	}
	warningGroups(t, sr, g)
	for _, advance := range []time.Duration{0, 61 * time.Second} {
		env.Now.Advance(advance)
		for _, tc := range []struct {
			body any
			want []byte
		}{{first, firstRaw}, {third, thirdRaw}} {
			status, _, raw := sendHTTP(t, env, sec, tc.body)
			if status != http.StatusOK || !bytes.Equal(raw, tc.want) {
				t.Fatalf("replay = %d %s, want original %s", status, raw, tc.want)
			}
		}
	}
	if n := count(t, env, `SELECT count(*) FROM group_thread_creations WHERE group_id=?`, g); n != 3 {
		t.Fatalf("replays changed creation count: %d", n)
	}
	warningGroups(t, mustSend(t, env, sec, req("after-expiry", g)))
}

func TestRapidThreadWarningRejectedAndRolledBackSendsDoNotCount(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := addGroup(t, env, a, a, b)
	mustSend(t, env, sec, req("first", g))
	status, _, _ := sendHTTP(t, env, sec, req("missing", g, ids.New(ids.Grp)))
	if status/100 == 2 {
		t.Fatal("unknown group was accepted")
	}
	// Unlike ordinary rejection/rollback, these cuts commit their audit counters.
	expired := req("expired-creation", g)
	expired["expires_at"] = envelope.FormatTime(env.Now.Now().Add(-time.Second))
	mustReject(t, env, sec, expired, http.StatusUnprocessableEntity, "expired")
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET threads_per_hour=1 WHERE peer_id=?`, a); err != nil {
		t.Fatal(err)
	}
	mustReject(t, env, sec, req("rate-cut", g), http.StatusTooManyRequests, "rate_limited")
	if _, err := env.Hub.DB.Exec(`UPDATE peers SET threads_per_hour=20 WHERE peer_id=?`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER fail_warning_send BEFORE INSERT ON idempotency WHEN NEW.key='rollback' BEGIN SELECT RAISE(ABORT,'synthetic late send failure'); END`); err != nil {
		t.Fatal(err)
	}
	caller, err := env.Hub.Auth.Resolve(context.Background(), sec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req("rollback", g))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.Hub.Send(context.Background(), caller, raw, "http_send"); err == nil {
		t.Fatal("late failure was accepted")
	}
	if n := count(t, env, `SELECT count(*) FROM threads WHERE created_by=?`, a); n != 1 {
		t.Fatalf("rolled-back thread remains: %d", n)
	}
	warningGroups(t, mustSend(t, env, sec, req("second", g)))
	warningGroups(t, mustSend(t, env, sec, req("third", g)), g)
	if n := count(t, env, `SELECT count(*) FROM group_thread_creations WHERE group_id=?`, g); n != 3 {
		t.Fatalf("rejected sends counted: %d", n)
	}
}

func TestRapidThreadWarningConcurrentAndPersisted(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := addGroup(t, env, a, a, b)
	caller, err := env.Hub.Auth.Resolve(context.Background(), sec)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		response *hub.SendResponse
		replayed bool
		err      error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(req(fmt.Sprint("concurrent", i), g))
			<-start
			sr, replayed, err := env.Hub.Send(context.Background(), caller, raw, "http_send")
			results <- result{sr, replayed, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	warned := 0
	for r := range results {
		if r.err != nil || r.replayed || r.response == nil {
			t.Fatalf("concurrent send failed: %+v", r)
		}
		raw, _ := json.Marshal(r.response)
		if bytes.Contains(raw, []byte(`"warnings"`)) {
			warned++
			warningGroups(t, *r.response, g)
		}
	}
	if warned != 6 {
		t.Fatalf("warned %d of 8 successful concurrent new threads, want 6", warned)
	}
	if n := count(t, env, `SELECT count(*) FROM group_thread_creations WHERE group_id=?`, g); n != 8 {
		t.Fatalf("concurrent creations = %d", n)
	}
	restarted := env.CloneDB(t)
	warningGroups(t, mustSend(t, restarted, sec, req("after-restart", g)), g)
}

func TestRapidThreadWarningSourceCreationsShareAccounting(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := warningTeam(t, env, a, b)
	src, _, _, err := env.Hub.AddSource(context.Background(), "github", "warning-source", g, nil, "hold")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		event := hub.SourceMessage{SrcID: src, ProviderDeliveryID: fmt.Sprint("delivery", i), OriginThreadID: fmt.Sprint("issue", i), OriginEventID: fmt.Sprint("event", i), EventName: "issues.opened", Summary: "opened", Raw: []byte(`{}`), MediaType: "application/json"}
		first, dropped, err := env.Hub.IngestSource(context.Background(), event)
		if err != nil || dropped {
			t.Fatalf("ingest = %s %v %v", first, dropped, err)
		}
		again, dropped, err := env.Hub.IngestSource(context.Background(), event)
		if err != nil || dropped || again != first {
			t.Fatalf("source replay = %s %v %v", again, dropped, err)
		}
		event.ProviderDeliveryID += "-reply"
		event.OriginEventID += "-reply"
		if _, dropped, err := env.Hub.IngestSource(context.Background(), event); err != nil || dropped {
			t.Fatalf("source reply = %v %v", dropped, err)
		}
	}
	warningGroups(t, mustSend(t, env, sec, req("after-sources", g)), g)
	if n := count(t, env, `SELECT count(*) FROM group_thread_creations WHERE group_id=?`, g); n != 3 {
		t.Fatalf("source accounting = %d, want 3", n)
	}
}

func TestRapidThreadWarningUpgradeContinuity(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := addGroup(t, env, a, a, b)
	// Both legacy threads have no primary group: the first recipient is direct.
	mustSend(t, env, sec, req("legacy-one", b, g))
	mustSend(t, env, sec, req("legacy-two", b, g))
	if _, err := env.Hub.DB.Exec(`DROP TRIGGER IF EXISTS group_thread_creations_delete_group; DROP TRIGGER IF EXISTS group_thread_creations_delete_team; DROP TABLE IF EXISTS group_thread_creations; DELETE FROM schema_migrations WHERE name='0038_group_thread_creations.sql'`); err != nil {
		t.Fatal(err)
	}
	upgraded := env.CloneDB(t)
	warningGroups(t, mustSend(t, upgraded, sec, req("after-upgrade", g)), g)
}

func warningTeam(t *testing.T, env *testhub.Env, members ...string) string {
	t.Helper()
	caller, err := env.Hub.Auth.Resolve(context.Background(), env.OperatorSecret)
	if err != nil {
		t.Fatal(err)
	}
	team, err := env.Hub.CreateTeam(context.Background(), caller, "warning-team", members)
	if err != nil {
		t.Fatal(err)
	}
	return team
}

func TestRapidThreadWarningMixedTeamsAndGroups(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	g := addGroup(t, env, a, a, b)
	team := warningTeam(t, env, a, b)
	warningGroups(t, mustSend(t, env, sec, req("one", b, team, g, team)))
	warningGroups(t, mustSend(t, env, sec, req("two", g, team)))
	warningGroups(t, mustSend(t, env, sec, req("three", b, team, g, team)), team, g)
	// The warning history persists for both recipient kinds across upgrades.
	if _, err := env.Hub.DB.Exec(`DROP TRIGGER group_thread_creations_delete_group; DROP TRIGGER group_thread_creations_delete_team; DROP TABLE group_thread_creations; DELETE FROM schema_migrations WHERE name='0038_group_thread_creations.sql'`); err != nil {
		t.Fatal(err)
	}
	upgraded := env.CloneDB(t)
	warningGroups(t, mustSend(t, upgraded, sec, req("after-upgrade", g, team)), g, team)
}
