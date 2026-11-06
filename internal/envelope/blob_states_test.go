package envelope_test

import (
	"encoding/json"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

func TestR2StateEventProjectionSchema(t *testing.T) {
	for _, class := range []string{"1h", "6h", "12h", "1d", "7d", "30d", "keep"} {
		for _, status := range []string{"available", "expired", "purged", "unavailable"} {
			t.Run(class+"/"+status, func(t *testing.T) {
				attachment := r2AttachmentFixture(class)
				state := map[string]any{"state": status}
				for _, key := range []string{"retention_class", "expires_at", "sha256_verified", "integrity"} {
					state[key] = attachment[key]
				}
				event := map[string]any{"cursor": "e1.1", "type": "delivery", "at": "2026-11-06T00:00:00.000Z", "data": loadExample(t, "02-delivery-direct.json"), "blob_states": map[string]any{attachment["blob_id"].(string): state}}
				raw := mustJSON(t, event)
				if err := envelope.Validate("event", raw); err != nil {
					t.Fatalf("valid projection rejected: %v", err)
				}
				var decoded envelope.Event
				if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.BlobStates) != 1 {
					t.Fatal("typed event lost projection")
				}
				for _, key := range []string{"url", "blob_url", "signature"} {
					state[key] = "forbidden"
					if envelope.Validate("event", mustJSON(t, event)) == nil {
						t.Fatal("state schema allows a capability")
					}
					delete(state, key)
				}
				for _, key := range []string{"retention_class", "expires_at", "sha256_verified", "integrity"} {
					saved := state[key]
					delete(state, key)
					if envelope.Validate("event", mustJSON(t, event)) == nil {
						t.Fatal("state schema accepts missing trust/retention")
					}
					state[key] = saved
				}
			})
		}
	}
}
