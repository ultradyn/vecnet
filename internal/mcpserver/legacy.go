package mcpserver

import "fmt"

// ParseLegacySessions validates the operator's legacy transport switch before
// startup opens a database or listener. Empty means the historical default.
func ParseLegacySessions(value string) (string, error) {
	switch value {
	case "", "on":
		return "on", nil
	case "off-for-oauth", "off":
		return value, nil
	default:
		return "", fmt.Errorf("MCP legacy sessions: expected on, off-for-oauth or off")
	}
}

func (s *Server) legacyAllowed(p Principal) bool {
	switch s.LegacySessions {
	case "off":
		return false
	case "off-for-oauth":
		return p.OAuth == nil
	default:
		return true
	}
}
