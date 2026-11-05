package eventpayload_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func put(t *testing.T, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id, body string, seq int) {
	t.Helper()
	_, err := q.ExecContext(context.Background(), `INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES (?,'thr_test',?,'hub','message','now',?)`, id, seq, body)
	if err != nil {
		t.Fatal("insert message failed", err)
	}
}
func encode(t *testing.T, db *store.DB, id, wire string) eventpayload.StoredPayload {
	t.Helper()
	var p eventpayload.StoredPayload
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		var err error
		p, err = eventpayload.EncodeDelivery(context.Background(), tx, id, []byte(wire))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func roundtrip(t *testing.T, db *store.DB, p eventpayload.StoredPayload, wire string) {
	t.Helper()
	err := db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		got, err := eventpayload.HydrateBatch(context.Background(), q, []eventpayload.StoredPayload{p})
		if err != nil {
			return err
		}
		if len(got) != 1 || !bytes.Equal(got[0], []byte(wire)) {
			t.Fatal("wire bytes changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestExactCompositionNestedEscapedAndLegacyBytes(t *testing.T) {
	db := openDB(t)
	body := `{ "message_id" : "msg_test", "sender":"hub", "attachments":[], "content":{"text":"synthetic \\\" message key","nested":{"message":{},"attachments":[]}},"unknown":[1.50,1e3,"<b>"] }`
	put(t, db, "msg_test", body, 1)
	wire := " \n{\"thread\":{\"title\":\"send-time\",\"message\":{}},\"message\" : " + body + ", \"attempt\":1,\"delivery_id\":\"dly_test\",\"unknown\":null}\t"
	p := encode(t, db, "msg_test", wire)
	if p.Format != eventpayload.DeliveryRefV1 || p.MsgID != "msg_test" || p.SnapshotID != "" {
		t.Fatal("unexpected reference identity")
	}
	roundtrip(t, db, p, wire)
	// Small non-delivery snapshots are passed through byte for byte.
	inline := eventpayload.StoredPayload{Format: eventpayload.Inline, Data: []byte(" {\"receipt\": 1.50} \n")}
	roundtrip(t, db, inline, string(inline.Data))
}
func TestAttachmentPatchesAndSnapshotDedup(t *testing.T) {
	db := openDB(t)
	canonical := `{"message_id":"msg_00000000000000000000000001","attachments":[{"sha256":"abc"}],"text":"original"}`
	put(t, db, "msg_00000000000000000000000001", canonical, 1)
	embedded := `{"message_id":"msg_00000000000000000000000001","attachments":[ {"sha256":"abc","url":"https://synthetic.invalid/?token=frozen"} ],"text":"original"}`
	wire := `{"delivery_id":"dly_test","message":` + embedded + `,"thread":{"title":"before rename"}}`
	p := encode(t, db, "msg_00000000000000000000000001", wire)
	if p.SnapshotID != "" {
		t.Fatal("attachment override created snapshot")
	}
	roundtrip(t, db, p, wire)
	if !bytes.Contains(p.Data, []byte("attachments_json")) {
		t.Fatal("missing frozen attachment override")
	}
	// migration 0018 and legacy formatting may differ from today's Message.
	legacy := `{ "unknown":2.00, "sender_info":{"name":"before","kind":"hub"}, ` + snapshotMessage(snapshotMessageID)[1:]
	legacyWire := `{"message":` + legacy + `}`
	first := encode(t, db, snapshotMessageID, legacyWire)
	second := encode(t, db, snapshotMessageID, legacyWire)
	if first.SnapshotID == "" || first.SnapshotID != second.SnapshotID {
		t.Fatal("snapshot not deduplicated")
	}
	roundtrip(t, db, first, legacyWire)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM event_message_snapshots`).Scan(&n); err != nil || n != 1 {
		t.Fatal("snapshot count", n, err)
	}
	missingWire := `{"message":` + snapshotMessage("msg_00000000000000000000000002") + `}`
	missing := encode(t, db, "msg_00000000000000000000000002", missingWire)
	if missing.SnapshotID == "" {
		t.Fatal("missing canonical body requires snapshot")
	}
	roundtrip(t, db, missing, missingWire)
}

func TestSnapshotDedupPreservesExistingIdentity(t *testing.T) {
	db := openDB(t)
	body := snapshotMessage(snapshotMessageID)
	sum := sha256.Sum256([]byte(body))
	if _, err := db.Exec(`INSERT INTO event_message_snapshots(snapshot_id,msg_id,body_sha256,body) VALUES ('snap_existing',?,?,?)`, snapshotMessageID, hex.EncodeToString(sum[:]), body); err != nil {
		t.Fatal(err)
	}
	wire := `{"message":` + body + `}`
	first := encode(t, db, snapshotMessageID, wire)
	if first.SnapshotID != "snap_existing" {
		t.Fatal("dedup changed existing snapshot identity")
	}
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data,payload_format,msg_id,snapshot_id) VALUES ('peer_test','delivery','now',?,?,?,?)`, string(first.Data), first.Format, first.MsgID, first.SnapshotID); err != nil {
		t.Fatal(err)
	}
	second := encode(t, db, snapshotMessageID, wire)
	if second.SnapshotID != first.SnapshotID {
		t.Fatal("pinned snapshot not deduplicated")
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO event_message_snapshots SELECT * FROM event_message_snapshots`); err != nil {
		t.Fatal("identical snapshot replacement rejected", err)
	}
	roundtrip(t, db, first, wire)
	roundtrip(t, db, second, wire)
}
func TestNotConvertiblePreservesInline(t *testing.T) {
	db := openDB(t)
	for _, wire := range []string{`{`, `{"message":null}`, `{"message":{},"message":{}}`, `{"message":{"message_id":"msg_test","message_id":"msg_other"}}`, `{"message":{"message_id":"msg_other"}}`, `{"message":{"message_id":"msg_test","attachments":null}}`, `{"message":{"message_id":"msg_test","attachments":{}}}`, `{"message":{"message_id":"msg_test"},"mess\u0061ge":{}}`} {
		err := db.Tx(context.Background(), func(tx *sql.Tx) error {
			p, err := eventpayload.EncodeDelivery(context.Background(), tx, "msg_test", []byte(wire))
			var nc *eventpayload.NotConvertibleError
			if !errors.Is(err, eventpayload.ErrNotConvertible) || !errors.As(err, &nc) || nc.Reason == "" {
				t.Fatal("expected typed non-convertible result")
			}
			if p.Format != eventpayload.Inline || !bytes.Equal(p.Data, []byte(wire)) {
				t.Fatal("fallback discarded original")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDuplicateCriticalKeysPreserveInline(t *testing.T) {
	db := openDB(t)
	const body = `{"message_id":"msg_test","attachments":[]}`
	put(t, db, "msg_test", body, 1)
	for _, tc := range []struct{ name, wire string }{
		{"message_equal", `{"message":` + body + `,"message":` + body + `}`},
		{"message_escaped", `{"message":` + body + `,"mess\u0061ge":` + body + `}`},
		{"message_id_equal", `{"message":{"message_id":"msg_test","message_id":"msg_test","attachments":[]}}`},
		{"message_id_escaped", `{"message":{"message_id":"msg_test","message_\u0069d":"msg_test","attachments":[]}}`},
		{"attachments_equal", `{"message":{"message_id":"msg_test","attachments":[],"attachments":[]}}`},
		{"attachments_escaped", `{"message":{"message_id":"msg_test","attachments":[],"attachm\u0065nts":[]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Tx(context.Background(), func(tx *sql.Tx) error {
				p, err := eventpayload.EncodeDelivery(context.Background(), tx, "msg_test", []byte(tc.wire))
				var nc *eventpayload.NotConvertibleError
				if !errors.Is(err, eventpayload.ErrNotConvertible) || !errors.As(err, &nc) || nc.Reason != "duplicate_key" {
					t.Fatal("duplicate critical key did not yield duplicate_key")
				}
				if p.Format != eventpayload.Inline || !bytes.Equal(p.Data, []byte(tc.wire)) || p.MsgID != "" || p.SnapshotID != "" {
					t.Fatal("duplicate fallback changed inline bytes or identity")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDuplicateCompleteReferenceRejectedWithoutPartialPage(t *testing.T) {
	db := openDB(t)
	const body = `{"message_id":"msg_test","attachments":[]}`
	put(t, db, "msg_test", body, 1)
	wire := `{"message":` + body + `}`
	p := encode(t, db, "msg_test", wire)
	roundtrip(t, db, p, wire)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(p.Data, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		for _, escaped := range []bool{false, true} {
			name := key
			spelling := key
			if escaped {
				name += "_escaped"
				spelling = fmt.Sprintf(`\u%04x%s`, key[0], key[1:])
			}
			t.Run(name, func(t *testing.T) {
				duplicate := p
				duplicate.Data = []byte(string(p.Data[:len(p.Data)-1]) + `,"` + spelling + `":` + string(value) + `}`)
				err := db.ReadSnapshot(context.Background(), func(c *sql.Conn) error {
					q := &countingQuery{q: c}
					got, err := eventpayload.HydrateBatch(context.Background(), q, []eventpayload.StoredPayload{p, duplicate})
					var se *eventpayload.StorageError
					if !errors.As(err, &se) || se.Reason != "reference_invalid" || got != nil || q.calls != 0 {
						t.Fatal("complete duplicate reference accepted or returned a partial page")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestMalformedMissingAndCorruptBodiesNoPartialPage(t *testing.T) {
	db := openDB(t)
	body := `{"message_id":"msg_test","attachments":[],"text":"synthetic-secret"}`
	put(t, db, "msg_test", body, 1)
	p := encode(t, db, "msg_test", `{"message":`+body+`}`)
	cases := []eventpayload.StoredPayload{p, p, p, p, p, p, p, p}
	cases[0].Data = []byte(`{"msg_id":"synthetic-secret","msg_id":"other"}`)
	cases[1].MsgID = "other"
	cases[2].Format = "unknown"
	var r map[string]any
	if err := json.Unmarshal(p.Data, &r); err != nil {
		t.Fatal(err)
	}
	r["body_sha256"] = strings.Repeat("0", 64)
	cases[3].Data, _ = json.Marshal(r)
	r["body_sha256"] = nil
	cases[4].Data, _ = json.Marshal(r)
	if err := json.Unmarshal(p.Data, &r); err != nil {
		t.Fatal(err)
	}
	r["wire_sha256"] = strings.Repeat("0", 64)
	cases[5].Data, _ = json.Marshal(r)
	cases[6].SnapshotID = "missing"
	cases[7].BodySHA256 = strings.Repeat("0", 64)
	check := func(p eventpayload.StoredPayload) {
		var got [][]byte
		err := db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
			var err error
			got, err = eventpayload.HydrateBatch(context.Background(), q, []eventpayload.StoredPayload{{Format: eventpayload.Inline, Data: []byte(`{}`)}, p})
			return err
		})
		var se *eventpayload.StorageError
		if err == nil || !errors.As(err, &se) || got != nil {
			t.Fatal("expected storage error and no partial page")
		}
		if strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("body leaked in error")
		}
	}
	for _, p := range cases {
		check(p)
	}
	if _, err := db.Exec(`UPDATE messages SET body='{}' WHERE msg_id='msg_test'`); err != nil {
		t.Fatal(err)
	}
	check(p)
	if _, err := db.Exec(`DELETE FROM messages`); err != nil {
		t.Fatal(err)
	}
	check(p)
}

type countingQuery struct {
	q              *sql.Conn
	calls, maxKeys int
	beforeQuery    func()
}

func (q *countingQuery) QueryContext(ctx context.Context, s string, args ...any) (*sql.Rows, error) {
	if q.beforeQuery != nil {
		q.beforeQuery()
	}
	q.calls++
	if len(args) > q.maxKeys {
		q.maxKeys = len(args)
	}
	return q.q.QueryContext(ctx, s, args...)
}
func TestBatchQueriesInputOrderAndBoundedBodyCache(t *testing.T) {
	db := openDB(t)
	const count = 405
	refs := make([]eventpayload.StoredPayload, 0, count*2)
	wires := make([]string, 0, count*2)
	totalBodyBytes := 0
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("msg_%d", i)
			body := fmt.Sprintf(`{"message_id":%q,"attachments":[],"text":%q}`, id, strings.Repeat("x", 24000))
			totalBodyBytes += len(body)
			put(t, tx, id, body, i+1)
			wire := `{"message":` + body + `}`
			p, err := eventpayload.EncodeDelivery(context.Background(), tx, id, []byte(wire))
			if err != nil {
				return err
			}
			refs = append(refs, p)
			wires = append(wires, wire)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reverse order and duplicates exercise grouping independently of SQL order.
	for i, j := 0, len(refs)-1; i < j; i, j = i+1, j-1 {
		refs[i], refs[j] = refs[j], refs[i]
		wires[i], wires[j] = wires[j], wires[i]
	}
	refs = append(refs, refs...)
	wires = append(wires, wires...)
	const bodyCeiling = 8 << 20
	if totalBodyBytes <= bodyCeiling {
		t.Fatal("fixture must exceed the working-body ceiling")
	}
	err = db.ReadSnapshot(context.Background(), func(c *sql.Conn) error {
		current, peak, acquisitions, releases, acquiredBytes := 0, 0, 0, 0, 0
		observe := func(retained int) {
			if retained > 0 {
				if current != 0 {
					t.Fatal("previous streaming body was not released")
				}
				acquisitions++
				acquiredBytes += retained
			} else {
				releases++
			}
			current = retained
			peak = max(peak, current)
			if current > bodyCeiling {
				t.Fatal("working-body retention exceeded 8 MiB")
			}
		}
		// Keep the observer across calls. Outputs intentionally remain live while
		// checking that only the working bodies are counted and released.
		var previous [][]byte
		for call := 1; call <= 2; call++ {
			q := &countingQuery{q: c, beforeQuery: func() {
				if current != 0 {
					t.Fatal("working bodies retained across query chunks")
				}
			}}
			got, err := eventpayload.HydrateBatchWithBodyObserverForTest(context.Background(), q, refs, observe)
			if err != nil {
				return err
			}
			if q.calls != 3 || q.maxKeys > 200 {
				t.Fatalf("queries=%d max keys=%d", q.calls, q.maxKeys)
			}
			if current != 0 || peak == 0 || acquisitions != call*count || releases != acquisitions || acquiredBytes != call*totalBodyBytes {
				t.Fatalf("body retention current=%d peak=%d acquired=%d released=%d bytes=%d", current, peak, acquisitions, releases, acquiredBytes)
			}
			if len(got) != len(wires) {
				t.Fatal("result size")
			}
			for i := range got {
				if string(got[i]) != wires[i] || (previous != nil && !bytes.Equal(previous[i], got[i])) {
					t.Fatal("batch order or bytes changed")
				}
			}
			previous = got
		}
		t.Logf("unique_body_bytes=%d peak_retained_body_bytes=%d acquisitions=%d releases=%d", totalBodyBytes, peak, acquisitions, releases)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkingBodyReleasedOnHydrationError(t *testing.T) {
	db := openDB(t)
	const body = `{"message_id":"msg_test","attachments":[]}`
	put(t, db, "msg_test", body, 1)
	wire := `{"message":` + body + `}`
	p := encode(t, db, "msg_test", wire)
	corrupt := p
	var fields map[string]any
	if err := json.Unmarshal(p.Data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["body_sha256"] = strings.Repeat("0", 64)
	corrupt.Data, _ = json.Marshal(fields)
	err := db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		current, acquisitions, releases := 0, 0, 0
		observe := func(retained int) {
			current = retained
			if retained > 0 {
				acquisitions++
			} else {
				releases++
			}
		}
		got, err := eventpayload.HydrateBatchWithBodyObserverForTest(context.Background(), q, []eventpayload.StoredPayload{p, corrupt}, observe)
		var se *eventpayload.StorageError
		if !errors.As(err, &se) || se.Reason != "body_hash_mismatch" || got != nil || current != 0 || acquisitions != 1 || releases != 1 {
			t.Fatal("error returned partial output or retained working body")
		}
		got, err = eventpayload.HydrateBatchWithBodyObserverForTest(context.Background(), q, []eventpayload.StoredPayload{p}, observe)
		if err != nil || len(got) != 1 || string(got[0]) != wire || current != 0 || acquisitions != 2 || releases != 2 {
			t.Fatal("working body leaked across error and subsequent call")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPassedWriterTransactionAndFourConnectionPool(t *testing.T) {
	db := openDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		put(t, tx, "msg_tx", `{"message_id":"msg_tx","attachments":[]}`, 1)
		p, err := eventpayload.EncodeDelivery(ctx, tx, "msg_tx", []byte(`{"message":{"message_id":"msg_tx","attachments":[]}}`))
		if err != nil {
			return err
		}
		// Occupy all three remaining connections: any hidden pool lookup deadlocks.
		var wg sync.WaitGroup
		ready := make(chan struct{}, 3)
		release := make(chan struct{})
		errs := make(chan error, 3)
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- db.ReadSnapshot(ctx, func(q *sql.Conn) error {
					var n int
					if err := q.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&n); err != nil {
						return err
					}
					ready <- struct{}{}
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
		}
		defer func() { close(release); wg.Wait() }()
		for i := 0; i < 3; i++ {
			select {
			case <-ready:
			case err := <-errs:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		got, err := eventpayload.HydrateBatch(ctx, tx, []eventpayload.StoredPayload{p})
		if err == nil && (len(got) != 1 || string(got[0]) != `{"message":{"message_id":"msg_tx","attachments":[]}}`) {
			t.Fatal("uncommitted bytes unavailable")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotHashMatchVerifiesFullBytes(t *testing.T) {
	db := openDB(t)
	body := snapshotMessage(snapshotMessageID)
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	if _, err := db.Exec(`INSERT INTO event_message_snapshots(snapshot_id,msg_id,body_sha256,body) VALUES ('snap_bad',?,?,'{}')`, snapshotMessageID, hash); err != nil {
		t.Fatal(err)
	}
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := eventpayload.EncodeDelivery(context.Background(), tx, snapshotMessageID, []byte(`{"message":`+body+`}`))
		return err
	})
	var se *eventpayload.StorageError
	if !errors.As(err, &se) || se.Reason != "snapshot_hash_collision" {
		t.Fatal("hash match did not verify complete bytes")
	}
	if strings.Contains(err.Error(), "synthetic-original") {
		t.Fatal("error contains body")
	}
}
func TestHydrateSnapshotConsistentDuringConcurrentDelete(t *testing.T) {
	db := openDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := `{"message_id":"msg_test","attachments":[]}`
	put(t, db, "msg_test", body, 1)
	wire := `{"message":` + body + `}`
	p := encode(t, db, "msg_test", wire)
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data,payload_format,msg_id) VALUES ('peer_test','delivery','now',?,?,?)`, string(p.Data), p.Format, p.MsgID); err != nil {
		t.Fatal(err)
	}
	err := db.ReadSnapshot(ctx, func(q *sql.Conn) error {
		results, err := q.QueryContext(ctx, `SELECT data,payload_format,msg_id FROM events`)
		if err != nil {
			return err
		}
		var rows []eventpayload.StoredPayload
		for results.Next() {
			var p eventpayload.StoredPayload
			if err := results.Scan(&p.Data, &p.Format, &p.MsgID); err != nil {
				results.Close()
				return err
			}
			rows = append(rows, p)
		}
		err = results.Err()
		results.Close()
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() {
			done <- db.Tx(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `DELETE FROM events`); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `DELETE FROM messages`)
				return err
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		got, err := eventpayload.HydrateBatch(ctx, q, rows)
		if err == nil && (len(got) != 1 || string(got[0]) != wire) {
			t.Fatal("lost read snapshot during delete")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]byte
	err = db.ReadSnapshot(ctx, func(q *sql.Conn) error {
		var err error
		got, err = eventpayload.HydrateBatch(ctx, q, []eventpayload.StoredPayload{p})
		return err
	})
	if got != nil || err == nil {
		t.Fatal("new read failed to observe deletion")
	}
}
func TestSnapshotQueriesChunkedOnlyWhenNeeded(t *testing.T) {
	db := openDB(t)
	var refs []eventpayload.StoredPayload
	var wires []string
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 201; i++ {
			id := fmt.Sprintf("msg_%026d", i)
			wire := `{"message":` + snapshotMessage(id) + `}`
			p, err := eventpayload.EncodeDelivery(context.Background(), tx, id, []byte(wire))
			if err != nil {
				return err
			}
			refs = append(refs, p)
			wires = append(wires, wire)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.ReadSnapshot(context.Background(), func(c *sql.Conn) error {
		q := &countingQuery{q: c}
		got, err := eventpayload.HydrateBatch(context.Background(), q, refs)
		if err != nil {
			return err
		}
		if q.calls != 2 || q.maxKeys != 200 {
			t.Fatal("snapshot lookup chunk count", q.calls, q.maxKeys)
		}
		for i := range wires {
			if string(got[i]) != wires[i] {
				t.Fatal("snapshot bytes changed")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestExistingDeliveryHashChecked(t *testing.T) {
	db := openDB(t)
	body := `{"message_id":"msg_test","attachments":[]}`
	put(t, db, "msg_test", body, 1)
	wire := `{"delivery_id":"dly_test","message":` + body + `}`
	const insert = `INSERT INTO deliveries(dly_id,msg_id,thr_id,recipient,priority,state,attempts_left,expires_at,created_at,updated_at,body_sha256) VALUES ('dly_test','msg_test','thr_test','peer_test','wake','queued',8,'later','now','now',?)`
	if _, err := db.Exec(insert, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := eventpayload.EncodeDelivery(context.Background(), tx, "msg_test", []byte(wire))
		return err
	})
	var se *eventpayload.StorageError
	if !errors.As(err, &se) || se.Reason != "delivery_hash_mismatch" {
		t.Fatal("existing delivery hash ignored")
	}
	sum := sha256.Sum256([]byte(wire))
	if _, err := db.Exec(`UPDATE deliveries SET body_sha256=?`, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	p := encode(t, db, "msg_test", wire)
	p.BodySHA256 = hex.EncodeToString(sum[:])
	roundtrip(t, db, p, wire)
}
func TestInvalidUTF8StaysInline(t *testing.T) {
	db := openDB(t)
	wire := append([]byte(`{"message":{"message_id":"msg_test"},"legacy":"`), 255)
	wire = append(wire, []byte(`"}`)...)
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		p, err := eventpayload.EncodeDelivery(context.Background(), tx, "msg_test", wire)
		if !errors.Is(err, eventpayload.ErrNotConvertible) || p.Format != eventpayload.Inline || !bytes.Equal(p.Data, wire) {
			t.Fatal("invalid UTF-8 silently normalized")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotsPinnedUntilReferencingEventsDeleted(t *testing.T) {
	db := openDB(t)
	wire := `{"message":` + snapshotMessage(snapshotMessageID) + `}`
	p := encode(t, db, snapshotMessageID, wire)
	if _, err := db.Exec(`INSERT INTO events(owner,type,at,data,payload_format,msg_id,snapshot_id) VALUES ('peer_test','delivery','now',?,?,?,?)`, string(p.Data), p.Format, p.MsgID, p.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM event_message_snapshots`); err == nil {
		t.Fatal("deleted snapshot with retained reference")
	}
	if _, err := db.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM event_message_snapshots`); err != nil {
		t.Fatal("unpinned snapshot could not be removed", err)
	}
}

func TestMixedFormatsHydrateInInputOrder(t *testing.T) {
	db := openDB(t)
	body := snapshotMessage(snapshotMessageID)
	put(t, db, snapshotMessageID, body, 1)
	wire := `{"message":` + body + `}`
	canonical := encode(t, db, snapshotMessageID, wire)
	legacyWire := `{"message":{ "legacy":1.50, ` + body[1:] + `}`
	snapshot := encode(t, db, snapshotMessageID, legacyWire)
	inline := eventpayload.StoredPayload{Format: eventpayload.Inline, Data: []byte(` {"receipt":true} `)}
	refs := []eventpayload.StoredPayload{snapshot, inline, canonical, snapshot, canonical}
	wants := []string{legacyWire, string(inline.Data), wire, legacyWire, wire}
	err := db.ReadSnapshot(context.Background(), func(c *sql.Conn) error {
		q := &countingQuery{q: c}
		got, err := eventpayload.HydrateBatch(context.Background(), q, refs)
		if err != nil {
			return err
		}
		if q.calls != 2 {
			t.Fatal("mixed batch must load canonical and snapshot once", q.calls)
		}
		for i := range wants {
			if string(got[i]) != wants[i] {
				t.Fatal("mixed batch changed bytes or order")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a corrupt restored snapshot after its event pin is removed.
	if _, err := db.Exec(`DELETE FROM event_message_snapshots`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO event_message_snapshots(snapshot_id,msg_id,body_sha256,body) VALUES (?,?,?,?)`, snapshot.SnapshotID, snapshot.MsgID, strings.Repeat("0", 64), body); err != nil {
		t.Fatal(err)
	}
	err = db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		got, err := eventpayload.HydrateBatch(context.Background(), q, []eventpayload.StoredPayload{inline, snapshot})
		if got != nil {
			t.Fatal("corrupt snapshot returned partial page")
		}
		return err
	})
	var se *eventpayload.StorageError
	if !errors.As(err, &se) || se.Reason != "snapshot_hash_mismatch" {
		t.Fatal("corrupt snapshot metadata accepted")
	}
	if _, err := db.Exec(`DELETE FROM event_message_snapshots`); err != nil {
		t.Fatal(err)
	}
	err = db.ReadSnapshot(context.Background(), func(q *sql.Conn) error {
		got, err := eventpayload.HydrateBatch(context.Background(), q, []eventpayload.StoredPayload{inline, snapshot})
		if got != nil {
			t.Fatal("missing snapshot returned partial page")
		}
		return err
	})
	if !errors.As(err, &se) || se.Reason != "body_missing" {
		t.Fatal("missing snapshot accepted")
	}
}
