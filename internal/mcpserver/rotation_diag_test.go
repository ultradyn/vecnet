package mcpserver_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

type rotationDiagFixture struct {
	env                         *testhub.Env
	subject, secret             string
	peer, secondPeer            string
	unrelatedPeer, unrelatedKey string
	rotate                      func(context.Context) (string, error)
}

func newRotationDiagFixture(t *testing.T, kind string) rotationDiagFixture {
	t.Helper()
	env := testhub.Start(t)
	f := rotationDiagFixture{env: env}
	if kind == "peer" {
		f.peer, f.secret = env.NewPeer("session", "rotation-diag")
		f.subject, f.secondPeer = f.peer, f.peer
		f.unrelatedPeer, f.unrelatedKey = env.NewPeer("session", "unrelated")
		f.rotate = func(ctx context.Context) (string, error) { return env.Hub.RotatePeer(ctx, f.subject) }
	} else {
		f.subject, f.secret = env.NewPrincipal("rotation-diag")
		f.peer, f.unrelatedKey = peerUnder(t, env, f.subject, "rotation-first")
		f.secondPeer, _ = peerUnder(t, env, f.subject, "rotation-second")
		// This peer's own bearer is independent of its principal's bearer.
		f.unrelatedPeer = f.peer
		f.rotate = func(ctx context.Context) (string, error) { return env.Hub.RotatePrincipal(ctx, f.subject) }
	}
	return f
}

func rotationPauseRecords(ids map[string]string) []mcpdiag.Record {
	var out []mcpdiag.Record
	for _, r := range mcpdiag.Snapshot(mcpdiag.Filter{}) {
		id, _ := r.Fields["subscription_id"].(string)
		if _, ok := ids[id]; ok && r.Kind == "events.pause" && r.Fields["reason"] == events.PausedCredentialRotated {
			out = append(out, r)
		}
	}
	return out
}

func TestCredentialRotationPauseDiagnostics(t *testing.T) {
	for _, kind := range []string{"peer", "principal"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := newRotationDiagFixture(t, kind)
			store := events.NewStore(f.env.Hub)
			changed := map[string]string{}
			canaries := []string{f.secret}
			for _, peer := range []string{f.peer, f.secondPeer} {
				cb := testhub.NewCallback(t)
				key := webhook.NewSecret()
				url := cb.URL + "/rotation-private-path?private-query=rotation-private-query"
				res := subscribeOK(t, f.env, f.secret, subParams(url, key, map[string]any{"arguments": map[string]any{"peer_id": peer}}))
				changed[res.ID] = peer
				canaries = append(canaries, key, url, "rotation-private-path", "rotation-private-query")
			}
			cb := testhub.NewCallback(t)
			operatorPaused := subscribeOK(t, f.env, f.secret, subParams(cb.URL, webhook.NewSecret(), map[string]any{"arguments": map[string]any{"peer_id": f.peer}}))
			if err := store.Pause(ctx, operatorPaused.ID); err != nil {
				t.Fatal(err)
			}
			otherCB := testhub.NewCallback(t)
			unrelated := subscribeOK(t, f.env, f.unrelatedKey, subParams(otherCB.URL, webhook.NewSecret(), nil))

			capture, output := captureDiag(t)
			fresh, err := f.rotate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			canaries = append(canaries, fresh)
			records := rotationPauseRecords(changed)
			if len(records) != len(changed) {
				t.Fatalf("rotation emitted %d pause records, want %d", len(records), len(changed))
			}
			seen := map[string]bool{}
			for _, record := range records {
				id := record.Fields["subscription_id"].(string)
				want := map[string]any{"subscription_id": id, "peer_id": changed[id], "reason": events.PausedCredentialRotated, "result": "paused"}
				if seen[id] || !reflect.DeepEqual(record.Fields, want) {
					t.Fatalf("unexpected or duplicate rotation pause fields: %v", record.Fields)
				}
				seen[id] = true
			}
			logs := 0
			for _, record := range capture.records {
				if record.Message == "events.pause" {
					logs++
				}
			}
			if logs != len(changed) {
				t.Fatalf("rotation emitted %d pause log lines, want %d", logs, len(changed))
			}
			for id := range changed {
				if sub, err := store.Get(ctx, id); err != nil || sub.PausedReason != events.PausedCredentialRotated {
					t.Fatalf("rotated subscription pause = %v, %v", sub.PausedReason, err)
				}
			}
			if sub, err := store.Get(ctx, operatorPaused.ID); err != nil || sub.PausedReason != events.PausedOperator {
				t.Fatalf("operator pause = %v, %v", sub.PausedReason, err)
			}
			if sub, err := store.Get(ctx, unrelated.ID); err != nil || sub.PausedReason != "" {
				t.Fatalf("unrelated credential subscription pause = %v, %v", sub.PausedReason, err)
			}
			if _, err := f.rotate(ctx); err != nil {
				t.Fatal(err)
			}
			if got := len(rotationPauseRecords(changed)); got != len(changed) {
				t.Fatalf("repeated rotation emitted duplicate pause records: %d", got)
			}
			if len(capture.records) != logs {
				t.Fatal("repeated rotation emitted additional diagnostics")
			}
			raw, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range canaries {
				if strings.Contains(string(raw), canary) || strings.Contains(output.String(), canary) {
					t.Fatal("rotation diagnostics retained a credential, secret, or callback URL/path/query")
				}
			}
		})
	}
}

func TestCredentialRotationRollbackDoesNotEmitPause(t *testing.T) {
	for _, kind := range []string{"peer", "principal"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := newRotationDiagFixture(t, kind)
			cb := testhub.NewCallback(t)
			res := subscribeOK(t, f.env, f.secret, subParams(cb.URL, webhook.NewSecret(), map[string]any{"arguments": map[string]any{"peer_id": f.peer}}))
			// The UPDATE succeeds and returns its row, but COMMIT fails. This
			// distinguishes commit-aware emission from emission inside the tx.
			for _, query := range []string{
				`CREATE TABLE rotation_diag_guard (peer_id TEXT REFERENCES peers(peer_id) DEFERRABLE INITIALLY DEFERRED)`,
				`CREATE TRIGGER reject_rotation_commit AFTER UPDATE OF paused_reason ON event_subscriptions
				 WHEN NEW.paused_reason = 'credential_rotated'
				 BEGIN INSERT INTO rotation_diag_guard VALUES ('peer_missing_rotation_diag'); END`,
			} {
				if _, err := f.env.Hub.DB.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			capture, _ := captureDiag(t)
			if secret, err := f.rotate(ctx); err == nil || !strings.Contains(err.Error(), "store: commit:") || secret != "" {
				t.Fatal("rotation did not fail at commit with an empty credential result")
			}
			if got := rotationPauseRecords(map[string]string{res.ID: f.peer}); len(got) != 0 {
				t.Fatal("rolled-back rotation entered diagnostic ring")
			}
			for _, record := range capture.records {
				if record.Message == "events.pause" {
					t.Fatal("rolled-back rotation emitted pause log")
				}
			}
			if sub, err := events.NewStore(f.env.Hub).Get(ctx, res.ID); err != nil || sub.PausedReason != "" {
				t.Fatalf("rolled-back subscription pause = %v, %v", sub.PausedReason, err)
			}
			if _, err := f.env.Hub.Auth.Resolve(ctx, f.secret); err != nil {
				t.Fatal("rolled-back rotation revoked original credential")
			}
		})
	}
}
