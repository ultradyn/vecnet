//go:build linux

package main

// This driver extends the real bench hub with deterministic, in-process core
// measurements. It does not launch workers or contact any route. Every database
// and blob is disposable and must be created by the build-box wrapper.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/webhook"
)

type compactionProfile struct {
	Fanout, TextBytes int
	Subscriptions     int
	Varied, Reused    bool
	Attachment        string
}
type compactionStage struct {
	Name                string
	Storage             storage
	Stats               store.StorageStats
	Checkpoint          store.CheckpointProgress
	PayloadBytes        map[string]int64
	EventFormats        map[string]int64
	ReferenceBytes      map[string]float64
	ObjectOverflowBytes map[string]int64
}
type compactionRow struct {
	BackfillWallMaxNanos                           int64
	SendSamples, ReadSamples, PushSamples          int
	Profile                                        compactionProfile
	References                                     bool
	Sends                                          int
	Schema                                         string
	Send, Read, Push                               map[string]float64
	SendCPU, ReadCPU, PushCPU                      float64
	SendAllocBytes, ReadAllocBytes, PushAllocBytes uint64
	Stages                                         []compactionStage
	Plans                                          map[string][]string
	BackfillMaxNanos                               int64
	RawBytesVerified                               int
}

func compactionCount(db *store.DB, q string, args ...any) (n int64, err error) {
	err = db.QueryRow(q, args...).Scan(&n)
	return
}
func compactionCapture(db *store.DB, dir, name string) (s compactionStage, err error) {
	s.Name = name
	s.PayloadBytes = map[string]int64{}
	s.EventFormats = map[string]int64{}
	s.ObjectOverflowBytes = map[string]int64{}
	// PASSIVE is measured before the harness's existing quiescent TRUNCATE probe.
	r, err := db.Maintain(context.Background(), store.MaintenanceOptions{Target: time.Second})
	if err != nil {
		return s, err
	}
	s.Stats = r.Before
	s.Checkpoint = r.Checkpoint
	s.Storage, err = readStorage(db, dir)
	if err != nil {
		return s, err
	}
	for key, q := range map[string]string{
		"messages.body":           "SELECT coalesce(sum(length(CAST(body AS BLOB))),0) FROM messages",
		"events.data":             "SELECT coalesce(sum(length(CAST(data AS BLOB))),0) FROM events",
		"snapshots.body":          "SELECT coalesce(sum(length(CAST(body AS BLOB))),0) FROM event_message_snapshots",
		"outbox.body":             "SELECT coalesce(sum(length(CAST(body AS BLOB))),0) FROM event_outbox",
		"idempotency.response":    "SELECT coalesce(sum(length(CAST(response AS BLOB))),0) FROM idempotency",
		"delivery.scalar_columns": "SELECT coalesce(sum(length(dly_id)+length(msg_id)+length(thr_id)+length(recipient)+length(also_via)+length(priority)+length(state)+length(expires_at)+length(created_at)+length(updated_at)+length(body_sha256)),0) FROM deliveries",
		"audit.details":           "SELECT coalesce(sum(length(CAST(detail AS BLOB))),0) FROM audit",
	} {
		n, e := compactionCount(db, q)
		if e != nil {
			return s, e
		}
		s.PayloadBytes[key] = n
	}
	rows, err := db.Query("SELECT payload_format,count(*) FROM events GROUP BY payload_format")
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var k string
		var n int64
		if err = rows.Scan(&k, &n); err != nil {
			break
		}
		s.EventFormats[k] = n
	}
	e := rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if e != nil {
		return s, e
	}
	var lengths []float64
	rows, err = db.Query("SELECT length(CAST(data AS BLOB)) FROM events WHERE payload_format='delivery_ref_v1'")
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var n float64
		if err = rows.Scan(&n); err != nil {
			break
		}
		lengths = append(lengths, n)
	}
	e = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if e != nil {
		return s, e
	}
	// percentiles is shared with bench/send.go; these are bytes, not milliseconds.
	s.ReferenceBytes = map[string]float64{}
	for k, v := range percentiles(lengths) {
		s.ReferenceBytes[strings.Replace(k, "_ms", "_bytes", 1)] = v
	}
	rows, err = db.Query("SELECT name,sum(pgsize) FROM dbstat WHERE pagetype='overflow' GROUP BY name")
	if err == nil {
		for rows.Next() {
			var k string
			var n int64
			if err = rows.Scan(&k, &n); err != nil {
				break
			}
			s.ObjectOverflowBytes[k] = n
		}
		e = rows.Err()
		rows.Close()
		if err != nil {
			return s, err
		}
		if e != nil {
			return s, e
		}
	} else if !strings.Contains(err.Error(), "no such table") && !strings.Contains(err.Error(), "no such module") {
		return s, err
	}
	return s, nil
}
func compactionPlans(db *store.DB, recipient, thread, at string) (map[string][]string, error) {
	// Full ClaimDue predecessor predicate copied from internal/hub/deliveries.go.
	queries := map[string]struct {
		sql  string
		args []any
	}{
		"claim_due":     {`SELECT d.dly_id,d.recipient,d.thr_id,d.route_id,r.url,r.kind,d.attempt,e.data,e.payload_format,coalesce(e.msg_id,''),coalesce(e.snapshot_id,''),d.body_sha256 FROM deliveries d JOIN routes r ON r.route_id=d.route_id JOIN events e ON e.seq=d.event_seq WHERE d.state IN ('queued','pushed') AND d.priority!='hold' AND d.held=0 AND r.verified_at IS NOT NULL AND (d.next_attempt_at IS NULL OR d.next_attempt_at<=?1) AND d.expires_at>?1 AND NOT EXISTS(SELECT 1 FROM deliveries p WHERE p.recipient=d.recipient AND p.thr_id=d.thr_id AND p.event_seq<d.event_seq AND p.state IN ('queued','pushed') AND p.priority!='hold' AND p.held=0 AND p.route_id IS NOT NULL) ORDER BY d.event_seq LIMIT 100`, []any{at}},
		"inbox":         {`SELECT dly_id,msg_id FROM deliveries WHERE recipient=? AND dly_id<? ORDER BY dly_id DESC LIMIT 50`, []any{recipient, "dly_zzzzzzzzzzzzzzzzzzzzzzzzzz"}},
		"unread":        {`SELECT count(*) FROM deliveries WHERE recipient=? AND read_at IS NULL`, []any{recipient}},
		"history":       {`SELECT body FROM messages WHERE thr_id=? AND seq>? ORDER BY seq LIMIT 100`, []any{thread, 0}},
		"purge":         {`SELECT seq,length(data) FROM events WHERE owner=? AND seq>? ORDER BY seq LIMIT 100`, []any{recipient, 0}},
		"idempotency":   {`SELECT rowid FROM idempotency WHERE created_at<? ORDER BY created_at LIMIT 100`, []any{at}},
		"message_gc":    {`SELECT msg_id FROM messages WHERE created_at<=? ORDER BY created_at,msg_id LIMIT 100`, []any{at}},
		"reference_pin": {`SELECT 1 FROM events WHERE msg_id=? LIMIT 1`, []any{"msg_synthetic"}},
	}
	addCompactionPlans(queries, recipient, thread, at)
	plans := map[string][]string{}
	for k, q := range queries {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+q.sql, q.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var a, b, c int
			var detail string
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				break
			}
			plans[k] = append(plans[k], detail)
		}
		e := rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if e != nil {
			return nil, e
		}
	}
	return plans, nil
}
func runCompactionProfile(p compactionProfile, references bool, n int) (out compactionRow, err error) {
	if os.Getenv("VECNET_BB") != "1" {
		return out, fmt.Errorf("compaction benchmark requires build box")
	}
	dir, err := os.MkdirTemp("", "vecnet-compaction-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		return out, err
	}
	defer db.Close()
	cfg := hub.DefaultConfig()
	cfg.DataDir = dir
	cfg.PublicURL = "https://synthetic.example"
	cfg.DeliveryReferences = references
	cfg.BackfillEnabled = true
	cfg.ThreadBudget = n + 10
	cfg.SendsPerMin = 600
	cfg.ThreadsPerHour = 200
	cfg.MessageHistoryRetention = 0
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	h, err := hub.New(db, cfg, func() time.Time { return now })
	if err != nil {
		return out, err
	}
	h.Now = func() time.Time { return now }
	at := envelope.FormatTime(now)
	sender := "peer_00000000000000000000000000"
	prn := "prn_00000000000000000000000000"
	var recipients []string
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		if _, e := tx.Exec("INSERT INTO principals(prn_id,name,created_at) VALUES(?,'benchmark',?)", prn, at); e != nil {
			return e
		}
		for i := 0; i <= p.Fanout; i++ {
			id := fmt.Sprintf("peer_%026d", i)
			if i > 0 {
				recipients = append(recipients, id)
			}
			if _, e := tx.Exec("INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at,sends_per_min,threads_per_hour) VALUES(?,?,'synthetic','session',?,600,200)", id, prn, at); e != nil {
				return e
			}
			if i > 0 {
				route := fmt.Sprintf("route_%026d", i)
				if _, e := tx.Exec("INSERT INTO routes(route_id,owner_kind,owner_id,url,verified_at,created_at) VALUES(?,'peer',?,'https://synthetic.example/never-contacted',?,?)", route, id, at, at); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	out = compactionRow{Profile: p, References: references, Sends: n}
	if err = db.QueryRow("SELECT group_concat(name,',') FROM (SELECT name FROM schema_migrations ORDER BY name)").Scan(&out.Schema); err != nil {
		return out, err
	}
	capture := func(name string) error {
		s, e := compactionCapture(db, dir, name)
		if e == nil {
			out.Stages = append(out.Stages, s)
		}
		return e
	}
	if err = capture("empty"); err != nil {
		return out, err
	}
	var worker *events.Worker
	var callbackOK atomic.Bool
	if p.Subscriptions > 0 {
		cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if callbackOK.Load() {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(503)
			}
		}))
		defer cb.Close()
		st := events.NewStore(h)
		for _, peer := range recipients {
			for sub := 0; sub < p.Subscriptions; sub++ {
				v, _, e := st.Upsert(context.Background(), events.Subscription{PrincipalKind: "vecnet", PrincipalID: peer, PeerID: peer, Name: events.Name, URL: fmt.Sprintf("%s/%s/%d", cb.URL, peer, sub), Arguments: map[string]any{}, Cursor: eventlog.Cursor{Epoch: h.Log.Epoch()}}, webhook.NewSecret(), 0, time.Time{})
				if e != nil {
					return out, e
				}
				if e = st.MarkVerified(context.Background(), v.ID); e != nil {
					return out, e
				}
			}
		}
		worker = &events.Worker{Hub: h, Store: st, Client: cb.Client(), Now: h.Now}
	}
	var attachments []envelope.Attachment
	if p.Attachment != "" {
		text := strings.Repeat("synthetic-attachment", 200)
		sha := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		if p.Attachment == "hosted" {
			if _, err = h.Blobs.Put(context.Background(), sender, strings.NewReader(text), "text/plain", 0); err != nil {
				return out, err
			}
		}
		attachments = []envelope.Attachment{{SHA256: sha, Size: int64(len(text)), MediaType: "text/plain", OriginURL: "https://synthetic.example/attachment"}}
	}
	rng := rand.New(rand.NewSource(8))
	alphabet := []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 <>/" + string([]byte{34, 92, 10}))
	var times []float64
	var mem0, mem1 runtime.MemStats
	runtime.ReadMemStats(&mem0)
	cpu0 := readSnapshot().CPUSeconds
	thread := ""
	firstMessage := ""
	for i := 0; i < n; i++ {
		text := strings.Repeat("x", p.TextBytes)
		if p.Varied {
			b := make([]byte, p.TextBytes)
			for j := range b {
				b[j] = alphabet[rng.Intn(len(alphabet))]
			}
			text = string(b)
		}
		req := map[string]any{"idempotency_key": fmt.Sprintf("compaction-%06d", i), "priority": "wake", "recipients": recipients, "content": map[string]string{"text": text}}
		if p.Reused && thread != "" {
			req["thread_id"] = thread
			req["in_reply_to"] = firstMessage
		}
		if attachments != nil {
			req["attachments"] = attachments
		}
		raw, e := json.Marshal(req)
		if e != nil {
			return out, e
		}
		start := time.Now()
		r, replay, e := h.Send(context.Background(), auth.Caller{Kind: "peer", PeerID: sender, PrincipalID: prn, PeerKind: "session"}, raw, "http_send")
		if e != nil {
			return out, e
		}
		if replay {
			return out, fmt.Errorf("unexpected replay")
		}
		times = append(times, float64(time.Since(start))/1e6)
		thread = r.ThreadID
		if firstMessage == "" {
			firstMessage = r.MessageID
		}
	}
	out.SendCPU = readSnapshot().CPUSeconds - cpu0
	runtime.ReadMemStats(&mem1)
	out.SendAllocBytes = mem1.TotalAlloc - mem0.TotalAlloc
	out.Send = percentiles(times)
	out.SendSamples = len(times)
	if _, err = db.Exec("ANALYZE"); err != nil {
		return out, err
	}
	out.Plans, err = compactionPlans(db, recipients[0], thread, at)
	if err != nil {
		return out, err
	}
	if worker != nil {
		if _, err = worker.Step(context.Background()); err != nil {
			return out, err
		}
	}
	if err = capture("immediate"); err != nil {
		return out, err
	}
	if references {
		var bad int64
		bad, err = compactionCount(db, "SELECT count(*) FROM events WHERE type='delivery' AND (payload_format!='delivery_ref_v1' OR json_type(data,'$.message') IS NOT NULL OR snapshot_id IS NOT NULL)")
		if err != nil {
			return out, err
		}
		if bad != 0 {
			return out, fmt.Errorf("normal delivery kept full body or snapshot")
		}
	}
	times = nil
	runtime.ReadMemStats(&mem0)
	cpu0 = readSnapshot().CPUSeconds
	for i := 0; i < max(30, n/2); i++ {
		start := time.Now()
		events, e := h.Log.Read(context.Background(), recipients, eventlog.Cursor{Epoch: h.Log.Epoch()}, 200)
		if e != nil {
			return out, e
		}
		if len(events) == 0 {
			return out, fmt.Errorf("empty event read")
		}
		times = append(times, float64(time.Since(start))/1e6)
	}
	out.ReadCPU = readSnapshot().CPUSeconds - cpu0
	runtime.ReadMemStats(&mem1)
	out.ReadAllocBytes = mem1.TotalAlloc - mem0.TotalAlloc
	out.Read = percentiles(times)
	out.ReadSamples = len(times)
	times = nil
	runtime.ReadMemStats(&mem0)
	cpu0 = readSnapshot().CPUSeconds
	for count := 0; count < n*p.Fanout; {
		start := time.Now()
		due, e := h.ClaimDue(context.Background(), now, p.Fanout)
		if e != nil {
			return out, e
		}
		times = append(times, float64(time.Since(start))/1e6)
		if len(due) == 0 {
			return out, fmt.Errorf("push fixture stalled at %d", count)
		}
		for _, d := range due {
			var expected string
			if e = db.QueryRow("SELECT body_sha256 FROM deliveries WHERE dly_id=?", d.DeliveryID).Scan(&expected); e != nil {
				return out, e
			}
			if fmt.Sprintf("%x", sha256.Sum256(d.Body)) != expected {
				return out, fmt.Errorf("push raw bytes changed")
			}
			out.RawBytesVerified++

		}
		if e = db.Tx(context.Background(), func(tx *sql.Tx) error {
			for _, d := range due {
				if _, err := tx.Exec("UPDATE deliveries SET state='accepted' WHERE dly_id=?", d.DeliveryID); err != nil {
					return err
				}
			}
			return nil
		}); e != nil {
			return out, e
		}
		count += len(due)
	}
	out.PushCPU = readSnapshot().CPUSeconds - cpu0
	runtime.ReadMemStats(&mem1)
	out.PushAllocBytes = mem1.TotalAlloc - mem0.TotalAlloc
	out.Push = percentiles(times)
	out.PushSamples = len(times)
	for i := 0; i < 10000; i++ {
		sliceStarted := time.Now()
		v, e := h.BackfillDeliverySlice(context.Background(), hub.BackfillLimits{})
		out.BackfillWallMaxNanos = max(out.BackfillWallMaxNanos, time.Since(sliceStarted).Nanoseconds())
		if e != nil {
			return out, e
		}
		out.BackfillMaxNanos = max(out.BackfillMaxNanos, v.LastSliceNanos)
		if v.PausedReason != "" {
			return out, fmt.Errorf("backfill paused: %s", v.PausedReason)
		}
		if v.Done {
			break
		}
		if i == 9999 {
			return out, fmt.Errorf("backfill budget exhausted")
		}
	}
	if err = capture("post_backfill_pre_reclaim"); err != nil {
		return out, err
	}
	if worker != nil {
		callbackOK.Store(true)
		now = now.Add(time.Hour)
		for i := 0; i < 100; i++ {
			if _, err = worker.Step(context.Background()); err != nil {
				return out, err
			}
			pending, e := compactionCount(db, "SELECT count(*) FROM event_outbox WHERE done_at IS NULL")
			if e != nil {
				return out, e
			}
			if pending == 0 {
				break
			}
		}
	}
	now = now.Add(25 * time.Hour)
	for i := 0; i < 1000; i++ {
		v, e := h.ExpireIdempotency(context.Background(), 100)
		if e != nil {
			return out, e
		}
		if v.Deleted == 0 {
			break
		}
	}
	if err = capture("idempotency_expired"); err != nil {
		return out, err
	}
	var high int64
	high, err = compactionCount(db, "SELECT coalesce(max(seq),0) FROM events")
	if err != nil {
		return out, err
	}
	for _, peer := range append(append([]string{}, recipients...), sender) {
		if err = h.Ack(context.Background(), auth.Caller{Kind: "peer", PeerID: peer, PrincipalID: prn, PeerKind: "session"}, "", eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: high}); err != nil {
			return out, err
		}
	}
	now = now.Add(8 * 24 * time.Hour)
	for i := 0; i < max(40, n*p.Fanout/20); i++ {
		if _, err = h.PruneEventSlice(context.Background()); err != nil {
			return out, err
		}
	}
	if err = capture("acked_events_expired"); err != nil {
		return out, err
	}
	operator := auth.Caller{Kind: "operator", PrincipalID: prn, PeerID: sender}
	snap, e := h.Preferences.ReadPreferences(context.Background(), operator)
	if e != nil {
		return out, e
	}
	patch := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(86400)}, ApplyExisting: true}
	preview, e := h.Preferences.PreviewPreferences(context.Background(), operator, snap.ETag, patch)
	if e != nil {
		return out, e
	}
	if len(preview.Risks) > 0 {
		patch.Confirm = []string{preview.ConfirmToken}
	}
	if _, e = h.Preferences.PatchPreferences(context.Background(), operator, snap.ETag, patch); e != nil {
		return out, e
	}
	now = now.Add(24 * time.Hour)
	for i := 0; i < max(20, n/10); i++ {
		if _, err = h.PruneHistorySlice(context.Background()); err != nil {
			return out, err
		}
	}
	if err = capture("finite_history"); err != nil {
		return out, err
	}
	for i := 0; i < 1000; i++ {
		r, e := db.Maintain(context.Background(), store.MaintenanceOptions{ReclaimPages: 128, Target: 50 * time.Millisecond})
		if e != nil {
			return out, e
		}
		if r.ReclaimedPages == 0 && r.PausedReason == "" {
			break
		}
	}
	var integrity string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return out, fmt.Errorf("integrity check failed")
	}
	fk, e := db.Query("PRAGMA foreign_key_check")
	if e != nil {
		return out, e
	}
	hasFK := fk.Next()
	e = fk.Err()
	fk.Close()
	if e != nil || hasFK {
		return out, fmt.Errorf("foreign key check failed")
	}
	if err = capture("post_reclaim"); err != nil {
		return out, err
	}
	return out, nil
}
func measureCompaction(n int, outfile, focus string) error {
	priorLogger := slog.Default()
	defer slog.SetDefault(priorLogger)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if os.Getenv("VECNET_BB") != "1" {
		return fmt.Errorf("compaction benchmark requires build box")
	}
	var profiles []compactionProfile
	for _, f := range []int{1, 2, 5, 20} {
		for _, size := range []int{2048, 4096} {
			for _, varied := range []bool{false, true} {
				profiles = append(profiles, compactionProfile{Fanout: f, TextBytes: size, Varied: varied, Reused: varied})
			}
		}
	}
	profiles = append(profiles, compactionProfile{Fanout: 2, TextBytes: 2048, Varied: true, Attachment: "reference"}, compactionProfile{Fanout: 2, TextBytes: 2048, Varied: true, Attachment: "hosted"})
	profiles = append(profiles, compactionProfile{Fanout: 2, TextBytes: 2048, Varied: true, Subscriptions: 1}, compactionProfile{Fanout: 2, TextBytes: 2048, Varied: true, Subscriptions: 3})
	if focus == "supplement" {
		profiles = []compactionProfile{{Fanout: 5, TextBytes: 4096, Varied: true, Reused: true}, {Fanout: 20, TextBytes: 4096, Varied: true, Reused: true}}
	} else if focus != "matrix" {
		return fmt.Errorf("invalid compaction focus")
	}
	result := map[string]any{"host": hostInfo(), "go": runtime.Version(), "gomaxprocs": runtime.GOMAXPROCS(0), "seed": 8, "synchronous": "FULL", "driver": "bench/compaction.go", "sends_per_fixture": n, "started_utc": time.Now().UTC(), "source_sha256": compactionSources(), "revision": os.Getenv("VECNET_BENCH_REVISION")}
	if focus == "supplement" {
		costs, err := measureSnapshotCosts(n)
		if err != nil {
			return err
		}
		result["snapshot_costs"] = costs
	}
	var rows []compactionRow
	for _, p := range profiles {
		for _, refs := range []bool{false, true} {
			fmt.Fprintf(os.Stderr, "compaction F=%d text=%d varied=%t reused=%t attachments=%s refs=%t\n", p.Fanout, p.TextBytes, p.Varied, p.Reused, p.Attachment, refs)
			r, e := runCompactionProfile(p, refs, n)
			if e != nil {
				result["error"] = e.Error()
				result["rows"] = rows
				_ = writeJSON(outfile, result)
				return e
			}
			rows = append(rows, r)
			result["rows"] = rows
			if e = writeJSON(outfile, result); e != nil {
				return e
			}
		}
	}
	return nil
}

func compactionSources() map[string]string {
	out := map[string]string{}
	for _, p := range []string{"bench/main.go", "bench/compaction.go", "bench/compaction_extra.go", "bench/server.go", "internal/store/store.go", "internal/store/maintenance.go", "internal/hub/send.go", "internal/hub/deliveries.go", "internal/hub/storage_backfill.go", "internal/hub/history_gc.go", "internal/eventpayload/payload.go"} {
		if b, err := os.ReadFile(p); err == nil {
			out[p] = fmt.Sprintf("%x", sha256.Sum256(b))
		}
	}
	return out
}
