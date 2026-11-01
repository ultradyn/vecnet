package egress

import (
	"crypto/x509"
	"net/http"
	"testing"
)

// SetRootCAs makes a Client from Policy.Client trust pool, so a test can
// talk TLS to an httptest server through the pinned transport.
func SetRootCAs(t *testing.T, c *http.Client, pool *x509.CertPool) {
	t.Helper()
	g, ok := c.Transport.(*guard)
	if !ok {
		t.Fatalf("Client transport is %T, want *guard", c.Transport)
	}
	g.base.TLSClientConfig.RootCAs = pool
}
