package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Report only status/reason on failure; a faulty preview must never leak its
// confirmation material into test output.
func prefsMustErr(t *testing.T, response *http.Response, raw []byte, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	err := json.Unmarshal(raw, &body)
	if err != nil || response.StatusCode != status || body.Error.Code != code {
		t.Fatalf("preference error: status=%d reason=%s, want status=%d reason=%s", response.StatusCode, body.Error.Code, status, code)
	}
}

func prefsHTTP(t *testing.T, e *testhub.Env, secret, method, path, body, tag, media string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if tag != "" {
		req.Header.Set("If-Match", tag)
	}
	if media != "" {
		req.Header.Set("Content-Type", media)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}
func prefsGET(t *testing.T, e *testhub.Env) (prefs.Snapshot, string) {
	t.Helper()
	resp, raw := e.Do(e.OperatorSecret, "GET", "/v1/admin/prefs", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET prefs status = %d, want 200", resp.StatusCode)
	}
	var s prefs.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s, resp.Header.Get("ETag")
}
func TestPreferencesHTTPMetadataAndConditionalRead(t *testing.T) {
	e := testhub.StartWith(t, func(c *hub.Config) {
		c.PreferenceBootstrap = map[string]prefs.Effective{"events.refresh_interval": {Value: prefs.Duration(1800), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL"}}
	})
	resp, raw := e.Do(e.OperatorSecret, "GET", "/v1/admin/prefs", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET prefs status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(resp.Header.Get("ETag"), `"prefs-0-`) {
		t.Fatal("missing private conditional headers")
	}
	var wire struct {
		Revision    uint64            `json:"revision"`
		Preferences []json.RawMessage `json:"preferences"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Revision != 0 || len(wire.Preferences) != 21 {
		t.Fatal("incomplete initial metadata")
	}
	const fixture = `{"key":"events.refresh_interval","type":"duration","default":1800,"min":60,"max":86400,"unit":"seconds","label":"Events refresh interval","description":"Refresh cadence for MCP Events subscriptions.","group":"notifications_presence","settable_by":"operator","editable":true,"runtime":"live","danger":[],"value":1800,"source":"flag","origin":"VECNET_EVENTS_REFRESH_INTERVAL"}`
	previous := ""
	found := false
	for _, row := range wire.Preferences {
		var d prefs.Descriptor
		if json.Unmarshal(row, &d) != nil {
			t.Fatal("bad descriptor")
		}
		if d.Key <= previous {
			t.Fatal("unstable order")
		}
		previous = d.Key
		if d.Key == "events.refresh_interval" {
			var got, want any
			_ = json.Unmarshal(row, &got)
			_ = json.Unmarshal([]byte(fixture), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatal("metadata fixture differs")
			}
			found = true
		}
		if d.Key == "retention.history" && (d.Editable || d.UnavailableReason == "") {
			t.Fatal("missing adapter became editable")
		}
	}
	if !found {
		t.Fatal("missing refresh descriptor")
	}
	for _, condition := range []string{resp.Header.Get("ETag"), "W/" + resp.Header.Get("ETag"), `"other", ` + resp.Header.Get("ETag"), "*"} {
		req, _ := http.NewRequest("GET", e.URL+"/v1/admin/prefs", nil)
		req.Header.Set("Authorization", "Bearer "+e.OperatorSecret)
		req.Header.Set("If-None-Match", condition)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 304 || len(b) != 0 || r.Header.Get("ETag") != resp.Header.Get("ETag") || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("conditional read failed")
		}
	}
}
func TestPreferencesHTTPOperatorAuthority(t *testing.T) {
	e := testhub.Start(t)
	peer, secret := e.NewPeer("session", "session")
	_, principal := e.NewPrincipal("principal")
	for _, route := range []struct{ method, path string }{{"GET", "/v1/admin/prefs"}, {"PATCH", "/v1/admin/prefs"}, {"POST", "/v1/admin/prefs/preview"}} {
		for _, credential := range []string{secret, principal} {
			r, b := prefsHTTP(t, e, credential, route.method, route.path, `{"set":{"agents.thread_budget":600}}`, "", "application/json")
			prefsMustErr(t, r, b, 403, "forbidden")
			if strings.Contains(string(b), "preferences") || strings.Contains(string(b), "thread_budget") {
				t.Fatal("forbidden disclosed values")
			}
		}
		r, b := prefsHTTP(t, e, "", route.method, route.path, "", "", "")
		prefsMustErr(t, r, b, 401, "unauthenticated")
		r, b = prefsHTTP(t, e, e.OperatorSecret, route.method, route.path+"?as="+peer, `{"set":{"agents.thread_budget":600}}`, "", "application/json")
		prefsMustErr(t, r, b, 403, "forbidden")
	}
	// The shipped protocol accepts OAuth on /mcp only; a valid OAuth token
	// still cannot authenticate to other HTTP routes.
	dot, _ := e.NewPeer("dot", "prefs-dot")
	var prn string
	if err := e.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, dot).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	at := envelope.FormatTime(e.Now.Now())
	if _, err := e.Hub.DB.Exec(`INSERT INTO oauth_principals VALUES(?,?,?,?)`, prn, "synthetic-subject", dot, at); err != nil {
		t.Fatal(err)
	}
	token := "vat_synthetic-prefs-oauth"
	if _, err := e.Hub.DB.Exec(`INSERT INTO oauth_tokens(token_hash,kind,client_id,principal_id,scope,resource,expires_at,created_at) VALUES(?,'access',?,?,?,?,?,?)`, auth.HashSecret(token), "synthetic-client", prn, oauth.Scope, e.URL+"/mcp", envelope.FormatTime(e.Now.Now().Add(time.Hour)), at); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", e.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if _, err := (&oauth.TokenAuth{S: e.OAuth}).Authenticate(context.Background(), req); err != nil {
		t.Fatal("OAuth fixture must be valid")
	}
	for _, route := range []struct{ method, path string }{{"GET", "/v1/admin/prefs"}, {"PATCH", "/v1/admin/prefs"}, {"POST", "/v1/admin/prefs/preview"}} {
		r, b := e.Do(token, route.method, route.path, nil)
		prefsMustErr(t, r, b, 401, "unauthenticated")
	}
}
func TestPreferencesHTTPStrictBodiesAndPreconditions(t *testing.T) {
	e := testhub.Start(t)
	_, tag := prefsGET(t, e)
	for _, method := range []string{"PATCH", "POST"} {
		path := "/v1/admin/prefs"
		if method == "POST" {
			path += "/preview"
		}
		cases := []struct {
			body, tag, media string
			status           int
			code             string
		}{
			{`{"set":{"agents.thread_budget":600}}`, "", "application/json", 428, "prefs_precondition_required"},
			{`{"set":{"agents.thread_budget":600}}`, "*", "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600}}`, "W/" + tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600}}`, tag + ", " + tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600}}`, `"stale"`, "application/json", 412, "prefs_revision_conflict"},
			{`{"set":{"agents.thread_budget":600}}`, `"stale,opaque"`, "application/json", 412, "prefs_revision_conflict"},
			{`{"set":{"agents.thread_budget":600}}`, tag, "text/plain", 415, "unsupported_media_type"},
			{`{"set":{"agents.thread_budget":600}}`, tag, "", 415, "unsupported_media_type"},
			{strings.Repeat(" ", 16385), tag, "application/json", 413, "payload_too_large"},
			{`{}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{},"set":{"agents.thread_budget":600}}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600,"agents.thread_budget":700}}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600},"reset":["agents.thread_budget"]}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600},"force":true}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600},"confirm":true}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":600},"confirm":["same","same"]}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":null}}`, tag, "application/json", 400, "invalid_body"},
			{`{"set":{"agents.thread_budget":1.5}}`, tag, "application/json", 422, "prefs_invalid_value"},
			{`{"set":{"agents.thread_budget":-1}}`, tag, "application/json", 422, "prefs_invalid_value"},
			{`{"set":{"agents.thread_budget":9223372036854775808}}`, tag, "application/json", 422, "prefs_invalid_value"},
			{`{"set":{"agents.thread_budget":true}}`, tag, "application/json", 422, "prefs_invalid_value"},
			{`{"set":{"agents.thread_budget":"request-canary"}}`, tag, "application/json", 422, "prefs_invalid_value"},
			{`{"set":{"request-canary":600}}`, tag, "application/json", 422, "prefs_unknown_key"},
			{`{"set":{"retention.history":86400}}`, tag, "application/json", 409, "prefs_unavailable"},
			{`{"set":{"presence.default_ttl":1800}}`, tag, "application/json", 200, ""},
		}
		for i, c := range cases {
			r, b := prefsHTTP(t, e, e.OperatorSecret, method, path, c.body, c.tag, c.media)
			if c.code == "" {
				if method == "PATCH" {
					prefsMustErr(t, r, b, 409, "prefs_confirmation_required")
				} else if r.StatusCode != 200 {
					t.Fatalf("preview status = %d", r.StatusCode)
				}
				continue
			}
			if r.StatusCode != c.status {
				t.Fatalf("%s case %d status = %d, want %d", method, i, r.StatusCode, c.status)
			}
			prefsMustErr(t, r, b, c.status, c.code)
			if strings.Contains(string(b), "request-canary") {
				t.Fatal("rejected input disclosed")
			}
			var envelope map[string]json.RawMessage
			_ = json.Unmarshal(b, &envelope)
			var inner map[string]json.RawMessage
			_ = json.Unmarshal(envelope["error"], &inner)
			if len(envelope) != 1 || len(inner) != 3 || string(inner["details"]) == "null" || len(inner["details"]) == 0 {
				t.Fatal("error envelope differs")
			}
		}
	}
	s, _ := prefsGET(t, e)
	if s.Revision != 0 {
		t.Fatal("failed requests mutated preferences")
	}
}
func TestPreferencesHTTPTruncatedBody(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodPost} {
		for _, c := range []struct{ name, body string }{
			{"partial_json", `{"set":{"agents.thread_budget":`},
			{"complete_json", `{"set":{"agents.thread_budget":600}}`},
		} {
			t.Run(method+"/"+c.name, func(t *testing.T) {
				e := testhub.Start(t)
				beforeWire, tag := prefsGET(t, e)
				before := e.Hub.Preferences.Current()
				var auditBefore int
				if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM audit`).Scan(&auditBefore); err != nil {
					t.Fatal(err)
				}
				path := "/v1/admin/prefs"
				if method == http.MethodPost {
					path += "/preview"
				}
				// Even a complete JSON prefix is invalid when the transport
				// reports that the declared request body was truncated.
				req := httptest.NewRequest(method, path, io.MultiReader(strings.NewReader(c.body), iotest.ErrReader(io.ErrUnexpectedEOF)))
				req.Header.Set("Authorization", "Bearer "+e.OperatorSecret)
				req.Header.Set("If-Match", tag)
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				httpapi.New(httpapi.NewStack(e.Hub).Deps()).ServeHTTP(response, req)
				const want = `{"error":{"code":"invalid_body","message":"Invalid preference request.","details":{}}}` + "\n"
				if response.Code != http.StatusBadRequest || response.Body.String() != want {
					t.Errorf("truncated request did not return the sanitized 400 envelope (status=%d)", response.Code)
				}
				after, afterTag := prefsGET(t, e)
				persisted, err := e.Hub.Preferences.LoadSnapshot(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if afterTag != tag || !reflect.DeepEqual(beforeWire.Descriptors(), after.Descriptors()) || persisted.ETag != tag || !reflect.DeepEqual(before.Descriptors(), persisted.Descriptors()) || e.Hub.Preferences.Current().ETag != tag {
					t.Fatal("truncated request changed persisted or published preferences")
				}
				var auditAfter int
				if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM audit`).Scan(&auditAfter); err != nil {
					t.Fatal(err)
				}
				if auditAfter != auditBefore {
					t.Fatal("truncated request changed audit")
				}
			})
		}
	}
}

func TestPreferencesHTTPConcurrentPatches(t *testing.T) {
	e := testhub.Start(t)
	_, tag := prefsGET(t, e)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, body := range []string{`{"set":{"agents.thread_budget":600}}`, `{"set":{"agents.thread_budget":700}}`} {
		wg.Go(func() {
			r, _ := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", body, tag, "application/json")
			codes <- r.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		t.Fatalf("concurrent statuses = %v", counts)
	}
	s, _ := prefsGET(t, e)
	if s.Revision != 1 {
		t.Fatal("lost update")
	}
}
func TestPreferencesHTTPRequestJourneyAndSanitization(t *testing.T) {
	e := testhub.Start(t)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	_, tag := prefsGET(t, e)
	r, _ := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"agents.thread_budget":600}}`, tag, "application/json")
	if r.StatusCode != 200 {
		t.Fatal("safe save failed")
	}
	r, b := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"agents.thread_budget":700}}`, tag, "application/json")
	prefsMustErr(t, r, b, 412, "prefs_revision_conflict")
	_, tag = prefsGET(t, e)
	proposal := `{"set":{"presence.default_ttl":1800}}`
	r, b = prefsHTTP(t, e, e.OperatorSecret, "POST", "/v1/admin/prefs/preview", proposal, tag, "application/json")
	if r.StatusCode != 200 {
		t.Fatal("preview failed")
	}
	var p prefs.Preview
	if json.Unmarshal(b, &p) != nil || p.ConfirmToken == "" || len(p.Risks) != 1 || p.Risks[0].Code != "presence_shortened" {
		t.Fatal("incomplete risk preview")
	}
	if expiry, err := time.Parse(time.RFC3339Nano, p.ExpiresAt); err != nil || !expiry.Equal(e.Now.Now().Add(5*time.Minute)) {
		t.Fatal("preview lifetime differs")
	}
	s, cancelTag := prefsGET(t, e)
	if s.Revision != 1 || cancelTag != tag {
		t.Fatal("preview/cancel mutated state")
	}
	body, _ := json.Marshal(map[string]any{"set": map[string]int{"presence.default_ttl": 1800}, "confirm": []string{p.ConfirmToken}})
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", string(body), tag, "application/json")
	if r.StatusCode != 200 {
		t.Fatalf("confirmed save status = %d", r.StatusCode)
	}
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", string(body), tag, "application/json")
	prefsMustErr(t, r, b, 412, "prefs_revision_conflict")
	cfg := e.Hub.Cfg
	cfg.PreferencePolicyGeneration = "restart-fixture"
	_, savedTag := prefsGET(t, e)
	again, err := hub.New(e.Hub.DB, cfg, e.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	e.Hub = again
	e.Configure(func(d *httpapi.Deps) { d.Hub = again })
	s, tag = prefsGET(t, e)
	if tag == savedTag || tag != again.Preferences.Current().ETag {
		t.Fatal("restart GET did not use the reconstructed hub")
	}
	value, _ := s.Get("presence.default_ttl")
	n, _ := value.Value.Int64()
	if s.Revision != 2 || n != 1800 || value.Source != prefs.SourceStored {
		t.Fatal("restart lost confirmed save")
	}
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"request-canary":600}}`, tag, "application/json")
	prefsMustErr(t, r, b, 422, "prefs_unknown_key")
	_, audit := e.Do(e.OperatorSecret, "GET", "/v1/admin/audit", nil)
	for _, sensitive := range []string{p.ConfirmToken, string(body), "request-canary"} {
		if strings.Contains(logs.String(), sensitive) || strings.Contains(string(audit), sensitive) {
			t.Fatal("sensitive request material entered logs/audit")
		}
	}
}

func TestPreferencesHTTPResetSourceAndErrorFixtures(t *testing.T) {
	e := testhub.StartWith(t, func(c *hub.Config) {
		c.PreferenceBootstrap = map[string]prefs.Effective{"agents.thread_budget": {Value: prefs.Int(700), Source: prefs.SourceFlag, Origin: "--thread-budget"}}
	})
	_, tag := prefsGET(t, e)
	r, b := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"agents.thread_budget":500,"events.refresh_interval":600}}`, tag, "application/json; charset=utf-8")
	if r.StatusCode != 200 {
		t.Fatal("safe multi-key override failed")
	}
	var s prefs.Snapshot
	if json.Unmarshal(b, &s) != nil {
		t.Fatal("snapshot decode failed")
	}
	v, _ := s.Get("agents.thread_budget")
	if v.Source != prefs.SourceStored || v.Origin != "" {
		t.Fatal("explicit built-in over flag must be stored")
	}
	tag = r.Header.Get("ETag")
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"reset":["agents.thread_budget","events.refresh_interval"]}`, tag, "application/json")
	if r.StatusCode != 200 || json.Unmarshal(b, &s) != nil {
		t.Fatal("reset failed")
	}
	v, _ = s.Get("agents.thread_budget")
	n, _ := v.Value.Int64()
	if n != 700 || v.Source != prefs.SourceFlag || v.Origin != "--thread-budget" {
		t.Fatal("reset did not restore flag provenance")
	}
	v, _ = s.Get("events.refresh_interval")
	n, _ = v.Value.Int64()
	if n != 1800 || v.Source != prefs.SourceDefault {
		t.Fatal("reset did not restore built-in provenance")
	}
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"agents.thread_budget":600}}`, "", "application/json")
	const fixture = "{\"error\":{\"code\":\"prefs_precondition_required\",\"message\":\"If-Match is required; read preferences first.\",\"details\":{}}}\n"
	if r.StatusCode != 428 || string(b) != fixture {
		t.Fatal("shipped empty-details envelope fixture differs")
	}
	_, tag = prefsGET(t, e)
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"agents.thread_budget":10001,"events.refresh_interval":600}}`, tag, "application/json")
	prefsMustErr(t, r, b, 422, "prefs_invalid_value")
	after, afterTag := prefsGET(t, e)
	if afterTag != tag || after.Revision != 2 {
		t.Fatal("invalid multi-key write partly committed")
	}
	// Missing preferences storage is a sanitized 503, while auth remains usable.
	if _, err := e.Hub.DB.Exec(`DROP TABLE hub_preferences`); err != nil {
		t.Fatal(err)
	}
	r, b = e.Do(e.OperatorSecret, "GET", "/v1/admin/prefs", nil)
	prefsMustErr(t, r, b, 503, "prefs_storage_unavailable")
}
func TestPreferencesHTTPStreamingLimitAndDuplicateHeaders(t *testing.T) {
	e := testhub.Start(t)
	_, tag := prefsGET(t, e)
	for _, method := range []string{"PATCH", "POST"} {
		path := "/v1/admin/prefs"
		if method == "POST" {
			path += "/preview"
		}
		req, _ := http.NewRequest(method, e.URL+path, strings.NewReader(strings.Repeat(" ", 16385)))
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer "+e.OperatorSecret)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", tag)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		prefsMustErr(t, resp, b, 413, "payload_too_large")
		req, _ = http.NewRequest(method, e.URL+path, strings.NewReader(`{"set":{"agents.thread_budget":600}}`))
		req.Header.Set("Authorization", "Bearer "+e.OperatorSecret)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Add("If-Match", tag)
		req.Header.Add("If-Match", tag)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		prefsMustErr(t, resp, b, 400, "invalid_body")
		for _, body := range []string{`{"reset":[` + strings.Repeat(`"agents.thread_budget",`, 64) + `"agents.thread_budget"]}`, `{"set":{"agents.thread_budget":600},"peer_id":"request-canary"}`, `{"set":{"agents.thread_budget":600},"scope":"operator"}`, `{"set":{"agents.thread_budget":600},"apply_existing":null}`, `{"set":{"agents.thread_budget":600},"reset":[null]}`, `{"set":{"agents.thread_budget":600},"confirm":[null]}`, `{"set":{"agents.thread_budget":600,"agents.\u0074hread_budget":700}}`} {
			resp, b := prefsHTTP(t, e, e.OperatorSecret, method, path, body, tag, "application/json")
			prefsMustErr(t, resp, b, 400, "invalid_body")
		}
	}
}
func TestPreferencesHTTPExpiredConfirmationAndNoAuthority(t *testing.T) {
	e := testhub.Start(t)
	_, tag := prefsGET(t, e)
	r, b := prefsHTTP(t, e, e.OperatorSecret, "POST", "/v1/admin/prefs/preview", `{"set":{"presence.default_ttl":1800}}`, tag, "application/json")
	if r.StatusCode != 200 {
		t.Fatal("preview failed")
	}
	var p prefs.Preview
	if json.Unmarshal(b, &p) != nil || p.ConfirmToken == "" {
		t.Fatal("preview decode failed")
	}
	body, err := json.Marshal(map[string]any{"set": map[string]int{"presence.default_ttl": 1800}, "confirm": []string{p.ConfirmToken}})
	if err != nil {
		t.Fatal("cannot encode synthetic confirmation")
	}
	_, secret := e.NewPeer("session", "unprivileged")
	r, b = prefsHTTP(t, e, secret, "PATCH", "/v1/admin/prefs", string(body), tag, "application/json")
	prefsMustErr(t, r, b, 403, "forbidden")
	e.Now.Advance(5 * time.Minute)
	r, b = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", string(body), tag, "application/json")
	prefsMustErr(t, r, b, 409, "prefs_confirmation_stale")
	s, latest := prefsGET(t, e)
	if s.Revision != 0 || latest != tag {
		t.Fatal("expired confirmation wrote preferences")
	}
}

func TestPreferencesHTTPStoredChangeMetadata(t *testing.T) {
	for _, source := range []string{"default", "flag"} {
		t.Run(source, func(t *testing.T) {
			e := testhub.StartWith(t, func(c *hub.Config) {
				if source == "flag" {
					c.PreferenceBootstrap = map[string]prefs.Effective{"events.refresh_interval": {Value: prefs.Duration(1800), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL"}}
				}
			})
			assertRow := func(raw []byte, value int, source, origin, updatedAt string) {
				t.Helper()
				var wire struct {
					Preferences []json.RawMessage `json:"preferences"`
				}
				if err := json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				const metadata = `{"key":"events.refresh_interval","type":"duration","default":1800,"min":60,"max":86400,"unit":"seconds","label":"Events refresh interval","description":"Refresh cadence for MCP Events subscriptions.","group":"notifications_presence","settable_by":"operator","editable":true,"runtime":"live","danger":[]}`
				var want map[string]any
				_ = json.Unmarshal([]byte(metadata), &want)
				want["value"], want["source"], want["origin"] = float64(value), source, origin
				if updatedAt != "" {
					want["updated_at"], want["updated_by"] = updatedAt, "operator"
				}
				for _, row := range wire.Preferences {
					var got map[string]any
					if err := json.Unmarshal(row, &got); err != nil {
						t.Fatal(err)
					}
					if got["key"] == "events.refresh_interval" {
						if !reflect.DeepEqual(got, want) {
							t.Fatal("stored change metadata fixture differs")
						}
						// Shared snapshot decoding must retain the wire metadata too.
						var snapshot prefs.Snapshot
						if err := json.Unmarshal(raw, &snapshot); err != nil {
							t.Fatal(err)
						}
						roundtrip, err := json.Marshal(snapshot)
						if err != nil {
							t.Fatal(err)
						}
						var decoded any
						_ = json.Unmarshal(roundtrip, &decoded)
						var original any
						_ = json.Unmarshal(raw, &original)
						if !reflect.DeepEqual(decoded, original) {
							t.Fatal("shared snapshot lost change metadata")
						}
						return
					}
				}
				t.Fatal("missing refresh descriptor")
			}
			_, tag := prefsGET(t, e)
			changedAt := envelope.FormatTime(e.Now.Now())
			r, raw := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"events.refresh_interval":600}}`, tag, "application/json")
			if r.StatusCode != 200 {
				t.Fatal("metadata save failed")
			}
			assertRow(raw, 600, "stored", "", changedAt)
			e.Now.Advance(time.Minute)
			// A write to another key preserves the original row's last-change metadata.
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"agents.thread_budget":600}}`, r.Header.Get("ETag"), "application/json")
			if r.StatusCode != 200 {
				t.Fatal("unrelated save failed")
			}
			assertRow(raw, 600, "stored", "", changedAt)
			again, err := hub.New(e.Hub.DB, e.Hub.Cfg, e.Now.Now)
			if err != nil {
				t.Fatal(err)
			}
			e.Configure(func(d *httpapi.Deps) { d.Hub = again })
			r, raw = e.Do(e.OperatorSecret, "GET", "/v1/admin/prefs", nil)
			if r.StatusCode != 200 {
				t.Fatal("restart GET failed")
			}
			assertRow(raw, 600, "stored", "", changedAt)
			// Re-saving the same value preserves both the ETag and change time.
			tag = r.Header.Get("ETag")
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"events.refresh_interval":600}}`, tag, "application/json")
			if r.StatusCode != 200 || r.Header.Get("ETag") != tag {
				t.Fatal("no-op changed revision")
			}
			assertRow(raw, 600, "stored", "", changedAt)
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"reset":["events.refresh_interval"]}`, tag, "application/json")
			if r.StatusCode != 200 {
				t.Fatal("metadata reset failed")
			}
			origin := ""
			if source == "flag" {
				origin = "VECNET_EVENTS_REFRESH_INTERVAL"
			}
			assertRow(raw, 1800, source, origin, "")
			r, raw = e.Do(e.OperatorSecret, "GET", "/v1/admin/prefs", nil)
			if r.StatusCode != 200 {
				t.Fatal("reset GET failed")
			}
			assertRow(raw, 1800, source, origin, "")
		})
	}
}
