package opclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type discoveryDiagFlagsError struct {
	message            string
	timeout, temporary bool
}

func (e *discoveryDiagFlagsError) Error() string   { return e.message }
func (e *discoveryDiagFlagsError) Timeout() bool   { return e.timeout }
func (e *discoveryDiagFlagsError) Temporary() bool { return e.temporary }

// Source-derived expected RED, UNRUN: discovery currently returns a stored
// checked-hub URL error whose nested received cause reaches public Error().
// Invoke the public methods through real Discover/Client.Do and the existing
// RoundTripper seam; never call outcome or the presentation helper directly.
func TestUploadDiscoveryTransportDiagnostics(t *testing.T) {
	causes := []struct {
		name               string
		timeout, temporary bool
	}{
		{"plain", false, false},
		{"url-wrapped", false, false},
		{"tls", false, false},
		{"net-op-neither", false, false},
		{"net-op-timeout-only", true, false},
		{"net-op-temporary-only", false, true},
		{"net-op-both", true, true},
	}
	hubs := []struct{ name, base string }{
		{"prefix", "https://checked.example/I6_CHECKED_SOURCE/Vecnet/%2Fexact"},
		{"ipv6", "https://[2001:db8::7]:8443/I6_CHECKED_SOURCE/Vecnet/%2Fexact"},
	}
	for _, tc := range causes {
		for _, hub := range hubs {
			for _, surface := range []string{"Upload", "UploadWithProgress"} {
				t.Run(tc.name+"/"+hub.name+"/"+surface, func(t *testing.T) {
					canary := "I6_DISCOVERY_" + tc.name
					var cause error
					switch tc.name {
					case "plain":
						cause = errors.New("received cause " + canary)
					case "url-wrapped":
						cause = &url.Error{Op: "parse", URL: "https://received.example/" + canary, Err: errors.New("received cause " + canary)}
					case "tls":
						cause = &tls.RecordHeaderError{Msg: "received TLS cause " + canary}
					default:
						cause = &net.OpError{Op: "dial", Net: "tcp", Err: &discoveryDiagFlagsError{
							message: "received network cause " + canary, timeout: tc.timeout, temporary: tc.temporary,
						}}
					}
					const secret = "vn_operator_I6_DISCOVERY_SYNTHETIC_ONLY"
					c := New(hub.base, secret)
					discovery, puts, other, progressCalls := 0, 0, 0, 0
					var progressBytes int64
					blocked := errors.New("discovery fixture blocked an unexpected request")
					c.hc.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
						other++
						return nil, blocked
					})
					c.uploadHC.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
						switch {
						case r.Method == http.MethodGet && r.URL.String() == hub.base+"/v1/blob-capabilities":
							discovery++
							if r.Header.Get("Authorization") != "Bearer "+secret {
								t.Error("discovery lost its synthetic hub credential")
							}
							return nil, cause
						case r.Method == http.MethodPut && r.URL.String() == hub.base+"/v1/blobs?ttl=86400":
							puts++
						default:
							other++
						}
						return nil, blocked // No real dials, proxies, or ambient transport.
					})
					input := strings.NewReader("abc")
					var err error
					if surface == "Upload" {
						_, err = c.Upload(context.Background(), input, 3, "text/plain", "fixture.txt")
					} else {
						_, err = c.UploadWithProgress(context.Background(), input, 3, "text/plain", "fixture.txt", func(n int64) {
							progressCalls++
							progressBytes = n
						})
					}
					if err == nil {
						t.Fatal("failed discovery unexpectedly allowed upload success")
					}
					t.Run("controls", func(t *testing.T) {
						if discovery != 1 || puts != 0 || other != 0 {
							t.Errorf("discovery/raw-PUT/other requests = %d/%d/%d, want 1/0/0", discovery, puts, other)
						}
						if progressCalls != 0 || progressBytes != 0 || input.Len() != 3 {
							t.Error("discovery failure consumed upload input or reported progress")
						}
						if !errors.Is(err, cause) {
							t.Error("public upload error lost the exact injected cause")
						}
						var api *Error
						if errors.As(err, &api) {
							t.Error("discovery transport failure became an API error")
						}
						var ue *url.Error
						if !errors.As(err, &ue) || ue.URL != hub.base || ue.Op != http.MethodGet {
							t.Fatal("original checked-hub URL/operation error was lost")
						}
						if !strings.Contains(err.Error(), hub.base) {
							t.Error("public diagnostic dropped the original checked hub URL")
						}
						var ne net.Error
						if !errors.As(err, &ne) || ne.Timeout() != tc.timeout || ne.Temporary() != tc.temporary {
							t.Error("discovery changed true/false Timeout or Temporary semantics")
						}
						if _, ok := err.(net.Error); !ok {
							t.Error("public upload error lost its net.Error interface")
						}
						switch want := cause.(type) {
						case *url.Error:
							var got *url.Error
							if !errors.As(ue.Err, &got) || got != want {
								t.Error("injected nested URL error identity was lost")
							}
						case *net.OpError:
							var got *net.OpError
							if !errors.As(err, &got) || got != want {
								t.Error("injected network operation error identity was lost")
							}
						case *tls.RecordHeaderError:
							var got *tls.RecordHeaderError
							if !errors.As(err, &got) || got != want {
								t.Error("injected TLS error identity was lost")
							}
						}
					})
					t.Run("privacy", func(t *testing.T) {
						if strings.Contains(err.Error(), canary) {
							t.Errorf("received discovery cause reached public upload diagnostics: %q", err.Error())
						}
					})
				})
			}
		}
	}
}
