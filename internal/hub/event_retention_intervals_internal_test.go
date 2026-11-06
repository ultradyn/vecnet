package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// Compare authorization to the pre-indexed ledger contract across arbitrary
// protective/prospective histories, repeated boundaries and large sequence IDs.
// This model intentionally retains the old ordered evaluation, not tree logic.
func TestRetentionIndexedIntervalsPreserveHistoricalRules(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE meta SET value='3' WHERE key='event_retention_initial_high_water'`); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(5))
	ages := []int64{0, 3600, 604800, 1209600, 2592000}
	var history []eventRuleBoundary
	high := int64(3)
	for i := range 150 {
		high += int64(rng.Intn(4))
		b := eventRuleBoundary{Revision: uint64(i + 1), FromSeq: high, BeforeSeconds: ages[rng.Intn(len(ages))], AfterSeconds: ages[rng.Intn(len(ages))]}
		if i == 148 {
			b.FromSeq = math.MaxInt64 - 1
			high = b.FromSeq
		}
		if i == 149 {
			b.FromSeq = math.MaxInt64
			high = b.FromSeq
		}
		history = append(history, b)
		err := db.Tx(ctx, func(tx *sql.Tx) error {
			if err := applyEventRuleBoundary(ctx, tx, b); err != nil {
				return err
			}
			for _, seq := range []int64{0, 1, 3, 4, high / 2, high, min(high, math.MaxInt64-1) + 1, math.MaxInt64} {
				seconds := history[0].BeforeSeconds
				if seq <= 3 {
					seconds = 0
				}
				for _, v := range history {
					if v.BeforeSeconds == 0 || seconds != 0 && v.BeforeSeconds > seconds {
						seconds = v.BeforeSeconds
					}
					if v.AfterSeconds == 0 || seconds != 0 && v.AfterSeconds > seconds || seq > v.FromSeq {
						seconds = v.AfterSeconds
					}
				}
				got, err := eventRuleSeconds(ctx, tx, seq)
				if err != nil {
					return err
				}
				if got != seconds {
					t.Fatalf("transition=%d candidate=%d got=%d want=%d", i, seq, got, seconds)
				}
			}
			return collectEventRuleIntervals(ctx, tx, 8)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// A global keep rule makes every previous interval obsolete. Collection
	// must remove at most the supplied number per transaction and preserve root.
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		return applyEventRuleBoundary(ctx, tx, eventRuleBoundary{FromSeq: math.MaxInt64, BeforeSeconds: 0, AfterSeconds: 0})
	}); err != nil {
		t.Fatal(err)
	}
	for {
		var before, after, pending int
		if err := db.QueryRow(`SELECT count(*) FROM event_retention_rule_nodes`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err := db.Tx(ctx, func(tx *sql.Tx) error { return collectEventRuleIntervals(ctx, tx, 1) }); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM event_retention_rule_nodes`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before-after > 1 {
			t.Fatal("compaction exceeded its row budget")
		}
		if err := db.QueryRow(`SELECT count(*) FROM event_retention_rule_garbage`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			if after != 1 {
				t.Fatalf("obsolete interval nodes survived: %d", after)
			}
			break
		}
	}
	rollback := errors.New("synthetic rollback")
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		if err := applyEventRuleBoundary(ctx, tx, eventRuleBoundary{FromSeq: 10, BeforeSeconds: 0, AfterSeconds: 3600}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		for _, seq := range []int64{1, 11, math.MaxInt64} {
			got, err := eventRuleSeconds(ctx, tx, seq)
			if err != nil {
				return err
			}
			if got != 0 {
				t.Fatalf("rollback lost protection for %d", seq)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionIntervalUpgradeResumesWithoutRewritingLegacyHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{DB: db}
	bounds := []eventRuleBoundary{
		{Revision: 1, FromSeq: 4, BeforeSeconds: 604800, AfterSeconds: 0, PolicyTag: "synthetic keep"},
		{Revision: 2, FromSeq: 8, BeforeSeconds: 0, AfterSeconds: 3600, PolicyTag: "synthetic short"},
		{Revision: 3, FromSeq: 12, BeforeSeconds: 3600, AfterSeconds: 604800, PolicyTag: "synthetic latest", AckCutoffAt: "2026-10-01T00:00:00.000Z", BootstrapGeneration: 7},
	}
	raw, err := json.Marshal(bounds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, eventRetentionLegacyProjectionKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	// Model a crash after replaying only the first boundary, while the original
	// ledger still exists and no complete v2 checkpoint has been published.
	if err := db.Tx(ctx, func(tx *sql.Tx) error { return applyEventRuleBoundary(ctx, tx, bounds[0]) }); err != nil {
		t.Fatal(err)
	}
	if err := h.upgradeEventRuleIntervals(ctx); err != nil {
		t.Fatal(err)
	}
	var legacy, latest string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, eventRetentionLegacyProjectionKey).Scan(&legacy); err != nil || legacy != string(raw) {
		t.Fatal("upgrade rewrote historical overflow pages", err)
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, eventRetentionProjectionKey).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	var got []eventRuleBoundary
	if err := json.Unmarshal([]byte(latest), &got); err != nil || len(got) != 1 || got[0] != bounds[2] {
		t.Fatal("latest fixed bounds changed", err)
	}
	for range 2 {
		if err := h.upgradeEventRuleIntervals(ctx); err != nil {
			t.Fatal(err)
		}
		if err := db.Tx(ctx, func(tx *sql.Tx) error {
			for seq, want := range map[int64]int64{1: 0, 4: 0, 8: 0, 9: 604800, 12: 604800, 13: 604800} {
				got, err := eventRuleSeconds(ctx, tx, seq)
				if err != nil {
					return err
				}
				if got != want {
					t.Fatalf("resumed upgrade candidate=%d got=%d want=%d", seq, got, want)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Missing indexed history must fail closed. Rebuilding from only the latest
	// compact boundary would lose older keep rules and could delete too early.
	if _, err := db.Exec(`DELETE FROM event_retention_rule_roots`); err != nil {
		t.Fatal(err)
	}
	if err := h.upgradeEventRuleIntervals(ctx); err == nil {
		t.Fatal("missing interval history accepted")
	}
}
