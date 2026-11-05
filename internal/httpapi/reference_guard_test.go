package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
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

// Guard-local transport helpers keep raw errors, endpoints and payloads out of
// diagnostics, including request creation, transport and response-read failures.
func guardRequest(t *testing.T, ctx context.Context, env *testhub.Env, secret, method, path, accept string, body any) *http.Response {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal("reason=guard_http_request_encode")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, env.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("reason=guard_http_request_create")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("reason=guard_http_transport")
	}
	return resp
}

func guardHTTP(t *testing.T, env *testhub.Env, secret, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := guardRequest(t, ctx, env, secret, method, path, "application/json", body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reason=guard_http_response_read status=%d bytes=%d sha256=%x", resp.StatusCode, len(raw), sha256.Sum256(raw))
	}
	return resp, raw
}

func guardPoll(t *testing.T, env *testhub.Env, secret, query string) (*http.Response, []byte) {
	t.Helper()
	return guardHTTP(t, env, secret, http.MethodGet, "/v1/events"+query, nil)
}

func TestReferenceGuardHTTPPollAndSSE(t *testing.T) {
	env, _ := guardEnv(t)
	for _, tc := range []struct{ name, secret, query string }{
		{"peer", guardSecret + guardB, "?wait=0"},
		{"principal", guardSecret + guardPrn, "?wait=0"},
		{"narrowed", guardSecret + guardPrn, "?peer=" + guardC + "&wait=0"},
		{"empty", guardSecret + guardA, "?wait=0"},
		{"head", guardSecret + guardB, "?after=head&wait=0"},
		{"equal", guardSecret + guardB, "?after=e1.12&wait=0"},
		{"future", guardSecret + guardB, "?after=e1.99&wait=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := guardPoll(t, env, tc.secret, tc.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("reason=guard_poll_status status=%d bytes=%d sha256=%x", resp.StatusCode, len(body), sha256.Sum256(body))
			}
			guardBytes(t, "poll-"+tc.name+".json", body)
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := guardRequest(t, ctx, env, guardSecret+guardPrn, http.MethodGet, "/v1/events?after=e1.0", "text/event-stream", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reason=guard_sse_status status=%d", resp.StatusCode)
	}
	rd := bufio.NewReader(resp.Body)
	var wire bytes.Buffer
	// Retry preamble, priming cursor, twelve Event frames. Preserve delimiters.
	for frame := 0; frame < 14; frame++ {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("reason=guard_sse_read count=%d bytes=%d", frame, wire.Len())
			}
			wire.WriteString(line)
			if line == "\n" {
				break
			}
		}
	}
	guardBytes(t, "events.sse", wire.Bytes())
}

func TestReferenceGuardAckScopes(t *testing.T) {
	env, _ := guardEnv(t)
	for _, tc := range []struct{ secret, path, cursor string }{
		{guardSecret + guardPrn, "/v1/ack", "e1.4"},
		{guardSecret + guardPrn, "/v1/ack?peer=" + guardB, "e1.8"},
		{guardSecret + guardB, "/v1/ack", "e1.2"},
	} {
		resp, body := guardHTTP(t, env, tc.secret, http.MethodPost, tc.path, map[string]string{"cursor": tc.cursor})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("reason=guard_ack_status status=%d bytes=%d sha256=%x", resp.StatusCode, len(body), sha256.Sum256(body))
		}
	}
	for _, tc := range []struct {
		owner, scope string
		seq          int64
	}{{guardPrn, "*", 4}, {guardPrn, guardB, 8}, {guardB, "*", 2}} {
		got, ok, err := env.Hub.Log.Acked(context.Background(), tc.owner, tc.scope)
		guardBytes(t, "ack-"+tc.owner+"-"+tc.scope+".txt", []byte(got.String()))
		if err != nil || !ok || got != (eventlog.Cursor{Epoch: 1, Seq: tc.seq}) {
			t.Fatal("ack scope changed")
		}
	}
}
