package events

import (
	"net/url"

	"github.com/XertroV/vecnet/internal/mcpdiag"
)

// LogLifecycle emits only routing metadata and allowlisted outcome fields.
// Callback paths, secrets, bodies and raw errors must never be passed here.
func LogLifecycle(kind string, s Subscription, fields map[string]any) {
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["peer_id"] = s.PeerID
	fields["subscription_id"] = s.ID
	if u, err := url.Parse(s.URL); err == nil {
		fields["callback_host"] = u.Hostname()
	}
	mcpdiag.Emit(kind, fields)
}
