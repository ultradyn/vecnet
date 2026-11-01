package httpapi

import (
	"github.com/XertroV/vecnet/internal/mcpserver"
)

// newMCP builds the /mcp front door (PROTOCOL §4). It authenticates itself,
// so isPublic exempts /mcp from the bearer middleware; limitBody still caps
// its bodies at MaxBodyBytes. Tasks 3–5 register their methods here, and
// Task 6 adds the OAuth authenticator to the chain.
func newMCP(d Deps) *mcpserver.Server {
	m := mcpserver.New(d.Hub, mcpserver.Chain{&mcpserver.BearerAuth{Resolver: d.Hub.Auth}}, d.Hub.Cfg.PublicURL)
	if d.Heartbeat > 0 {
		m.Heartbeat = d.Heartbeat
	}
	m.Stop = d.Stop
	return m
}
