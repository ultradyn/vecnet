package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Exercise hub.New and the Settings API, including its first stored-override
// load. Do not install keys or replace the policy from the test.
func TestRetentionBootstrapProductionPreferences(t *testing.T) {
	for _, backend := range []string{"disk", "r2"} {
		t.Run(backend, func(t *testing.T) {
			var e *testhub.Env
			var f *r2HTTPFixture
			if backend == "r2" {
				f = newR2HTTP(t)
				e = f.env
			} else {
				e = testhub.Start(t)
			}
			e.Hub.Cfg.PreferenceBootstrap = map[string]prefs.Effective{
				"retention.ack_events":   {Value: prefs.Duration(14 * 86400), Source: prefs.SourceFlag, Origin: "--retention-ack-events"},
				"retention.history":      {Value: prefs.Duration(3 * 86400), Source: prefs.SourceFlag, Origin: "VECNET_RETENTION_HISTORY"},
				"retention.archive_mode": {Value: prefs.Bool(true), Source: prefs.SourceFlag, Origin: "--retention-archive-mode"},
			}
			if f != nil {
				e.Hub.Cfg.PreferenceBootstrap["blobs.expiry"] = prefs.Effective{Value: prefs.Enum("6h"), Source: prefs.SourceFlag, Origin: "--blobs-expiry"}
			}
			e = e.CloneDB(t)
			check := func(s prefs.Snapshot, source prefs.Source, values map[string]prefs.Value) {
				t.Helper()
				for key, want := range values {
					got, err := s.Get(key)
					if err != nil || got.Value != want || got.Source != source {
						t.Fatalf("%s lost value or provenance", key)
					}
					found := false
					for _, d := range s.Descriptors() {
						if d.Key == key {
							found = true
							if !d.Editable || d.Runtime != "live" || d.UnavailableReason != "" {
								t.Fatalf("%s unavailable in production Settings", key)
							}
						}
					}
					if !found {
						t.Fatalf("missing descriptor %s", key)
					}
				}
			}
			bootstrap := map[string]prefs.Value{}
			for key, value := range e.Hub.Cfg.PreferenceBootstrap {
				bootstrap[key] = value.Value
			}
			s, tag := prefsGET(t, e)
			check(s, prefs.SourceFlag, bootstrap)
			values := map[string]prefs.Value{
				"retention.ack_events":   prefs.Duration(7 * 86400),
				"retention.history":      prefs.Duration(2 * 86400),
				"retention.archive_mode": prefs.Bool(false),
			}
			if f != nil {
				values["blobs.expiry"] = prefs.Enum("off")
			}
			patch := prefs.Patch{Set: values}
			raw, err := json.Marshal(patch)
			if err != nil {
				t.Fatal(err)
			}
			r, body := prefsHTTP(t, e, e.OperatorSecret, http.MethodPost, "/v1/admin/prefs/preview", string(raw), tag, "application/json")
			var preview prefs.Preview
			if r.StatusCode != 200 || json.Unmarshal(body, &preview) != nil || preview.ConfirmToken == "" {
				t.Fatalf("combined policy preview failed: status=%d", r.StatusCode)
			}
			if f != nil && (preview.Impact.Storage == nil || !preview.Impact.Storage.Valid()) {
				t.Fatal("R2 storage estimate lost when retention policy was installed")
			}
			patch.Confirm = []string{preview.ConfirmToken}
			raw, err = json.Marshal(patch)
			if err != nil {
				t.Fatal(err)
			}
			r, body = prefsHTTP(t, e, e.OperatorSecret, http.MethodPatch, "/v1/admin/prefs", string(raw), tag, "application/json")
			if r.StatusCode != 200 || json.Unmarshal(body, &s) != nil {
				t.Fatalf("combined policy activation failed: status=%d", r.StatusCode)
			}
			check(s, prefs.SourceStored, values)
			e = e.CloneDB(t)
			s, _ = prefsGET(t, e)
			check(s, prefs.SourceStored, values)
			if f != nil && r2ProviderCalls(f) != 0 {
				t.Fatal("Settings bootstrap or policy activation contacted R2")
			}
		})
	}
}
