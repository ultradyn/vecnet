package blob_test

import (
	"bytes"
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

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// These tests exercise real SQLite transactions, the merged preference reader,
// and the production HEAD adapter against an in-process S3 fake only. They are
// not provider acceptance evidence.
type transferFixture struct {
	env        *testhub.Env
	s          *blob.Transfers
	cfg        blob.TransfersConfig
	server     *httptest.Server
	mu         sync.Mutex
	objects    map[string]http.Header
	deleteDown bool
	headWait   func()
	deleteWait func()
}

func newTransfers(t *testing.T) *transferFixture {
	t.Helper()
	f := &transferFixture{env: testhub.Start(t), objects: map[string]http.Header{}}
	// Keep short abandonment windows within the issue day. Tests for quota
	// rollover move this clock to their own explicit midnight boundary.
	now := f.env.Now.Now()
	f.env.Now.Advance(now.Truncate(24 * time.Hour).Add(12 * time.Hour).Sub(now))
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		f.mu.Lock()
		h, ok := f.objects[key]
		h = h.Clone()
		down, wait, deleteWait := f.deleteDown, f.headWait, f.deleteWait
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
			if deleteWait != nil {
				deleteWait()
			}
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

func TestR2ReservationDoesNotDisclosePurge(t *testing.T) {
	for _, purgeDuringGrant := range []bool{false, true} {
		t.Run(fmt.Sprint(purgeDuringGrant), func(t *testing.T) {
			f := newTransfers(t)
			ctx := context.Background()
			outsider, _ := f.env.NewPeer("session", "outsider")
			purgedSHA := hexSHA([]byte("abc"))
			if !purgeDuringGrant {
				if err := f.s.PurgeHash(ctx, purgedSHA); err != nil {
					t.Fatal(err)
				}
			}
			for _, sha := range []string{purgedSHA, hexSHA([]byte("xyz"))} {
				in := blobwire.UploadRequest{Size: 3, SHA256: sha, MediaType: "text/plain"}
				grant, err := f.s.Reserve(ctx, outsider, sha, in)
				if err != nil {
					t.Fatalf("first reservation disclosed hash state: %v", err)
				}
				if purgeDuringGrant && sha == purgedSHA {
					if err := f.s.PurgeHash(ctx, sha); err != nil {
						t.Fatal(err)
					}
				}
				replay, err := f.s.Reserve(ctx, outsider, sha, in)
				if err != nil || replay.BlobID != grant.BlobID {
					t.Fatalf("unverified replay disclosed hash state: %v", err)
				}
				state, err := f.s.State(ctx, grant.BlobID)
				if err != nil || state.State != "unavailable" {
					t.Errorf("unverified state disclosed purge: state=%s err=%v", state.State, err)
				}
				if err := f.env.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
					return f.s.AddReference(ctx, tx, grant.BlobID, sha, "not-a-message")
				}); !errors.Is(err, blob.ErrR2Incomplete) {
					t.Errorf("unverified attach must fail without disclosing purge: %v", err)
				}
				if _, err := f.s.Complete(ctx, outsider, grant.BlobID); !errors.Is(err, blob.ErrR2Incomplete) {
					t.Fatalf("completion without possession: want incomplete, got %v", err)
				}
				bad, err := f.s.Reserve(ctx, outsider, sha+"-bad-proof", in)
				if err != nil {
					t.Fatal(err)
				}
				f.upload(t, bad)
				o := f.object(t, bad.BlobID)
				f.mu.Lock()
				f.objects[o.ObjectKey].Set("X-Amz-Checksum-Sha256", "invalid")
				f.mu.Unlock()
				if _, err := f.s.Complete(ctx, outsider, bad.BlobID); !errors.Is(err, blob.ErrR2Proof) {
					t.Fatalf("invalid proof disclosed hash state: %v", err)
				}
				f.upload(t, grant)
				_, err = f.s.Complete(ctx, outsider, grant.BlobID)
				if sha == purgedSHA {
					if !errors.Is(err, blob.ErrR2Purged) {
						t.Fatalf("verified possession must encounter purge: %v", err)
					}
					if f.object(t, grant.BlobID).SHA256Verified {
						t.Fatal("purged grant became verified")
					}
				} else if err != nil {
					t.Fatalf("unknown hash completion: %v", err)
				}
			}
		})
	}
}

func TestR2WireTimestampsAreCanonical(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(envelope.SchemaJSON()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:vecnet:envelope:1", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:vecnet:envelope:1#/$defs/attachment")
	if err != nil {
		t.Fatal(err)
	}
	for _, fraction := range []time.Duration{0, 120 * time.Millisecond, 123456789 * time.Nanosecond} {
		t.Run(fraction.String(), func(t *testing.T) {
			f := newTransfers(t)
			f.env.Now.Advance(fraction - time.Duration(f.env.Now.Now().Nanosecond()))
			grant := f.reserve(t, "timestamp", 3)
			checkTime := func(label, value string, want time.Time) {
				t.Helper()
				got, err := envelope.ParseTime(value)
				if err != nil || !got.Equal(want.Truncate(time.Millisecond)) {
					t.Errorf("%s must use canonical millisecond UTC", label)
				}
			}
			checkRef := func(label string, ref blobwire.Ref) {
				t.Helper()
				if ref.ExpiresAt == nil {
					t.Fatal("expiring reference has no deadline")
				}
				checkTime(label, *ref.ExpiresAt, f.env.Now.Now().Add(24*time.Hour))
				raw, err := json.Marshal(ref)
				if err != nil {
					t.Fatal(err)
				}
				var attachment map[string]any
				if err := json.Unmarshal(raw, &attachment); err != nil {
					t.Fatal(err)
				}
				attachment["media_type"] = "text/plain"
				if err := schema.Validate(attachment); err != nil {
					t.Errorf("%s reference violates envelope schema: %v", label, err)
				}
			}
			checkRef("reserve", grant.Ref)
			checkTime("upload", grant.Upload.ExpiresAt, f.env.Now.Now().Add(blob.R2UploadTTL))
			f.upload(t, grant)
			ref, err := f.s.Complete(context.Background(), f.env.OperatorPeer, grant.BlobID)
			if err != nil {
				t.Fatal(err)
			}
			checkRef("complete", ref)
			state, err := f.s.State(context.Background(), grant.BlobID)
			if err != nil || state.ExpiresAt == nil {
				t.Fatal("state missing reference deadline")
			}
			checkTime("state", *state.ExpiresAt, f.env.Now.Now().Add(24*time.Hour))
			replay := f.reserve(t, "timestamp", 3)
			checkRef("replay", replay.Ref)
			checkTime("replay upload", replay.Upload.ExpiresAt, f.env.Now.Now().Add(blob.R2UploadTTL))
		})
	}
}

func TestR2ConcurrentDailyAndStorageReservations(t *testing.T) {
	for _, storage := range []bool{false, true} {
		t.Run(fmt.Sprint(storage), func(t *testing.T) {
			f := newTransfers(t)
			f.cfg.DailyQuota = 5
			if storage {
				f.cfg.DailyQuota = 100
				f.cfg.StorageQuota = 5
			}
			f.restart(t)
			errs := make(chan error, 2)
			for _, key := range []string{"a", "b"} {
				go func(key string) {
					_, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, key, blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
					errs <- err
				}(key)
			}
			wins := 0
			for range 2 {
				err := <-errs
				if err == nil {
					wins++
				} else if storage && !errors.Is(err, blob.ErrR2StorageQuota) {
					t.Fatal(err)
				} else if !storage && !errors.Is(err, blob.ErrQuota) {
					t.Fatal(err)
				}
			}
			if wins != 1 {
				t.Fatalf("successful reservations=%d want 1", wins)
			}
			f.charge(t, 3, 0)
		})
	}
}
func TestR2ReplayCompletionAndIssueDay(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "one", 3)
	replay := f.reserve(t, "one", 3)
	if replay.BlobID != g.BlobID || replay.Upload.ExpiresAt != g.Upload.ExpiresAt {
		t.Fatal("replay changed grant")
	}
	if g.SHA256Verified == nil || *g.SHA256Verified || g.Integrity.Verified {
		t.Fatal("pending marked verified")
	}
	if _, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, "one", blobwire.UploadRequest{Size: 4, SHA256: g.SHA256, MediaType: "text/plain"}); !errors.Is(err, blob.ErrR2Idempotency) {
		t.Fatal("changed replay accepted")
	}
	issue := f.env.Now.Now().UTC().Format("2006-01-02")
	f.complete(t, g)
	for range 2 {
		if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); err != nil {
			t.Fatal(err)
		}
	}
	f.charge(t, 0, 3)
	if n := f.number(t, `SELECT accepted_bytes FROM blob_r2_quota WHERE uploader=? AND day=?`, f.env.OperatorPeer, issue); n != 3 {
		t.Fatalf("accepted=%d", n)
	}
	f.env.Now.Advance(24 * time.Hour)
	f.tick(t)
	f.restart(t)
	f.tick(t)
	if n := f.number(t, `SELECT accepted_bytes FROM blob_r2_quota WHERE uploader=? AND day=?`, f.env.OperatorPeer, issue); n != 3 {
		t.Fatal("midnight refunded issue day")
	}
	if _, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, "one", blobwire.UploadRequest{Size: 3, SHA256: g.SHA256, MediaType: "text/plain"}); !errors.Is(err, blob.ErrR2GrantExpired) {
		t.Fatal("expired replay issued a grant")
	}
	if n := f.number(t, `SELECT count(*) FROM blob_usage_events WHERE blob_id=? AND kind='verified'`, g.BlobID); n != 1 {
		t.Fatal("verification charged more than once")
	}
}
func TestR2AbandonedAndRejectedAreNonrefundable(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			f := newTransfers(t)
			f.cfg.DailyQuota = 5
			f.restart(t)
			g := f.reserve(t, "one", 3)
			if reject {
				f.upload(t, g)
				o := f.object(t, g.BlobID)
				f.mu.Lock()
				f.objects[o.ObjectKey].Set("X-Amz-Checksum-Sha256", "wrong")
				f.mu.Unlock()
				if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); !errors.Is(err, blob.ErrR2Proof) {
					t.Fatal("bad proof accepted")
				}
			}
			f.env.Now.Advance(7 * time.Minute)
			f.tick(t)
			f.restart(t)
			f.tick(t)
			f.charge(t, 0, 0)
			_, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, "two", blobwire.UploadRequest{Size: 3, SHA256: g.SHA256, MediaType: "text/plain"})
			if !errors.Is(err, blob.ErrQuota) {
				t.Fatal("failed reservation refunded")
			}
			column := "abandoned_bytes"
			if reject {
				column = "rejected_bytes"
			}
			if n := f.number(t, `SELECT `+column+` FROM blob_r2_quota WHERE uploader=?`, f.env.OperatorPeer); n != 3 {
				t.Fatalf("terminal daily charge=%d", n)
			}
		})
	}
}
func TestR2DeletionSafetyReplayAndRestart(t *testing.T) {
	f := newTransfers(t)
	f.cfg.StorageQuota = 3
	f.restart(t)
	g := f.reserve(t, "one", 3)
	f.upload(t, g)
	if err := f.s.PurgeHash(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	f.charge(t, 3, 0)
	// A still-live PUT can recreate after an early deletion. Persisted cleanup
	// must delete this incarnation again after the original deadline plus 60s.
	f.upload(t, g)
	f.env.Now.Advance(5*time.Minute + 59*time.Second)
	f.tick(t)
	f.charge(t, 3, 0)
	f.mu.Lock()
	f.deleteDown = true
	f.mu.Unlock()
	f.env.Now.Advance(2 * time.Second)
	f.tick(t)
	f.charge(t, 3, 0)
	f.restart(t)
	f.mu.Lock()
	f.deleteDown = false
	f.mu.Unlock()
	f.env.Now.Advance(time.Hour)
	f.tick(t)
	f.tick(t)
	f.charge(t, 0, 0)
	if n := f.number(t, `SELECT count(*) FROM blob_delete_jobs`); n != 0 {
		t.Fatal("cleanup not completed")
	}
	if n := f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='storage_released'`); n != 1 {
		t.Fatal("release not idempotent")
	}
	// No verified possession was recorded before deletion. The expired grant
	// must stay unusable without revealing the hash's purge state.
	if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); !errors.Is(err, blob.ErrR2GrantExpired) {
		t.Fatal("deleted unverified grant must remain expired")
	}
}
func TestR2ProofAndPurgeRaces(t *testing.T) {
	for _, mutation := range []string{"metadata", "checksum", "length", "etag", "missing", "purge", "expiry", "disable"} {
		t.Run(mutation, func(t *testing.T) {
			f := newTransfers(t)
			g := f.reserve(t, "one", 3)
			f.upload(t, g)
			o := f.object(t, g.BlobID)
			f.mu.Lock()
			switch mutation {
			case "metadata":
				f.objects[o.ObjectKey].Del("X-Amz-Meta-Vecnet-Checksum")
			case "checksum":
				f.objects[o.ObjectKey].Set("X-Amz-Checksum-Sha256", "wrong")
			case "length":
				f.objects[o.ObjectKey].Set("Content-Length", "4")
			case "etag":
				f.objects[o.ObjectKey].Del("ETag")
			case "missing":
				delete(f.objects, o.ObjectKey)
			}
			f.mu.Unlock()
			if mutation == "purge" || mutation == "expiry" || mutation == "disable" {
				f.cfg.Checkpoint = func(point string) error {
					if point == "after_head" {
						switch mutation {
						case "purge":
							return f.s.PurgeHash(context.Background(), g.SHA256)
						case "expiry":
							f.env.Now.Advance(25 * time.Hour)
						case "disable":
							_, err := f.env.Hub.DB.Exec(`UPDATE peers SET disabled=1 WHERE peer_id=?`, f.env.OperatorPeer)
							return err
						}
					}
					return nil
				}
				f.restart(t)
			}
			_, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID)
			if err == nil {
				t.Fatal("unsafe completion accepted")
			}
			if f.object(t, g.BlobID).SHA256Verified {
				t.Fatal("failed completion persisted verification")
			}
			f.charge(t, 3, 0)
		})
	}
}
func TestR2PreferenceGrantSerialization(t *testing.T) {
	f := newTransfers(t)
	// The same writer transaction covers a revision read and reservation. Race
	// the merged reader against an independent committed revision change; no
	// production consumer adapters are installed to fabricate runtime support.
	done := make(chan error, 1)
	go func() {
		done <- f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE hub_preferences_state SET revision=revision+1 WHERE id=1`)
			return err
		})
	}()
	g := f.reserve(t, "race", 3)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	o := f.object(t, g.BlobID)
	if o.ExpiryPrefValue != "1d" || o.ExpiryPrefSource != "default" || o.ExpiryPrefRevision > 1 || g.RetentionClass != "1d" {
		t.Fatal("mixed preference snapshot")
	}
}
func TestR2CrashRecovery(t *testing.T) {
	for _, point := range []string{"after_upload", "after_head", "after_commit", "after_delete"} {
		t.Run(point, func(t *testing.T) {
			f := newTransfers(t)
			g := f.reserve(t, "one", 3)
			f.upload(t, g)
			crash := errors.New("injected_crash")
			f.cfg.Checkpoint = func(at string) error {
				if at == point {
					return crash
				}
				return nil
			}
			f.restart(t)
			if point == "after_delete" {
				if err := f.s.PurgeHash(context.Background(), g.SHA256); err != nil {
					t.Fatal(err)
				}
				f.env.Now.Advance(7 * time.Minute)
				if err := f.s.Tick(context.Background()); !errors.Is(err, crash) {
					t.Fatal("delete crash not injected")
				}
				f.charge(t, 3, 0)
			} else {
				if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); !errors.Is(err, crash) {
					t.Fatal("completion crash not injected")
				}
			}
			f.cfg.Checkpoint = nil
			f.restart(t)
			if point == "after_delete" {
				f.tick(t)
				f.charge(t, 0, 0)
			} else {
				if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); err != nil {
					t.Fatal(err)
				}
				f.charge(t, 0, 3)
			}
		})
	}
}
func TestR2IncompleteAndAuthorizationBeforeState(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "one", 3)
	if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); !errors.Is(err, blob.ErrR2Incomplete) {
		t.Fatal("missing upload was terminal")
	}
	if _, err := f.s.Complete(context.Background(), "unrelated", g.BlobID); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("unowned completion disclosed state")
	}
	if err := f.s.PurgeHash(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Complete(context.Background(), "unrelated", g.BlobID); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("purged existence disclosed")
	}
}

func TestR2ConcurrentCompletionsCrossMidnight(t *testing.T) {
	f := newTransfers(t)
	now := f.env.Now.Now().UTC()
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 50, 0, time.UTC)
	f.env.Now.Advance(end.Sub(now))
	g := f.reserve(t, "midnight", 3)
	f.upload(t, g)
	day := end.Format("2006-01-02")
	f.env.Now.Advance(20 * time.Second)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	f.mu.Lock()
	f.headWait = func() { entered <- struct{}{}; <-release }
	f.mu.Unlock()
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); done <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("HEADs did not reach network outside transaction")
		}
	}
	_, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID)
	if !errors.Is(err, blob.ErrR2Busy) {
		close(release)
		t.Fatal("third concurrent HEAD admitted")
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	f.charge(t, 0, 3)
	if n := f.number(t, `SELECT accepted_bytes FROM blob_r2_quota WHERE uploader=? AND day=?`, f.env.OperatorPeer, day); n != 3 {
		t.Fatal("completion moved issue-day charge incorrectly")
	}
	if n := f.number(t, `SELECT count(*) FROM blob_r2_quota`); n != 1 {
		t.Fatal("completion charged current day")
	}
}

func TestR2DeleteStartedBeforeSafetyWindowCannotRelease(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "one", 3)
	f.upload(t, g)
	if err := f.s.PurgeHash(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	// A slow DELETE starts while recreation is possible and returns after the
	// safety deadline. Only a later request, started after that deadline, proves
	// capacity can be released.
	f.mu.Lock()
	f.deleteWait = func() { f.env.Now.Advance(7 * time.Minute) }
	f.mu.Unlock()
	f.tick(t)
	f.charge(t, 3, 0)
	f.mu.Lock()
	f.deleteWait = nil
	f.mu.Unlock()
	f.tick(t)
	f.charge(t, 0, 0)
}

func TestR2ActiveGrantLimitAndStorageRebuild(t *testing.T) {
	f := newTransfers(t)
	for i := range 20 {
		f.reserve(t, fmt.Sprint(i), 1)
	}
	_, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, "overflow", blobwire.UploadRequest{Size: 1, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
	if !errors.Is(err, blob.ErrR2Busy) {
		t.Fatal("active-grant cap not enforced")
	}
	if _, err := f.env.Hub.DB.Exec(`UPDATE blob_r2_storage SET storage_reserved_bytes=0,retained_bytes=99`); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.charge(t, 20, 0)
	f.restart(t)
	f.charge(t, 20, 0)
	f.env.Now.Advance(7 * time.Minute)
	f.tick(t)
	f.charge(t, 0, 0)
	if n := f.number(t, `SELECT abandoned_bytes FROM blob_r2_quota`); n != 20 {
		t.Fatal("restart lost daily reservations")
	}
}

func TestR2ImmutableMetadataProofAndClockProjection(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "one", 3)
	f.upload(t, g)
	o := f.object(t, g.BlobID)
	// The Task 0 accepted immutable signed-metadata fallback is permitted when
	// the provider omits its optional checksum header; mutable metadata alone is
	// rejected by TestR2ProofAndPurgeRaces.
	f.mu.Lock()
	f.objects[o.ObjectKey].Del("X-Amz-Checksum-Sha256")
	f.mu.Unlock()
	if _, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID); err != nil {
		t.Fatal(err)
	}
	f.env.Now.Advance(24 * time.Hour)
	state, err := f.s.State(context.Background(), g.BlobID)
	if err != nil || state.State != "expired" {
		t.Fatal("logical expiry waited for ticker")
	}
	f.charge(t, 0, 3) // still charged until confirmed deletion
}

type invalidProofObjects struct{ err error }

func (p invalidProofObjects) Head(context.Context, string) (blob.ObjectProof, error) {
	return blob.ObjectProof{Length: 3, ETag: `"fake-etag"`, ChecksumProfile: "sha256", SHA256Verified: true}, p.err
}
func (invalidProofObjects) Delete(context.Context, string) error { return nil }
func TestR2InvalidAdapterProofCannotComplete(t *testing.T) {
	for _, providerErr := range []error{nil, errors.New("untrusted_provider_detail")} {
		t.Run(fmt.Sprint(providerErr != nil), func(t *testing.T) {
			f := newTransfers(t)
			g := f.reserve(t, "one", 3)
			f.cfg.Objects = invalidProofObjects{err: providerErr}
			f.restart(t)
			_, err := f.s.Complete(context.Background(), f.env.OperatorPeer, g.BlobID)
			if providerErr == nil {
				if !errors.Is(err, blob.ErrR2Proof) {
					t.Fatal("missing immutable evidence accepted")
				}
			} else {
				if !errors.Is(err, blob.ErrR2Unavailable) {
					t.Fatal("unsanitized adapter failure")
				}
			}
			f.charge(t, 3, 0)
		})
	}
}

func TestR2CutoverAndRollbackKeepCombinedQuota(t *testing.T) {
	f := newTransfers(t)
	f.cfg.DailyQuota = 5
	f.restart(t)
	f.env.Hub.Blobs.DailyQuota = 5
	ctx := context.Background()
	if _, err := f.env.Hub.Blobs.Put(ctx, f.env.OperatorPeer, strings.NewReader("abc"), "text/plain", 0); err != nil {
		t.Fatal(err)
	}
	g := f.reserve(t, "remote", 2)
	if _, err := f.env.Hub.Blobs.Put(ctx, f.env.OperatorPeer, strings.NewReader("d"), "text/plain", 0); !errors.Is(err, blob.ErrQuota) {
		t.Fatal("rollback ignored remote reservation")
	}
	f.env.Now.Advance(7 * time.Minute)
	f.tick(t)
	if _, err := f.env.Hub.Blobs.Put(ctx, f.env.OperatorPeer, strings.NewReader("d"), "text/plain", 0); !errors.Is(err, blob.ErrQuota) {
		t.Fatal("rollback refunded remote abandonment")
	}
	if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, "another", blobwire.UploadRequest{Size: 1, SHA256: g.SHA256, MediaType: "text/plain"}); !errors.Is(err, blob.ErrQuota) {
		t.Fatal("cutover reset disk quota")
	}
}
func TestR2ConcurrentIdempotentIssuance(t *testing.T) {
	f := newTransfers(t)
	type result struct {
		g   blobwire.UploadGrant
		err error
	}
	done := make(chan result, 4)
	for range 4 {
		go func() {
			g, err := f.s.Reserve(context.Background(), f.env.OperatorPeer, "same", blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
			done <- result{g, err}
		}()
	}
	var id string
	for range 4 {
		r := <-done
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id != "" && r.g.BlobID != id {
			t.Fatal("idempotency race minted another object")
		}
		id = r.g.BlobID
	}
	f.charge(t, 3, 0)
	if n := f.number(t, `SELECT reserved_bytes FROM blob_r2_quota`); n != 3 {
		t.Fatal("idempotency race double-charged daily quota")
	}
}
func TestR2RetainedStorageBlocksNewGrantsDuringDeletionOutage(t *testing.T) {
	f := newTransfers(t)
	f.cfg.StorageQuota = 3
	f.restart(t)
	g := f.reserve(t, "one", 3)
	f.complete(t, g)
	ctx := context.Background()
	request := blobwire.UploadRequest{Size: 1, SHA256: hexSHA([]byte("other")), MediaType: "text/plain"}
	if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, "another", request); !errors.Is(err, blob.ErrR2StorageQuota) {
		t.Fatal("retained bytes not counted")
	}
	f.mu.Lock()
	f.deleteDown = true
	f.mu.Unlock()
	f.env.Now.Advance(24 * time.Hour)
	f.tick(t)
	f.charge(t, 0, 3)
	if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, "another", request); !errors.Is(err, blob.ErrR2StorageQuota) {
		t.Fatal("deletion outage released capacity")
	}
	f.mu.Lock()
	f.deleteDown = false
	f.mu.Unlock()
	f.env.Now.Advance(time.Minute)
	f.tick(t)
	f.charge(t, 0, 0)
	if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, "another", request); err != nil {
		t.Fatal("confirmed deletion did not free capacity")
	}
}

// Admission must not expose a hash tombstone to an uploader that has never
// uploaded bytes or seen a reference, even when its active slots are full.
func TestR2ActiveGrantCapDoesNotDiscloseConcurrentPurge(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprint(purge), func(t *testing.T) {
			f := newTransfers(t)
			ctx := context.Background()
			for i := range 20 {
				request := blobwire.UploadRequest{Size: 1, SHA256: hexSHA([]byte(fmt.Sprint(i))), MediaType: "text/plain"}
				if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, fmt.Sprint(i), request); err != nil {
					t.Fatal(err)
				}
			}
			request := blobwire.UploadRequest{Size: 1, SHA256: hexSHA([]byte("new")), MediaType: "text/plain"}
			for _, phase := range []string{"before", "after", "restart"} {
				if phase == "after" && purge {
					if err := f.s.PurgeHash(ctx, hexSHA([]byte("0"))); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "restart" {
					f.restart(t)
				}
				if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, "overflow", request); !errors.Is(err, blob.ErrR2Busy) {
					t.Errorf("%s: admission at cap = %v, want upload_busy", phase, err)
				}
			}
			f.env.Now.Advance(blob.R2UploadTTL)
			if _, err := f.s.Reserve(ctx, f.env.OperatorPeer, "after-deadline", request); err != nil {
				t.Fatalf("original deadline did not release slots: %v", err)
			}
		})
	}
}
