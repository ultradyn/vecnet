package envelope_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

func r2AttachmentFixture(class string) map[string]any {
	var end any = "2026-11-06T00:00:00.000Z"
	if class == "keep" {
		end = nil
	}
	return map[string]any{"blob_id": "blo_00000000000000000000000001", "sha256": strings.Repeat("a", 64), "size": 7, "media_type": "text/plain", "retention_class": class, "expires_at": end, "sha256_verified": true, "integrity": map[string]any{"algorithm": "sha256", "value": strings.Repeat("a", 64), "verified": true}}
}

func TestR2AttachmentSchemaAndServerVerification(t *testing.T) {
	for _, class := range []string{"1h", "6h", "12h", "1d", "7d", "30d", "keep"} {
		t.Run(class, func(t *testing.T) {
			attachment := r2AttachmentFixture(class)
			message := loadExample(t, "02-delivery-direct.json")["message"].(map[string]any)
			message["attachments"] = []any{attachment}
			if err := envelope.Validate("message", mustJSON(t, message)); err != nil {
				t.Fatalf("R2 fixture rejected: %v", err)
			}
			for _, field := range []string{"sha256_verified", "integrity", "retention_class", "expires_at"} {
				saved := attachment[field]
				delete(attachment, field)
				if envelope.Validate("message", mustJSON(t, message)) == nil {
					t.Errorf("R2 accepted missing %s", field)
				}
				attachment[field] = saved
			}
			for _, field := range []string{"sha256_verified", "integrity"} {
				saved := attachment[field]
				attachment[field] = nil
				if envelope.Validate("message", mustJSON(t, message)) == nil {
					t.Errorf("null %s accepted", field)
				}
				attachment[field] = saved
			}
			saved := attachment["expires_at"]
			if class == "keep" {
				attachment["expires_at"] = "9999-01-01T00:00:00.000Z"
			} else {
				attachment["expires_at"] = nil
			}
			if envelope.Validate("message", mustJSON(t, message)) == nil {
				t.Fatal("class/deadline mismatch accepted")
			}
			attachment["expires_at"] = saved
			req := loadExample(t, "01-send-request.json")
			req["attachments"] = []any{attachment}
			if envelope.Validate("sendRequest", mustJSON(t, req)) == nil {
				t.Fatal("send accepted server verification assertions")
			}
			verification := map[string]any{
				"sha256_verified": attachment["sha256_verified"],
				"integrity":       attachment["integrity"],
			}
			delete(attachment, "sha256_verified")
			delete(attachment, "integrity")
			for _, field := range []string{"sha256_verified", "integrity"} {
				t.Run("send_"+field+"_only", func(t *testing.T) {
					attachment[field] = verification[field]
					if envelope.Validate("sendRequest", mustJSON(t, req)) == nil {
						t.Errorf("send accepted server-only %s assertion", field)
					}
					delete(attachment, field)
				})
			}
			if err := envelope.Validate("sendRequest", mustJSON(t, req)); err != nil {
				t.Fatalf("reference-only send rejected: %v", err)
			}
			attachment["content_md5"] = "fake"
			if envelope.Validate("sendRequest", mustJSON(t, req)) == nil {
				t.Fatal("send accepted parked checksum field")
			}
		})
	}
}

func TestR2AttachmentWireAndDiskCompatibility(t *testing.T) {
	// Existing disk fixtures continue to validate without additive R2 fields.
	for _, f := range []string{"02-delivery-direct.json", "03-delivery-group-hold.json"} {
		if err := envelope.Validate("delivery", mustJSON(t, loadExample(t, f))); err != nil {
			t.Fatal(err)
		}
	}
	disk := envelope.Attachment{SHA256: strings.Repeat("a", 64), Size: 7, MediaType: "text/plain"}
	b := mustJSON(t, disk)
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		t.Fatal("decode")
	}
	for _, key := range []string{"blob_id", "sha256_verified", "integrity", "retention_class", "expires_at"} {
		if _, ok := m[key]; ok {
			t.Errorf("legacy attachment added %s", key)
		}
	}
	raw := mustJSON(t, r2AttachmentFixture("keep"))
	var a envelope.Attachment
	if json.Unmarshal(raw, &a) != nil {
		t.Fatal("R2 attachment decode")
	}
	b = mustJSON(t, a)
	if json.Unmarshal(b, &m) != nil {
		t.Fatal("R2 attachment encode")
	}
	if value, ok := m["expires_at"]; !ok || value != nil || m["blob_id"] != "blo_00000000000000000000000001" || m["sha256_verified"] != true || m["integrity"] == nil {
		t.Fatal("R2 roundtrip lost recorded fields/null")
	}
}
