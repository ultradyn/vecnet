package mcpserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

func TestProjectionCatalogDescriptions(t *testing.T) {
	var doc struct {
		Defs schema `json:"$defs"`
	}
	if err := json.Unmarshal(envelope.SchemaJSON(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, spec := range append(toolSpecs(doc.Defs), guideSpec(), routeStatsSpec()) {
		if strings.TrimSpace(toolDescriptions[spec.name]) == "" {
			t.Errorf("missing MCP description for %s", spec.name)
		}
	}
	if toolPropertyDescriptions["vecnet_peer_lifecycle"]["action"] == "" {
		t.Error("missing lifecycle action description")
	}
	if strings.TrimSpace(toolPropertyDescriptions["vecnet_route_stats"]["id"]) == "" {
		t.Error("missing route stats id description")
	}
}

func TestProjectionMissingDescriptionFallsBack(t *testing.T) {
	entry := schema{"name": "vecnet_future_tool", "description": "Original safe description.",
		"inputSchema": schema{"type": "object", "properties": schema{}}}
	if got := projectTool(entry); !reflect.DeepEqual(got, entry) {
		t.Errorf("fallback changed original entry: %v", got)
	}
}
