package mcpserver

import (
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
)

func TestGuideInboxAndSubscriptionSemantics(t *testing.T) {
	for _, kind := range []string{"peer", "grokbot", "dot"} {
		p := Principal{Caller: auth.Caller{Kind: "peer", PeerKind: kind}}
		if kind != "peer" {
			p.OAuth = &OAuthInfo{}
		}
		for _, topic := range []string{"start", "events"} {
			guide := makeGuide(p, topic)
			for _, phrase := range []string{
				"head starts at the current log position and skips the existing backlog",
				"Omit after to read from your last ack, including unhandled backlog",
				"no active MCP Events subscription",
			} {
				if !strings.Contains(guide.Text, phrase) {
					t.Errorf("%s %s lacks %q", kind, topic, phrase)
				}
			}
			if kind != "dot" {
				for _, phrase := range []string{"Webhook and Grok route push health is independent of this list"} {
					if !strings.Contains(guide.Text, phrase) {
						t.Errorf("%s %s lacks %q", kind, topic, phrase)
					}
				}
			}
			if strings.Contains(guide.Text, "pushes are inactive") {
				t.Errorf("%s %s conflates MCP Events subscriptions with route push health", kind, topic)
			}
		}
	}
}
