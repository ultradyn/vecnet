package events_test

import (
	"context"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"net/http"
	"time"

	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
	"os"
	"testing"
)

// These baseline fixtures are frozen at fcc71a10e9c2bf7c496983b7cb0dc363f6382d16.
// No UPDATE_GOLDEN path: later storage changes must preserve these bytes.
const guardA = "peer_00000000000000000000000001"
const guardB = "peer_00000000000000000000000002"
const guardC = "peer_00000000000000000000000003"
const guardPrn = "prn_00000000000000000000000001"
const guardAt = "2030-01-02T03:04:05.006Z"
const guardSecret = "vecnet_synthetic_reference_guard_only"
const guardWhsec = "whsec_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFh"

type guardFixture struct{ Name, Owner, Type, At, Data, SHA256 string }

func guardFixtures(t *testing.T) []guardFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/reference_guard/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []guardFixture
	if err := json.Unmarshal(raw, &fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Type == "delivery" && f.Name != "oversized" {
			if err := envelope.Validate("delivery", []byte(f.Data)); err != nil {
				t.Fatalf("fixture schema mismatch: %s", f.Name)
			}
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(f.Data))) != f.SHA256 {
			t.Fatalf("fixture hash mismatch: %s", f.Name)
		}
	}
	return fs
}

func guardExec(t *testing.T, env *testhub.Env, q string, args ...any) {
	t.Helper()
	if _, err := env.Hub.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// Seed only disposable storage with fixed ids, exact legacy bytes and fake time.
// Transport addresses use httptest's ephemeral listener; payload URLs are synthetic.
func guardEnv(t *testing.T) (*testhub.Env, []guardFixture) {
	t.Helper()
	env := testhub.StartWith(t, func(c *hub.Config) { c.PublicURL = "https://frozen.example" })
	fixed, err := envelope.ParseTime(guardAt)
	if err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(fixed.Sub(env.Now.Now()))
	guardExec(t, env, `INSERT INTO principals(prn_id,name,created_at) VALUES (?,'Guard principal',?)`, guardPrn, guardAt)
	for _, p := range []string{guardA, guardB, guardC} {
		guardExec(t, env, `INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES (?,?,'Sender <一>','session',?)`, p, guardPrn, guardAt)
		guardExec(t, env, `INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES (?,'peer',?,?,?)`, p, p, auth.HashSecret(guardSecret+p), guardAt)
	}
	guardExec(t, env, `INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES ('guard-prn','principal',?,?,?)`, guardPrn, auth.HashSecret(guardSecret+guardPrn), guardAt)
	fs := guardFixtures(t)
	for i, f := range fs {
		guardExec(t, env, `INSERT INTO events(seq,owner,type,at,data) VALUES (?,?,?,?,?)`, i+1, f.Owner, f.Type, f.At, f.Data)
		if f.Type != "delivery" {
			continue
		}
		var d struct {
			DeliveryID, Recipient string
			Thread                struct {
				ThreadID string `json:"thread_id"`
			}
			Message json.RawMessage
		}
		// Field spellings are explicit because delivery_id is not DeliveryID.
		var ids struct {
			DeliveryID string           `json:"delivery_id"`
			Recipient  string           `json:"recipient"`
			Message    envelope.Message `json:"message"`
		}
		if err := json.Unmarshal([]byte(f.Data), &d); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(f.Data), &ids); err != nil {
			t.Fatal(err)
		}
		m := ids.Message
		guardExec(t, env, `INSERT INTO threads(thr_id,title,created_by,created_at,last_message_at,message_count,history_access) VALUES (?,'Frozen "一"',?,?,?,1,'full')`, m.ThreadID, guardA, guardAt, guardAt)
		for _, p := range []string{guardA, guardB} {
			guardExec(t, env, `INSERT INTO thread_participants(thr_id,peer_id,joined_at,joined_after_seq) VALUES (?,?,?,0)`, m.ThreadID, p, guardAt)
		}
		guardExec(t, env, `INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES (?,?,1,?,?,?,?)`, m.MessageID, m.ThreadID, m.Sender, m.Kind, guardAt, string(d.Message))
		guardExec(t, env, `INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,also_via,priority,state,attempts_left,expires_at,created_at,updated_at,event_seq,body_sha256) VALUES (?,?,?,?,'[]','wake','queued',8,?,?,?,?,?)`, ids.DeliveryID, m.MessageID, m.ThreadID, ids.Recipient, m.Loop.ExpiresAt, guardAt, guardAt, i+1, f.SHA256)
	}
	guardExec(t, env, `INSERT INTO reactions(msg_id,peer_id,emoji,created_at) VALUES (?,?,?,?)`, "msg_00000000000000000000000001", guardA, "👍", guardAt)
	return env, fs
}

func guardBytes(t *testing.T, name string, got []byte) {
	t.Helper()
	path := "testdata/reference_guard/" + name
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire mismatch %s: got sha256=%x want sha256=%x", name, sha256.Sum256(got), sha256.Sum256(want))
	}
}

func guardSubscription(t *testing.T, env *testhub.Env, cb *testhub.Callback) events.Subscription {
	t.Helper()
	sub, _, err := env.Events.Store.Upsert(context.Background(), events.Subscription{PrincipalKind: "vecnet", PrincipalID: guardB, PeerID: guardB, Name: events.Name, Types: []string{"delivery", "receipt"}, URL: cb.URL, Cursor: eventlog.Cursor{Epoch: 1}}, guardWhsec, 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Events.Store.MarkVerified(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestReferenceGuardEventsCallbackAndOutbox(t *testing.T) {
	env, fs := guardEnv(t)
	cb := testhub.NewCallback(t)
	guardExec(t, env, `UPDATE events SET owner=? WHERE type='receipt'`, guardB)
	sub := guardSubscription(t, env, cb)
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(503) })
	step(t, env)
	posts := cb.Posts()
	if len(posts) != 1 {
		t.Fatalf("posts=%d want=1", len(posts))
	}
	// Every enqueued body is frozen, including the oversized preview wrapper.
	rows, err := env.Hub.DB.Query(`SELECT event_id,body FROM event_outbox WHERE sub_id=? ORDER BY seq`, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			t.Fatal(err)
		}
		count++
		guardBytes(t, fmt.Sprintf("callback-%02d.json", count), []byte(body))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("outbox rows=%d want=11", count)
	}
	if len(posts[0].Body) == 0 || !bytes.Equal(posts[0].Body, mustGuardFile(t, "callback-01.json")) {
		t.Fatal("callback differs from outbox")
	}
	cur, err := env.Events.Store.ReplayCursor(context.Background(), sub.ID, eventlog.Cursor{Epoch: 1, Seq: 12})
	if err != nil || cur.Seq != 0 {
		t.Fatal("pending callback replay cursor changed")
	}
	env.Now.Advance(time.Minute)
	env.Hub.Cfg.PublicURL = "https://changed.example"
	guardExec(t, env, `UPDATE threads SET title='Changed'`)
	guardExec(t, env, `UPDATE peers SET name='Changed'`)
	step(t, env)
	posts = cb.Posts()
	if len(posts) != 2 || posts[0].ID != posts[1].ID || !bytes.Equal(posts[0].Body, posts[1].Body) {
		t.Fatal("retry identity or callback body changed")
	}
	if posts[0].Timestamp == posts[1].Timestamp {
		t.Fatal("timestamp did not change")
	}
	// Resume successful delivery; oversized event stays the frozen preview.
	cb.OnPost(nil)
	env.Now.Advance(time.Minute)
	step(t, env)
	posts = cb.Posts()
	if len(posts) != 13 {
		t.Fatalf("posts=%d want=13", len(posts))
	}
	for i, p := range posts[2:] {
		if !bytes.Equal(p.Body, mustGuardFile(t, fmt.Sprintf("callback-%02d.json", i+1))) {
			t.Fatalf("callback bytes changed: index=%d", i)
		}
	}
	if len(posts[11].Body) >= hub.MaxPushBody || bytes.Contains(posts[11].Body, []byte(fs[9].Data)) {
		t.Fatal("oversized event was not previewed")
	}
	cur, err = env.Events.Store.ReplayCursor(context.Background(), sub.ID, eventlog.Cursor{Epoch: 1, Seq: 12})
	if err != nil || cur.Seq != 12 {
		t.Fatal("settled replay cursor changed")
	}
}
func mustGuardFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/reference_guard/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReferenceGuardExhaustedSuppression(t *testing.T) {
	env, _ := guardEnv(t)
	cb := testhub.NewCallback(t)
	sub := guardSubscription(t, env, cb)
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(503) })
	for i := 0; i < hub.PushAttempts; i++ {
		step(t, env)
		env.Now.Advance(5 * time.Minute)
	}
	posts := cb.Posts()
	if len(posts) != hub.PushAttempts {
		t.Fatal("attempt budget changed")
	}
	for _, p := range posts {
		if p.ID != posts[0].ID || !bytes.Equal(p.Body, posts[0].Body) {
			t.Fatal("exhausted callback bytes changed")
		}
	}
	_, done, term := outbox(t, env, sub.ID, posts[0].ID)
	if !done || term != "exhausted" {
		t.Fatal("exhaustion state changed")
	}
	step(t, env)
	if len(cb.Posts()) != hub.PushAttempts {
		t.Fatal("exhausted callback was sent again")
	}
	guardBytes(t, "exhausted-callback.json", posts[0].Body)
	cur, err := env.Events.Store.ReplayCursor(context.Background(), sub.ID, eventlog.Cursor{Epoch: 1, Seq: 12})
	if err != nil || cur.Seq != 0 {
		t.Fatal("exhausted callback lost replay pin")
	}
}

func TestReferenceGuardLegacyOutbox0018(t *testing.T) {
	env, fs := guardEnv(t)
	ctx := context.Background()
	guardExec(t, env, `UPDATE events SET data=json_remove(data,'$.message.sender_info') WHERE seq=9`)
	cb := testhub.NewCallback(t)
	sub := guardSubscription(t, env, cb)
	if err := env.Events.Store.SetCursor(ctx, sub.ID, eventlog.Cursor{Epoch: 1, Seq: 8}, false); err != nil {
		t.Fatal(err)
	}
	cb.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(503) })
	step(t, env)
	posts := cb.Posts()
	if len(posts) != 1 {
		t.Fatal("legacy callback missing")
	}
	guardBytes(t, "legacy-outbox-before-0018.json", posts[0].Body)
	guardExec(t, env, `UPDATE events SET data=json_set(events.data,'$.message.sender_info',json(json_extract(m.body,'$.sender_info'))) FROM messages m WHERE events.seq=9 AND m.msg_id=json_extract(events.data,'$.message.message_id')`)
	env.Now.Advance(time.Minute)
	step(t, env)
	posts = cb.Posts()
	if len(posts) != 2 || posts[0].ID != posts[1].ID || !bytes.Equal(posts[0].Body, posts[1].Body) {
		t.Fatal("0018 changed queued callback bytes")
	}
	var body string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM event_outbox WHERE sub_id=? AND event_id=?`, sub.ID, posts[0].ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(body), posts[0].Body) {
		t.Fatal("0018 changed durable outbox copy")
	}
	evs, err := env.Hub.Log.Read(ctx, []string{guardB}, eventlog.Cursor{Epoch: 1, Seq: 8}, 1)
	if err != nil || len(evs) != 1 || !bytes.Equal(evs[0].Data, []byte(fs[8].Data)) {
		t.Fatal("0018 event replay bytes changed")
	}
}
