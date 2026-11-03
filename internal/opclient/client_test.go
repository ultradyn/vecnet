package opclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Fill every wire field, including nested/optional fields, so adding a field to
// opapi automatically extends this round-trip test instead of hiding a zero.
func wireFixture(v reflect.Value) {
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		v.SetBytes([]byte(`{"fixture":true}`))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		wireFixture(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			wireFixture(v.Field(i))
		}
	case reflect.String:
		v.SetString("fixture &/+? ü")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(17)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		wireFixture(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		x := reflect.New(v.Type().Elem()).Elem()
		wireFixture(x)
		v.SetMapIndex(reflect.ValueOf("fixture"), x)
	case reflect.Interface:
		v.Set(reflect.ValueOf("fixture"))
	default:
		panic(v.Type().String())
	}
}
func fixture[T any]() T { var v T; wireFixture(reflect.ValueOf(&v).Elem()); return v }

func TestReadsDecode(t *testing.T) {
	ctx := context.Background()
	// Values and wrappers reflect the protocol, including the bare subscription array.
	cases := []struct {
		name, method, path, query string
		want                      any
		call                      func(*Client) (any, error)
		wrap                      func(any) any
	}{
		{"inbox", "GET", "/v1/admin/inbox", "before=dly_a&limit=7&unread=1", fixture[opapi.Inbox](), func(c *Client) (any, error) {
			return c.Inbox(ctx, opapi.InboxQuery{Limit: 7, Before: "dly_a", UnreadOnly: true})
		}, nil},
		{"read", "POST", "/v1/admin/threads/thr_a/read", "", fixture[opapi.ReadResult](), func(c *Client) (any, error) { return c.ReadThread(ctx, "thr_a") }, nil},
		{"threads", "GET", "/v1/admin/threads", "cursor=a%2B%2F%3D&limit=7&participant=peer_a", fixture[opapi.ThreadPage](), func(c *Client) (any, error) {
			return c.Threads(ctx, opapi.ThreadQuery{Participant: "peer_a", Cursor: "a+/=", Limit: 7})
		}, nil},
		{"thread", "GET", "/v1/admin/threads/thr_a", "", fixture[opapi.ThreadDetail](), func(c *Client) (any, error) { return c.Thread(ctx, "thr_a") }, nil},
		{"history", "GET", "/v1/threads/thr_a/messages", "after=msg_a&limit=7", fixture[opapi.History](), func(c *Client) (any, error) { return c.History(ctx, "thr_a", "msg_a", 7) }, nil},
		{"matrix", "GET", "/v1/admin/threads/thr_a/deliveries", "after=msg_a&limit=7", fixture[opapi.Matrix](), func(c *Client) (any, error) { return c.Matrix(ctx, "thr_a", "msg_a", 7) }, nil},
		{"peers", "GET", "/v1/admin/peers", "", fixture[[]opapi.PeerRow](), func(c *Client) (any, error) { return c.Peers(ctx) }, func(v any) any { return map[string]any{"peers": v} }},
		{"cuts", "GET", "/v1/admin/loop-cuts", "hours=48", fixture[opapi.LoopCuts](), func(c *Client) (any, error) { return c.LoopCuts(ctx, 48) }, nil},
		{"health", "GET", "/v1/admin/delivery-health", "", fixture[opapi.DeliveryHealth](), func(c *Client) (any, error) { return c.DeliveryHealth(ctx) }, nil},
		{"subscriptions", "GET", "/v1/admin/subscriptions", "", fixture[[]opapi.Subscription](), func(c *Client) (any, error) { return c.Subscriptions(ctx) }, nil},
		{"audit", "GET", "/v1/admin/audit", "before=42&limit=7", fixture[opapi.AuditPage](), func(c *Client) (any, error) { return c.Audit(ctx, 7, "42") }, nil},
		{"counters", "GET", "/v1/admin/counters", "", map[string]int{"loop_cut": 3, "dead_letters": 2}, func(c *Client) (any, error) { return c.Counters(ctx) }, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path || r.URL.RawQuery != tt.query {
					t.Errorf("request = %s %s, want %s %s?%s", r.Method, r.URL, tt.method, tt.path, tt.query)
				}
				if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("headers = %v", r.Header)
				}
				if r.URL.Query().Get("secret") != "" {
					t.Error("credential leaked in URL")
				}
				if r.Method == "POST" && (r.ContentLength != 0 || r.Body != http.NoBody) {
					t.Error("read must have no request body")
				}
				value := tt.want
				if tt.wrap != nil {
					value = tt.wrap(value)
				}
				_ = json.NewEncoder(w).Encode(value)
			}))
			defer srv.Close()
			got, err := tt.call(New(srv.URL+"/", "secret"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decoded %#v, want %#v", got, tt.want)
			}
		})
	}
	t.Run("resume", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/v1/admin/subscriptions/sub_a/resume" || r.ContentLength != 0 {
				t.Errorf("resume request: %s %s len=%d", r.Method, r.URL, r.ContentLength)
			}
			w.WriteHeader(204)
		}))
		defer srv.Close()
		if err := New(srv.URL, "secret").ResumeSubscription(ctx, "sub_a"); err != nil {
			t.Fatal(err)
		}
	})
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func rawJSON(t *testing.T, env *testhub.Env, secret, method, path string, body any, out any) {
	t.Helper()
	resp, raw := env.Do(secret, method, path, body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		mustOK(t, json.Unmarshal(raw, out))
	}
}
func TestExistingEndpointsAgainstTestHub(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	c := New(env.URL, env.OperatorSecret)
	peer, secret := env.NewPeer("session", "recipient")
	var blob struct {
		SHA string `json:"sha256"`
	}
	rawJSON(t, env, env.OperatorSecret, "PUT", "/v1/blobs", strings.NewReader("blob bytes"), &blob)
	sent, err := c.Send(ctx, envelope.SendRequest{IdempotencyKey: "send-one", Recipients: []string{peer}, Priority: "hold", Content: envelope.SendContent{Text: "hello"}, Attachments: []envelope.Attachment{{SHA256: blob.SHA, Size: 10, MediaType: "text/plain", Name: "note.txt"}}})
	mustOK(t, err)
	if sent.MessageID == "" || sent.ThreadID == "" || len(sent.Deliveries) != 1 || sent.Deliveries[0].Recipient != peer || sent.Spilled {
		t.Fatalf("send = %+v", sent)
	}
	history, err := c.History(ctx, sent.ThreadID, "", 20)
	mustOK(t, err)
	if len(history.Messages) != 1 || history.Messages[0].Content.Text != "hello" || history.Messages[0].MessageID != sent.MessageID {
		t.Fatalf("history = %+v", history)
	}
	data, media, err := c.Blob(ctx, blob.SHA, 10)
	mustOK(t, err)
	if string(data) != "blob bytes" || media == "" {
		t.Fatalf("blob %q %q", data, media)
	}
	if _, _, err = c.Blob(ctx, blob.SHA, 9); err == nil {
		t.Fatal("oversize blob accepted")
	}
	mustOK(t, c.SetPresence(ctx, "working", env.Now.Now()))
	var directory struct {
		Peers []opapi.Peer `json:"peers"`
	}
	rawJSON(t, env, env.OperatorSecret, "GET", "/v1/peers", nil, &directory)
	found := false
	for _, p := range directory.Peers {
		if p.PeerID == env.OperatorPeer {
			found = p.Presence == "working"
		}
	}
	if !found {
		t.Fatalf("presence missing: %+v", directory)
	}
	var poll struct {
		Events []envelope.Event `json:"events"`
		Cursor string           `json:"next_cursor"`
	}
	rawJSON(t, env, secret, "GET", "/v1/events?wait=0", nil, &poll)
	if poll.Cursor == "" || len(poll.Events) == 0 {
		t.Fatalf("poll %+v", poll)
	}
	var delivery envelope.Delivery
	for _, e := range poll.Events {
		if e.Type == "delivery" {
			mustOK(t, json.Unmarshal(e.Data, &delivery))
		}
	}
	if delivery.Ingress != "operator_ui" {
		t.Fatalf("ingress %q", delivery.Ingress)
	}
	mustOK(t, New(env.URL, secret).Ack(ctx, poll.Cursor))
	audit, err := c.Audit(ctx, 200, "")
	mustOK(t, err)
	if len(audit.Entries) == 0 {
		t.Fatal("empty audit")
	}
	counters, err := c.Counters(ctx)
	mustOK(t, err)
	if _, ok := counters["dead_letters"]; !ok {
		t.Fatalf("counters %+v", counters)
	}
	// A verified receiver that answers 410 makes real terminal dead letters.
	pushPeer, _ := env.NewPeer("dot", "gone receiver")
	callback := testhub.NewCallback(t)
	callback.OnPost(func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusGone) })
	var route struct {
		ID string `json:"route_id"`
	}
	rawJSON(t, env, env.OperatorSecret, "POST", "/v1/admin/routes", map[string]any{"owner": pushPeer, "url": callback.URL}, &route)
	rawJSON(t, env, env.OperatorSecret, "POST", "/v1/admin/routes/"+route.ID+"/verify", nil, nil)
	dead, err := c.Send(ctx, envelope.SendRequest{IdempotencyKey: "dead", Recipients: []string{pushPeer}, Priority: "wake", Content: envelope.SendContent{Text: "gone"}})
	mustOK(t, err)
	_, err = env.Worker.Step(ctx)
	mustOK(t, err)
	counters, err = c.Counters(ctx)
	mustOK(t, err)
	if counters["dead_letters"] != 1 {
		t.Fatalf("expected one dead letter: %v", counters)
	}
	mustOK(t, c.RetryDeadLetter(ctx, dead.Deliveries[0].DeliveryID))
	_, err = env.Worker.Step(ctx)
	mustOK(t, err)
	if len(callback.Posts()) != 2 {
		t.Fatalf("retry did not push again: %d posts", len(callback.Posts()))
	}
	mustOK(t, c.DiscardDeadLetter(ctx, dead.Deliveries[0].DeliveryID))
	var letters struct {
		Dead []struct {
			ID string `json:"delivery_id"`
		} `json:"dead_letters"`
	}
	rawJSON(t, env, env.OperatorSecret, "GET", "/v1/admin/dead-letters", nil, &letters)
	for _, d := range letters.Dead {
		if d.ID == dead.Deliveries[0].DeliveryID {
			t.Fatal("discarded delivery remains listed")
		}
	}
	audit, err = c.Audit(ctx, 200, "")
	mustOK(t, err)
	actions := map[string]bool{}
	for _, entry := range audit.Entries {
		actions[entry.Action] = true
	}
	for _, action := range []string{"dead_letter_retry", "dead_letter_discard"} {
		if !actions[action] {
			t.Fatalf("missing %s in audit", action)
		}
	}
}

func TestErrorEnvelope(t *testing.T) {
	for _, code := range []int{422, 502} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				if code == 422 {
					fmt.Fprint(w, `{"error":{"code":"invalid_value","message":"bad field","details":{"field":"limit"}}}`)
				} else {
					fmt.Fprint(w, "upstream unavailable")
				}
			}))
			defer srv.Close()
			_, err := New(srv.URL, "secret").Inbox(context.Background(), opapi.InboxQuery{})
			var got *Error
			if !errors.As(err, &got) || got.Status != code {
				t.Fatalf("error = %v", err)
			}
			if code == 422 && (got.Code != "invalid_value" || got.Message != "bad field" || got.Details["field"] != "limit") {
				t.Fatalf("error = %+v", got)
			}
		})
	}
}

func TestAnswerIdempotent(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	requester, secret := env.NewPeer("session", "requester")
	c := New(env.URL, env.OperatorSecret)
	request, err := New(env.URL, secret).Send(ctx, envelope.SendRequest{IdempotencyKey: "ask", Recipients: []string{env.OperatorPeer}, Priority: "hold", Kind: "approval_request", Content: envelope.SendContent{Text: "Proceed?"}, ApprovalRequest: &envelope.ApprovalRequest{ApprovalID: "apr_01ARZ3NDEKTSV4RRFFQ69G5FAV", Question: "Proceed?", Options: []string{"yes", "no"}, ExpiresAt: envelope.FormatTime(env.Now.Now().Add(time.Hour))}})
	mustOK(t, err)
	a := opapi.OpenApproval{ApprovalID: "apr_01ARZ3NDEKTSV4RRFFQ69G5FAV", ThreadID: request.ThreadID, MessageID: request.MessageID, Requester: requester, Options: []string{"yes", "no"}}
	mustOK(t, c.Answer(ctx, a, 0))
	mustOK(t, c.Answer(ctx, a, 0))
	h, err := c.History(ctx, request.ThreadID, "", 20)
	mustOK(t, err)
	if len(h.Messages) != 2 {
		t.Fatalf("%d messages after repeated answer", len(h.Messages))
	}
	response := h.Messages[1]
	if response.Kind != "approval_response" || response.Priority != "wake" || response.InReplyTo == nil || *response.InReplyTo != request.MessageID || response.ApprovalResponse == nil || response.ApprovalResponse.Decision != "yes" || !reflect.DeepEqual(response.Recipients, []string{requester}) {
		t.Fatalf("response %+v", response)
	}
	err = c.Answer(ctx, a, 1)
	var got *Error
	if !errors.As(err, &got) || got.Code != "approval_closed" {
		t.Fatalf("closed error = %v", err)
	}
	if err = c.Answer(ctx, a, -1); !errors.As(err, &got) || got.Code != "invalid_option" {
		t.Fatalf("invalid option = %v", err)
	}
}

func TestAnswerWire(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/messages" || r.Header.Get("X-Vecnet-Ingress") != "operator_ui" {
			t.Errorf("answer request = %s %s %v", r.Method, r.URL, r.Header)
		}
		var got map[string]any
		mustOK(t, json.NewDecoder(r.Body).Decode(&got))
		want := map[string]any{
			"idempotency_key": "tui-apr-apr_test-1", "thread_id": "thr_test", "in_reply_to": "msg_test", "recipients": []any{"peer_requester"}, "kind": "approval_response", "priority": "wake", "content": map[string]any{"text": "no"}, "approval_response": map[string]any{"approval_id": "apr_test", "decision": "no"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("answer body=%#v want=%#v", got, want)
		}
		fmt.Fprint(w, `{"message_id":"msg_answer","thread_id":"thr_test","deliveries":[],"spilled":false}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "secret")
	a := opapi.OpenApproval{ApprovalID: "apr_test", ThreadID: "thr_test", MessageID: "msg_test", Requester: "peer_requester", Options: []string{"yes", "no"}}
	mustOK(t, c.Answer(context.Background(), a, 1))
	mustOK(t, c.Answer(context.Background(), a, 1))
	if calls != 2 {
		t.Fatalf("requests=%d", calls)
	}
}

func TestBlobLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.(http.Flusher).Flush() // no Content-Length
		fmt.Fprint(w, "12345")
	}))
	defer srv.Close()
	c := New(srv.URL, "secret")
	for _, tt := range []struct {
		max int64
		ok  bool
	}{{-1, false}, {0, false}, {4, false}, {5, true}, {6, true}} {
		got, media, err := c.Blob(context.Background(), "hash", tt.max)
		if tt.ok {
			if err != nil || string(got) != "12345" || media != "text/plain" {
				t.Fatalf("max=%d: %q %q %v", tt.max, got, media, err)
			}
		} else if err == nil || got != nil {
			t.Fatalf("max=%d returned %q, %v", tt.max, got, err)
		}
	}
}

func TestUploadSendsBytesAndType(t *testing.T) {
	for _, mediaType := range []string{"text/plain; charset=utf-8", ""} {
		t.Run(mediaType, func(t *testing.T) {
			env := testhub.Start(t)
			target, err := url.Parse(env.URL)
			mustOK(t, err)
			proxy := httputil.NewSingleHostReverseProxy(target)
			wantType := mediaType
			if wantType == "" {
				wantType = "application/octet-stream"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.URL.RequestURI() != "/v1/blobs?ttl=86400" {
					t.Errorf("upload request = %s %s", r.Method, r.URL)
				}
				if r.ContentLength != 3 || r.Header.Get("Content-Length") != "3" || len(r.TransferEncoding) != 0 {
					t.Errorf("upload length=%d header=%q transfer=%v", r.ContentLength, r.Header.Get("Content-Length"), r.TransferEncoding)
				}
				if r.Header.Get("Content-Type") != wantType || r.Header.Get("Authorization") != "Bearer "+env.OperatorSecret {
					t.Error("upload content type or bearer does not match")
				}
				proxy.ServeHTTP(w, r)
			}))
			defer srv.Close()
			// Hide the reader's length so the client must set ContentLength itself.
			reader := struct{ io.Reader }{strings.NewReader("abc")}
			client := New(srv.URL, env.OperatorSecret)
			client.hc.Timeout = time.Nanosecond // Upload must not use the JSON client's timeout.
			got, err := client.Upload(context.Background(), reader, 3, mediaType, "note.txt")
			mustOK(t, err)
			want := envelope.Attachment{SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Size: 3, MediaType: wantType, Name: "note.txt"}
			if got != want {
				t.Fatalf("Upload = %+v, want %+v", got, want)
			}
			resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/blobs/"+got.SHA256, nil)
			if resp.StatusCode != http.StatusOK || string(raw) != "abc" || resp.Header.Get("Content-Type") != wantType {
				t.Fatalf("GET uploaded blob = %d %q type=%q", resp.StatusCode, raw, resp.Header.Get("Content-Type"))
			}
		})
	}
}

func TestUploadTooLarge(t *testing.T) {
	env := testhub.Start(t)
	env.Hub.Blobs.MaxSize = 2
	got, err := New(env.URL, env.OperatorSecret).Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "note.txt")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 413 || apiErr.Code != "blob_too_large" || apiErr.Details["limit"] != float64(2) {
		t.Fatalf("Upload error = %#v, want *Error 413 blob_too_large with limit 2", err)
	}
	if got != (envelope.Attachment{}) {
		t.Fatalf("failed upload returned %+v", got)
	}
}

func TestUploadEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 || r.Header.Get("Content-Length") != "0" || len(r.TransferEncoding) != 0 {
			t.Errorf("empty upload length=%d header=%q transfer=%v", r.ContentLength, r.Header.Get("Content-Length"), r.TransferEncoding)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			t.Errorf("empty upload body=%q, error=%v", body, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","size":0}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "secret").Upload(context.Background(), struct{ io.Reader }{strings.NewReader("")}, 0, "", "empty.bin")
	mustOK(t, err)
	if got.Size != 0 || got.MediaType != "application/octet-stream" || got.Name != "empty.bin" || got.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty upload = %+v", got)
	}
}

func TestUploadContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a cancelled upload reached the server")
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(srv.URL, "secret").Upload(ctx, strings.NewReader("abc"), 3, "text/plain", "note.txt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload error = %v, want context.Canceled", err)
	}
}

func TestUploadDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/blobs" {
			t.Error("upload followed an authenticated redirect")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/redirect-target", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "secret").Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "note.txt")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect || apiErr.Message != http.StatusText(http.StatusTemporaryRedirect) {
		t.Fatalf("redirect upload error = %#v, want *Error 307 with fallback message", err)
	}
}

func TestUploadQuotaError(t *testing.T) {
	env := testhub.Start(t)
	env.Hub.Blobs.DailyQuota = 2
	_, err := New(env.URL, env.OperatorSecret).Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "note.txt")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.Code != "upload_quota_exceeded" || apiErr.Details["limit"] != float64(2) {
		t.Fatalf("quota upload error = %#v, want *Error 429 upload_quota_exceeded with limit 2", err)
	}
	if seconds, ok := apiErr.Details["retry_after_seconds"].(float64); !ok || seconds <= 0 || seconds > 86400 {
		t.Fatalf("quota retry_after_seconds = %v, want seconds until the next UTC day", apiErr.Details["retry_after_seconds"])
	}
}

// TestClientReact adds and then removes a reaction against a real hub. The
// proxy checks the operator_ui ingress header on every reaction request.
func TestClientReact(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	target, err := url.Parse(env.URL)
	mustOK(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var reacts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/reactions" {
			reacts.Add(1)
			if r.Method != "POST" || r.Header.Get("X-Vecnet-Ingress") != "operator_ui" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("reaction request %s ingress %q type %q", r.Method, r.Header.Get("X-Vecnet-Ingress"), r.Header.Get("Content-Type"))
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := New(srv.URL, env.OperatorSecret)
	peer, _ := env.NewPeer("session", "reacted-to")
	sent, err := c.Send(ctx, envelope.SendRequest{IdempotencyKey: "react-target", Recipients: []string{peer}, Priority: "hold", Content: envelope.SendContent{Text: "react to me"}})
	mustOK(t, err)
	check := envelope.ReactionEmoji[5]

	added, err := c.React(ctx, envelope.ReactionRequest{MessageID: sent.MessageID, Emoji: check, Op: "add", Priority: "hold"})
	mustOK(t, err)
	want := []envelope.ReactionGroup{{Emoji: check, Peers: []string{env.OperatorPeer}}}
	if added.MessageID != sent.MessageID || added.ThreadID != sent.ThreadID || added.Emoji != check || added.Op != "add" || !added.Changed || !reflect.DeepEqual(added.Reactions, want) {
		t.Fatalf("add = %+v", added)
	}
	if added.Superseded == nil || len(added.Superseded) != 0 || added.Wake != nil {
		t.Fatalf("add superseded %#v wake %+v, want [] and nil", added.Superseded, added.Wake)
	}

	removed, err := c.React(ctx, envelope.ReactionRequest{MessageID: sent.MessageID, Emoji: check, Op: "remove"})
	mustOK(t, err)
	if removed.Op != "remove" || !removed.Changed || removed.Reactions == nil || len(removed.Reactions) != 0 {
		t.Fatalf("remove = %+v", removed)
	}
	if removed.Superseded == nil || len(removed.Superseded) != 0 || removed.Wake != nil {
		t.Fatalf("remove superseded %#v wake %+v, want [] and nil", removed.Superseded, removed.Wake)
	}
	if n := reacts.Load(); n != 2 {
		t.Fatalf("%d reaction requests, want 2", n)
	}
}
