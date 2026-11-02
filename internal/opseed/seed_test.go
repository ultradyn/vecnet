package opseed_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opseed"
	"github.com/XertroV/vecnet/internal/testhub"
)

func seed(t *testing.T, scale bool) (*testhub.Env, opseed.Scenario) {
	t.Helper()
	e := testhub.Start(t)
	c, err := e.Hub.Auth.Resolve(context.Background(), e.OperatorSecret)
	if err != nil {
		t.Fatal(err)
	}
	s, err := opseed.Seed(context.Background(), e.Hub, c, opseed.Options{Scale: scale})
	if err != nil {
		t.Fatal(err)
	}
	return e, s
}

func count(t *testing.T, e *testhub.Env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Hub.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSeedScenario(t *testing.T) {
	e, s := seed(t, false)
	if s.OperatorPeer != e.OperatorPeer {
		t.Fatal("wrong operator")
	}
	if len(s.Peers) != 10 || count(t, e, "SELECT count(*) FROM peers") != 10 {
		t.Fatal("want ten peers including operator")
	}
	for _, kind := range []string{"operator", "session", "grokbot", "dot", "openclaw", "pagchat-bot", "source"} {
		if count(t, e, "SELECT count(*) FROM peers WHERE kind = ?", kind) == 0 {
			t.Errorf("missing kind %s", kind)
		}
	}
	for _, key := range []string{"hark", "hosting", "ticket18", "digest", "quota", "hostile"} {
		if s.Threads[key] == "" {
			t.Errorf("missing thread %s", key)
		}
	}
	if count(t, e, `SELECT count(*) FROM approvals a WHERE closed_at IS NULL AND EXISTS (SELECT 1 FROM deliveries d WHERE d.msg_id=a.msg_id AND recipient=?)`, e.OperatorPeer) != 2 {
		t.Fatal("want two open approvals addressed to operator")
	}
	if len(s.DeadLetters) != 3 || count(t, e, "SELECT count(*) FROM deliveries WHERE state='dead_letter'") != 3 {
		t.Fatal("want three dead letters")
	}
	if count(t, e, "SELECT count(*) FROM deliveries WHERE state='failed' AND last_status=409") != 1 {
		t.Fatal("want one failed 409")
	}
	if count(t, e, "SELECT budget_remaining FROM threads WHERE thr_id=?", s.Threads["quota"]) != 0 {
		t.Fatal("quota must be exhausted")
	}
	const hostile = "<img src=x onerror=alert(1)>\x1b]52;c;aGk=\x07\x1b[2Jpwn\u202e"
	var body string
	if err := e.Hub.DB.QueryRow("SELECT body FROM messages WHERE thr_id=? ORDER BY seq LIMIT 1", s.Threads["hostile"]).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var m envelope.Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content.Text != hostile {
		t.Fatalf("hostile bytes changed: %q", m.Content.Text)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Name != "..\x1b[31m.txt" {
		t.Fatal("hostile attachment missing")
	}
	if len(s.Subscriptions) != 6 || count(t, e, "SELECT count(*) FROM event_subscriptions") != 6 {
		t.Fatal("want six subscriptions")
	}
	for _, reason := range []string{"token_expired", "delivery_failed", "unauthorized", "operator"} {
		if count(t, e, "SELECT count(*) FROM event_subscriptions WHERE paused_reason=?", reason) != 1 {
			t.Errorf("missing subscription health %s", reason)
		}
	}
	if count(t, e, "SELECT count(*) FROM event_subscriptions WHERE paused_reason IS NULL AND token_expires_at > ?", envelope.FormatTime(e.Now.Now())) != 2 {
		t.Fatal("want healthy and refresh-due subscriptions")
	}
	if count(t, e, "SELECT count(*) FROM event_subscriptions WHERE paused_reason IS NULL AND token_expires_at < ?", envelope.FormatTime(e.Now.Now().Add(5*time.Minute))) != 1 {
		t.Fatal("want one refresh-due subscription")
	}
	if count(t, e, "SELECT count(*) FROM peers WHERE kind='dot' AND reachability='reachable' AND presence_state IS NULL") != 1 {
		t.Fatal("dot must be reachable with unknown presence")
	}
	for _, check := range []struct{ index, options int }{{0, 3}, {1, 2}} {
		if count(t, e, "SELECT count(*) FROM approvals WHERE apr_id=? AND closed_at IS NULL AND json_array_length(options)=?", s.Approvals[check.index], check.options) != 1 {
			t.Fatal("actionable approvals must be first and have three/two options")
		}
	}
	var peerName string
	if err := e.Hub.DB.QueryRow("SELECT name FROM peers WHERE peer_id=?", s.Peers["hostile"]).Scan(&peerName); err != nil {
		t.Fatal(err)
	}
	if peerName != hostile {
		t.Fatal("hostile peer name changed")
	}
	rows, err := e.Hub.DB.Query("SELECT secret_enc,arguments,types,last_refresh_at,token_hash FROM event_subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sealed, args, types, refreshed, tokenHash string
		if err := rows.Scan(&sealed, &args, &types, &refreshed, &tokenHash); err != nil {
			t.Fatal(err)
		}
		if secret, err := e.Hub.Unseal(sealed); err != nil || len(secret) == 0 {
			t.Fatal("invalid callback secret")
		}
		if !json.Valid([]byte(args)) || !json.Valid([]byte(types)) || refreshed == "" || tokenHash == "" {
			t.Fatal("invalid subscription metadata")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Secrets["dot"]; ok {
		t.Fatal("dot must not have a bearer")
	}
	if count(t, e, "SELECT count(*) FROM peers WHERE kind='grokbot' AND reachability='unreachable'") == 0 {
		t.Fatal("missing unreachable grokbot")
	}
	if count(t, e, "SELECT count(*) FROM peers WHERE peer_id=? AND presence_state='working'", e.OperatorPeer) != 1 {
		t.Fatal("operator must be working")
	}
	if count(t, e, "SELECT count(*) FROM audit WHERE action='loop_cut'") != 3 {
		t.Fatal("want three loop cut reasons")
	}
	if count(t, e, "SELECT count(*) FROM deliveries WHERE state='pending' AND attempt>0 AND next_attempt_at IS NOT NULL") == 0 {
		t.Fatal("missing retry-blocked thread")
	}
}

func TestSeedIsRepeatableOnFreshDB(t *testing.T) {
	a, _ := seed(t, false)
	b, _ := seed(t, false)
	for _, table := range []string{"peers", "messages", "threads", "approvals", "deliveries", "event_subscriptions"} {
		na := count(t, a, "SELECT count(*) FROM "+table)
		nb := count(t, b, "SELECT count(*) FROM "+table)
		if na != nb {
			t.Errorf("%s counts differ: %d vs %d", table, na, nb)
		}
	}
}

func TestSeedScale(t *testing.T) {
	e, s := seed(t, true)
	if count(t, e, "SELECT count(*) FROM thread_participants WHERE thr_id=?", s.Threads["participants"]) != 64 {
		t.Fatal("want 64 participants")
	}
	if count(t, e, "SELECT count(*) FROM messages WHERE thr_id=?", s.Threads["long"]) != 500 {
		t.Fatal("want 500 messages")
	}
}
