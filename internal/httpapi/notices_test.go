package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/testhub"
)

func httpNotice(t *testing.T, e *testhub.Env, body any) envelope.HubNotice {
	t.Helper()
	raw := adminDo(t, e, "POST", "/v1/admin/notices", body, 201)
	var out struct {
		Notice envelope.HubNotice `json:"notice"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	noticeJSON, err := json.Marshal(out.Notice)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate("hubNotice", noticeJSON); err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyNotice(e.Hub.IdentityKey().Pubkey(), out.Notice); err != nil {
		t.Fatal(err)
	}
	return out.Notice
}

func TestPostNoticeOperatorOnly(t *testing.T) {
	e := testhub.Start(t)
	_, peer := e.NewPeer("session", "notice peer")
	_, principal := e.NewPrincipal("notice principal")
	body := map[string]string{"kind": "maintenance", "message": "hello"}
	for _, secret := range []string{peer, principal} {
		resp, raw := e.Do(secret, "POST", "/v1/admin/notices", body)
		mustErr(t, resp, raw, 403, "forbidden")
	}
	var kicks atomic.Int32
	e.Configure(func(d *httpapi.Deps) { d.Kick = func() { kicks.Add(1) } })
	n := httpNotice(t, e, body)
	if !strings.HasPrefix(n.NoticeID, "ntc_") || n.Hub != e.URL || kicks.Load() != 1 {
		t.Fatalf("notice %+v; kicks %d", n, kicks.Load())
	}
}

func TestPostNoticeValidation(t *testing.T) {
	e := testhub.Start(t)
	for _, tc := range []struct {
		name, field, value string
		moving             bool
	}{
		{"kind", "kind", "unknown", false}, {"empty", "message", "", false}, {"long", "message", strings.Repeat("x", 1025), false},
		{"unicode bytes", "message", strings.Repeat("😀", 257), false}, {"missing URL", "new_url", "", true}, {"restart URL", "new_url", "https://next.example", false},
		{"userinfo", "new_url", "https://user@next.example", true}, {"query", "new_url", "https://next.example?q=1", true}, {"own", "new_url", e.URL + "/", true},
		{"past expiry", "expires_at", envelope.FormatTime(e.Now.Now().Add(-time.Second)), false}, {"bad expiry", "expires_at", "tomorrow", false}, {"at", "at", "tomorrow", false},
		{"comma at", "at", strings.Replace(envelope.FormatTime(e.Now.Now().Add(time.Hour)), ".", ",", 1), false},
		{"comma expiry", "expires_at", strings.Replace(envelope.FormatTime(e.Now.Now().Add(2*time.Hour)), ".", ",", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]string{"kind": "restart", "message": "hello"}
			if tc.moving {
				body["kind"] = "moving"
			}
			body[tc.field] = tc.value
			resp, raw := e.Do(e.OperatorSecret, "POST", "/v1/admin/notices", body)
			got := mustErr(t, resp, raw, 422, "invalid_value")
			if got.Error.Details["field"] != tc.field {
				t.Fatalf("details: %s", raw)
			}
		})
	}
	// Structural errors still use the existing invalid_body contract.
	for _, body := range []string{`{"kind":7,"message":"hello"}`, `{"kind":"restart","message":"hello","unknown":1}`, `{"kind":"restart","message":"hello","at":null}`} {
		resp, raw := e.Do(e.OperatorSecret, "POST", "/v1/admin/notices", body)
		mustErr(t, resp, raw, 400, "invalid_body")
	}
	at := envelope.FormatTime(e.Now.Now().Add(time.Hour))
	expiry := envelope.FormatTime(e.Now.Now().Add(2 * time.Hour))
	n := httpNotice(t, e, map[string]string{"kind": "restart", "message": "hello", "at": at, "expires_at": expiry})
	if n.At != at || n.ExpiresAt != expiry {
		t.Fatalf("timestamps changed: %+v", n)
	}
}

func TestClearNotice(t *testing.T) {
	e := testhub.Start(t)
	n := httpNotice(t, e, map[string]string{"kind": "maintenance", "message": "hello"})
	var kicks atomic.Int32
	e.Configure(func(d *httpapi.Deps) { d.Kick = func() { kicks.Add(1) } })
	adminDo(t, e, "POST", "/v1/admin/notices/"+n.NoticeID+"/clear", nil, 204)
	adminDo(t, e, "POST", "/v1/admin/notices/"+n.NoticeID+"/clear", nil, 204)
	resp, raw := e.Do(e.OperatorSecret, "POST", "/v1/admin/notices/ntc_unknown/clear", nil)
	mustErr(t, resp, raw, 404, "not_found")
	var count int
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM events WHERE type='hub_notice'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("events %d, want posted and one cleared", count)
	}
	var data string
	if err := e.Hub.DB.QueryRow(`SELECT data FROM events WHERE type='hub_notice' ORDER BY seq DESC LIMIT 1`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var ev envelope.HubNoticeEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Change != "cleared" || ev.Notice.ClearedHow != "operator" || ev.Notice.ClearedAt == "" {
		t.Fatalf("event %+v", ev)
	}
	if err := hubid.VerifyNotice(e.Hub.IdentityKey().Pubkey(), ev.Notice); err != nil {
		t.Fatal(err)
	}
	if kicks.Load() < 1 {
		t.Fatal("clear did not kick")
	}
	active, err := e.Hub.ActiveNotices(context.Background())
	if err != nil || len(active) != 0 {
		t.Fatalf("active %+v %v", active, err)
	}
}

func TestNoticeOperatorAuditActor(t *testing.T) {
	e := testhub.Start(t)
	first := httpNotice(t, e, map[string]string{"kind": "maintenance", "message": "first"})
	second := httpNotice(t, e, map[string]string{"kind": "maintenance", "message": "second"})
	adminDo(t, e, "POST", "/v1/admin/notices/"+second.NoticeID+"/clear", nil, 204)
	raw := adminDo(t, e, "GET", "/v1/admin/audit?limit=50", nil, 200)
	var page struct {
		Entries []hub.AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	type auditKey struct{ action, noticeID, how string }
	want := map[auditKey]int{
		{"notice_post", first.NoticeID, ""}:           1,
		{"notice_post", second.NoticeID, ""}:          1,
		{"notice_clear", first.NoticeID, "replaced"}:  1,
		{"notice_clear", second.NoticeID, "operator"}: 1,
	}
	got := map[auditKey]int{}
	for _, entry := range page.Entries {
		if entry.Action != "notice_post" && entry.Action != "notice_clear" {
			continue
		}
		if entry.Actor != "operator" {
			t.Errorf("%s actor = %q, want operator", entry.Action, entry.Actor)
		}
		noticeID, _ := entry.Detail["notice_id"].(string)
		how, _ := entry.Detail["how"].(string)
		key := auditKey{entry.Action, noticeID, how}
		got[key]++
		if want[key] != 1 || len(entry.Targets) != 1 || entry.Targets[0] != noticeID {
			t.Errorf("unexpected notice audit entry: %+v", entry)
		}
	}
	for key, count := range want {
		if got[key] != count {
			t.Errorf("audit %v: got %d rows, want %d", key, got[key], count)
		}
	}
}

func TestGetHubNoticesAnyCredential(t *testing.T) {
	e := testhub.Start(t)
	_, peer := e.NewPeer("session", "reader")
	_, principal := e.NewPrincipal("reader principal")
	for _, secret := range []string{peer, principal, e.OperatorSecret} {
		resp, raw := e.Do(secret, "GET", "/v1/hub/notices", nil)
		if resp.StatusCode != 200 || string(raw) != "{\"notices\":[]}\n" {
			t.Fatalf("empty list: %d %s", resp.StatusCode, raw)
		}
	}
	n := httpNotice(t, e, map[string]string{"kind": "restart", "message": "hello"})
	for _, secret := range []string{peer, principal, e.OperatorSecret} {
		resp, raw := e.Do(secret, "GET", "/v1/hub/notices", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET: %d %s", resp.StatusCode, raw)
		}
		var out struct {
			Notices []envelope.HubNotice `json:"notices"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Notices) != 1 || out.Notices[0].NoticeID != n.NoticeID {
			t.Fatalf("list: %s", raw)
		}
	}
	resp, raw := e.Do("", "GET", "/v1/hub/notices", nil)
	mustErr(t, resp, raw, 401, "unauthenticated")
	e.Now.Advance(25 * time.Hour)
	resp, raw = e.Do(peer, "GET", "/v1/hub/notices", nil)
	if resp.StatusCode != 200 || string(raw) != "{\"notices\":[]}\n" {
		t.Fatalf("expired list: %d %s", resp.StatusCode, raw)
	}
}
