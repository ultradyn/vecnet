package mcpserver_test

import (
	"encoding/json"
	"testing"

	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestProjectedPagingBoundsMatchCalls(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "paging bounds")
	var sent sendResult
	toolOK(t, env, secret, "vecnet_send", map[string]any{
		"idempotency_key": "bounds", "priority": "hold", "recipients": []string{peer},
		"content": map[string]any{"text": "bounds"},
	}, &sent)
	_, listed := rpc(t, env, secret, "tools/list", nil)
	var catalog struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(listed.Result, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, tool := range catalog.Tools {
		maximum := 200
		args := map[string]any{}
		switch tool.Name {
		case "vecnet_inbox", "vecnet_threads":
		case "vecnet_thread":
			args["id"] = sent.ThreadID
		case "vecnet_blob_get":
			maximum = 131072
			args["sha256"] = "missing"
		default:
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			limit := tool.InputSchema["properties"].(map[string]any)["limit"].(map[string]any)
			if limit["minimum"] != float64(1) || limit["maximum"] != float64(maximum) {
				t.Errorf("projected limit = %v, want minimum 1 maximum %d", limit, maximum)
			}
			for _, value := range []int{0, 1, maximum, maximum + 1} {
				args["limit"] = value
				_, response := rpc(t, env, secret, "tools/call", map[string]any{"name": tool.Name, "arguments": args})
				if value == 0 || value > maximum {
					if response.Error == nil || response.Error.Code != mcpserver.ErrInvalidParams {
						t.Errorf("limit %d: want invalid params, got %+v", value, response)
					}
				} else if response.Error != nil {
					t.Errorf("limit %d refused: %+v", value, response.Error)
				}
			}
		})
	}
}
