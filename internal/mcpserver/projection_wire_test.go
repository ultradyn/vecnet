package mcpserver_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestProjectionPreservesOutputContracts(t *testing.T) {
	env := testhub.Start(t)
	secret := env.OperatorSecret
	status, r := rpc(t, env, secret, "tools/list", nil)
	if status != 200 || r.Error != nil {
		t.Fatalf("tools/list: %d %+v", status, r.Error)
	}
	var result struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/output-before-projection.json")
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/output-onboarding-before-projection.json")
	if err != nil {
		t.Fatal(err)
	}
	var onboarding map[string]any
	if err := json.Unmarshal(raw, &onboarding); err != nil {
		t.Fatal(err)
	}
	for name, doc := range onboarding {
		if _, exists := before[name]; exists {
			t.Fatalf("duplicate output baseline for %s", name)
		}
		before[name] = doc
	}
	// Overlay only R2 outputs independently captured from unprojected r2-build.
	raw, err = os.ReadFile("testdata/output-r2-transfers.json")
	if err != nil {
		t.Fatal(err)
	}
	var transfers map[string]any
	if err := json.Unmarshal(raw, &transfers); err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 3 {
		t.Fatal("R2 output fixture must cover exactly three tools")
	}
	for _, name := range []string{"vecnet_blob_put", "vecnet_blob_get", "vecnet_blob_status"} {
		doc, ok := transfers[name]
		if !ok {
			t.Fatalf("missing R2 output baseline %s", name)
		}
		before[name] = doc
	}
	after := map[string]any{}
	for _, tool := range result.Tools {
		if tool.OutputSchema != nil {
			after[tool.Name] = withoutDescriptions(tool.OutputSchema)
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("projection changed an output validation contract")
	}
}

func withoutDescriptions(value any) any {
	switch node := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range node {
			if k != "description" {
				out[k] = withoutDescriptions(v)
			}
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, v := range node {
			out[i] = withoutDescriptions(v)
		}
		return out
	}
	return value
}

// The one hidden top-level argument still follows its old validation and
// handling: a valid origin is ignored, while a malformed one is refused.
func TestProjectedHiddenOriginStillValidatedAndIgnored(t *testing.T) {
	env := testhub.Start(t)
	_, sender := env.NewPeer("session", "origin sender")
	peer, _ := env.NewPeer("session", "origin recipient")
	args := map[string]any{
		"idempotency_key": "origin-1", "priority": "hold", "content": map[string]any{"text": "hello"},
		"recipients": []string{peer}, "origin": map[string]any{"system": "github", "thread_id": "42"},
	}
	var sent sendResult
	toolOK(t, env, sender, "vecnet_send", args, &sent)
	var history struct {
		Messages []map[string]any `json:"messages"`
	}
	toolOK(t, env, sender, "vecnet_thread", map[string]any{"id": sent.ThreadID}, &history)
	if len(history.Messages) != 1 || history.Messages[0]["origin"] != nil {
		t.Fatalf("MCP origin was not ignored: %v", history.Messages)
	}
	args["origin"] = map[string]any{"system": "github"}
	if path := invalidPath(t, env, sender, "vecnet_send", args); path != "/origin/thread_id" {
		t.Fatalf("hidden origin validation path=%q", path)
	}
}

func TestGuideCoversProjectedDetails(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "guide limits")
	for topic, needles := range map[string][]string{
		"replying": {"auto-spills", "history_access", "locked_participants", "max_depth_exceeded", "60 sends/minute", "quota on providers billed per run", "sha256_verified", "blob_id", "job_key", "sequence", "escalate_to_operator", "removing/re-adding", "origin is ignored", "24h", "4096", "64 KiB", "phase grant", "phase complete", "300 seconds", "600 seconds", "HTTP Range", "model context", "30 URL mints", "blob_url_rate_limited", "blob_expired", "blob_purged", "24-hour retention", "blobs.expiry", "Content-Length", "If-None-Match", "x-amz-checksum-sha256", "10 minutes"},
		"events":   {"weekly_remaining_pct", "Presence is self-reported", "Receipt note is at most 1024", "limit is an integer 1-200"},
		"start":    {"SVG is refused", "10-second fetch", "changes/hour", "active-peer limit", "not idempotent"},
	} {
		t.Run(topic, func(t *testing.T) {
			result := callTool(t, env, secret, "vecnet_guide", map[string]any{"topic": topic})
			var guide guideWire
			if err := json.Unmarshal(result.StructuredContent, &guide); err != nil {
				t.Fatal(err)
			}
			for _, needle := range needles {
				if !strings.Contains(guide.Text, needle) {
					t.Errorf("guide %s lacks %q", topic, needle)
				}
			}
		})
	}
}
