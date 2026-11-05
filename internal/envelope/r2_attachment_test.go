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

// TestR2AttachmentTypedRoundTripAllClasses: every retention class survives a
// typed envelope.Attachment round trip with its exact class, deadline (null
// only for keep) and stable reference fields, and the serialized attachment
// validates inside a real Message and Delivery.
func TestR2AttachmentTypedRoundTripAllClasses(t *testing.T) {
	for _, class := range []string{"1h", "6h", "12h", "1d", "7d", "30d", "keep"} {
		t.Run(class, func(t *testing.T) {
			fixture := r2AttachmentFixture(class)
			var a envelope.Attachment
			if err := json.Unmarshal(mustJSON(t, fixture), &a); err != nil {
				t.Fatalf("decode: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(mustJSON(t, a), &got); err != nil {
				t.Fatalf("encode: %v", err)
			}
			for _, key := range []string{"blob_id", "sha256", "size", "media_type", "retention_class", "sha256_verified"} {
				if got[key] != fixture[key] && !(key == "size" && got[key] == float64(7)) {
					t.Errorf("%s = %v, want %v", key, got[key], fixture[key])
				}
			}
			end, ok := got["expires_at"]
			if !ok {
				t.Fatal("expires_at omitted")
			}
			if class == "keep" && end != nil {
				t.Fatalf("keep expires_at = %v, want null", end)
			}
			if class != "keep" && end != fixture["expires_at"] {
				t.Fatalf("expires_at = %v, want %v", end, fixture["expires_at"])
			}
			integ, _ := got["integrity"].(map[string]any)
			if integ["algorithm"] != "sha256" || integ["value"] != strings.Repeat("a", 64) || integ["verified"] != true {
				t.Fatalf("integrity = %v", got["integrity"])
			}
			delivery := loadExample(t, "02-delivery-direct.json")
			message := delivery["message"].(map[string]any)
			message["attachments"] = []any{got}
			if err := envelope.Validate("message", mustJSON(t, message)); err != nil {
				t.Fatalf("serialized R2 attachment rejected in message: %v", err)
			}
			if err := envelope.Validate("delivery", mustJSON(t, delivery)); err != nil {
				t.Fatalf("serialized R2 attachment rejected in delivery: %v", err)
			}
		})
	}
}

// TestR2AttachmentForbidsBlobURL: an R2 reference is a stable reference only;
// a blob_url beside blob_id is rejected in Messages, Deliveries and sends,
// while a legacy disk attachment keeps its blob_url.
func TestR2AttachmentForbidsBlobURL(t *testing.T) {
	const harmless = "https://vecnet.example/v1/blobs/synthetic"
	delivery := loadExample(t, "02-delivery-direct.json")
	message := delivery["message"].(map[string]any)
	legacy := message["attachments"].([]any)[0].(map[string]any)
	if _, ok := legacy["blob_url"]; !ok || legacy["blob_id"] != nil {
		t.Fatal("legacy disk fixture must carry blob_url and no blob_id")
	}
	if err := envelope.Validate("delivery", mustJSON(t, delivery)); err != nil {
		t.Fatalf("legacy disk blob_url rejected: %v", err)
	}
	for _, class := range []string{"1d", "keep"} {
		t.Run(class, func(t *testing.T) {
			delivery := loadExample(t, "02-delivery-direct.json")
			message := delivery["message"].(map[string]any)
			attachment := r2AttachmentFixture(class)
			message["attachments"] = []any{attachment}
			if err := envelope.Validate("delivery", mustJSON(t, delivery)); err != nil {
				t.Fatalf("R2 control rejected: %v", err)
			}
			attachment["blob_url"] = harmless
			if envelope.Validate("message", mustJSON(t, message)) == nil {
				t.Error("message accepted blob_url on an R2 attachment")
			}
			if envelope.Validate("delivery", mustJSON(t, delivery)) == nil {
				t.Error("delivery accepted blob_url on an R2 attachment")
			}
			send := loadExample(t, "01-send-request.json")
			ref := r2AttachmentFixture(class)
			delete(ref, "sha256_verified")
			delete(ref, "integrity")
			send["attachments"] = []any{ref}
			if err := envelope.Validate("sendRequest", mustJSON(t, send)); err != nil {
				t.Fatalf("send reference control rejected: %v", err)
			}
			ref["blob_url"] = harmless
			if envelope.Validate("sendRequest", mustJSON(t, send)) == nil {
				t.Error("send accepted blob_url on an R2 reference")
			}
		})
	}
}
