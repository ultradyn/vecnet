package hub

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/sha256"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/prefs"
	"path/filepath"
)

// These tests exercise real SQLite transactions, the merged preference reader,
// and the production HEAD adapter against an in-process S3 fake only. They are
// not provider acceptance evidence.
type transferFixture struct {
	env        *transferTestEnv
	s          *blob.Transfers
	cfg        blob.TransfersConfig
	server     *httptest.Server
	mu         sync.Mutex
	objects    map[string]http.Header
	deleteDown bool
	headWait   func()
}

func newTransfers(t *testing.T) *transferFixture {
	t.Helper()
	f := &transferFixture{env: newTransferTestEnv(t), objects: map[string]http.Header{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		f.mu.Lock()
		h, ok := f.objects[key]
		h = h.Clone()
		down, wait := f.deleteDown, f.headWait
		if r.Method == http.MethodDelete && !down {
			delete(f.objects, key)
		}
		f.mu.Unlock()
		switch r.Method {
		case http.MethodHead:
			if wait != nil {
				wait()
			}
			if !ok {
				w.WriteHeader(404)
				return
			}
			for k, v := range h {
				w.Header()[k] = v
			}
			w.WriteHeader(200)
		case http.MethodDelete:
			if down {
				w.WriteHeader(503)
			} else {
				w.WriteHeader(204)
			}
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(f.server.Close)
	ns := "0123456789abcdef0123456789abcdef"
	objects, err := blob.NewR2Objects(blob.R2ObjectsConfig{Endpoint: f.server.URL, Bucket: "test-bucket", Namespace: ns, AccessID: "fake-access", Secret: "fake-secret", Client: f.server.Client(), Now: f.env.Now.Now, Lookup: func(ctx context.Context, key string) (blob.R2Object, error) { return f.s.Lookup(ctx, key) }})
	if err != nil {
		t.Fatal("fake metadata adapter initialization failed")
	}
	f.cfg = blob.TransfersConfig{DB: f.env.Hub.DB, Now: f.env.Now.Now, Namespace: ns, Preferences: f.env.Hub.Preferences, Objects: objects, MaxSize: 10, DailyQuota: 50, StorageQuota: 100, SignPUT: func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		digest, _ := hex.DecodeString(o.SHA256)
		h := http.Header{"Content-Length": {fmt.Sprint(o.Size)}, "Content-Type": {"application/octet-stream"}, "Content-Disposition": {"attachment"}, "Cache-Control": {"private, no-store"}, "If-None-Match": {"*"}, "X-Amz-Checksum-Sha256": {base64.StdEncoding.EncodeToString(digest)}, "X-Amz-Meta-Vecnet-Checksum-Profile": {"sha256"}, "X-Amz-Meta-Vecnet-Checksum": {base64.StdEncoding.EncodeToString(digest)}}
		u, err := blob.PresignPUT(f.server.URL, "test-bucket", o.ObjectKey, "fake-access", "fake-secret", o.IssuedAt, blob.R2UploadTTL, h)
		headers := map[string]string{}
		for k := range h {
			headers[k] = h.Get(k)
		}
		return blobwire.Request{Method: "PUT", URL: u, Headers: headers, ExpiresAt: o.UploadDeadline.Format(time.RFC3339Nano)}, err
	}}
	f.restart(t)
	return f
}
func (f *transferFixture) restart(t *testing.T) {
	t.Helper()
	var err error
	f.s, err = blob.NewTransfers(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.env.Hub.Blobs.Transfers = f.s
}
func (f *transferFixture) reserve(t *testing.T, key string, n int64) blobwire.UploadGrant {
	t.Helper()
	g, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, key, blobwire.UploadRequest{Size: n, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func (f *transferFixture) object(t *testing.T, id string) blob.R2Object {
	t.Helper()
	var key string
	if err := f.env.Hub.DB.QueryRow(`SELECT object_key FROM blob_objects WHERE blob_id=?`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	o, err := f.s.Lookup(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func (f *transferFixture) upload(t *testing.T, g blobwire.UploadGrant) {
	t.Helper()
	o := f.object(t, g.BlobID)
	h := http.Header{}
	for k, v := range g.Upload.Headers {
		h.Set(k, v)
	}
	h.Set("ETag", `"fake-etag"`)
	f.mu.Lock()
	f.objects[o.ObjectKey] = h
	f.mu.Unlock()
}
func (f *transferFixture) complete(t *testing.T, g blobwire.UploadGrant) {
	t.Helper()
	f.upload(t, g)
	r, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if r.SHA256Verified == nil || !*r.SHA256Verified || r.Integrity == nil || !r.Integrity.Verified {
		t.Fatal("completed reference lacks recorded verification")
	}
}
func (f *transferFixture) tick(t *testing.T) {
	t.Helper()
	if err := f.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// These are test-only bootstrap fixtures on the REAL merged preference
// service. No adapter is registered, no fake preference store is supplied,
// and production remains unavailable for blobs.expiry mutations.
func (f *transferFixture) pref(t *testing.T, value string) {
	t.Helper()
	if _, err := f.env.Hub.DB.Exec(`UPDATE hub_preferences_state SET revision=revision+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	f.env.Hub.Preferences.bootstrap["blobs.expiry"] = prefs.Effective{Value: prefs.Enum(value), Source: prefs.SourceFlag, Origin: "--blobs-expiry"}
}
func (f *transferFixture) number(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.env.Hub.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *transferFixture) charge(t *testing.T, reserved, retained int64) {
	t.Helper()
	if got := f.number(t, `SELECT storage_reserved_bytes FROM blob_r2_storage`); got != reserved {
		t.Fatalf("reserved=%d want %d", got, reserved)
	}
	if got := f.number(t, `SELECT retained_bytes FROM blob_r2_storage`); got != retained {
		t.Fatalf("retained=%d want %d", got, retained)
	}
}

type transferClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *transferClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *transferClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

type transferTestEnv struct {
	Hub               *Hub
	Now               *transferClock
	OperatorPeer      string
	OperatorPrincipal string
}

func newTransferTestEnv(t *testing.T) *transferTestEnv {
	t.Helper()
	dir := t.TempDir()
	db := openTransferFixtureStore(t, filepath.Join(dir, "hub.db"))
	clock := &transferClock{t: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)}
	cfg := DefaultConfig()
	cfg.DataDir = dir
	cfg.PublicURL = "http://127.0.0.1"
	h, err := New(db, cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, _, err := h.EnsureOperator(context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	var principal string
	if err := db.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, peer).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	return &transferTestEnv{Hub: h, Now: clock, OperatorPeer: peer, OperatorPrincipal: principal}
}
func hexSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func TestR2PreferenceSnapshotsAndClasses(t *testing.T) {
	for _, value := range []string{"1h", "6h", "12h", "1d", "7d", "30d", "off"} {
		t.Run(value, func(t *testing.T) {
			f := newTransfers(t)
			f.pref(t, value)
			g := f.reserve(t, "one", 3)
			o := f.object(t, g.BlobID)
			if o.ExpiryPrefValue != value || o.ExpiryPrefSource != "flag" || o.ExpiryPrefRevision != 1 {
				t.Fatal("snapshot provenance lost")
			}
			if value == "off" {
				if g.RetentionClass != "keep" || g.ExpiresAt != nil || !strings.Contains(o.ObjectKey, "/keep/") {
					t.Fatal("invalid keep snapshot")
				}
			} else {
				if g.RetentionClass != value || g.ExpiresAt == nil || !strings.Contains(o.ObjectKey, "/exp/"+value+"/") {
					t.Fatal("invalid expiring snapshot")
				}
			}
			next := "off"
			if value == "off" {
				next = "1d"
			}
			f.pref(t, next)
			again := f.reserve(t, "one", 3)
			newGrant := f.reserve(t, "two", 3)
			if again.RetentionClass != g.RetentionClass || again.Upload.ExpiresAt != g.Upload.ExpiresAt || f.object(t, g.BlobID).ObjectKey != o.ObjectKey {
				t.Fatal("preference changed existing grant")
			}
			if (newGrant.RetentionClass == "keep") != (next == "off") {
				t.Fatal("new grant ignored preference")
			}
		})
	}
	f := newTransfers(t)
	g := f.reserve(t, "default", 3)
	if g.RetentionClass != "1d" {
		t.Fatal("wrong default")
	}
	if _, err := f.env.Hub.DB.Exec(`INSERT INTO hub_preferences(key,value_json,updated_at,updated_by) VALUES('blobs.expiry','"invalid"',?,'operator')`, f.env.Now.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, "bad", blobwire.UploadRequest{Size: 3, SHA256: g.SHA256, MediaType: "text/plain"}); err == nil {
		t.Fatal("invalid prefs failed open")
	}
	f.charge(t, 3, 0)
}
func TestR2KeepReferencesAndRollback(t *testing.T) {
	f := newTransfers(t)
	f.pref(t, "off")
	g := f.reserve(t, "kept", 3)
	f.complete(t, g)
	// A real retained message supplies the FK target without bypassing any
	// message schema or source/peer authority checks.
	body := map[string]any{"idempotency_key": "reference-fixture", "recipients": []string{f.env.OperatorPeer}, "priority": "hold", "content": map[string]any{"text": "reference"}}
	raw, _ := json.Marshal(body)
	sent, _, err := f.env.Hub.Send(context.Background(), auth.Caller{Kind: "operator", PeerID: f.env.OperatorPeer, PrincipalID: f.env.OperatorPrincipal}, raw, "http_send")
	if err != nil || sent == nil {
		t.Fatalf("message fixture failed: %v", err)
	}
	rollback := errors.New("rollback")
	err = f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		if err := f.s.AddReference(context.Background(), tx, g.BlobID, g.SHA256, sent.MessageID); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if n := f.number(t, `SELECT count(*) FROM blob_object_refs`); n != 0 {
		t.Fatal("rollback left reference")
	}
	if err = f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		return f.s.AddReference(context.Background(), tx, g.BlobID, g.SHA256, sent.MessageID)
	}); err != nil {
		t.Fatal(err)
	}
	f.env.Now.Advance(48 * time.Hour)
	f.tick(t)
	s, err := f.s.State(context.Background(), g.BlobID)
	if err != nil || s.State != "available" || s.ExpiresAt != nil {
		t.Fatal("expiry deleted referenced keep")
	}
	f.charge(t, 0, 3)
	if _, err := f.env.Hub.DB.Exec(`DELETE FROM blob_object_refs WHERE blob_id=?`, g.BlobID); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	f.charge(t, 0, 0)
	if err = f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		return f.s.AddReference(context.Background(), tx, g.BlobID, g.SHA256, sent.MessageID)
	}); err == nil {
		t.Fatal("late attach resurrected GC object")
	}
}
func TestR2KeepFirstAttachTimeout(t *testing.T) {
	f := newTransfers(t)
	f.pref(t, "off")
	g := f.reserve(t, "one", 3)
	f.complete(t, g)
	o := f.object(t, g.BlobID)
	if o.FirstAttachDeadline == nil || !o.FirstAttachDeadline.Equal(f.env.Now.Now().Add(24*time.Hour)) {
		t.Fatal("missing first-attach deadline")
	}
	f.env.Now.Advance(24 * time.Hour)
	f.tick(t)
	f.charge(t, 0, 0)
	s, err := f.s.State(context.Background(), g.BlobID)
	if err != nil || s.State == "available" {
		t.Fatal("unreferenced keep never collected")
	}
}

func TestR2PreferenceClassChangeSerializesWithGrant(t *testing.T) {
	f := newTransfers(t)
	done := make(chan error, 1)
	go func() {
		done <- f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
			// Change only the test fixture's real reader bootstrap. The production
			// preference write gate remains uninstalled and unavailable.
			f.env.Hub.Preferences.bootstrap["blobs.expiry"] = prefs.Effective{Value: prefs.Enum("off"), Source: prefs.SourceFlag, Origin: "--blobs-expiry"}
			_, err := tx.Exec(`UPDATE hub_preferences_state SET revision=revision+1 WHERE id=1`)
			return err
		})
	}()
	g := f.reserve(t, "race", 3)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	o := f.object(t, g.BlobID)
	if !(o.ExpiryPrefRevision == 0 && o.ExpiryPrefValue == "1d" && o.RetentionClass == "1d" && o.ExpiresAt != nil || o.ExpiryPrefRevision == 1 && o.ExpiryPrefValue == "off" && o.RetentionClass == "keep" && o.ExpiresAt == nil) {
		t.Fatal("class and revision were not one snapshot")
	}
}

func TestR2KeepLastReferenceGCRacesAttach(t *testing.T) {
	f := newTransfers(t)
	f.pref(t, "off")
	g := f.reserve(t, "one", 3)
	f.complete(t, g)
	makeMessage := func(key string) string {
		raw, _ := json.Marshal(map[string]any{"idempotency_key": key, "recipients": []string{f.env.OperatorPeer}, "priority": "hold", "content": map[string]any{"text": "message"}})
		sent, _, err := f.env.Hub.Send(context.Background(), auth.Caller{Kind: "operator", PeerID: f.env.OperatorPeer, PrincipalID: f.env.OperatorPrincipal}, raw, "http_send")
		if err != nil {
			t.Fatal(err)
		}
		return sent.MessageID
	}
	first, next := makeMessage("first"), makeMessage("next")
	if err := f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error { return f.s.AddReference(context.Background(), tx, g.BlobID, g.SHA256, first) }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`DELETE FROM blob_object_refs WHERE blob_id=?`, g.BlobID); err != nil {
		t.Fatal(err)
	}
	f.env.Now.Advance(48 * time.Hour)
	attached := make(chan error, 1)
	ticked := make(chan error, 1)
	go func() {
		attached <- f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error { return f.s.AddReference(context.Background(), tx, g.BlobID, g.SHA256, next) })
	}()
	go func() { ticked <- f.s.Tick(context.Background()) }()
	attachErr := <-attached
	if err := <-ticked; err != nil {
		t.Fatal(err)
	}
	o := f.object(t, g.BlobID)
	refs := f.number(t, `SELECT count(*) FROM blob_object_refs WHERE blob_id=?`, g.BlobID)
	if attachErr == nil {
		if o.State != "complete" || refs != 1 {
			t.Fatal("GC defeated committed attach")
		}
		f.charge(t, 0, 3)
	} else {
		if o.State == "complete" || refs != 0 {
			t.Fatal("attach resurrected GC object")
		}
		f.charge(t, 0, 0)
	}
}
