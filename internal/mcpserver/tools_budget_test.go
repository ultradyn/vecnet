package mcpserver_test

import (
	"encoding/json"
	"testing"

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
	t.Logf("tools/list compact JSON: %s", compact)
	if len(compact) > 14000 {
		t.Errorf("tools/list: %d bytes exceeds budget 14000", len(compact))
	}
	for _, raw := range result.Tools {
		var entry struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", entry.Name, len(raw))
		if len(raw) > 3000 {
			t.Errorf("%s: %d bytes exceeds budget 3000", entry.Name, len(raw))
		}
	}
}
