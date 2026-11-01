package httpapi

import (
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/oauth"
)

// newMCP builds the /mcp front door (PROTOCOL §4). It authenticates itself,
// so isPublic exempts /mcp from the bearer middleware; limitBody still caps
// its bodies at MaxBodyBytes. Tasks 4–5 register their methods here. A vecnet
// bearer and an OAuth access token are both accepted (PROTOCOL §4.5, mixed
// auth); without Deps.OAuth only the bearer is.
func newMCP(d Deps) *mcpserver.Server {
	chain := mcpserver.Chain{&mcpserver.BearerAuth{Resolver: d.Hub.Auth}}
	if d.OAuth != nil {
		chain = append(chain, &oauth.TokenAuth{S: d.OAuth})
	}
	m := mcpserver.New(d.Hub, chain, d.Hub.Cfg.PublicURL)
	if d.OAuth != nil {
		m.ResolveDotPeer = d.OAuth.ResolveDotPeer
	}
	if d.Heartbeat > 0 {
		m.Heartbeat = d.Heartbeat
	}
	m.Stop = d.Stop
	m.Kick = d.Kick
	return m
}
