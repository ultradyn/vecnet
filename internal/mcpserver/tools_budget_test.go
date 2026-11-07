package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/XertroV/vecnet/internal/testhub"
)

// Measure the compact tools/list result a peer actually receives, including
// annotations, output schemas, security schemes and the tools array wrapper.
func TestToolsBudget(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "schema budget")
	status, r := rpc(t, env, secret, "tools/list", nil)
	if status != 200 || r.Error != nil {
		t.Fatalf("tools/list: HTTP %d, error %+v", status, r.Error)
	}
	var result struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tools/list: %d bytes", len(compact))
	// Keep master's total and every per-tool pin unchanged. R2 has separate
	// pins because its two transfer schemas grow and status is a new tool.
	const achievedTotal = 21276
	const achievedR2Total = 28051
	if budget := achievedR2Total * 110 / 100; len(compact) > budget {
		t.Errorf("R2 tools/list: %d bytes exceeds budget %d (achieved %d + 10%%)", len(compact), budget, achievedR2Total)
	}
	// Freeze each achieved size independently so one tool cannot silently
	// consume another's savings. A new tool needs an explicit baseline.
	achieved := map[string]int{
		"vecnet_send": 2826, "vecnet_reply": 1696, "vecnet_react": 844,
		"vecnet_inbox": 706, "vecnet_subscriptions": 432, "vecnet_ack": 624,
		"vecnet_receipt": 610, "vecnet_presence": 1005, "vecnet_presence_subscribe": 514,
		"vecnet_threads": 791, "vecnet_thread": 670, "vecnet_peers": 370,
		"vecnet_peer_create": 747, "vecnet_peer_set_disabled": 555, "vecnet_groups": 389,
		"vecnet_group_create": 627, "vecnet_group_add": 613, "vecnet_whoami": 726,
		"vecnet_blob_put": 873, "vecnet_blob_get": 1340, "vecnet_set_avatar": 670,
		"vecnet_clear_avatar": 459, "vecnet_guide": 1534,
		"vecnet_peer_lifecycle": 634, "vecnet_peer_invite_get": 468,
		"vecnet_peer_invite_revoke": 495,
	}
	r2Achieved := map[string]int{
		"vecnet_blob_put": 4676, "vecnet_blob_get": 2753, "vecnet_blob_status": 1558,
	}
	if len(result.Tools) != len(achieved)+1 {
		t.Errorf("tool budget coverage: listed=%d pinned=%d", len(result.Tools), len(achieved)+1)
	}
	legacyTotal := len(compact)
	for _, raw := range result.Tools {
		var entry struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", entry.Name, len(raw))
		baseline, ok := achieved[entry.Name]
		if r2Baseline, isR2 := r2Achieved[entry.Name]; isR2 {
			// Remove only the explicit R2 additions when applying master's total
			// budget. All unchanged tools still use their original per-tool pins.
			legacyTotal += baseline - len(raw)
			if !ok {
				legacyTotal-- // remove the new tool's array separator
			}
			baseline, ok = r2Baseline, true
		}
		if !ok {
			t.Errorf("%s: %d bytes; missing achieved-size budget", entry.Name, len(raw))
			continue
		}
		if budget := baseline * 115 / 100; len(raw) > budget {
			t.Errorf("%s: %d bytes exceeds budget %d (achieved %d + 15%%)", entry.Name, len(raw), budget, baseline)
		}
	}
	if budget := achievedTotal * 110 / 100; legacyTotal > budget {
		t.Errorf("catalog excluding R2 growth: %d bytes exceeds original budget %d", legacyTotal, budget)
	}
}

// These names convey their meaning without prose, in both input and output.
// Keep this explicit and small; ambiguous names such as as/after/limit need
// descriptions at the point of use instead of expanding this allowlist.
var selfExplanatoryProperties = map[string]bool{
	"id": true, "name": true, "text": true, "note": true,
	"peer_id": true, "principal_id": true, "group_id": true,
	"message_id": true, "delivery_id": true,
	"sha256": true, "media_type": true, "size": true,
	"encoding": true, "weekly_remaining_pct": true, "kind": true,
}

func TestProjectedProperties(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "schema wording")
	status, r := rpc(t, env, secret, "tools/list", nil)
	if status != 200 || r.Error != nil {
		t.Fatalf("tools/list: HTTP %d, error %+v", status, r.Error)
	}
	var result struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			if tool.Name == "vecnet_send" {
				names := map[string]bool{}
				propertyNames(tool.InputSchema, names)
				for _, hidden := range []string{"origin", "sha256_verified", "integrity"} {
					if names[hidden] {
						t.Errorf("discovery exposes hub-derived or forbidden field %s", hidden)
					}
				}
			}
			for label, doc := range map[string]map[string]any{"input": tool.InputSchema, "output": tool.OutputSchema} {
				if doc == nil {
					continue
				}
				compiler := jsonschema.NewCompiler()
				uri := "urn:vecnet:projected:" + tool.Name + ":" + label
				if err := compiler.AddResource(uri, doc); err != nil {
					t.Fatal(err)
				}
				if _, err := compiler.Compile(uri); err != nil {
					t.Fatalf("%s schema: %v", label, err)
				}
				checkProjectedProperties(t, doc, label)
			}
		})
	}
}

func checkProjectedProperties(t *testing.T, value any, path string) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		if description, ok := node["description"].(string); ok {
			if len(description) > 100 {
				t.Errorf("%s: property description is %d bytes", path, len(description))
			}
			for _, httpProse := range []string{"POST ", "PATCH ", "/v1/"} {
				if strings.Contains(description, httpProse) {
					t.Errorf("%s: HTTP prose %q", path, description)
				}
			}
		}
		if props, ok := node["properties"].(map[string]any); ok {
			for name, value := range props {
				prop := value.(map[string]any)
				if desc, _ := prop["description"].(string); strings.TrimSpace(desc) == "" && !selfExplanatoryProperties[name] {
					t.Errorf("%s/%s: needs a description or an explicitly self-explanatory name", path, name)
				}
			}
		}
		for key, child := range node {
			checkProjectedProperties(t, child, path+"/"+key)
		}
	case []any:
		for _, child := range node {
			checkProjectedProperties(t, child, path+"/[]")
		}
	}
}
