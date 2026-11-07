package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

func stateMessage(t *testing.T, f *transferFixture, g blobwire.UploadGrant, key string) ([]envelope.Message, []byte, []byte) {
	t.Helper()
	h := f.env.Hub
	c := transferCaller(f)
	raw, err := json.Marshal(map[string]any{"idempotency_key": key, "priority": "hold", "recipients": []string{c.PeerID}, "content": map[string]any{"text": "retained"}, "attachments": []any{map[string]any{"blob_id": g.BlobID, "sha256": g.SHA256, "size": g.Size, "media_type": "text/plain", "name": "report.txt", "retention_class": g.RetentionClass, "expires_at": g.ExpiresAt}}})
	if err != nil {
		t.Fatal(err)
	}
	sent, _, err := h.Send(context.Background(), c, raw, "http_send")
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := h.ThreadMessages(context.Background(), c, sent.ThreadID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	return msgs, raw, response
}

func TestR2StateExpiryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		class    string
		duration time.Duration
	}{{"1h", time.Hour}, {"6h", 6 * time.Hour}, {"12h", 12 * time.Hour}, {"1d", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}} {
		t.Run(tc.class, func(t *testing.T) {
			f := newTransfers(t)
			f.pref(t, tc.class)
			g := f.reserve(t, "expiry", 3)
			f.complete(t, g)
			msgs, request, response := stateMessage(t, f, g, "expiry-message")
			before, _ := json.Marshal(msgs)
			c := transferCaller(f)
			ctx := context.Background()
			f.env.Now.Advance(tc.duration - time.Millisecond)
			for i, want := range []string{"available", "expired", "expired"} {
				if i > 0 {
					f.env.Now.Advance(time.Millisecond)
				}
				states, err := f.env.Hub.ProjectBlobStates(ctx, c, msgs)
				if err != nil {
					t.Fatal(err)
				}
				s, ok := states[g.BlobID]
				if !ok || len(states) != 1 || s.State != want || s.RetentionClass != tc.class || s.ExpiresAt == nil || *s.ExpiresAt != *g.ExpiresAt || s.SHA256Verified == nil || !*s.SHA256Verified || s.Integrity == nil || !s.Integrity.Verified {
					t.Fatalf("projection lost state or immutable proof at boundary %d", i)
				}
				if i > 0 {
					_, err = f.env.Hub.BlobDownload(ctx, &c, "", g.SHA256, g.BlobID)
					requireBlobCode(t, err, "blob_expired")
					if apiErr, ok := err.(*APIError); !ok || apiErr.Status != 410 {
						t.Fatal("expiry must return HTTP 410")
					}
				}
				_, err = f.env.Hub.BlobDownload(ctx, nil, "", g.SHA256, g.BlobID)
				requireBlobCode(t, err, "forbidden")
				var stored string
				if err := f.env.Hub.DB.QueryRow(`SELECT response FROM idempotency WHERE sender=? AND key='expiry-message'`, c.PeerID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(response, []byte(stored)) {
					t.Fatal("expiry changed stored idempotency bytes")
				}
				if tc.duration < 24*time.Hour {
					replay, _, err := f.env.Hub.Send(ctx, c, request, "http_send")
					encoded, _ := json.Marshal(replay)
					if err != nil || !bytes.Equal(response, encoded) {
						t.Fatal("expiry changed replay response bytes")
					}
				}

				history, _, err := f.env.Hub.ThreadMessages(ctx, c, msgs[0].ThreadID, "", 0)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(history)
				if !bytes.Equal(before, after) || history[0].Attachments[0].Name != "report.txt" || history[0].Attachments[0].SHA256 != g.SHA256 {
					t.Fatal("projection changed retained message")
				}
			}
			if f.object(t, g.BlobID).State != "complete" {
				t.Fatal("test required clock-derived expiry without GC")
			}
		})
	}
}

func TestR2ReuploadDoesNotReviveOldMessage(t *testing.T) {
	f := newTransfers(t)
	f.pref(t, "1h")
	a := f.reserve(t, "old", 3)
	f.complete(t, a)
	old, _, _ := stateMessage(t, f, a, "old-message")
	f.env.Now.Advance(time.Hour)
	b := f.reserve(t, "new", 3)
	f.complete(t, b)
	current, _, _ := stateMessage(t, f, b, "new-message")
	states, err := f.env.Hub.ProjectBlobStates(context.Background(), transferCaller(f), append(old, current...))
	if err != nil {
		t.Fatal(err)
	}
	if a.BlobID == b.BlobID || a.SHA256 != b.SHA256 || len(states) != 2 || states[a.BlobID].State != "expired" || states[b.BlobID].State != "available" {
		t.Fatal("reupload revived old reference")
	}
	f.charge(t, 0, 6)
	f.tick(t)
	f.charge(t, 0, 3)
	if f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='verified'`) != 2 || f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='delete_confirmed'`) != 1 {
		t.Fatal("incarnations did not retain independent occupancy intervals")
	}
}

func TestR2StateKeepReferenceGCAndPurge(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-reference", true: "purge"}[purge], func(t *testing.T) {
			f := newTransfers(t)
			f.pref(t, "off")
			g := f.reserve(t, "keep", 3)
			f.complete(t, g)
			msgs, _, _ := stateMessage(t, f, g, "kept-message")
			f.env.Now.Advance(31 * 24 * time.Hour)
			f.tick(t)
			states, err := f.env.Hub.ProjectBlobStates(context.Background(), transferCaller(f), msgs)
			if err != nil || states[g.BlobID].State != "available" || states[g.BlobID].ExpiresAt != nil {
				t.Fatal("referenced keep expired")
			}
			f.charge(t, 0, 3)
			if purge {
				if err := f.s.PurgeHash(context.Background(), g.SHA256); err != nil {
					t.Fatal(err)
				}
				states, err = f.env.Hub.ProjectBlobStates(context.Background(), transferCaller(f), msgs)
				if err != nil || states[g.BlobID].State != "purged" {
					t.Fatal("purge not projected")
				}
			} else {
				if _, err := f.env.Hub.DB.Exec(`DELETE FROM messages WHERE msg_id=?`, msgs[0].MessageID); err != nil {
					t.Fatal(err)
				}
			}
			f.tick(t)
			f.charge(t, 0, 0)
		})
	}
}

func TestR2UsageIssuanceRetry(t *testing.T) {
	f, g, c := downloadFixture(t)
	ctx := context.Background()
	event := blob.DownloadGrantEvent{EventID: ids.New(ids.Blo), BlobID: g.BlobID, CallerIdentity: c.PrincipalID, Kind: "download_grant_issued", At: f.env.Now.Now()}
	firstID := event.EventID
	for i := 0; i < 2; i++ {
		if _, err := f.s.RecordDownloadGrant(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	event.EventID = ids.New(ids.Blo)
	if _, err := f.s.RecordDownloadGrant(ctx, event); err != nil {
		t.Fatal(err)
	}
	if f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='download_grant_issued' AND byte_count=0`) != 2 {
		t.Fatal("event retries and distinct gets conflated")
	}
	event.EventID = firstID
	event.CallerIdentity = ids.New(ids.Prn)
	if _, err := f.s.RecordDownloadGrant(ctx, event); err == nil {
		t.Fatal("conflicting issuance retry accepted")
	}
	if f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='verified'`) != 1 {
		t.Fatal("completion counted more than once")
	}
}

func TestR2StateProjectionRequiresVisibleMessage(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "visible", 3)
	f.complete(t, g)
	msgs, _, _ := stateMessage(t, f, g, "visible-message")
	c := transferCaller(f)
	c.Kind = "peer"
	// An attachment supplied in an invented message must not inherit uploader authority.
	forged := append([]envelope.Message(nil), msgs...)
	forged[0].MessageID = "msg_00000000000000000000000000"
	s, err := f.env.Hub.ProjectBlobStates(context.Background(), c, forged)
	if err != nil || len(s) != 0 {
		t.Fatal("projection trusts caller-supplied message")
	}
	s, err = f.env.Hub.ProjectBlobStates(context.Background(), auth.Caller{}, msgs)
	if err != nil || len(s) != 0 {
		t.Fatal("projection disclosed states to unauthenticated caller")
	}
}
