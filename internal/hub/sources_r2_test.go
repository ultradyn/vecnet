package hub_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestIngestSourceR2DurableAndReplay(t *testing.T) {
	env := testhub.Start(t)
	transfers := enableR2Durable(t, env)
	ctx := context.Background()
	b, bSec := env.NewPeer("session", "target")
	src, _, _, err := env.Hub.AddSource(ctx, "github", "source", b, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"event":"synthetic"}`)
	m := hub.SourceMessage{SrcID: src, ProviderDeliveryID: "one", OriginThreadID: "issue/1", OriginEventID: "one", EventName: "issues.opened", Summary: "source text", Raw: raw, MediaType: "application/json"}
	id, dropped, err := env.Hub.IngestSource(ctx, m)
	if err != nil || dropped || id == "" {
		t.Fatal("source ingest failed")
	}
	again, _, err := env.Hub.IngestSource(ctx, m)
	if err != nil || again != id {
		t.Fatal("source replay changed message")
	}
	var count int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM durable_blob_payloads WHERE purpose='source_raw'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("source raw not durable")
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM blob_r2_quota`).Scan(&count); err != nil || count != 0 {
		t.Fatal("source consumed attachment quota")
	}
	env.Now.Advance(48 * time.Hour)
	if err := transfers.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	resp, data := env.Do(bSec, "GET", "/v1/blobs/"+shaHex(string(raw)), nil)
	if resp.StatusCode != 200 || string(data) != string(raw) {
		t.Fatal("source data lost authorized DB path")
	}
	var body string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE msg_id=?`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Attachments []struct {
			Name string `json:"name"`
		}
	}
	if json.Unmarshal([]byte(body), &msg) != nil || len(msg.Attachments) != 1 || msg.Attachments[0].Name != "github-event.json" {
		t.Fatal("source attachment representation changed")
	}
	if _, err := env.Hub.DB.Exec(`DELETE FROM messages WHERE msg_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := transfers.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM durable_blob_payloads`).Scan(&count); err != nil || count != 0 {
		t.Fatal("last durable ref GC leaked source bytes")
	}
}

func TestIngestSourceR2FailureRollsBackPayload(t *testing.T) {
	env := testhub.Start(t)
	enableR2Durable(t, env)
	ctx := context.Background()
	b, _ := env.NewPeer("session", "target")
	src, _, _, err := env.Hub.AddSource(ctx, "github", "source", b, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// The actual send fails after the durable write, proving payload, quota and
	// purpose refs share the message transaction.
	if _, err := env.Hub.DB.Exec(`CREATE TRIGGER reject_source_message BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'injected_message_failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err = env.Hub.IngestSource(ctx, hub.SourceMessage{SrcID: src, ProviderDeliveryID: "one", OriginThreadID: "issue/1", OriginEventID: "one", EventName: "issues.opened", Summary: "summary", Raw: []byte(`{}`), MediaType: "application/json"})
	if err == nil {
		t.Fatal("failure injection did not run")
	}
	var n int
	for _, q := range []string{`SELECT count(*) FROM durable_blob_payloads`, `SELECT count(*) FROM durable_blob_refs`, `SELECT count(*) FROM blob_quota WHERE peer_id LIKE 'durable:%'`} {
		if err := env.Hub.DB.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatal("source rollback leaked data or quota")
		}
	}
}
