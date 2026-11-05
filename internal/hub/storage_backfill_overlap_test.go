package hub_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/hub"
	"modernc.org/sqlite"
)

func TestBackfillConcurrentTrafficDuringConversionTransaction(t *testing.T) {
	env, fs := backfillEnv(t) // A disposable t.TempDir database and FakeClock.
	h := env.Hub
	h.Cfg.DeliveryReferences = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	active, release, sendBegin := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce, beginOnce sync.Once
	var watchSend atomic.Bool
	var workers sync.WaitGroup
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		unblock()
		cancel()
		workers.Wait() // Also join on assertion failures, before testhub closes DB.
	}()

	// Observe real SQLite transaction boundaries on every physical connection,
	// without global driver hooks or production instrumentation. Hold COMMIT
	// compilation after the event conversion and checkpoint writes have run.
	// Authorizers only signal/wait; they never issue SQL on their connection.
	const poolSize = 4
	h.DB.SetMaxOpenConns(poolSize)
	h.DB.SetMaxIdleConns(poolSize)
	var conns []*sql.Conn
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()
	for range poolSize {
		conn, err := h.DB.Conn(ctx)
		if err != nil {
			t.Fatal("could not acquire observer connection")
		}
		conns = append(conns, conn)
		if err := conn.Raw(func(raw any) error {
			registerer, ok := raw.(sqlite.AuthorizerRegisterer)
			if !ok {
				return fmt.Errorf("SQLite authorizer unavailable")
			}
			return registerer.RegisterAuthorizer(func(action sqlite.AuthorizerActionCode, arg1, _, _, _ string) sqlite.AuthorizerReturnCode {
				if action == sqlite.AuthTransaction {
					switch arg1 {
					case "COMMIT":
						holdOnce.Do(func() {
							close(active)
							select {
							case <-release:
							case <-ctx.Done():
							}
						})
					case "BEGIN":
						if watchSend.Load() {
							beginOnce.Do(func() { close(sendBegin) })
						}
					}
				}
				return sqlite.AuthorizerOK
			})
		}); err != nil {
			t.Fatal("could not install transaction observer")
		}
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatal("could not return observer connection")
		}
	}
	conns = nil

	type sliceResult struct {
		progress hub.BackfillProgress
		elapsed  time.Duration
		err      error
	}
	sliceDone := make(chan sliceResult, 1)
	workers.Go(func() {
		start := time.Now()
		p, err := h.BackfillDeliverySlice(ctx, hub.BackfillLimits{Rows: 1})
		sliceDone <- sliceResult{p, time.Since(start), err}
	})
	select {
	case <-active:
	case <-sliceDone:
		t.Fatal("conversion returned before the active-transaction barrier")
	case <-ctx.Done():
		t.Fatal("conversion did not reach its commit barrier")
	}

	owners := []string{guardA, guardB, guardC}
	expected := make([]envelope.Event, len(fs))
	for i, f := range fs {
		expected[i] = envelope.Event{Cursor: fmt.Sprintf("e1.%d", i+1), Type: f.Type, At: f.At, Data: json.RawMessage(f.Data)}
	}
	readExact := func() []envelope.Event {
		t.Helper()
		page, err := h.Log.Read(ctx, owners, eventlog.Cursor{Epoch: 1}, 100)
		if err != nil || len(page) != len(expected) {
			t.Fatal("public replay failed or returned an incomplete page")
		}
		for i, want := range expected {
			got := page[i]
			if got.Cursor != want.Cursor || got.Type != want.Type || got.At != want.At || !bytes.Equal(got.Data, want.Data) {
				t.Fatalf("public replay changed sequence, metadata or exact bytes at index=%d", i)
			}
		}
		return page
	}
	readExact() // Must finish while the conversion and checkpoint are uncommitted.
	var format, data string
	if err := h.DB.QueryRowContext(ctx, `SELECT payload_format,data FROM events WHERE seq=1`).Scan(&format, &data); err != nil || format != eventpayload.Inline || data != fs[0].Data {
		t.Fatal("reader observed an uncommitted event conversion")
	}
	var checkpointCount int
	if err := h.DB.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key='delivery_backfill_v1'`).Scan(&checkpointCount); err != nil || checkpointCount != 0 {
		t.Fatal("reader observed an uncommitted checkpoint")
	}

	type sendResult struct {
		response *hub.SendResponse
		err      error
	}
	sendDone := make(chan sendResult, 1)
	watchSend.Store(true)
	workers.Go(func() {
		// Reuse the frozen send-byte fixture, including escaped Unicode, rather
		// than constructing expected new bytes from the reference being tested.
		request, err := json.Marshal(map[string]any{
			"idempotency_key": "backfill-overlap", "recipients": []string{guardB}, "priority": "wake",
			"content": map[string]any{"text": "Hello 一 🛰 \"quoted\" \\ newline\n<>&\u2028"},
		})
		if err != nil {
			sendDone <- sendResult{err: err}
			return
		}
		response, _, err := h.Send(ctx, auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}, request, "http_send")
		sendDone <- sendResult{response, err}
	})
	select {
	case <-sendBegin: // SQLite is compiling the send's actual BEGIN IMMEDIATE.
	case <-sendDone:
		t.Fatal("send returned without attempting a transaction")
	case <-ctx.Done():
		t.Fatal("send never attempted a transaction while conversion held the writer")
	}
	watchSend.Store(false)
	readExact() // A public reader progresses while both writers are in flight.
	select {
	case <-sendDone:
		t.Fatal("send completed before the conversion released the writer")
	default:
	}
	unblock()
	converted := <-sliceDone
	result := <-sendDone
	workers.Wait()
	p := converted.progress
	if converted.err != nil || p.Converted != 1 || p.Skipped != 0 || p.Failed != 0 || p.Done || p.HighSeq != int64(len(fs)) || p.LastSeq != 1 || len(p.Seqs) != 1 || p.Seqs[0] != 1 {
		t.Fatal("overlapped slice did not commit exactly the first captured event")
	}
	if result.err != nil || result.response == nil || len(result.response.Deliveries) != 1 {
		t.Fatal("send did not complete after the conversion committed")
	}
	if err := h.DB.QueryRowContext(ctx, `SELECT payload_format FROM events WHERE seq=1`).Scan(&format); err != nil || format != eventpayload.DeliveryRefV1 {
		t.Fatal("held transaction did not commit its event conversion")
	}

	delivery := result.response.Deliveries[0]
	var seq int64
	var at, hash, msgID string
	if err := h.DB.QueryRowContext(ctx, `SELECT e.seq,e.at,e.payload_format,e.msg_id,d.body_sha256 FROM events e JOIN deliveries d ON d.event_seq=e.seq WHERE d.dly_id=?`, delivery.DeliveryID).Scan(&seq, &at, &format, &msgID, &hash); err != nil || seq != p.HighSeq+1 || format != eventpayload.DeliveryRefV1 || msgID != result.response.MessageID || delivery.Recipient != guardB {
		t.Fatal("send sequence/reference identity changed or entered the captured range")
	}
	newPage, err := h.Log.Read(ctx, []string{guardB}, eventlog.Cursor{Epoch: 1, Seq: p.HighSeq}, 100)
	if err != nil || len(newPage) != 1 || newPage[0].Cursor != fmt.Sprintf("e1.%d", seq) || newPage[0].Type != "delivery" || newPage[0].At != at {
		t.Fatal("new delivery public replay lost its sequence or metadata")
	}
	guardSendTemplate(t, "send-nil_optional.json", newPage[0].Data, map[string]string{
		"$MESSAGE_ID": result.response.MessageID, "$DELIVERY_ID": delivery.DeliveryID, "$THREAD_ID": result.response.ThreadID,
	})
	expected = append(expected, newPage[0])
	readExact()

	worst := converted.elapsed
	for range 100 {
		start := time.Now()
		p = slice(t, h, hub.BackfillLimits{Rows: 1})
		worst = max(worst, time.Since(start))
		if p.HighSeq != converted.progress.HighSeq || p.Failed != 0 || p.Skipped != 0 {
			t.Fatal("resumption changed captured high-water or introduced exceptions")
		}
		if p.Done {
			break
		}
	}
	if !p.Done || p.LastSeq != p.HighSeq {
		t.Fatal("backfill did not finish at its original captured high-water")
	}
	readExact()
	assertBackfillBytes(t, h, fs)
	for i, f := range fs {
		if f.Type != "delivery" {
			continue
		}
		var actualHash string
		if err := h.DB.QueryRowContext(ctx, `SELECT body_sha256 FROM deliveries WHERE event_seq=?`, i+1).Scan(&actualHash); err != nil || actualHash != f.SHA256 {
			t.Fatalf("original delivery sequence/hash changed at seq=%d", i+1)
		}
	}
	var actualHash string
	if err := h.DB.QueryRowContext(ctx, `SELECT body_sha256 FROM deliveries WHERE event_seq=?`, seq).Scan(&actualHash); err != nil || actualHash != hash || fmt.Sprintf("%x", sha256.Sum256(newPage[0].Data)) != hash {
		t.Fatal("new delivery sequence/hash changed after resumption")
	}
	var integrity string
	if err := h.DB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("integrity check failed")
	}
	rows, err := h.DB.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal("foreign key check failed")
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("foreign key violation")
	}
	t.Logf("worst_slice_duration_including_barrier=%s required_headroom_bytes=%d", worst, p.RequiredHeadroomBytes)
}
