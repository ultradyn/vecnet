package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
)

func TestR2HTTPStateHistoryInboxEvents(t *testing.T) {
	f := newR2HTTP(t)
	_, secret := f.env.NewPeer("session", "owner")
	reader, readerSecret := f.env.NewPeer("session", "reader")
	g := f.upload(t, secret, "projection")
	body := map[string]any{"idempotency_key": "state-message", "priority": "hold", "recipients": []string{reader, f.env.OperatorPeer}, "content": map[string]any{"text": "retained"}, "attachments": []any{map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "name": "report.txt", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}}}
	resp, raw := f.request(t, secret, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 202 {
		t.Fatal("send failed")
	}
	var sent struct {
		ThreadID string `json:"thread_id"`
	}
	if json.Unmarshal(raw, &sent) != nil {
		t.Fatal("invalid send")
	}
	before, err := f.env.Hub.Log.Read(context.Background(), []string{reader}, eventlog.Cursor{Epoch: f.env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := json.Marshal(before)
	for i, want := range []string{"available", "expired"} {
		if i > 0 {
			f.env.Now.Advance(24 * time.Hour)
		}
		for _, secret := range []string{secret, readerSecret, f.env.OperatorSecret} {
			resp, raw = f.request(t, secret, "GET", "/v1/threads/"+sent.ThreadID+"/messages", nil, "")
			var page struct {
				States   map[string]blobwire.State `json:"blob_states"`
				Messages []envelope.Message        `json:"messages"`
			}
			if resp.StatusCode != 200 || json.Unmarshal(raw, &page) != nil || page.States[g.BlobID].State != want || len(page.Messages) != 1 || page.Messages[0].Attachments[0].Name != "report.txt" {
				t.Fatalf("history state missing at phase %d", i)
			}
		}
		resp, raw = f.request(t, f.env.OperatorSecret, "GET", "/v1/admin/inbox", nil, "")
		var inbox struct {
			States map[string]blobwire.State `json:"blob_states"`
		}
		if resp.StatusCode != 200 || json.Unmarshal(raw, &inbox) != nil || inbox.States[g.BlobID].State != want {
			t.Fatal("operator inbox missing state projection")
		}
		resp, raw = f.request(t, readerSecret, "GET", "/v1/events?wait=0", nil, "")
		var poll struct {
			Events []struct {
				Type   string                    `json:"type"`
				States map[string]blobwire.State `json:"blob_states"`
			} `json:"events"`
		}
		if resp.StatusCode != 200 || json.Unmarshal(raw, &poll) != nil {
			t.Fatal("event poll failed")
		}
		found := false
		for _, event := range poll.Events {
			if event.Type == "delivery" {
				found = true
				if event.States[g.BlobID].State != want {
					t.Fatal("replayed event missing current state")
				}
			}
		}
		if !found {
			t.Fatal("delivery missing")
		}
		if strings.Contains(string(raw), `"blob_url"`) || strings.Contains(string(raw), "X-Amz-") {
			t.Fatal("event projection contains capability")
		}
	}
	after, err := f.env.Hub.Log.Read(context.Background(), []string{reader}, eventlog.Cursor{Epoch: f.env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, _ := json.Marshal(after)
	if string(beforeRaw) != string(afterRaw) {
		t.Fatal("projection mutated durable event")
	}
}

func TestR2HTTPStateProviderLossAndIncomplete(t *testing.T) {
	f := newR2HTTP(t)
	_, secret := f.env.NewPeer("session", "owner")
	_, stranger := f.env.NewPeer("session", "stranger")
	g := f.upload(t, secret, "provider-loss")
	f.mu.Lock()
	f.headFailure = true
	f.mu.Unlock()
	resp, raw := f.request(t, secret, "GET", "/v1/blobs/"+g.SHA256+"/download?blob_id="+g.BlobID, nil, "")
	if resp.StatusCode != 503 {
		t.Fatal("provider loss misclassified")
	}
	requireCode(t, raw, "blob_unavailable")
	resp, _ = f.request(t, stranger, "GET", "/v1/blobs/"+g.SHA256+"/download?blob_id="+g.BlobID, nil, "")
	if resp.StatusCode != 403 {
		t.Fatal("unauthorized provider state exposed")
	}
	resp, raw = f.request(t, secret, "POST", "/v1/blobs/uploads", blobwire.UploadRequest{Size: 3, SHA256: g.SHA256, MediaType: "text/plain"}, "incomplete")
	var pending blobwire.UploadGrant
	if resp.StatusCode != 201 || json.Unmarshal(raw, &pending) != nil {
		t.Fatal("pending grant failed")
	}
	resp, raw = f.request(t, secret, "GET", "/v1/blobs/"+pending.SHA256+"/download?blob_id="+pending.BlobID, nil, "")
	if resp.StatusCode != 409 {
		t.Fatal("pending not incomplete")
	}
	requireCode(t, raw, "upload_incomplete")
}

func TestR2StateProjectionFromJoinPrincipalAndDisabled(t *testing.T) {
	f := newR2HTTP(t)
	_, secret := f.env.NewPeer("session", "owner")
	reader, readerSecret := f.env.NewPeer("session", "reader")
	late, lateSecret := f.env.NewPeer("session", "late")
	g := f.upload(t, secret, "visibility")
	request := map[string]any{"idempotency_key": "old-visible", "priority": "hold", "recipients": []string{reader}, "content": map[string]any{"text": "old"}, "attachments": []any{map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}}}
	resp, raw := f.request(t, secret, "POST", "/v1/messages", request, "")
	if resp.StatusCode != 202 {
		t.Fatal("old send failed")
	}
	var sent struct {
		ThreadID string `json:"thread_id"`
	}
	if json.Unmarshal(raw, &sent) != nil {
		t.Fatal("send decode failed")
	}
	request["idempotency_key"] = "add-late"
	request["thread_id"] = sent.ThreadID
	request["recipients"] = []string{late}
	delete(request, "attachments")
	resp, _ = f.request(t, secret, "POST", "/v1/messages", request, "")
	if resp.StatusCode != 202 {
		t.Fatal("late join failed")
	}
	operator, err := f.env.Hub.Auth.Resolve(context.Background(), f.env.OperatorSecret)
	if err != nil {
		t.Fatal("operator resolve failed")
	}
	messages, _, err := f.env.Hub.ThreadMessages(context.Background(), operator, sent.ThreadID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{readerSecret, lateSecret} {
		caller, err := f.env.Hub.Auth.Resolve(context.Background(), secret)
		if err != nil {
			t.Fatal("caller resolve failed")
		}
		for _, principal := range []bool{false, true} {
			c := caller
			if principal {
				c.Kind = "principal"
				c.PeerID = ""
				c.CredentialID = ""
			}
			states, err := f.env.Hub.ProjectBlobStates(context.Background(), c, messages)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if secret == lateSecret {
				want = 0
			}
			if len(states) != want {
				t.Fatal("projection bypassed from_join or principal visibility")
			}
		}
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, reader); err != nil {
		t.Fatal(err)
	}
	caller, err := f.env.Hub.Auth.Resolve(context.Background(), secret)
	if err != nil {
		t.Fatal("owner resolve failed")
	}
	// A real disabled peer and its principal must not inherit uploader authority.
	var principalID string
	if err := f.env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, reader).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	caller.Kind = "principal"
	caller.PrincipalID = principalID
	caller.PeerID = ""
	caller.CredentialID = ""
	states, err := f.env.Hub.ProjectBlobStates(context.Background(), caller, messages)
	if err != nil || len(states) != 0 {
		t.Fatal("disabled principal peer disclosed state")
	}
}

func TestR2StateSSEProjection(t *testing.T) {
	f := newR2HTTP(t)
	_, secret := f.env.NewPeer("session", "owner")
	reader, readerSecret := f.env.NewPeer("session", "reader")
	g := f.upload(t, secret, "sse-state")
	body := map[string]any{"idempotency_key": "sse-message", "priority": "hold", "recipients": []string{reader}, "content": map[string]any{"text": "retained"}, "attachments": []any{map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": 3, "media_type": "text/plain", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}}}
	resp, _ := f.request(t, secret, "POST", "/v1/messages", body, "")
	if resp.StatusCode != 202 {
		t.Fatal("send failed")
	}
	f.env.Now.Advance(24 * time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", f.env.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal("request failed")
	}
	req.Header.Set("Authorization", "Bearer "+readerSecret)
	req.Header.Set("Accept", "text/event-stream")
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("stream failed")
	}
	defer stream.Body.Close()
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev envelope.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil || ev.Type != "delivery" {
			continue
		}
		if ev.BlobStates[g.BlobID].State != "expired" {
			t.Fatal("SSE used stale projection")
		}
		if err := envelope.Validate("event", []byte(strings.TrimPrefix(line, "data: "))); err != nil {
			t.Fatal("SSE projection violates schema")
		}
		return
	}
	t.Fatal("stream ended before projected delivery")
}
