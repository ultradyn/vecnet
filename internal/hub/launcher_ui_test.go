package hub_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// The web launcher uses the existing presence/directory/message contracts.
// Verify that discovery survives storage and that the exact native launch
// shape reaches the daemon with a job identity, thread and deadline.
func TestLauncherUIDiscoveryAndJobRequest(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("launcher", "host launcher")
	caps := []string{"launch:codex-app-server", "preset:codex-app-server/default", "mode:ask", "project:vecnet"}
	resp, raw := env.Do(secret, "POST", "/v1/presence", map[string]any{
		"state": "idle", "at": envelope.FormatTime(env.Now.Now()), "capabilities": caps,
		"quota": map[string]any{"max_sessions": 2, "running": 0, "backends": map[string]string{"codex-app-server": "ok"}},
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("presence: %d %s", resp.StatusCode, raw)
	}
	resp, raw = env.Do(env.OperatorSecret, "GET", "/v1/admin/peers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("directory: %d %s", resp.StatusCode, raw)
	}
	var page struct {
		Peers []struct {
			Peer struct {
				ID           string         `json:"peer_id"`
				Presence     string         `json:"presence"`
				Capabilities []string       `json:"capabilities"`
				Quota        map[string]any `json:"quota"`
			} `json:"peer"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range page.Peers {
		if r.Peer.ID != peer {
			continue
		}
		found = true
		if r.Peer.Presence != "idle" || !slices.Equal(r.Peer.Capabilities, caps) || r.Peer.Quota["backends"].(map[string]any)["codex-app-server"] != "ok" {
			t.Fatalf("launcher discovery changed: %+v", r.Peer)
		}
	}
	if !found {
		t.Fatal("launcher missing from admin directory")
	}

	deadline := envelope.FormatTime(env.Now.Now().Add(time.Hour))
	input := map[string]any{"backend": "codex-app-server", "project": "vecnet", "preset": "default", "permission_mode": "ask", "title": "UI launch"}
	body := map[string]any{
		"idempotency_key": "launch-ui-123", "recipients": []string{peer}, "kind": "message", "priority": "wake",
		"content": map[string]any{"text": "Check the project"}, "thread_title": "UI launch", "expires_at": deadline,
		"job_request": map[string]any{"version": 1, "job_key": "launch-ui-123", "operation": "launch", "instructions": "Check the project", "input": input},
	}
	sent := mustSend(t, env, env.OperatorSecret, body)
	resp, raw = env.Do(env.OperatorSecret, "POST", "/v1/messages", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("idempotent retry: %d %s", resp.StatusCode, raw)
	}
	var again hub.SendResponse
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if sent.JobID == "" || sent.ThreadID == "" || again.JobID != sent.JobID || again.ThreadID != sent.ThreadID || again.MessageID != sent.MessageID {
		t.Fatalf("launch retry duplicated job/thread: %+v %+v", sent, again)
	}
	evs := deliveryEvents(t, env, peer)
	if len(evs) != 1 {
		t.Fatalf("launch generated %d deliveries, want 1", len(evs))
	}
	_, _, message := decodeDelivery(t, evs[0])
	if message.JobRequest == nil || message.JobRequest.Operation != "launch" || message.Loop.ExpiresAt != deadline || message.Metadata["vecnet.job_id"] != sent.JobID {
		t.Fatalf("daemon launch envelope: %+v", message)
	}
	var delivered map[string]any
	if err := json.Unmarshal(message.JobRequest.Input, &delivered); err != nil || delivered["project"] != "vecnet" || delivered["preset"] != "default" || delivered["backend"] != "codex-app-server" {
		t.Fatalf("native input changed: %s (%v)", message.JobRequest.Input, err)
	}
	resp, raw = env.Do(env.OperatorSecret, "GET", "/v1/admin/threads/"+sent.ThreadID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resulting UI thread unavailable: %d %s", resp.StatusCode, raw)
	}
}
