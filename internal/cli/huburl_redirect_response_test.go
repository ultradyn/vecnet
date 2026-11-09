package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Source-derived hypothesis, not an executed reproduction. Malformed 302
// cases target real Client.Do parsing; parseable 307s retain API/no-follow
// semantics. Every value/credential is synthetic. Do not parallelize these
// DefaultTransport fixtures, following huburl_redact_test.go's convention.
var redirectDiagCLICases = []struct {
	name, location, canary string
	malformed              bool
}{
	{"authority", "https://received.example:I6_AUTHORITY/vecnet", "I6_AUTHORITY", true},
	{"ipv6", "https://[I6_IPV6/vecnet", "I6_IPV6", true},
	{"escape", "https://received.example/I6_ESCAPE%zz", "I6_ESCAPE", true},
	{"userinfo", "https://alice:I6_USERINFO@received.example/vecnet", "I6_USERINFO", false},
	{"username-only", "https://I6_USERNAME@received.example/vecnet", "I6_USERNAME", false},
	{"query", "https://received.example/vecnet?token=I6_QUERY", "I6_QUERY", false},
	{"empty-query", "https://received.example/I6_EMPTY_QUERY?", "I6_EMPTY_QUERY", false},
	{"fragment", "https://received.example/vecnet#I6_FRAGMENT", "I6_FRAGMENT", false},
	{"empty-fragment", "https://received.example/I6_EMPTY_FRAGMENT#", "I6_EMPTY_FRAGMENT", false},
	{"relative", "/received-relative", "", false},
	{"https-prefix", "https://received.example/Vecnet/%2Fexact/", "", false},
	{"https-ipv6-prefix", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", "", false},
}

const redirectDiagCLISecret = "vn_operator_I6_SYNTHETIC_ONLY"

type redirectDiagCLIBody struct {
	io.ReadCloser
	closed *atomic.Int32
	once   atomic.Bool
}

func (b *redirectDiagCLIBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		b.closed.Add(1)
	}
	return b.ReadCloser.Close()
}

type redirectDiagCLIFixture struct {
	base, path string
	getenv     func(string) string

	requests, probes, destinations, closed atomic.Int32
}

func newRedirectDiagCLIFixture(t *testing.T, location, path string, status int) *redirectDiagCLIFixture {
	t.Helper()
	f := &redirectDiagCLIFixture{path: path}
	const prefix = "/I6_CHECKED_SOURCE/vecnet"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix+"/v1/hub/identity" && r.Method == http.MethodGet {
			f.probes.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("identity preflight carried a credential")
			}
			http.NotFound(w, r) // Existing best-effort TOFU supports old hubs.
			return
		}
		if r.URL.Path != prefix+path || r.Method != http.MethodGet {
			f.destinations.Add(1) // Includes an accidentally followed relative URL.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+redirectDiagCLISecret {
			t.Error("command did not use its synthetic profile credential")
		}
		w.Header().Set("Location", location) // Do not normalize via http.Redirect.
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "redirect fixture\n")
	}))
	t.Cleanup(source.Close)
	f.base = source.URL + prefix
	_, baseEnv := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n", f.base, redirectDiagCLISecret))
	f.getenv = func(k string) string {
		if k == "VECNET_CODEX_AUTO_DELIVERY" {
			return "0"
		}
		return baseEnv(k) // Map-only HOME/config/cache; no ambient environment.
	}
	wire := &http.Transport{} // No ambient proxy or destination dial.
	t.Cleanup(wire.CloseIdleConnections)
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(source.URL, "http://") {
			f.destinations.Add(1)
			return nil, errors.New("redirect fixture blocked a non-source destination")
		}
		resp, err := wire.RoundTrip(r)
		if err == nil && r.URL.Path == prefix+path && resp.StatusCode == status {
			resp.Body = &redirectDiagCLIBody{ReadCloser: resp.Body, closed: &f.closed}
		}
		return resp, err
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return f
}

// Observe the installed stdlib's ordering without requiring the hypothesis.
// This is one explicit reference request, separate from the product request.
func (f *redirectDiagCLIFixture) reference(t *testing.T) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.base+f.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+redirectDiagCLISecret)
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if !errors.As(err, &ue) || ue.URL != f.base+f.path {
			t.Fatalf("unexpected reference transport chain: %T", err)
		}
		t.Log("real net/http reference: malformed Location returned a transport error")
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("reference redirect status = %d, want 302", resp.StatusCode)
	}
	t.Log("real net/http reference: malformed Location remained a no-follow API response")
	return false
}

func (f *redirectDiagCLIFixture) check(t *testing.T, want, wantProbes int32) {
	t.Helper()
	if f.requests.Load() != want || f.probes.Load() != wantProbes || f.destinations.Load() != 0 {
		t.Errorf("source/identity/destination requests = %d/%d/%d, want %d/%d/0", f.requests.Load(), f.probes.Load(), f.destinations.Load(), want, wantProbes)
	}
	if f.closed.Load() < want {
		t.Error("real redirect response body was not closed")
	}
}

func TestRedirectResponseCLIMalformedDiagnostics(t *testing.T) { runRedirectDiagCLI(t, true) }
func TestRedirectResponseCLINoFollowControls(t *testing.T)     { runRedirectDiagCLI(t, false) }

func runRedirectDiagCLI(t *testing.T, malformed bool) {
	t.Helper()
	for _, tc := range redirectDiagCLICases {
		if tc.malformed != malformed {
			continue
		}
		for _, surface := range []string{"stderr", "prefs-json", "error-chain"} {
			t.Run(tc.name+"/"+surface, func(t *testing.T) {
				path, status := "/v1/peers", http.StatusTemporaryRedirect
				if malformed {
					status = http.StatusFound
				}
				if surface == "prefs-json" {
					path = "/v1/admin/prefs"
				}
				f := newRedirectDiagCLIFixture(t, tc.location, path, status)
				parseFailure, wantRequests := false, int32(1)
				if malformed {
					parseFailure, wantRequests = f.reference(t), 2
				}
				var visible string
				if surface == "error-chain" {
					err := newClient(f.base, redirectDiagCLISecret).do(context.Background(), http.MethodGet, path, nil, nil, nil)
					if err == nil {
						t.Fatal("redirect unexpectedly succeeded")
					}
					visible = err.Error()
					var ne *netError
					var he *hubError
					if parseFailure {
						var ue *url.Error
						if !errors.As(err, &ne) || errors.As(err, &he) || !errors.As(err, &ue) {
							t.Errorf("malformed Location lost network/URL error classification: %T", err)
						} else if ue.URL != f.base+path || ue.Err == nil || !errors.Is(err, ue.Err) {
							t.Error("checked-source URL or original transport cause was lost")
						}
					} else if !errors.As(err, &he) || errors.As(err, &ne) || he.Status != status || he.Message != http.StatusText(status) {
						t.Error("parseable blocked redirect changed its API status classification")
					}
				} else {
					args := []string{"--net", "p", "peers"}
					if surface == "prefs-json" {
						args = []string{"--net", "p", "--json", "prefs", "list"}
					}
					var out, errs bytes.Buffer
					code := RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, f.getenv)
					wantExit, wantCode := exitHub, fmt.Sprintf("http_%d", status)
					if parseFailure {
						wantExit, wantCode = exitNetwork, "network"
					}
					if code != wantExit {
						t.Errorf("redirect exit = %d, want %d", code, wantExit)
					}
					visible = out.String() + errs.String()
					if surface == "prefs-json" {
						var result struct {
							Error struct {
								Code    string `json:"code"`
								Message string `json:"message"`
							} `json:"error"`
						}
						if json.Unmarshal(out.Bytes(), &result) != nil || result.Error.Code != wantCode || result.Error.Message == "" || errs.Len() != 0 {
							t.Error("preferences lost its complete JSON error envelope/class")
						}
					} else if out.Len() != 0 || !strings.Contains(errs.String(), wantCode+":") {
						t.Error("CLI lost its stderr-only diagnostic/class")
					}
				}
				if tc.canary != "" && (strings.Contains(visible, tc.canary) || strings.Contains(visible, tc.location)) {
					t.Errorf("received Location bytes reached public diagnostics: %q", visible) // Expected privacy RED, unexecuted.
				}
				if parseFailure && !strings.Contains(visible, f.base+path) {
					t.Error("diagnostic dropped the original checked-source URL")
				}
				wantProbes := int32(1)
				if surface == "error-chain" {
					wantProbes = 0
				}
				f.check(t, wantRequests, wantProbes)
			})
		}
	}
}

// This exercises the existing reconnect loop/presentation with real Do errors
// and its clock/sleep seams, not public cmdWatch identity/follow/bootstrap.
func TestRedirectResponseCLIReconnectDiagnostics(t *testing.T) {
	for _, tc := range redirectDiagCLICases {
		t.Run(tc.name, func(t *testing.T) {
			status := http.StatusTemporaryRedirect
			if tc.malformed {
				status = http.StatusFound
			}
			f := newRedirectDiagCLIFixture(t, tc.location, "/v1/events", status)
			parseFailure, wantRequests := false, int32(2)
			if tc.malformed {
				parseFailure, wantRequests = f.reference(t), 3
			}
			var errs bytes.Buffer
			w := &watcher{
				a: &app{ctx: context.Background(), stdout: io.Discard, stderr: &errs, getenv: f.getenv},
				c: newClient(f.base, redirectDiagCLISecret), forMonitor: true,
				identityOff: true, replayed: true, initialized: true, seen: newDedupe(8),
			}
			now, sleeps := time.Unix(100, 0), 0
			w.now = func() time.Time { return now }
			w.sleep = func(context.Context, time.Duration) bool {
				sleeps++
				now = now.Add(61 * time.Second)
				return sleeps < 2
			}
			if err := w.run(); err != nil {
				t.Errorf("reconnect loop returned %T", err)
			}
			if tc.canary != "" && (strings.Contains(errs.String(), tc.canary) || strings.Contains(errs.String(), tc.location)) {
				t.Errorf("received Location reached reconnect reporting: %q", errs.String())
			}
			if !strings.Contains(errs.String(), "reconnecting") || (parseFailure && !strings.Contains(errs.String(), f.base+f.path)) {
				t.Error("reconnect reporting lost the outage or checked-source detail")
			}
			f.check(t, wantRequests, 0) // Two attempts; malformed cases add one reference.
		})
	}
}
