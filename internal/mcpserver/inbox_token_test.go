package mcpserver_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// No MCP inbox read carries a blob read token (PROTOCOL §4.3): neither
// representation of vecnet_inbox, nor resources/read. The hub-set blob_url
// stays in the stored Delivery that GET /v1/events and pushes serve.
func TestInboxCarriesNoBlobToken(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "a")
	b, bSecret := env.NewPeer("session", "b")
	hosted := []byte("hosted attachment bytes")
	sum := sha256.Sum256(hosted)
	sha := hex.EncodeToString(sum[:])
	if resp, body := env.Do(aSecret, http.MethodPut, "/v1/blobs", hosted); resp.StatusCode/100 != 2 {
		t.Fatalf("PUT /v1/blobs: %d %s", resp.StatusCode, body)
	}
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "hosted", "recipients": []string{b}, "priority": "hold",
		"content":     map[string]any{"text": "attached"},
		"attachments": []any{map[string]any{"sha256": sha, "size": len(hosted), "media_type": "text/plain"}},
	}, nil)
	// Past 4096 bytes the hub spills the text into a hosted attachment.
	toolOK(t, env, aSecret, "vecnet_send", map[string]any{
		"idempotency_key": "spilled", "recipients": []string{b}, "priority": "hold",
		"content": map[string]any{"text": strings.Repeat("spill ", 2000)},
	}, nil)

	// HTTP keeps the per-recipient token.
	resp, body := env.Do(bSecret, http.MethodGet, "/v1/events?wait=0", nil)
	if resp.StatusCode != http.StatusOK || strings.Count(string(body), "?t=") != 2 {
		t.Fatalf("GET /v1/events = %d, want two blob_url tokens: %s", resp.StatusCode, body)
	}

	status, r := rpc(t, env, bSecret, "tools/call", map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{}})
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("vecnet_inbox: HTTP %d, error %+v", status, r.Error)
	}
	resource := inboxText(t, env, bSecret, peerURI(b))
	for name, raw := range map[string]string{"vecnet_inbox": string(r.Result), "resources/read": resource} {
		for _, leak := range []string{"blob_url", "?t="} {
			if strings.Contains(raw, leak) {
				t.Fatalf("%s result carries %q: %s", name, leak, raw)
			}
		}
	}

	// Attachments keep their hashes, so vecnet_blob_get reads both.
	var inbox inboxResult
	var res toolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.StructuredContent, &inbox); err != nil {
		t.Fatal(err)
	}
	ds := inbox.deliveries()
	if len(ds) != 2 || len(ds[0].Data.Message.Attachments) != 1 || len(ds[1].Data.Message.Attachments) != 1 ||
		ds[0].Data.Message.Attachments[0].SHA256 != sha {
		t.Fatalf("deliveries = %+v", ds)
	}
	for _, d := range ds {
		var got map[string]any
		toolOK(t, env, bSecret, "vecnet_blob_get", map[string]any{"sha256": d.Data.Message.Attachments[0].SHA256}, &got)
		if got["data"] == "" {
			t.Fatalf("blob_get %s: %v", d.Data.Message.Attachments[0].SHA256, got)
		}
	}
}
