package mcpserver_test

import (
	"encoding/json"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestProjectedCursorKinds(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "cursor descriptions")
	_, response := rpc(t, env, secret, "tools/list", nil)
	var catalog struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &catalog); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ property, description string }{
		"vecnet_thread":  {"after", "Message ID: next_after; locators also accept expired anchors."},
		"vecnet_inbox":   {"after", "Event cursor e<epoch>.<seq>. head skips existing backlog. Omit for last ack."},
		"vecnet_threads": {"cursor", "Opaque cursor: next from the previous page."},
		"vecnet_ack":     {"cursor", "Event cursor: next_cursor from the inbox page you have fully handled."},
	}
	for _, tool := range catalog.Tools {
		if expected, ok := want[tool.Name]; ok {
			property := tool.InputSchema["properties"].(map[string]any)[expected.property].(map[string]any)
			if property["description"] != expected.description {
				t.Errorf("%s.%s description = %q, want %q", tool.Name, expected.property, property["description"], expected.description)
			}
			delete(want, tool.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}
