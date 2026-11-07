package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// The operator UI may connect to exactly the configured R2 S3 origin (direct
// transfers, R2 blobs plan Task 7) and nothing wider; a disk hub adds nothing.
func TestR2UIConnectSrcIsTheExactConfiguredOrigin(t *testing.T) {
	f := newR2HTTP(t)
	origin := "https://" + fakeAccount + ".r2.cloudflarestorage.com"
	resp, body := f.env.Do("", "GET", "/ui/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/ = %d; body %.200s", resp.StatusCode, body)
	}
	mustCSP(t, resp, "R2 GET /ui/")
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' "+origin+";") {
		t.Fatalf("CSP %q lacks connect-src for exactly %s", csp, origin)
	}
	if strings.Count(csp, "r2.cloudflarestorage.com") != 1 {
		t.Fatalf("CSP %q names the R2 origin outside connect-src", csp)
	}
	// Capabilities advertise the same single origin.
	r, raw := f.request(t, f.env.OperatorSecret, "GET", "/v1/blob-capabilities", nil, "")
	if r.StatusCode != 200 || !strings.Contains(string(raw), `"download_origins":["`+origin+`"]`) {
		t.Fatalf("capabilities = %d %s", r.StatusCode, raw)
	}

	disk := testhub.Start(t)
	resp, _ = disk.Do("", "GET", "/ui/", nil)
	if got := resp.Header.Get("Content-Security-Policy"); strings.Contains(got, "r2.cloudflarestorage.com") || !strings.Contains(got, "connect-src 'self';") {
		t.Fatalf("disk hub CSP = %q", got)
	}
}
