package hub_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestR2MessageDataQuotaSurvivesCutoverAndRollback(t *testing.T) {
	for _, purpose := range []string{"spill", "source_raw"} {
		for _, firstR2 := range []bool{false, true} {
			name := "disk-to-r2"
			if firstR2 {
				name = "r2-to-disk"
			}
			t.Run(purpose+"/"+name, func(t *testing.T) {
				env := testhub.Start(t)
				transfers := enableR2Durable(t, env)
				env.Hub.Cfg.TextCap = 4
				env.Hub.Blobs.DailyQuota = 11
				ctx := context.Background()
				b, _ := env.NewPeer("session", "recipient")
				var submit func(int) error
				if purpose == "spill" {
					_, sender := env.NewPeer("session", "sender")
					submit = func(i int) error {
						body := req([]string{"first", "second"}[i], b)
						body["content"] = map[string]any{"text": []string{"first!!", "second!"}[i]}
						resp, data := env.Do(sender, "POST", "/v1/messages", body)
						if resp.StatusCode == http.StatusAccepted {
							return nil
						}
						if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(data), "upload_quota_exceeded") {
							t.Fatalf("unexpected spill status %d", resp.StatusCode)
						}
						return blob.ErrQuota
					}
				} else {
					src, _, _, err := env.Hub.AddSource(ctx, "github", "source", b, nil, "")
					if err != nil {
						t.Fatal(err)
					}
					submit = func(i int) error {
						_, _, err := env.Hub.IngestSource(ctx, hub.SourceMessage{SrcID: src, ProviderDeliveryID: []string{"one", "two"}[i], OriginThreadID: "issue/1", OriginEventID: []string{"one", "two"}[i], EventName: "issues.opened", Summary: "raw", Raw: []byte([]string{`{"n":1}`, `{"n":2}`}[i]), MediaType: "application/json"})
						return err
					}
				}
				if !firstR2 {
					env.Hub.Blobs.Transfers = nil
				}
				if err := submit(0); err != nil {
					t.Fatal(err)
				}
				if firstR2 {
					env.Hub.Blobs.Transfers = nil
				} else {
					env.Hub.Blobs.Transfers = transfers
				}
				err := submit(1)
				if purpose == "spill" {
					if !errors.Is(err, blob.ErrQuota) {
						t.Fatal("backend switch reset spill quota")
					}
				} else {
					var he *hub.APIError
					if !errors.As(err, &he) || he.Code != "upload_quota_exceeded" {
						t.Fatalf("backend switch reset source quota: %v", err)
					}
				}
				var messages, charged int
				if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM messages`).Scan(&messages); err != nil {
					t.Fatal(err)
				}
				if err := env.Hub.DB.QueryRow(`SELECT coalesce(sum(bytes),0) FROM blob_quota`).Scan(&charged); err != nil {
					t.Fatal(err)
				}
				if messages != 1 || charged != 7 {
					t.Fatalf("rejected message changed accounting: messages=%d charged=%d", messages, charged)
				}
				// The finite message allowance resets at the UTC day boundary.
				env.Now.Advance(24 * time.Hour)
				if err := submit(1); err != nil {
					t.Fatalf("next-day allowance unavailable: %v", err)
				}
			})
		}
	}
}

func TestR2DiskOnlyPurgeSerializesDurableMessageWrite(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	b, _ := env.NewPeer("session", "recipient")
	_, sender := env.NewPeer("session", "sender")
	id := mustSend(t, env, sender, req("seed", b)).MessageID
	data := []byte("disk-only")
	s := env.Hub.Blobs
	meta, err := s.Put(ctx, env.OperatorPeer, strings.NewReader(string(data)), "text/plain", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Run a writer at the purge's timestamp boundary. Previously this was
	// between the empty durable-purge transaction and the disk metadata UPDATE.
	// The write commits first or sees a tombstone; it must never survive purge.
	called := false
	s.Now = func() time.Time {
		if !called {
			called = true
			err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
				if err := blob.PutDurable(ctx, tx, "spill", meta.SHA256, "text/plain", data); err != nil {
					return err
				}
				return blob.PinDurable(ctx, tx, "spill", meta.SHA256, id)
			})
			if err != nil && !errors.Is(err, blob.ErrPurged) {
				t.Errorf("interleaved writer: %v", err)
			}
		}
		return env.Now.Now()
	}
	if err := s.Purge(ctx, meta.SHA256); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("interleaved writer was not exercised")
	}
	var bytes, tombstones int
	if err := s.DB.QueryRow(`SELECT coalesce(sum(length(bytes)),0) FROM durable_blob_payloads WHERE sha256=?`, meta.SHA256).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM blob_purge_tombstones WHERE sha256=?`, meta.SHA256).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if bytes != 0 || tombstones != 1 {
		t.Fatalf("purge missed interleaved message bytes: bytes=%d tombstones=%d", bytes, tombstones)
	}
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return blob.PutDurable(ctx, tx, "spill", meta.SHA256, "text/plain", data)
	}); !errors.Is(err, blob.ErrPurged) {
		t.Fatal("post-purge writer restored bytes")
	}
}
