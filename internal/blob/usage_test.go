package blob_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/ids"
)

func TestR2UsageOccupancyThroughConfirmedDelete(t *testing.T) {
	f := newTransfers(t)
	var operations []blob.TransferOperation
	f.cfg.ObserveOperation = func(event blob.TransferOperation) { operations = append(operations, event) }
	f.restart(t)
	g := f.reserve(t, "usage", 3)
	f.complete(t, g)
	f.complete(t, g)
	f.env.Now.Advance(time.Hour)
	if err := f.s.PurgeHash(context.Background(), g.SHA256); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deleteDown = true
	f.mu.Unlock()
	f.tick(t)
	usage, err := f.s.Usage(context.Background())
	if err != nil || usage.RequestedBytes != 3 || usage.VerifiedBytes != 3 || usage.OccupiedByteSeconds != 10800 || usage.DurableBytes != 0 {
		t.Fatal("usage lost separate requested, accepted, occupied or database bytes")
	}
	if f.number(t, `SELECT count(*) FROM blob_usage_events WHERE kind='delete_confirmed'`) != 0 {
		t.Fatal("failed delete counted as confirmation")
	}
	f.env.Now.Advance(time.Hour)
	f.mu.Lock()
	f.deleteDown = false
	f.mu.Unlock()
	f.tick(t)
	usage, err = f.s.Usage(context.Background())
	if err != nil || usage.OccupiedByteSeconds != 21600 {
		t.Fatal("logical purge ended physical occupancy early")
	}
	f.env.Now.Advance(time.Hour)
	f.tick(t)
	usage, err = f.s.Usage(context.Background())
	if err != nil || usage.OccupiedByteSeconds != 21600 {
		t.Fatal("confirmed deletion kept accumulating occupancy")
	}
	heads, deletes, failed := 0, 0, 0
	for _, event := range operations {
		if event.BlobID != g.BlobID || event.EventID == "" || event.At.IsZero() {
			t.Fatal("operation hook missing sanitized identity")
		}
		switch event.Method {
		case "HEAD":
			heads++
		case "DELETE":
			deletes++
		default:
			t.Fatal("invented object operation")
		}
		if event.Status == "unavailable" {
			failed++
		}
	}
	if heads != 1 || deletes != 2 || failed != 1 {
		t.Fatalf("observed operations HEAD=%d DELETE=%d failed=%d", heads, deletes, failed)
	}
}

func TestR2UsageRetryAfterStubCollection(t *testing.T) {
	f := newTransfers(t)
	g := f.reserve(t, "retry-collected", 3)
	f.complete(t, g)
	event := blob.DownloadGrantEvent{EventID: ids.New(ids.Blo), BlobID: g.BlobID, CallerIdentity: f.env.OperatorPeer, Kind: "download_grant_issued", At: f.env.Now.Now()}
	inserted, err := f.s.RecordDownloadGrant(context.Background(), event)
	if err != nil || !inserted {
		t.Fatal("first issuance write failed")
	}
	f.env.Now.Advance(8 * 24 * time.Hour)
	f.tick(t)
	if f.number(t, `SELECT count(*) FROM blob_objects WHERE blob_id=?`, g.BlobID) != 0 {
		t.Fatal("stub was not collected")
	}
	f.restart(t)
	inserted, err = f.s.RecordDownloadGrant(context.Background(), event)
	if err != nil || inserted {
		t.Fatal("retry after restart and GC lost deduplication")
	}
	if f.number(t, `SELECT count(*) FROM blob_usage_events WHERE event_id=?`, event.EventID) != 1 {
		t.Fatal("retry duplicated metering")
	}
}

func TestR2UsageDurableBytesRemainSeparate(t *testing.T) {
	f := newTransfers(t)
	f.reserve(t, "requested-only", 3)
	err := f.env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		return blob.PutDurable(context.Background(), tx, "spill", hexSHA([]byte("durable")), "text/markdown", []byte("durable"))
	})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := f.s.Usage(context.Background())
	if err != nil || usage.DurableBytes != 7 || usage.RequestedBytes != 3 || usage.VerifiedBytes != 0 || usage.OccupiedByteSeconds != 0 {
		t.Fatal("durable content or pending requests charged as accepted R2 bytes")
	}
}
