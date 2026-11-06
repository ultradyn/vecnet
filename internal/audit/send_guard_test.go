package audit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestSendAckSourceAdminAuditCompactGuard(t *testing.T) {
	const sentinel = "SYNTHETIC_CONTENT_SECRET_SENTINEL_TASK06"
	const a = "peer_00000000000000000000000001"
	const b = "peer_00000000000000000000000002"
	const prn = "prn_00000000000000000000000001"
	ctx := context.Background()
	env := testhub.Start(t)
	fixed, err := envelope.ParseTime("2030-01-02T03:04:05.006Z")
	if err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(fixed.Sub(env.Now.Now()))
	err = env.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES (?,'Synthetic audit principal',?)`, prn, envelope.FormatTime(fixed)); err != nil {
			return err
		}
		for _, peer := range []string{a, b} {
			if _, err := tx.Exec(`INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES (?,?,'Synthetic audit peer','session',?)`, peer, prn, envelope.FormatTime(fixed)); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES (?,'peer',?,?,?)`, peer, peer, auth.HashSecret(sentinel+peer), envelope.FormatTime(fixed)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"idempotency_key":"audit-guard","priority":"hold","recipients":["` + b + `"],"content":{"text":"` + sentinel + `"},"metadata":{"synthetic":"` + sentinel + `"}}`)
	result, _, err := env.Hub.Send(ctx, auth.Caller{Kind: "peer", PeerID: a, PrincipalID: prn}, body, "http_send")
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := env.Hub.Log.Current(ctx, []string{b})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.Ack(ctx, auth.Caller{Kind: "peer", PeerID: b, PrincipalID: prn}, "", cursor); err != nil {
		t.Fatal(err)
	}
	title := sentinel
	if err := env.Hub.PatchThread(ctx, result.ThreadID, hub.ThreadPatch{TitleSet: true, Title: &title}); err != nil {
		t.Fatal(err)
	}
	src, _, sourceSecret, err := env.Hub.AddSource(ctx, "github", sentinel, b, nil, "hold")
	if err != nil {
		t.Fatal(err)
	}
	_, dropped, err := env.Hub.IngestSource(ctx, hub.SourceMessage{SrcID: src, ProviderDeliveryID: "synthetic-provider", OriginThreadID: "synthetic-origin", EventName: "issues.opened", Title: sentinel, Summary: sentinel, Raw: []byte(`{"synthetic":"` + sentinel + `"}`), MediaType: "application/json"})
	if err != nil || dropped {
		t.Fatalf("source send failed: dropped=%t err=%v", dropped, err)
	}
	rows, err := env.Hub.DB.Query(`SELECT actor,action,target_ids,detail FROM audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]int{}
	bytesByAction := map[string]int{}
	allowed := map[string]map[string]bool{
		"send":         {"ingress": true, "deliveries": true, "new_thread": true},
		"ack":          {"scope": true, "cursor": true, "accepted": true},
		"source_add":   {"kind": true},
		"thread_patch": {"fields": true},
	}
	for rows.Next() {
		var actor, action, targets, detail string
		if err := rows.Scan(&actor, &action, &targets, &detail); err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{sentinel, sourceSecret} {
			if strings.Contains(actor+action+targets+detail, private) {
				t.Fatalf("audit leaked synthetic private data in action %s", action)
			}
		}
		keys, ok := allowed[action]
		if !ok {
			continue
		}
		seen[action]++
		bytesByAction[action] += len(actor) + len(action) + len(targets) + len(detail)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(detail), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != len(keys) {
			t.Fatalf("action %s detail fields = %d, want %d", action, len(fields), len(keys))
		}
		for key := range fields {
			if !keys[key] {
				t.Fatalf("action %s unexpected detail field %s", action, key)
			}
		}
		if action == "send" {
			var ids []string
			if err := json.Unmarshal([]byte(targets), &ids); err != nil {
				t.Fatal(err)
			}
			if len(ids) != 2 || !strings.HasPrefix(ids[0], "msg_") || !strings.HasPrefix(ids[1], "thr_") {
				t.Fatal("send audit targets must be message/thread ids")
			}
			var counts struct {
				Deliveries int    `json:"deliveries"`
				NewThread  bool   `json:"new_thread"`
				Ingress    string `json:"ingress"`
			}
			if err := json.Unmarshal([]byte(detail), &counts); err != nil {
				t.Fatal(err)
			}
			if counts.Deliveries != 1 || !counts.NewThread || (counts.Ingress != "http_send" && counts.Ingress != "inbound_webhook") {
				t.Fatal("send audit ids/counts/ingress changed")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]int{"send": 2, "ack": 1, "source_add": 1, "thread_patch": 1} {
		if seen[action] != want {
			t.Fatalf("action %s rows = %d, want %d", action, seen[action], want)
		}
		t.Logf("audit action=%s rows=%d logical_field_bytes=%d", action, seen[action], bytesByAction[action])
	}
	// Advancing maintenance must retain the complete append-only audit prefix.
	rows.Close()
	var before, after string
	var high int64
	if err := env.Hub.DB.QueryRow(`SELECT max(id) FROM audit`).Scan(&high); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT json_group_array(json_array(id,at,actor,action,target_ids,detail)) FROM audit WHERE id<=?`, high).Scan(&before); err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(365 * 24 * 60 * 60 * 1e9)
	env.Hub.Tick(ctx, true)
	if err := env.Hub.DB.QueryRow(`SELECT json_group_array(json_array(id,at,actor,action,target_ids,detail)) FROM audit WHERE id<=?`, high).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("maintenance changed append-only audit prefix")
	}
}
