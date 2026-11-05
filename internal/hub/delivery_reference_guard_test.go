package hub_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
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

func TestReferenceGuardClaimDueFrozenRetry(t *testing.T) {
	env, fs := guardEnv(t)
	ctx := context.Background()
	guardExec(t, env, `INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,verified_at,created_at) VALUES ('guard-route','peer',?,'http','https://push.example/guard',?,?)`, guardB, guardAt, guardAt)
	guardExec(t, env, `INSERT INTO route_keys(route_id,whsec,created_at) VALUES ('guard-route',?,?)`, guardWhsec, guardAt)
	guardExec(t, env, `UPDATE deliveries SET route_id='guard-route',held=1`)
	if due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 100); err != nil || len(due) != 0 {
		t.Fatal("held deliveries became pushable")
	}
	if err := env.Hub.OnIdle(ctx, guardB); err != nil {
		t.Fatal(err)
	}
	first, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 10 {
		t.Fatalf("claimed=%d want=10", len(first))
	}
	for i, d := range first {
		guardBytes(t, fmt.Sprintf("claim-%02d.json", i+1), d.Body)
		if d.Attempt != 1 || !bytes.Equal(d.Body, []byte(fs[i].Data)) {
			t.Fatalf("first claim bytes changed: seq=%d", i+1)
		}
		var sum string
		if err := env.Hub.DB.QueryRow(`SELECT body_sha256 FROM deliveries WHERE dly_id=?`, d.DeliveryID).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		if sum != fmt.Sprintf("%x", sha256.Sum256(d.Body)) {
			t.Fatal("stored delivery digest changed")
		}
	}
	// Mutate every input a hydration implementation must never reconsult.
	env.Now.Advance(time.Hour)
	env.Hub.Cfg.PublicURL = "https://changed.example"
	guardExec(t, env, `UPDATE peers SET name='Renamed',kind='launcher',note='directory changed' WHERE peer_id IN (?,?)`, guardA, guardB)
	guardExec(t, env, `UPDATE threads SET title='Changed',message_count=99,budget_remaining=0`)
	retry, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry) != len(first) {
		t.Fatal("retry count changed")
	}
	for i, d := range retry {
		if d.Attempt != 2 || d.DeliveryID != first[i].DeliveryID || !bytes.Equal(d.Body, first[i].Body) {
			t.Fatalf("retry body or identity changed: seq=%d", i+1)
		}
	}
	// Compare against independently frozen corpus as well as previous attempt.
	evs, err := env.Hub.Log.Read(ctx, []string{guardB}, eventlog.Cursor{Epoch: 1}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if !bytes.Equal(evs[i].Data, []byte(fs[i].Data)) {
			t.Fatalf("replay changed: seq=%d", i+1)
		}
	}
}

func TestReferenceGuardWebhookAttempts(t *testing.T) {
	env, fs := guardEnv(t)
	ctx := context.Background()
	var mu sync.Mutex
	var bodies [][]byte
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("read callback failed")
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		headers = append(headers, r.Header.Clone())
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	guardExec(t, env, `INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,verified_at,created_at) VALUES ('guard-route','peer',?,'http',?,?,?)`, guardB, srv.URL, guardAt, guardAt)
	guardExec(t, env, `INSERT INTO route_keys(route_id,whsec,created_at) VALUES ('guard-route',?,?)`, guardWhsec, guardAt)
	guardExec(t, env, `UPDATE deliveries SET route_id='guard-route' WHERE dly_id='dly_00000000000000000000000008'`)
	if _, err := env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(time.Minute)
	env.Hub.Cfg.PublicURL = "https://changed.example"
	guardExec(t, env, `UPDATE threads SET title='Changed'`)
	guardExec(t, env, `UPDATE peers SET name='Changed'`)
	if _, err := env.Worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("attempt count=%d", len(bodies))
	}
	for i, body := range bodies {
		guardBytes(t, "webhook-attempt.json", body)
		if !bytes.Equal(body, []byte(fs[7].Data)) {
			t.Fatalf("webhook bytes changed: attempt=%d", i+1)
		}
		if headers[i].Get(webhook.HeaderID) != "dly_00000000000000000000000008" || headers[i].Get("X-Vecnet-Attempt") != fmt.Sprint(i+1) {
			t.Fatal("webhook identity/attempt changed")
		}
		if err := webhook.Verify([]string{guardWhsec}, headers[i].Get(webhook.HeaderID), headers[i].Get(webhook.HeaderTimestamp), headers[i].Get(webhook.HeaderSignature), body, env.Now.Now()); err != nil {
			t.Fatal("signature changed")
		}
	}
	if headers[0].Get(webhook.HeaderTimestamp) == headers[1].Get(webhook.HeaderTimestamp) {
		t.Fatal("retry timestamp did not advance")
	}
}

// Guard-local setup never reports response bytes, URLs, credentials or
// transport errors (which can contain the request URL).
func guardHTTP(t *testing.T, env *testhub.Env, secret, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var raw []byte
	if b, ok := body.([]byte); ok {
		raw = b
	} else if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal("guard HTTP request encode failed")
		}
	}
	req, err := http.NewRequest(method, env.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("guard HTTP request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("guard HTTP transport failed")
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal("guard HTTP response read failed")
	}
	return resp, out
}

func guardPutNotify(t *testing.T, env *testhub.Env, url string) {
	t.Helper()
	resp, raw := guardHTTP(t, env, env.OperatorSecret, http.MethodPut, "/v1/admin/notify", map[string]any{
		"routes": []map[string]string{{"url": url, "filter": "wake"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("notification setup failed: status=%d bytes=%d sha256=%x", resp.StatusCode, len(raw), sha256.Sum256(raw))
	}
	var result struct {
		Routes []struct{ ID, URL, Filter, Whsec string }
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("notification setup decode failed: bytes=%d sha256=%x", len(raw), sha256.Sum256(raw))
	}
	if len(result.Routes) != 1 {
		t.Fatalf("notification setup route count=%d", len(result.Routes))
	}
	route := result.Routes[0]
	if route.ID == "" || route.URL != url || route.Filter != "wake" || !strings.HasPrefix(route.Whsec, "whsec_") {
		t.Fatal("notification setup route invalid")
	}
}

func TestReferenceGuardOperatorNotifications(t *testing.T) {
	env, fs := guardEnv(t)
	guardExec(t, env, `UPDATE peers SET kind='operator' WHERE peer_id=?`, guardB)
	rcv := newNotifyReceiver(t)
	guardPutNotify(t, env, rcv.srv.URL)
	// Replaying the send's post-commit notification hook must read exact data.
	for i, f := range fs {
		if f.Type != "delivery" {
			continue
		}
		env.Hub.NotifyOperatorReferenceGuard(context.Background(), fmt.Sprintf("msg_%026d", i+1))
	}
	env.Hub.WaitNotifies()
	hits := rcv.Hits()
	if len(hits) != 10 {
		t.Fatalf("notifications=%d", len(hits))
	}
	byID := map[string][]byte{}
	for _, h := range hits {
		byID[h.Header.Get(webhook.HeaderID)] = h.Body
	}
	for i := 0; i < 10; i++ {
		guardBytes(t, fmt.Sprintf("notify-%02d.json", i+1), byID[fmt.Sprintf("e1.%d", i+1)])
	}
}

// The production send path mints crypto-random ULIDs. Only those returned ids
// are bound into a fixed byte template; actual output is never normalised,
// decoded/re-encoded, or compared semantically.
func TestReferenceGuardFanOutSendBytes(t *testing.T) {
	for _, name := range []string{"unicode", "nil_optional", "empty_optional", "attachments", "operator", "source", "hub", "reaction", "held_release"} {
		t.Run(name, func(t *testing.T) {
			env, _ := guardEnv(t)
			ctx := context.Background()
			env.Hub.Blobs.Key = bytes.Repeat([]byte{0x42}, 32)
			caller := auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}
			ingress := "http_send"
			request := map[string]any{"idempotency_key": "guard-send", "recipients": []string{guardB}, "priority": "wake", "content": map[string]any{"text": "Hello 一 🛰 \"quoted\" \\ newline\n<>&\u2028"}}
			bindings := map[string]string{}
			if name == "held_release" {
				request["priority"] = "wake_idle"
				guardExec(t, env, `UPDATE peers SET presence_state='working',presence_at=? WHERE peer_id=?`, guardAt, guardB)
			}
			if name == "empty_optional" {
				request["attachments"] = []any{}
				request["metadata"] = map[string]any{}
				request["thread_title"] = ""
			}
			if name == "attachments" {
				hosted := []byte("synthetic hosted bytes")
				sha := fmt.Sprintf("%x", sha256.Sum256(hosted))
				resp, _ := guardHTTP(t, env, guardSecret+guardA, "PUT", "/v1/blobs", hosted)
				if resp.StatusCode != http.StatusCreated {
					t.Fatal("fixture upload failed")
				}
				request["attachments"] = []any{map[string]any{"sha256": sha, "size": len(hosted), "media_type": "text/plain", "name": "一.txt"}, map[string]any{"sha256": fmt.Sprintf("%064d", 1), "size": 32, "media_type": "application/json", "origin_url": "https://origin.example/reference.json"}}
			}
			if name == "operator" {
				guardExec(t, env, `UPDATE peers SET kind='operator' WHERE peer_id=?`, guardA)
				caller.Kind = "operator"
				ingress = "operator_ui"
			}
			var msgID string
			if name == "source" {
				src, _, _, err := env.Hub.AddSource(ctx, "github", "Source 一", guardB, nil, "wake")
				if err != nil {
					t.Fatal("source creation failed")
				}
				bindings["$SOURCE_ID"] = src
				msgID, _, err = env.Hub.IngestSource(ctx, hub.SourceMessage{SrcID: src, ProviderDeliveryID: "synthetic-1", OriginThreadID: "origin-1", OriginEventID: "event-1", EventName: "issues.opened", Title: "Source 一", Summary: "source \"一\"", Raw: []byte(`{"synthetic":true}`)})
				if err != nil {
					t.Fatal("source ingestion failed")
				}
			} else {
				if name == "hub" {
					request["kind"] = "approval_request"
					request["approval_request"] = map[string]any{"approval_id": "apr_00000000000000000000000001", "question": "Approve 一?", "options": []string{"yes", "no"}, "expires_at": envelope.FormatTime(env.Now.Now().Add(time.Minute))}
				}
				raw, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				sent, _, err := env.Hub.Send(ctx, caller, raw, ingress)
				if err != nil {
					t.Fatal("guard send failed")
				}
				msgID = sent.MessageID
				if name == "hub" {
					bindings["$PARENT_ID"] = msgID
					env.Now.Advance(2 * time.Minute)
					if n, err := env.Hub.ExpireApprovals(ctx, env.Now.Now()); err != nil || n != 1 {
						t.Fatal("approval expiry failed")
					}
					if err := env.Hub.DB.QueryRow(`SELECT msg_id FROM messages WHERE sender='hub' AND thr_id=?`, sent.ThreadID).Scan(&msgID); err != nil {
						t.Fatal(err)
					}
				}
				if name == "reaction" {
					bindings["$PARENT_ID"] = msgID
					body := map[string]any{"message_id": msgID, "emoji": "👍", "op": "add", "priority": "wake"}
					resp, reactionRaw := guardHTTP(t, env, guardSecret+guardB, "POST", "/v1/reactions", body)
					if resp.StatusCode != 200 {
						var failure struct{ Error struct{ Code string } }
						if err := json.Unmarshal(reactionRaw, &failure); err != nil {
							t.Fatal("reaction response failed")
						}
						t.Fatalf("reaction send failed: status=%d code=%s", resp.StatusCode, failure.Error.Code)
					}
					if err := env.Hub.DB.QueryRow(`SELECT msg_id FROM messages WHERE kind='reaction' AND thr_id=?`, sent.ThreadID).Scan(&msgID); err != nil {
						t.Fatal(err)
					}
				}
			}
			assertReferenceWrites(t, env, msgID)
			var dly, thr, recipient, sum string
			var eventSeq int64
			if err := env.Hub.DB.QueryRow(`SELECT dly_id,thr_id,recipient,event_seq,body_sha256 FROM deliveries WHERE msg_id=? ORDER BY event_seq DESC LIMIT 1`, msgID).Scan(&dly, &thr, &recipient, &eventSeq, &sum); err != nil {
				t.Fatal("delivery metadata read failed")
			}
			// Assert the public replay contract, independent of the event's
			// internal inline/reference storage representation.
			evs, err := env.Hub.Log.Read(ctx, []string{recipient}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: eventSeq - 1}, 1)
			if err != nil {
				t.Fatalf("delivery replay failed: seq=%d", eventSeq)
			}
			if len(evs) != 1 || evs[0].Type != "delivery" || evs[0].Cursor != (eventlog.Cursor{Epoch: env.Hub.Log.Epoch(), Seq: eventSeq}).String() {
				t.Fatalf("delivery replay mismatch: seq=%d count=%d", eventSeq, len(evs))
			}
			body := evs[0].Data
			bindings["$MESSAGE_ID"] = msgID
			bindings["$DELIVERY_ID"] = dly
			bindings["$THREAD_ID"] = thr
			if sum != fmt.Sprintf("%x", sha256.Sum256(body)) {
				t.Fatal("fanout delivery hash changed")
			}
			guardSendTemplate(t, "send-"+name+".json", body, bindings)
			if name == "held_release" {
				guardExec(t, env, `INSERT INTO routes(route_id,owner_kind,owner_id,kind,url,verified_at,created_at) VALUES ('guard-held-route','peer',?,'http','https://push.example/guard',?,?)`, guardB, guardAt, guardAt)
				guardExec(t, env, `UPDATE deliveries SET route_id='guard-held-route' WHERE dly_id=?`, dly)
				due, err := env.Hub.ClaimDue(ctx, env.Now.Now(), 100)
				if err != nil || len(due) != 0 {
					t.Fatal("working peer's wake_idle was not held")
				}
				env.Now.Advance(time.Minute)
				env.Hub.Cfg.PublicURL = "https://changed.example"
				guardExec(t, env, `UPDATE peers SET name='Changed',presence_state='idle' WHERE peer_id=?`, guardB)
				guardExec(t, env, `UPDATE threads SET title='Changed' WHERE thr_id=?`, thr)
				if err := env.Hub.OnIdle(ctx, guardB); err != nil {
					t.Fatal(err)
				}
				due, err = env.Hub.ClaimDue(ctx, env.Now.Now(), 100)
				if err != nil || len(due) != 1 || !bytes.Equal(due[0].Body, body) {
					t.Fatal("released wake_idle bytes changed")
				}
			}

		})
	}
}

func guardSendTemplate(t *testing.T, name string, got []byte, bindings map[string]string) {
	t.Helper()
	path := "testdata/reference_guard/" + name
	template, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := bytes.Clone(template)
	for slot, value := range bindings {
		want = bytes.ReplaceAll(want, []byte(slot), []byte(value))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("send bytes changed %s: got sha256=%x want sha256=%x", name, sha256.Sum256(got), sha256.Sum256(want))
	}
}
