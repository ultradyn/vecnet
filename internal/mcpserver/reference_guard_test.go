package mcpserver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	venv "github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Canonical baseline fixtures are frozen at fcc71a10e9c2bf7c496983b7cb0dc363f6382d16.
// R-C61 updates only compact MCP inbox tool/resource views to omit ancestry.
// No UPDATE_GOLDEN path: storage changes must preserve canonical bytes.
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

// Guard helpers deliberately omit payloads, URLs, tokens and raw errors
// from every failure diagnostic, including malformed protocol responses.
func guardRequest(t *testing.T, ctx context.Context, env *testhub.Env, secret, method, path string, body any) *http.Response {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal("guard HTTP request encode failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, env.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("guard HTTP request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	if path == "/mcp" {
		req.Header.Set("MCP-Protocol-Version", mcpserver.ProtoCurrent)
		req.Header.Set("Accept", "application/json, text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("guard HTTP transport failed")
	}
	return resp
}

func guardHTTP(t *testing.T, env *testhub.Env, secret, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	resp := guardRequest(t, context.Background(), env, secret, method, path, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal("guard HTTP response read failed")
	}
	return resp, raw
}

func guardRPC(t *testing.T, env *testhub.Env, secret, method string, params any) rpcResp {
	t.Helper()
	resp, raw := guardHTTP(t, env, secret, http.MethodPost, "/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("guard RPC HTTP failure: status=%d bytes=%d sha256=%x", resp.StatusCode, len(raw), sha256.Sum256(raw))
	}
	var r rpcResp
	if err := json.Unmarshal(raw, &r); err != nil || r.JSONRPC != "2.0" {
		t.Fatalf("guard RPC decode failed: bytes=%d sha256=%x", len(raw), sha256.Sum256(raw))
	}
	if r.Error != nil {
		t.Fatalf("guard RPC error: code=%d", r.Error.Code)
	}
	return r
}

func guardResource(t *testing.T, env *testhub.Env, secret, uri string) (json.RawMessage, string) {
	t.Helper()
	r := guardRPC(t, env, secret, "resources/read", map[string]any{"uri": uri})
	var result struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("guard resource decode failed: bytes=%d sha256=%x", len(r.Result), sha256.Sum256(r.Result))
	}
	if len(result.Contents) != 1 {
		t.Fatalf("guard resource contents count=%d", len(result.Contents))
	}
	return r.Result, result.Contents[0].Text
}

func guardListen(t *testing.T, ctx context.Context, env *testhub.Env, secret string, uris []string) (*http.Response, <-chan frame) {
	t.Helper()
	resp := guardRequest(t, ctx, env, secret, http.MethodPost, "/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "subscriptions/listen",
		"params": map[string]any{"resourceSubscriptions": uris},
	})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		t.Fatalf("guard listen failed: status=%d", resp.StatusCode)
	}
	ch := make(chan frame, 64)
	go readFrames(resp.Body, ch)
	return resp, ch
}

func guardNextMessage(t *testing.T, frames <-chan frame, within time.Duration) (json.RawMessage, bool) {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return nil, false
			}
			if f.data == nil {
				continue
			}
			var n notif
			if err := json.Unmarshal(f.data, &n); err != nil {
				t.Fatalf("guard notification decode failed: bytes=%d sha256=%x", len(f.data), sha256.Sum256(f.data))
			}
			return f.data, true
		case <-deadline.C:
			return nil, false
		}
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
			r := guardRPC(t, env, tc.secret, "tools/call", map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{}})
			guardBytes(t, "tool-"+tc.name+".json", r.Result)
			result, resource := guardResource(t, env, tc.secret, tc.uri)
			guardBytes(t, "resources-read/"+tc.name+".json", result)
			guardBytes(t, "resource-"+tc.name+".json", []byte(resource))
			for _, body := range []string{string(r.Result), string(result), resource} {
				if strings.Contains(body, "blob_url") || strings.Contains(body, "?t=") {
					t.Fatal("MCP inbox leaked blob token")
				}
			}
		})
	}
	resp, raw := guardHTTP(t, env, guardSecret+guardB, "GET", "/v1/events?wait=0", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "?t=synthetic.guard.token") {
		t.Fatal("raw HTTP lost frozen blob URL")
	}
}

func TestReferenceGuardMCPInboxNotifications(t *testing.T) {
	env, _ := guardEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, frames := guardListen(t, ctx, env, guardSecret+guardB, []string{peerURI(guardB)})
	defer resp.Body.Close()
	raw, ok := guardNextMessage(t, frames, 5*time.Second)
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
	raw, ok = guardNextMessage(t, frames, 5*time.Second)
	if !ok {
		t.Fatal("inbox notification missing")
	}
	guardBytes(t, "resource-updated.json", raw)
}
