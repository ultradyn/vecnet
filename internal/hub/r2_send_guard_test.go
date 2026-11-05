package hub_test

import (
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// TestSendRejectsR2ReferenceOnDiskPipeline: the send schema admits the
// additive R2 reference fields (blob_id, retention_class, expires_at), but
// until grant-aware attachment authorization lands the disk send path cannot
// resolve a persisted R2 object. A send naming a blob_id is therefore 422
// unknown_blob before any SHA lookup or origin_url fallback, and creates no
// Message or Event; ordinary disk references keep working.
func TestSendRejectsR2ReferenceOnDiskPipeline(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	const content = "owned disk bytes"
	sha := putBlob(t, env, aSec, content)
	eventsBefore := count(t, env, `SELECT count(*) FROM events`)

	r2Ref := func(extra map[string]any) []any {
		att := map[string]any{
			"blob_id": "blo_00000000000000000000000001", "sha256": sha, "size": len(content),
			"media_type": "text/plain", "retention_class": "keep", "expires_at": nil,
		}
		for k, v := range extra {
			att[k] = v
		}
		return []any{att}
	}
	cases := map[string][]any{
		"owned_disk_hash":  r2Ref(nil),
		"origin_url_only":  r2Ref(map[string]any{"sha256": shaHex("not stored"), "origin_url": "https://example.com/r.txt"}),
		"finite_retention": r2Ref(map[string]any{"retention_class": "1d", "expires_at": "2026-11-06T00:00:00.000Z"}),
	}
	for name, atts := range cases {
		t.Run(name, func(t *testing.T) {
			body := req("r2-"+name, b)
			body["attachments"] = atts
			_, details := mustReject(t, env, aSec, body, http.StatusUnprocessableEntity, "unknown_blob")
			if details["path"] != "/attachments/0/blob_id" {
				t.Fatalf("details.path = %v, want /attachments/0/blob_id", details["path"])
			}
			if _, ok := details["size"]; ok {
				t.Fatalf("rejection details %v reveal a size", details)
			}
		})
	}
	if n := count(t, env, `SELECT count(*) FROM messages`); n != 0 {
		t.Fatalf("messages = %d after rejected R2 references, want 0", n)
	}
	if n := count(t, env, `SELECT count(*) FROM events`); n != eventsBefore {
		t.Fatalf("events = %d after rejected R2 references, want %d", n, eventsBefore)
	}

	// The ordinary disk reference to the same owned hash still sends.
	body := req("disk", b)
	body["attachments"] = []any{map[string]any{"sha256": sha, "size": len(content), "media_type": "text/plain"}}
	mustSend(t, env, aSec, body)
	m := onlyDelivery(t, env, b)
	if len(m.Attachments) != 1 || m.Attachments[0].BlobID != "" || m.Attachments[0].BlobURL == "" {
		t.Fatalf("disk attachment = %+v, want a legacy hosted reference with blob_url", m.Attachments)
	}
}
