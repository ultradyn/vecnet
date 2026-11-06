package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
)

func callRouteStats(ctx context.Context, s *Server, c auth.Caller, raw []byte) (any, error) {
	if c.Kind != "operator" {
		return nil, hub.E(http.StatusForbidden, "forbidden", "route stats are operator-only")
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
		return nil, hub.E(http.StatusUnprocessableEntity, "invalid_value", "id is required")
	}
	return s.Hub.RouteStats(ctx, p.ID)
}
