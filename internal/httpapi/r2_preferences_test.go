package httpapi_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// These tests use hub.New through testhub and the real preferences HTTP API.
// All object I/O is confined to newR2HTTP's in-process TLS fake.
func r2ExpiryValue(t *testing.T, s prefs.Snapshot, value string, source prefs.Source, origin string, available bool) {
	t.Helper()
	e, err := s.Get("blobs.expiry")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.Value.Enum()
	if got != value || e.Source != source || e.Origin != origin {
		t.Fatal("expiry value or provenance differs")
	}
	for _, d := range s.Descriptors() {
		if d.Key == "blobs.expiry" {
			if available && (!d.Editable || d.Runtime != "live" || d.UnavailableReason != "") {
				t.Fatal("installed expiry consumer is unavailable")
			}
			if !available && (d.Editable || d.Runtime != "unavailable" || d.UnavailableReason == "") {
				t.Fatal("missing expiry consumer became available")
			}
		}
	}
}

func r2PatchExpiry(t *testing.T, e *testhub.Env, tag string, patch prefs.Patch) (prefs.Snapshot, string) {
	t.Helper()
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal("patch encoding failed")
	}
	r, b := prefsHTTP(t, e, e.OperatorSecret, http.MethodPatch, "/v1/admin/prefs", string(raw), tag, "application/json")
	var s prefs.Snapshot
	if r.StatusCode != 200 || json.Unmarshal(b, &s) != nil || r.Header.Get("ETag") == "" {
		t.Fatalf("expiry save failed: status=%d", r.StatusCode)
	}
	return s, r.Header.Get("ETag")
}

func r2PreviewOff(t *testing.T, e *testhub.Env, tag string) prefs.Preview {
	t.Helper()
	r, b := prefsHTTP(t, e, e.OperatorSecret, http.MethodPost, "/v1/admin/prefs/preview", `{"set":{"blobs.expiry":"off"}}`, tag, "application/json")
	var preview prefs.Preview
	if r.StatusCode != 200 || json.Unmarshal(b, &preview) != nil || preview.ConfirmToken == "" || len(preview.Risks) != 1 || preview.Risks[0].Code != "blob_expiry_off" || preview.Impact.Storage == nil || !preview.Impact.Storage.Valid() {
		t.Fatalf("expiry cost preview failed: status=%d", r.StatusCode)
	}
	return preview
}

func r2ProviderCalls(f *r2HTTPFixture) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, count := range f.calls {
		n += count
	}
	return n
}

type r2GrantRetention struct {
	key, class, value, source string
	revision                  uint64
	expires                   sql.NullString
}

func r2GrantSnapshot(t *testing.T, e *testhub.Env, g blobwire.UploadGrant) r2GrantRetention {
	t.Helper()
	var out r2GrantRetention
	err := e.Hub.DB.QueryRow(`SELECT object_key,retention_class,expiry_pref_value,expiry_pref_source,expiry_pref_revision,expires_at FROM blob_objects WHERE blob_id=?`, g.BlobID).Scan(&out.key, &out.class, &out.value, &out.source, &out.revision, &out.expires)
	if err != nil {
		t.Fatal("grant snapshot read failed")
	}
	return out
}

func r2ReserveWithExpiry(t *testing.T, f *r2HTTPFixture, key, value string, source prefs.Source, revision uint64, duration time.Duration) blobwire.UploadGrant {
	t.Helper()
	r, raw := f.request(t, f.env.OperatorSecret, http.MethodPost, "/v1/blobs/uploads", blobwire.UploadRequest{Size: 3, SHA256: shaOf("abc"), MediaType: "text/plain"}, key)
	var g blobwire.UploadGrant
	if r.StatusCode != 201 || json.Unmarshal(raw, &g) != nil {
		t.Fatalf("expiry reservation failed: status=%d", r.StatusCode)
	}
	o := r2GrantSnapshot(t, f.env, g)
	class, prefix := value, "/exp/"+value+"/"
	if value == "off" {
		class, prefix = "keep", "/keep/"
	}
	if g.RetentionClass != class || o.class != class || o.value != value || o.source != string(source) || o.revision != revision || !strings.Contains(o.key, prefix) {
		t.Fatal("new grant lost expiry policy or provenance")
	}
	if value == "off" {
		if g.ExpiresAt != nil || o.expires.Valid {
			t.Fatal("keep grant has a logical deadline")
		}
	} else {
		if g.ExpiresAt == nil || !o.expires.Valid {
			t.Fatal("finite grant has no logical deadline")
		}
		deadline, err := time.Parse(time.RFC3339Nano, *g.ExpiresAt)
		storedDeadline, storedErr := time.Parse(time.RFC3339Nano, o.expires.String)
		// SQLite stores RFC3339Nano (trailing zeros trimmed), while the wire
		// uses fixed milliseconds. Compare instants, not their spellings.
		if err != nil || storedErr != nil || !deadline.Equal(f.env.Now.Now().Add(duration)) || !storedDeadline.Equal(deadline) {
			t.Fatal("grant deadline did not snapshot the selected duration")
		}
	}
	return g
}

func TestR2PreferencesHTTPProductionAvailabilityConfirmationRestartAndImmutableGrants(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	s, tag := prefsGET(t, e)
	r2ExpiryValue(t, s, "1d", prefs.SourceDefault, "", true)
	if s.Revision != 0 || e.Hub.Preferences.Policy == nil || e.Hub.Blobs.Transfers == nil || r2ProviderCalls(f) != 0 {
		t.Fatal("constructor did not install the local R2 consumer and policy")
	}
	complete := f.upload(t, e.OperatorSecret, "default-completed")
	grants := []blobwire.UploadGrant{complete, r2ReserveWithExpiry(t, f, "default-pending", "1d", prefs.SourceDefault, 0, 24*time.Hour)}
	original := map[string]r2GrantRetention{}
	for _, g := range grants {
		original[g.BlobID] = r2GrantSnapshot(t, e, g)
	}
	for _, tc := range []struct {
		value    string
		duration time.Duration
	}{{"1h", time.Hour}, {"6h", 6 * time.Hour}, {"12h", 12 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}} {
		s, tag = r2PatchExpiry(t, e, tag, prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum(tc.value)}})
		r2ExpiryValue(t, s, tc.value, prefs.SourceStored, "", true)
		g := r2ReserveWithExpiry(t, f, "duration-"+tc.value, tc.value, prefs.SourceStored, s.Revision, tc.duration)
		grants = append(grants, g)
		original[g.BlobID] = r2GrantSnapshot(t, e, g)
	}
	beforeTag, beforeRevision := tag, s.Revision
	beforeCalls := r2ProviderCalls(f)
	for _, body := range []string{`{"set":{"blobs.expiry":"off"}}`, `{"reset":["blobs.expiry"],"apply_existing":true}`} {
		r, b := prefsHTTP(t, e, e.OperatorSecret, http.MethodPatch, "/v1/admin/prefs", body, tag, "application/json")
		if strings.Contains(body, "apply_existing") {
			prefsMustErr(t, r, b, 422, prefs.CodeInvalidValue)
		} else {
			prefsMustErr(t, r, b, 409, prefs.CodeConfirmationRequired)
		}
	}
	preview := r2PreviewOff(t, e, tag)
	wantUsage := prefs.StorageUsage{RetainedBytes: 3, ReservedBytes: 18, UsedBytes: 21, LimitBytes: 100}
	if preview.BaseRevision != beforeRevision || *preview.Impact.Storage != wantUsage || r2ProviderCalls(f) != beforeCalls {
		t.Fatal("cost preview omitted accounting or contacted provider")
	}
	s, tag = prefsGET(t, e)
	if tag != beforeTag || s.Revision != beforeRevision {
		t.Fatal("preview or refused writes changed preferences")
	}
	// Approval is bound to this exact proposal, not a general off bypass.
	altered, _ := json.Marshal(prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("6h")}, Confirm: []string{preview.ConfirmToken}})
	r, b := prefsHTTP(t, e, e.OperatorSecret, http.MethodPatch, "/v1/admin/prefs", string(altered), tag, "application/json")
	prefsMustErr(t, r, b, 409, prefs.CodeConfirmationStale)
	s, tag = r2PatchExpiry(t, e, tag, prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("off")}, Confirm: []string{preview.ConfirmToken}})
	r2ExpiryValue(t, s, "off", prefs.SourceStored, "", true)
	keep := f.upload(t, e.OperatorSecret, "keep-completed")
	if keep.RetentionClass != "keep" || keep.ExpiresAt != nil {
		t.Fatal("confirmed off did not reach production transfers")
	}
	grants = append(grants, keep)
	original[keep.BlobID] = r2GrantSnapshot(t, e, keep)
	// CloneDB closes over a fresh production constructor and handler, with the
	// persisted off override loaded before any requests or transfer grants.
	f.env = e.CloneDB(t)
	e = f.env
	s, tag = prefsGET(t, e)
	r2ExpiryValue(t, s, "off", prefs.SourceStored, "", true)
	if s.Revision != beforeRevision+1 {
		t.Fatal("restart lost confirmed revision")
	}
	r2ReserveWithExpiry(t, f, "keep-after-restart", "off", prefs.SourceStored, s.Revision, 0)
	s, tag = r2PatchExpiry(t, e, tag, prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("1d")}})
	r2ExpiryValue(t, s, "1d", prefs.SourceDefault, "", true)
	r2ReserveWithExpiry(t, f, "duration-after-keep", "1d", prefs.SourceDefault, s.Revision, 24*time.Hour)
	for _, g := range grants {
		if r2GrantSnapshot(t, e, g) != original[g.BlobID] {
			t.Fatal("preference edits or restart rewrote an existing grant")
		}
	}
	// Idempotent pending grants preserve the original policy after all edits.
	replay := r2ReserveWithExpiry(t, f, "default-pending", "1d", prefs.SourceDefault, 0, 24*time.Hour)
	if replay.BlobID != grants[1].BlobID {
		t.Fatal("grant replay created a new object")
	}
	f.download(t, e.OperatorSecret, complete)
	f.download(t, e.OperatorSecret, keep)
	var actor, detail string
	if err := e.Hub.DB.QueryRow(`SELECT actor,detail FROM audit WHERE action='prefs_changed' AND detail LIKE '%blob_expiry_off%'`).Scan(&actor, &detail); err != nil || actor != "operator" || !strings.Contains(detail, `"approved_risks":["blob_expiry_off"]`) || strings.Contains(detail, preview.ConfirmToken) {
		t.Fatal("confirmed expiry change lacks sanitized operator audit")
	}
}

func TestR2PreferencesHTTPRollbackRetainsConsumerAndRemoteReads(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	g := f.upload(t, e.OperatorSecret, "retained")
	_, tag := prefsGET(t, e)
	r2PatchExpiry(t, e, tag, prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("6h")}})
	e.Hub.Cfg.BlobBackend = "disk"
	before := r2ProviderCalls(f)
	f.env = e.CloneDB(t)
	e = f.env
	s, tag := prefsGET(t, e)
	r2ExpiryValue(t, s, "6h", prefs.SourceStored, "", true)
	if e.Hub.Blobs.Transfers == nil || e.Hub.Preferences.Policy == nil || r2ProviderCalls(f) != before {
		t.Fatal("disk rollback lost retained R2 support or contacted provider")
	}
	preview := r2PreviewOff(t, e, tag)
	if r2ProviderCalls(f) != before {
		t.Fatal("rollback preview contacted provider")
	}
	s, _ = r2PatchExpiry(t, e, tag, prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("off")}, Confirm: []string{preview.ConfirmToken}})
	r2ExpiryValue(t, s, "off", prefs.SourceStored, "", true)
	// Preference edits do not switch disk uploads back to remote transfers.
	local := putOK(t, e, e.OperatorSecret, "text/plain", "disk after rollback")
	if local.SHA256 == "" || e.Hub.Cfg.BlobBackend != "disk" {
		t.Fatal("expiry edit changed rollback upload mode")
	}
	f.download(t, e.OperatorSecret, g)
	// Dropping the adapter while remote objects remain still fails startup.
	cfg := e.Hub.Cfg
	cfg.R2 = hub.R2Config{}
	if again, err := hub.New(e.Hub.DB, cfg, e.Now.Now); err == nil || again != nil {
		t.Fatal("retained remote objects started without their consumer")
	}
}

func TestR2PreferencesHTTPMissingConsumerAndInvalidConfigFailClosed(t *testing.T) {
	disk := testhub.Start(t)
	s, tag := prefsGET(t, disk)
	r2ExpiryValue(t, s, "1d", prefs.SourceDefault, "", false)
	for _, method := range []string{http.MethodPatch, http.MethodPost} {
		path := "/v1/admin/prefs"
		if method == http.MethodPost {
			path += "/preview"
		}
		r, b := prefsHTTP(t, disk, disk.OperatorSecret, method, path, `{"set":{"blobs.expiry":"off"}}`, tag, "application/json")
		prefsMustErr(t, r, b, 409, prefs.CodeUnavailable)
	}
	f := newR2HTTP(t)
	for _, backend := range []string{"r2", "disk"} {
		for _, mutate := range []func(*hub.Config){
			func(c *hub.Config) { c.R2.StorageQuotaBytes = 0 },
			func(c *hub.Config) { c.R2.StorageQuotaBytes = -1 },
			func(c *hub.Config) { c.R2.ChecksumProfile = "md5" },
			func(c *hub.Config) { c.R2.ChecksumProfile = "" },
		} {
			cfg := f.env.Hub.Cfg
			cfg.BlobBackend = backend
			mutate(&cfg)
			if h, err := hub.New(f.env.Hub.DB, cfg, f.env.Now.Now); err == nil || h != nil {
				t.Fatal("unverified retention adapter started")
			}
		}
	}
	if r2ProviderCalls(f) != 0 {
		t.Fatal("configuration checks contacted provider")
	}
}

func TestR2PreferencesHTTPQuotaChangeInvalidatesOffConfirmation(t *testing.T) {
	f := newR2HTTP(t)
	e := f.env
	_, tag := prefsGET(t, e)
	preview := r2PreviewOff(t, e, tag)
	e.Hub.Cfg.R2.StorageQuotaBytes = 50
	f.env = e.CloneDB(t)
	e = f.env
	_, nextTag := prefsGET(t, e)
	if nextTag == tag {
		t.Fatal("quota change did not invalidate preference generation")
	}
	body, _ := json.Marshal(prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("off")}, Confirm: []string{preview.ConfirmToken}})
	r, b := prefsHTTP(t, e, e.OperatorSecret, http.MethodPatch, "/v1/admin/prefs", string(body), tag, "application/json")
	prefsMustErr(t, r, b, 412, prefs.CodeRevisionConflict)
	r, b = prefsHTTP(t, e, e.OperatorSecret, http.MethodPatch, "/v1/admin/prefs", string(body), nextTag, "application/json")
	prefsMustErr(t, r, b, 409, prefs.CodeConfirmationStale)
	next := r2PreviewOff(t, e, nextTag)
	if next.Impact.Storage.LimitBytes != 50 || reflect.DeepEqual(next.Impact.Storage, preview.Impact.Storage) || r2ProviderCalls(f) != 0 {
		t.Fatal("restart preview lost the new finite quota or contacted provider")
	}
}

func TestR2PreferencesHTTPBootstrapProvenanceOverridesAndReset(t *testing.T) {
	for _, origin := range []string{"--blobs-expiry", "VECNET_BLOBS_EXPIRY"} {
		t.Run(origin, func(t *testing.T) {
			f := newR2HTTP(t)
			e := f.env
			// Force .010: the DB renders .01Z and HTTP renders .010Z. This
			// deterministically covers equivalent timestamps with different text.
			e.Now.Advance(time.Second + 10*time.Millisecond - time.Duration(e.Now.Now().Nanosecond()))
			// Supply bootstrap to a fresh real constructor; do not edit the
			// preference store, installed-key gate or policy hooks in the test.
			e.Hub.Cfg.PreferenceBootstrap = map[string]prefs.Effective{"blobs.expiry": {Value: prefs.Enum("6h"), Source: prefs.SourceFlag, Origin: origin}}
			f.env = e.CloneDB(t)
			e = f.env
			s, tag := prefsGET(t, e)
			r2ExpiryValue(t, s, "6h", prefs.SourceFlag, origin, true)
			g := r2ReserveWithExpiry(t, f, "bootstrap", "6h", prefs.SourceFlag, 0, 6*time.Hour)
			original := r2GrantSnapshot(t, e, g)
			s, _ = r2PatchExpiry(t, e, tag, prefs.Patch{Set: map[string]prefs.Value{"blobs.expiry": prefs.Enum("12h")}})
			r2ExpiryValue(t, s, "12h", prefs.SourceStored, "", true)
			f.env = e.CloneDB(t)
			e = f.env
			s, tag = prefsGET(t, e)
			r2ExpiryValue(t, s, "12h", prefs.SourceStored, "", true)
			if s.Revision != 1 {
				t.Fatal("startup did not load the stored override over bootstrap")
			}
			s, _ = r2PatchExpiry(t, e, tag, prefs.Patch{Reset: []string{"blobs.expiry"}})
			r2ExpiryValue(t, s, "6h", prefs.SourceFlag, origin, true)
			r2ReserveWithExpiry(t, f, "reset-bootstrap", "6h", prefs.SourceFlag, 2, 6*time.Hour)
			if r2GrantSnapshot(t, e, g) != original || r2ProviderCalls(f) != 0 {
				t.Fatal("bootstrap/reset rewrote an old grant or contacted provider")
			}
		})
	}
}
