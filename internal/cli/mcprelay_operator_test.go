package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A profile secret padded with whitespace is still the operator's: the hub
// trims the bearer before authenticating it.
func TestMCPRelayRefusesPaddedOperator(t *testing.T) {
	for _, pad := range []string{" ", "  ", "\t", " ", " ", "　"} {
		t.Run(fmt.Sprintf("%q", pad), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer server.Close()
			u := profileUser(t, server.URL, pad+"vn_operator_test-secret")
			relayRefusal(t, u, []string{"--net", "p", "mcp"}, "vecnet mcp: operator credentials stay on the CLI; enrol a peer or principal profile for MCP")
			if hits.Load() != 0 {
				t.Fatalf("hub reached %d times", hits.Load())
			}
		})
	}
}
