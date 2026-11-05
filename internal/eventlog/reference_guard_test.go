package eventlog_test

import (
	"context"
	"github.com/XertroV/vecnet/internal/eventlog"

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
	prepareReferenceStorage(t, env)
	return env, fs
}

func guardBytes(t *testing.T, name string, got []byte) {
	t.Helper()
	assertPublicStorageKeys(t, got)
	path := "testdata/reference_guard/" + name
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire mismatch %s: got sha256=%x want sha256=%x", name, sha256.Sum256(got), sha256.Sum256(want))
	}
}

func TestReferenceGuardLogReadBytes(t *testing.T) {
	env, fs := guardEnv(t)
	evs, err := env.Hub.Log.Read(context.Background(), []string{guardB, guardC}, eventlog.Cursor{Epoch: 1}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != len(fs) {
		t.Fatalf("events=%d want=%d", len(evs), len(fs))
	}
	for i, f := range fs {
		t.Run(f.Name, func(t *testing.T) {
			ev := evs[i]
			if ev.Cursor != fmt.Sprintf("e1.%d", i+1) || ev.Type != f.Type || ev.At != f.At {
				t.Fatal("event metadata changed")
			}
			if !bytes.Equal(ev.Data, []byte(f.Data)) {
				t.Fatalf("event bytes changed: seq=%d", i+1)
			}
		})
	}
	raw, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	guardBytes(t, "log-events.json", raw)
}

func TestReferenceGuardCursorOrdering(t *testing.T) {
	env, fs := guardEnv(t)
	ctx := context.Background()
	// Interleave the receipt owner with delivery owners, rather than grouping owners.
	guardExec(t, env, `UPDATE events SET owner=? WHERE seq IN (2,4,6)`, guardC)
	for _, tc := range []struct {
		name   string
		owners []string
		after  eventlog.Cursor
		seqs   []int
	}{
		{"merged", []string{guardB, guardC}, eventlog.Cursor{Epoch: 1}, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}},
		{"peer", []string{guardB}, eventlog.Cursor{Epoch: 1}, []int{1, 3, 5, 7, 8, 9, 10, 12}},
		{"narrowed", []string{guardC}, eventlog.Cursor{Epoch: 1, Seq: 3}, []int{4, 6, 11}},
		{"empty", []string{guardA}, eventlog.Cursor{Epoch: 1}, nil},
		{"head", []string{guardB}, eventlog.Head, nil},
		{"equal", []string{guardB}, eventlog.Cursor{Epoch: 1, Seq: 12}, nil},
		// Both Log.Read and HTTP return an empty page at a future cursor.
		{"future", []string{guardB}, eventlog.Cursor{Epoch: 1, Seq: 99}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs, err := env.Hub.Log.Read(ctx, tc.owners, tc.after, 100)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(evs)
			if err != nil {
				t.Fatal(err)
			}
			guardBytes(t, "cursors-"+tc.name+".json", raw)
			if len(evs) != len(tc.seqs) {
				t.Fatalf("event count=%d want=%d", len(evs), len(tc.seqs))
			}
			for i, n := range tc.seqs {
				if evs[i].Cursor != fmt.Sprintf("e1.%d", n) || !bytes.Equal(evs[i].Data, []byte(fs[n-1].Data)) {
					t.Fatalf("order/bytes changed: seq=%d", n)
				}
			}
		})
	}
	for _, tc := range []struct {
		owner string
		seq   int64
	}{{guardA, 0}, {guardB, 12}, {guardC, 11}} {
		cur, err := env.Hub.Log.Current(ctx, []string{tc.owner})
		if err != nil || cur != (eventlog.Cursor{Epoch: 1, Seq: tc.seq}) {
			t.Fatal("owner head changed")
		}
	}
}

// Migration 0018 appended sender_info with SQLite json_set, while previously
// enqueued callback copies stayed untouched. Preserve both byte histories.
func TestReferenceGuardLegacy0018(t *testing.T) {
	env, fs := guardEnv(t)
	guardExec(t, env, `UPDATE events SET data=json_remove(data,'$.message.sender_info') WHERE seq=9`)
	var before string
	if err := env.Hub.DB.QueryRow(`SELECT data FROM events WHERE seq=9`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	guardBytes(t, "legacy-before-0018.json", []byte(before))
	guardExec(t, env, `UPDATE events SET data=json_set(events.data,'$.message.sender_info',json(json_extract(m.body,'$.sender_info'))) FROM messages m WHERE events.seq=9 AND m.msg_id=json_extract(events.data,'$.message.message_id')`)
	hydrateReferenceStorage(t, env)
	evs, err := env.Hub.Log.Read(context.Background(), []string{guardB}, eventlog.Cursor{Epoch: 1, Seq: 8}, 1)
	if err != nil || len(evs) != 1 {
		t.Fatal("legacy read failed")
	}
	if !bytes.Equal(evs[0].Data, []byte(fs[8].Data)) {
		t.Fatal("0018 historical event bytes differ")
	}
	guardBytes(t, "legacy-after-0018.json", evs[0].Data)
}
