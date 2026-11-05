package mcpserver_test

import (
	"context"
	"database/sql"
	"time"

	"net/http"
	"strings"

	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
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
			if err := venv.Validate("delivery", []byte(f.Data)); err != nil {
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
	fixed, err := venv.ParseTime(guardAt)
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
			DeliveryID string       `json:"delivery_id"`
			Recipient  string       `json:"recipient"`
			Message    venv.Message `json:"message"`
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

func TestReferenceGuardMCPInboxBytes(t *testing.T) {
	env, _ := guardEnv(t)
	for _, tc := range []struct{ name, secret, uri string }{
		{"peer", guardSecret + guardB, peerURI(guardB)},
		{"principal", guardSecret + guardPrn, prnURI(guardPrn)},
		{"empty", guardSecret + guardA, peerURI(guardA)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, r := rpc(t, env, tc.secret, "tools/call", map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{}})
			if status != http.StatusOK || r.Error != nil {
				t.Fatal("inbox tool failed")
			}
			guardBytes(t, "tool-"+tc.name+".json", r.Result)
			resource := inboxText(t, env, tc.secret, tc.uri)
			guardBytes(t, "resource-"+tc.name+".json", []byte(resource))
			for _, body := range []string{string(r.Result), resource} {
				if strings.Contains(body, "blob_url") || strings.Contains(body, "?t=") {
					t.Fatal("MCP inbox leaked blob token")
				}
			}
		})
	}
	resp, raw := env.Do(guardSecret+guardB, "GET", "/v1/events?wait=0", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "?t=synthetic.guard.token") {
		t.Fatal("raw HTTP lost frozen blob URL")
	}
}

func TestReferenceGuardMCPInboxNotifications(t *testing.T) {
	env, _ := guardEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, frames := listen(t, ctx, env, http.DefaultClient, guardSecret+guardB, []string{peerURI(guardB)})
	defer resp.Body.Close()
	_, raw, ok := nextMessage(t, frames, 5*time.Second)
	if !ok {
		t.Fatal("listen acknowledgement missing")
	}
	guardBytes(t, "listen-ack.json", raw)
	err := env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := env.Hub.Log.Append(context.Background(), tx, guardB, "presence", env.Now.Now(), json.RawMessage(`{"peer_id":"peer_00000000000000000000000001","state":"idle","at":"2030-01-02T03:04:05.006Z"}`))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Hub.Log.Notify(guardB)
	_, raw, ok = nextMessage(t, frames, 5*time.Second)
	if !ok {
		t.Fatal("inbox notification missing")
	}
	guardBytes(t, "resource-updated.json", raw)
}
