package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Only a newly constructed hub receives the changed startup configuration.
func reconstructPreferencesRuntime(t *testing.T, e *testhub.Env, cfg hub.Config) {
	t.Helper()
	h, err := hub.New(e.Hub.DB, cfg, e.Now.Now)
	if err != nil {
		t.Fatal(err)
	}
	e.Hub = h
	e.Configure(func(d *httpapi.Deps) { d.Hub = h })
}

func preferencesRuntimeBody(t *testing.T, raw []byte, enabled bool) prefs.Snapshot {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	var runtime map[string]any
	if err := json.Unmarshal(wire["runtime"], &runtime); err != nil {
		t.Errorf("runtime must be explicitly present as an object: %v", err)
	} else if value, ok := runtime["event_retention_enabled"].(bool); !ok || value != enabled || len(runtime) != 1 {
		t.Errorf("event_retention_enabled must be explicit boolean %t; got %v", enabled, runtime)
	}
	var snapshot opapi.Preferences
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("existing operator client cannot decode preferences: %v", err)
	}
	return snapshot
}

func preferencesNoRuntime(t *testing.T, raw []byte) {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["runtime"]; ok {
		t.Error("preview or error unexpectedly contains runtime")
	}
}

func TestPreferencesHTTPEventRetentionRuntimeProjection(t *testing.T) {
	for _, backend := range []string{"disk", "r2"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", backend, enabled), func(t *testing.T) {
				bootstrap := map[string]prefs.Effective{"retention.ack_events": {Value: prefs.Duration(14 * 86400), Source: prefs.SourceFlag, Origin: "--retention-ack-events"}}
				var e *testhub.Env
				var f *r2HTTPFixture
				if backend == "r2" {
					f = newR2HTTP(t)
					e = f.env
					cfg := e.Hub.Cfg
					cfg.EventRetentionEnabled, cfg.PreferenceBootstrap = enabled, bootstrap
					reconstructPreferencesRuntime(t, e, cfg)
					if _, err := e.Hub.DB.Exec(`UPDATE blob_r2_storage SET retained_bytes=9,storage_reserved_bytes=4 WHERE singleton=1`); err != nil {
						t.Fatal(err)
					}
				} else {
					e = testhub.StartWith(t, func(c *hub.Config) {
						c.EventRetentionEnabled, c.PreferenceBootstrap = enabled, bootstrap
					})
				}
				for _, method := range []string{"GET", "PATCH"} {
					_, tag := prefsGET(t, e)
					r, raw := prefsHTTP(t, e, e.OperatorSecret, method, "/v1/admin/prefs", `{"set":{"agents.thread_budget":600}}`, tag, "application/json")
					if r.StatusCode != 200 {
						t.Fatalf("%s status = %d", method, r.StatusCode)
					}
					s := preferencesRuntimeBody(t, raw, enabled)
					current := e.Hub.Preferences.Current()
					// Compare the public JSON; decoding normalizes scalar values.
					var wire map[string]json.RawMessage
					_ = json.Unmarshal(raw, &wire)
					wantRows, err := json.Marshal(current.Descriptors())
					if err != nil {
						t.Fatal(err)
					}
					var gotRows, expectedRows any
					_ = json.Unmarshal(wire["preferences"], &gotRows)
					_ = json.Unmarshal(wantRows, &expectedRows)
					if s.Revision != current.Revision || !reflect.DeepEqual(gotRows, expectedRows) || r.Header.Get("ETag") != current.ETag || r.Header.Get("Cache-Control") != "no-store" {
						t.Fatal("projection changed snapshot or conditional headers")
					}
					ack, err := s.Get("retention.ack_events")
					if err != nil || ack.Value != prefs.Duration(14*86400) || ack.Source != prefs.SourceFlag || ack.Origin != "--retention-ack-events" {
						t.Fatal("projection lost retention policy or provenance")
					}
					for _, d := range s.Descriptors() {
						if d.Key == "retention.ack_events" && (!d.Editable || d.Runtime != "live" || d.UnavailableReason != "") {
							t.Fatal("startup gate changed retention editability")
						}
					}
					if method == "PATCH" {
						budget, _ := s.Get("agents.thread_budget")
						if s.Revision != 1 || budget.Value != prefs.Int(600) || budget.Source != prefs.SourceStored {
							t.Fatal("PATCH did not preserve saved preference semantics")
						}
					}
					storage, present := wire["storage"]
					if present != (backend == "r2") {
						t.Fatal("optional storage projection changed")
					}
					if present {
						var got map[string]int64
						want := map[string]int64{"retained_bytes": 9, "reserved_bytes": 4, "used_bytes": 13, "limit_bytes": 100}
						if json.Unmarshal(storage, &got) != nil || !reflect.DeepEqual(got, want) {
							t.Fatal("storage accounting changed")
						}
					}
				}
				if f != nil {
					if r2ProviderCalls(f) != 0 {
						t.Fatal("preferences contacted synthetic R2 provider")
					}
					if _, err := e.Hub.DB.Exec(`DROP TABLE blob_r2_storage`); err != nil {
						t.Fatal(err)
					}
					for _, method := range []string{"GET", "PATCH"} {
						tag := e.Hub.Preferences.Current().ETag
						r, raw := prefsHTTP(t, e, e.OperatorSecret, method, "/v1/admin/prefs", `{"set":{"agents.thread_budget":600}}`, tag, "application/json")
						prefsMustErr(t, r, raw, 503, "blob_unavailable")
						preferencesNoRuntime(t, raw)
					}
				}
			})
		}
	}
}

func TestPreferencesHTTPEventRetentionRuntimeRestartValidator(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("%t_to_%t", initial, !initial), func(t *testing.T) {
			e := testhub.StartWith(t, func(c *hub.Config) { c.EventRetentionEnabled = initial })
			_, tag := prefsGET(t, e)
			r, raw := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"retention.ack_events":1209600}}`, tag, "application/json")
			if r.StatusCode != 200 {
				t.Fatalf("stored retention override status = %d", r.StatusCode)
			}
			before, oldTag := prefsGET(t, e)
			proposal := `{"set":{"presence.default_ttl":1800}}`
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "POST", "/v1/admin/prefs/preview", proposal, oldTag, "application/json")
			var oldPreview prefs.Preview
			if r.StatusCode != 200 || json.Unmarshal(raw, &oldPreview) != nil || oldPreview.ConfirmToken == "" {
				t.Fatal("initial confirmation fixture failed")
			}
			// Clone persisted rows before reconstructing with a copied startup bool.
			e = e.CloneDB(t)
			_, sameTag := prefsGET(t, e)
			if sameTag != oldTag {
				t.Error("same-config reconstruction changed shared ETag")
			}
			cfg := e.Hub.Cfg
			cfg.EventRetentionEnabled = !initial
			reconstructPreferencesRuntime(t, e, cfg)
			after, newTag := prefsGET(t, e)
			ack, _ := after.Get("retention.ack_events")
			if after.Revision != before.Revision || after.Revision != 1 || ack.Value != prefs.Duration(1209600) || ack.Source != prefs.SourceStored || !reflect.DeepEqual(before.Descriptors(), after.Descriptors()) {
				t.Fatal("gate-only reconstruction changed saved policy, source or revision")
			}
			if newTag == oldTag || newTag != e.Hub.Preferences.Current().ETag {
				t.Error("gate-only reconstruction must change the shared preferences ETag")
			}
			for i, condition := range []string{oldTag, "W/" + oldTag, `"other", ` + oldTag, newTag} {
				req, err := http.NewRequest("GET", e.URL+"/v1/admin/prefs", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+e.OperatorSecret)
				req.Header.Set("If-None-Match", condition)
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				want := 200
				if i == 3 {
					want = 304
				}
				if response.StatusCode != want || response.Header.Get("ETag") != newTag || response.Header.Get("Cache-Control") != "no-store" {
					t.Errorf("conditional %s: status=%d, want %d/current shared tag", condition, response.StatusCode, want)
				}
				if want == 200 && response.StatusCode == 200 {
					preferencesRuntimeBody(t, body, !initial)
				} else if response.StatusCode == 304 && len(body) != 0 {
					t.Error("304 returned a body")
				}
			}
			confirmed := func(token string) string {
				b, err := json.Marshal(prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}, Confirm: []string{token}})
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", confirmed(oldPreview.ConfirmToken), oldTag, "application/json")
			prefsMustErr(t, r, raw, 412, prefs.CodeRevisionConflict)
			preferencesNoRuntime(t, raw)
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", proposal, newTag, "application/json")
			prefsMustErr(t, r, raw, 409, prefs.CodeConfirmationRequired)
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", confirmed(oldPreview.ConfirmToken), newTag, "application/json")
			prefsMustErr(t, r, raw, 409, prefs.CodeConfirmationStale)
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "POST", "/v1/admin/prefs/preview", proposal, newTag, "application/json")
			var fresh prefs.Preview
			if r.StatusCode != 200 || json.Unmarshal(raw, &fresh) != nil || fresh.ConfirmToken == "" {
				t.Fatal("fresh preview failed after restart conflict")
			}
			preferencesNoRuntime(t, raw)
			r, raw = prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", confirmed(fresh.ConfirmToken), newTag, "application/json")
			if r.StatusCode != 200 {
				t.Fatalf("fresh confirmation save status = %d", r.StatusCode)
			}
			preferencesRuntimeBody(t, raw, !initial)
		})
	}
}

func TestPreferencesHTTPEventRetentionRuntimeReadOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			e := testhub.StartWith(t, func(c *hub.Config) { c.EventRetentionEnabled = enabled })
			before, tag := prefsGET(t, e)
			for _, method := range []string{"PATCH", "POST"} {
				path := "/v1/admin/prefs"
				if method == "POST" {
					path += "/preview"
				}
				for _, tc := range []struct {
					body, code string
					status     int
				}{
					{`{"set":{"agents.thread_budget":600},"runtime":{"event_retention_enabled":true}}`, prefs.CodeInvalidBody, 400},
					{`{"set":{"runtime.event_retention_enabled":true}}`, prefs.CodeUnknownKey, 422},
					{`{"reset":["runtime.event_retention_enabled"]}`, prefs.CodeUnknownKey, 422},
				} {
					r, raw := prefsHTTP(t, e, e.OperatorSecret, method, path, tc.body, tag, "application/json")
					prefsMustErr(t, r, raw, tc.status, tc.code)
					preferencesNoRuntime(t, raw)
				}
			}
			peer, secret := e.NewPeer("session", "runtime authority")
			for _, route := range []struct{ method, path string }{{"GET", "/v1/admin/prefs"}, {"PATCH", "/v1/admin/prefs"}, {"POST", "/v1/admin/prefs/preview"}} {
				for _, auth := range []struct {
					secret, suffix, code string
					status               int
				}{
					{"", "", "unauthenticated", 401},
					{secret, "", "forbidden", 403},
					{e.OperatorSecret, "?as=" + peer, "forbidden", 403},
				} {
					r, raw := prefsHTTP(t, e, auth.secret, route.method, route.path+auth.suffix, `{"set":{"presence.default_ttl":1800}}`, tag, "application/json")
					prefsMustErr(t, r, raw, auth.status, auth.code)
					preferencesNoRuntime(t, raw)
				}
			}
			r, raw := prefsHTTP(t, e, e.OperatorSecret, "PATCH", "/v1/admin/prefs", `{"set":{"presence.default_ttl":1800}}`, tag, "application/json")
			prefsMustErr(t, r, raw, 409, prefs.CodeConfirmationRequired)
			preferencesNoRuntime(t, raw)
			after, afterTag := prefsGET(t, e)
			if e.Hub.Cfg.EventRetentionEnabled != enabled || afterTag != tag || after.Revision != before.Revision || !reflect.DeepEqual(before.Descriptors(), after.Descriptors()) {
				t.Fatal("rejected runtime writes or authority checks changed config/preferences")
			}
			if _, err := prefs.Lookup("runtime.event_retention_enabled"); err == nil {
				t.Fatal("runtime report became a preference key")
			}
		})
	}
}
