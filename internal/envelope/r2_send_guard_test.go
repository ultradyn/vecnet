package envelope_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/testhub"
)

// TestSendRejectsR2ReferenceOnDiskPipeline preserves the send regression at
// the envelope integration boundary. The disk-only send schema rejects R2
// references before SHA lookup or origin_url fallback, creates no Message or
// Event, and continues to admit ordinary disk references. Grant-aware sends
// are deferred to Task 4; the future reference contract is tested separately.
func TestSendRejectsR2ReferenceOnDiskPipeline(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	const content = "owned disk bytes"
	sha := putBlob(t, env, aSec, content)
	eventsBefore := count(t, env, `SELECT count(*) FROM events`)

	r2Ref := func(extra map[string]any) []any {
		att := map[string]any{
			"blob_id": "blo_00000000000000000000000001", "sha256": sha, "size": len(content),
			"media_type": "text/plain", "retention_class": "keep", "expires_at": nil,
		}
		for k, v := range extra {
			att[k] = v
		}
		return []any{att}
	}
	cases := map[string][]any{
		"owned_disk_hash":  r2Ref(nil),
		"origin_url_only":  r2Ref(map[string]any{"sha256": shaHex("not stored"), "origin_url": "https://example.com/r.txt"}),
		"finite_retention": r2Ref(map[string]any{"retention_class": "1d", "expires_at": "2026-11-06T00:00:00.000Z"}),
	}
	for name, atts := range cases {
		t.Run(name, func(t *testing.T) {
			body := req("r2-"+name, b)
			body["attachments"] = atts
			_, details := mustReject(t, env, aSec, body, http.StatusBadRequest, "invalid_body")
			if details["path"] != "/attachments/0/blob_id" {
				t.Fatalf("details.path = %v, want /attachments/0/blob_id", details["path"])
			}
			if _, ok := details["size"]; ok {
				t.Fatalf("rejection details %v reveal a size", details)
			}
		})
	}
	if n := count(t, env, `SELECT count(*) FROM messages`); n != 0 {
		t.Fatalf("messages = %d after rejected R2 references, want 0", n)
	}
	if n := count(t, env, `SELECT count(*) FROM events`); n != eventsBefore {
		t.Fatalf("events = %d after rejected R2 references, want %d", n, eventsBefore)
	}

	// The ordinary disk reference to the same owned hash still sends.
	body := req("disk", b)
	body["attachments"] = []any{map[string]any{"sha256": sha, "size": len(content), "media_type": "text/plain"}}
	mustSend(t, env, aSec, body)
	m := onlyDelivery(t, env, b)
	if len(m.Attachments) != 1 || m.Attachments[0].BlobID != "" || m.Attachments[0].BlobURL == "" {
		t.Fatal("expected a legacy disk attachment with blob_url and no blob_id")
	}
}

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func putBlob(t *testing.T, env *testhub.Env, secret, content string) string {
	t.Helper()
	resp, raw := env.Do(secret, "PUT", "/v1/blobs", content)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT /v1/blobs = %d, want 201", resp.StatusCode)
	}
	var result struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.SHA256
}

func req(key string, recipients ...string) map[string]any {
	return map[string]any{"idempotency_key": key, "priority": "wake", "content": map[string]any{"text": "hello " + key}, "recipients": recipients}
}

func count(t *testing.T, env *testhub.Env, q string) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustReject(t *testing.T, env *testhub.Env, secret string, body any, status int, code string) (*http.Response, map[string]any) {
	t.Helper()
	resp, raw := env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != status {
		t.Fatalf("POST /v1/messages = %d, want %d", resp.StatusCode, status)
	}
	var result struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != code {
		t.Fatalf("error code = %q, want %q", result.Error.Code, code)
	}
	return resp, result.Error.Details
}

func mustSend(t *testing.T, env *testhub.Env, secret string, body any) {
	t.Helper()
	resp, _ := env.Do(secret, "POST", "/v1/messages", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /v1/messages = %d, want 202", resp.StatusCode)
	}
}

func onlyDelivery(t *testing.T, env *testhub.Env, peer string) envelope.Message {
	t.Helper()
	events, err := env.Hub.Log.Read(context.Background(), []string{peer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var messages []envelope.Message
	for _, event := range events {
		if event.Type != "delivery" {
			continue
		}
		var delivery envelope.Delivery
		if err := json.Unmarshal(event.Data, &delivery); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, delivery.Message)
	}
	if len(messages) != 1 {
		t.Fatalf("delivery count = %d, want 1", len(messages))
	}
	return messages[0]
}
