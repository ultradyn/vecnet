package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyAddsOnlyExactHTTPSOrigins(t *testing.T) {
	const r2 = "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	got := Policy(r2, r2)
	want := "connect-src 'self' " + r2 + ";"
	if !strings.Contains(got, want) || strings.Count(got, r2) != 1 {
		t.Fatalf("Policy(r2) = %q, want one %q", got, want)
	}
	if strings.Replace(got, " "+r2, "", 1) != CSP {
		t.Fatalf("Policy changed more than connect-src: %q", got)
	}
	for _, bad := range []string{
		"", "*", "https://*.r2.cloudflarestorage.com", "http://acct.r2.cloudflarestorage.com",
		"https://acct.r2.cloudflarestorage.com/", "https://acct.r2.cloudflarestorage.com/bucket",
		"https://acct.r2.cloudflarestorage.com?x=1", "https://user@acct.r2.cloudflarestorage.com",
		"https://acct.r2.cloudflarestorage.com; script-src *", "https://a.example https://b.example",
		"'unsafe-inline'", "data:", "blob:", "javascript:alert(1)", "https://acct.r2.cloudflarestorage.com#x",
		"HTTPS://acct.r2.cloudflarestorage.com",
	} {
		if p := Policy(bad); p != CSP {
			t.Fatalf("Policy(%q) widened the policy: %q", bad, p)
		}
	}
	if Policy() != CSP {
		t.Fatal("no origins must leave the policy unchanged")
	}
}

func TestHandlerServesThePolicy(t *testing.T) {
	const r2 = "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	for _, tc := range []struct {
		connect []string
		want    string
	}{{nil, CSP}, {[]string{r2}, Policy(r2)}} {
		rec := httptest.NewRecorder()
		Handler("", tc.connect...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ui/ = %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != tc.want {
			t.Fatalf("CSP = %q, want %q", got, tc.want)
		}
		if strings.Contains(rec.Header().Get("Content-Security-Policy"), "*") {
			t.Fatal("CSP carries a wildcard")
		}
	}
}
