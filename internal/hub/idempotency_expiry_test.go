package hub_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// The maintenance seam is Hub.Tick/ExpireIdempotency; Send is the lookup seam.
// Every fixture is disposable, with fixed synthetic identities and a fake clock.
func idemEnv(t *testing.T, configure func(*hub.Config)) *testhub.Env {
	t.Helper()
	env := testhub.StartWith(t, configure)
	fixed, err := envelope.ParseTime(guardAt)
	if err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(fixed.Sub(env.Now.Now()))
	err = env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES (?,'Synthetic expiry principal',?)`, guardPrn, guardAt); err != nil {
			return err
		}
		for _, peer := range []string{guardA, guardB} {
			if _, err := tx.Exec(`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES (?,?,'Synthetic expiry peer','session',?)`, peer, guardPrn, guardAt); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES (?,'peer',?,?,?)`, peer, peer, auth.HashSecret(guardSecret+peer), guardAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestIdempotencyExpiryWithoutKeyReuse(t *testing.T) {
	env := idemEnv(t, nil)
	mustSend(t, env, guardSecret+guardA, req("never-reused", guardB))
	env.Now.Advance(24*time.Hour + time.Millisecond)
	env.Hub.Tick(context.Background(), true)
	if got := count(t, env, `SELECT count(*) FROM idempotency`); got != 0 {
		t.Fatalf("expired keys without reuse = %d, want 0", got)
	}
	if got := count(t, env, `SELECT count(*) FROM messages`); got != 1 {
		t.Fatalf("message rows = %d, want 1", got)
	}
}

func TestIdempotencyExactCutoff(t *testing.T) {
	env := idemEnv(t, nil)
	body := req("boundary", guardB)
	status, first, original := sendHTTP(t, env, guardSecret+guardA, body)
	if status != 202 {
		t.Fatalf("initial status = %d", status)
	}
	env.Now.Advance(24*time.Hour - time.Millisecond)
	status, _, replay := sendHTTP(t, env, guardSecret+guardA, body)
	if status != 200 || !bytes.Equal(original, replay) {
		t.Fatal("just-inside replay changed status or bytes")
	}
	changed := req("boundary", guardB)
	changed["content"] = map[string]any{"text": "synthetic changed request"}
	mustReject(t, env, guardSecret+guardA, changed, 409, "idempotency_conflict")
	env.Now.Advance(time.Millisecond)
	env.Hub.Tick(context.Background(), true)
	status, _, replay = sendHTTP(t, env, guardSecret+guardA, body)
	if status != 200 || !bytes.Equal(original, replay) {
		t.Fatalf("at-cutoff replay status = %d, want 200 and original bytes", status)
	}
	mustReject(t, env, guardSecret+guardA, changed, 409, "idempotency_conflict")
	env.Now.Advance(time.Millisecond)
	fresh := mustSend(t, env, guardSecret+guardA, changed)
	if fresh.MessageID == first.MessageID {
		t.Fatal("expired key did not create a new message")
	}
}

func TestIdempotencyPolicyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		window  time.Duration
		set     bool
		advance time.Duration
		expired bool
	}{
		{"default", 0, false, 24*time.Hour + time.Millisecond, true},
		{"48h-inside", 48 * time.Hour, true, 30 * time.Hour, false},
		{"48h-boundary", 48 * time.Hour, true, 48 * time.Hour, false},
		{"48h-expired", 48 * time.Hour, true, 48*time.Hour + time.Millisecond, true},
		{"forever", 0, true, 365 * 24 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := idemEnv(t, func(c *hub.Config) { c.IdempotencyWindow = tc.window; c.IdempotencyWindowSet = tc.set })
			body := req("configured", guardB)
			_, first, original := sendHTTP(t, env, guardSecret+guardA, body)
			var hash, response string
			if err := env.Hub.DB.QueryRow(`SELECT body_sha256,response FROM idempotency`).Scan(&hash, &response); err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(tc.advance)
			result, err := env.Hub.ExpireIdempotency(context.Background(), 2)
			if err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if tc.expired {
				want = 1
			}
			if result.Deleted != want {
				t.Fatalf("deleted = %d, want %d", result.Deleted, want)
			}
			if tc.set && tc.window == 0 && !result.Cutoff.IsZero() {
				t.Fatal("forever policy reported expiry cutoff")
			}
			if tc.expired {
				next := mustSend(t, env, guardSecret+guardA, body)
				if next.MessageID == first.MessageID {
					t.Fatal("expired key reused original message")
				}
			} else {
				var afterHash, afterResponse string
				if err := env.Hub.DB.QueryRow(`SELECT body_sha256,response FROM idempotency`).Scan(&afterHash, &afterResponse); err != nil {
					t.Fatal(err)
				}
				if afterHash != hash || afterResponse != response {
					t.Fatal("cleanup changed retained hash or response bytes")
				}
				status, _, replay := sendHTTP(t, env, guardSecret+guardA, body)
				if status != 200 || !bytes.Equal(original, replay) {
					t.Fatal("configured replay changed status or bytes")
				}
				changed := req("configured", guardB)
				changed["content"] = map[string]any{"text": "synthetic different body"}
				mustReject(t, env, guardSecret+guardA, changed, 409, "idempotency_conflict")
			}
		})
	}
}

func TestIdempotencyInvalidWindow(t *testing.T) {
	for _, d := range []time.Duration{-time.Hour, time.Hour, 24*time.Hour - time.Nanosecond} {
		t.Run(d.String(), func(t *testing.T) {
			env := idemEnv(t, nil)
			cfg := env.Hub.Cfg
			cfg.IdempotencyWindow = d
			cfg.IdempotencyWindowSet = true
			_, err := hub.New(env.Hub.DB, cfg, env.Now.Now)
			if !errors.Is(err, hub.ErrInvalidIdempotencyWindow) {
				t.Fatalf("invalid window error = %v", err)
			}
		})
	}
}

func TestIdempotencyBoundedIndexedExpiry(t *testing.T) {
	env := idemEnv(t, nil)
	now := env.Now.Now()
	err := env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 7; i++ {
			_, err := tx.Exec(`INSERT INTO idempotency(sender,key,body_sha256,response,created_at) VALUES (?,?,?,'{}',?)`, guardA, fmt.Sprint(i), "synthetic-hash", envelope.FormatTime(now.Add(-25*time.Hour)))
			if err != nil {
				return err
			}
		}
		for key, at := range map[string]time.Time{"at-cutoff": now.Add(-24 * time.Hour), "future": now.Add(time.Hour)} {
			if _, err := tx.Exec(`INSERT INTO idempotency(sender,key,body_sha256,response,created_at) VALUES (?,?,?,'{}',?)`, guardA, key, "synthetic-hash", envelope.FormatTime(at)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{3, 3, 1, 0} {
		result, err := env.Hub.ExpireIdempotency(context.Background(), 3)
		if err != nil || result.Deleted != want || !result.Cutoff.Equal(now.Add(-24*time.Hour)) {
			t.Fatalf("slice = %+v, %v; want %d and exact cutoff", result, err, want)
		}
	}
	if n := count(t, env, `SELECT count(*) FROM idempotency`); n != 2 {
		t.Fatalf("remaining keys = %d, want boundary and future", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := env.Hub.ExpireIdempotency(ctx, 3); err == nil {
		t.Fatal("canceled expiry succeeded")
	}
}

func TestIdempotencyCleanupRacesSend(t *testing.T) {
	for _, offset := range []time.Duration{0, time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			env := idemEnv(t, nil)
			raw, err := json.Marshal(req("racing", guardB))
			if err != nil {
				t.Fatal(err)
			}
			caller := auth.Caller{Kind: "peer", PeerID: guardA, PrincipalID: guardPrn}
			first, _, err := env.Hub.Send(context.Background(), caller, raw, "http_send")
			if err != nil {
				t.Fatal(err)
			}
			env.Now.Advance(24*time.Hour + offset)
			// Hold a real BEGIN IMMEDIATE writer so both operations contend for it.
			blocker, err := env.Hub.DB.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			start := make(chan struct{})
			ready := make(chan struct{}, 2)
			errs := make(chan error, 2)
			var got *hub.SendResponse
			var replay bool
			go func() {
				ready <- struct{}{}
				<-start
				var e error
				got, replay, e = env.Hub.Send(context.Background(), caller, raw, "http_send")
				errs <- e
			}()
			go func() {
				ready <- struct{}{}
				<-start
				_, e := env.Hub.ExpireIdempotency(context.Background(), 1)
				errs <- e
			}()
			<-ready
			<-ready
			close(start)
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				select {
				case err := <-errs:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("writer race timed out")
				}
			}
			wantReplay := offset == 0
			if replay != wantReplay || (got.MessageID == first.MessageID) != wantReplay {
				t.Fatal("cleanup/send race violated replay boundary")
			}
			if n := count(t, env, `SELECT count(*) FROM idempotency`); n != 1 {
				t.Fatalf("keys = %d, want 1", n)
			}
			wantMessages := 1
			if !wantReplay {
				wantMessages = 2
			}
			if n := count(t, env, `SELECT count(*) FROM messages`); n != wantMessages {
				t.Fatalf("messages = %d, want %d", n, wantMessages)
			}
		})
	}
}

func TestIdempotencyExpiryHardCapAndDefault(t *testing.T) {
	env := idemEnv(t, nil)
	err := env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 1105; i++ {
			if _, err := tx.Exec(`INSERT INTO idempotency VALUES (?,?,?,'{}',?)`, guardA, fmt.Sprint(i), "synthetic-hash", envelope.FormatTime(env.Now.Now().Add(-25*time.Hour))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		limit int
		want  int64
	}{{1000000, 1000}, {0, 100}, {-1, 5}} {
		result, err := env.Hub.ExpireIdempotency(context.Background(), tc.limit)
		if err != nil || result.Deleted != tc.want {
			t.Fatalf("limit=%d deleted=%d err=%v want=%d", tc.limit, result.Deleted, err, tc.want)
		}
	}
}
