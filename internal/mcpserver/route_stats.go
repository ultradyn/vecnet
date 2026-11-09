package mcpserver

import (
	"context"

	"github.com/XertroV/vecnet/internal/auth"
)

func routeStatsSpec() toolSpec {
	return toolSpec{
		name:        "vecnet_route_stats",
		description: toolDescriptions["vecnet_route_stats"],
		input: schema{
			"type": "object", "additionalProperties": false, "required": []string{"id"},
			"properties": schema{"id": schema{"type": "string", "minLength": 1, "maxLength": 80}},
		},
		annotations: readAnnotations(), call: callRouteStats,
	}
}

func callRouteStats(ctx context.Context, s *Server, c auth.Caller, raw []byte) (any, error) {
	if err := operatorOnly(c); err != nil {
		return nil, err
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	return s.Hub.RouteStats(ctx, c, args.ID)
}
