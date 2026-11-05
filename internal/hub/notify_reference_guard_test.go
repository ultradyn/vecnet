package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/envelope"
	"strings"
	"time"
)

// Test-only access to the post-commit raw SQL reader, without adding a
// production hook or a public API solely for baseline tests.
func (h *Hub) NotifyOperatorReferenceGuard(ctx context.Context, msgID string) {
	h.notifyOperatorDeliveries(ctx, msgID)
}

func TestReferenceGuardWireRenderer(t *testing.T) {
	raw, err := os.ReadFile("testdata/reference_guard/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []struct{ Name, Type, Data string }
	if err := json.Unmarshal(raw, &fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Type != "delivery" {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			var wire deliveryWire
			if err := json.Unmarshal([]byte(f.Data), &wire); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			assertReferenceRenderer(t, "wire-"+f.Name+".json", got)
			// Exercise the public Message renderer too, including nil/empty optionals.
			var msg envelope.Message
			if err := json.Unmarshal(wire.Message, &msg); err != nil {
				t.Fatal(err)
			}
			if f.Name == "nil_optional" {
				msg.Attachments = nil
				msg.Metadata = nil
			}
			got, err = json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			assertReferenceRenderer(t, "message-"+f.Name+".json", got)
		})
	}
	for _, s := range []sender{{peer: "hub", kind: "hub", name: "hub"}, {peer: "src_00000000000000000000000001", kind: "source", name: "Source 一"}, {kind: "operator", name: "Operator"}} {
		got, err := json.Marshal(s.info())
		if err != nil {
			t.Fatal(err)
		}
		assertReferenceRenderer(t, "sender-"+s.kind+".json", got)
	}
}

func assertReferenceRenderer(t *testing.T, name string, got []byte) {
	t.Helper()
	path := "testdata/reference_guard/" + name
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal(fmt.Sprintf("renderer mismatch %s: got sha256=%x want sha256=%x", name, sha256.Sum256(got), sha256.Sum256(want)))
	}
}

func TestReferenceGuardBlobURLRenderer(t *testing.T) {
	fixed, err := envelope.ParseTime("2030-01-02T03:04:05.006Z")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.PublicURL = "https://frozen.example"
	h := &Hub{Cfg: cfg, Now: func() time.Time { return fixed }, Blobs: &blob.Store{Key: bytes.Repeat([]byte{0x42}, 32)}}
	msg := envelope.Message{Attachments: []envelope.Attachment{{SHA256: strings.Repeat("a", 64), Size: 16, MediaType: "text/plain"}, {SHA256: strings.Repeat("b", 64), Size: 32, MediaType: "application/json", OriginURL: "https://origin.example/reference.json"}}}
	var urls []string
	for _, peer := range []string{"peer_00000000000000000000000002", "peer_00000000000000000000000003"} {
		rendered := h.deliveryBlobURLs(msg, peer, map[string]bool{strings.Repeat("a", 64): true})
		got, err := json.Marshal(rendered.Attachments)
		if err != nil {
			t.Fatal(err)
		}
		assertReferenceRenderer(t, "blob-"+peer+".json", got)
		urls = append(urls, rendered.Attachments[0].BlobURL)
		if rendered.Attachments[1].BlobURL != "" || msg.Attachments[0].BlobURL != "" {
			t.Fatal("blob scoping or canonical attachment changed")
		}
	}
	if urls[0] == urls[1] {
		t.Fatal("blob URLs lost recipient scoping")
	}
}
