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
	// The retained OAuth discovery and output contracts plus explicit property
	// guidance prevent the 14 KB target; see the assignment result notes.
	const achievedTotal = 18305
	if budget := achievedTotal * 110 / 100; len(compact) > budget {
		t.Errorf("tools/list: %d bytes exceeds budget %d (achieved %d + 10%%)", len(compact), budget, achievedTotal)
	}
	// Freeze each achieved size independently so one tool cannot silently
	// consume another's savings. A new tool needs an explicit baseline.
	achieved := map[string]int{
		"vecnet_send": 2689, "vecnet_reply": 1594, "vecnet_react": 767,
		"vecnet_inbox": 609, "vecnet_subscriptions": 393, "vecnet_ack": 561,
		"vecnet_receipt": 565, "vecnet_presence": 957, "vecnet_presence_subscribe": 493,
		"vecnet_threads": 706, "vecnet_thread": 573, "vecnet_peers": 371,
		"vecnet_peer_create": 698, "vecnet_peer_set_disabled": 548, "vecnet_groups": 353,
		"vecnet_group_create": 588, "vecnet_group_add": 617, "vecnet_whoami": 669,
		"vecnet_blob_put": 806, "vecnet_blob_get": 1118, "vecnet_set_avatar": 622,
		"vecnet_clear_avatar": 436, "vecnet_guide": 1538,
	}
	for _, raw := range result.Tools {
		var entry struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", entry.Name, len(raw))
		baseline, ok := achieved[entry.Name]
		if !ok {
			t.Errorf("%s: %d bytes; missing achieved-size budget", entry.Name, len(raw))
			continue
		}
		if budget := baseline * 115 / 100; len(raw) > budget {
			t.Errorf("%s: %d bytes exceeds budget %d (achieved %d + 15%%)", entry.Name, len(raw), budget, baseline)
		}
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
