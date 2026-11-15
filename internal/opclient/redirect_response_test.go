package opclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/XertroV/vecnet/internal/opapi"
	"testing"
)

// Exercise real net/http Location parsing and both request and upload clients.
var redirectDiagOPCases = []struct {
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

type redirectDiagOPBody struct {
	io.ReadCloser
	closed *atomic.Int32
	once   atomic.Bool
}

func (b *redirectDiagOPBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		b.closed.Add(1)
	}
	return b.ReadCloser.Close()
}

type redirectDiagOPFixture struct {
	client     *Client
	base, path string

	requests, capabilities, destinations, closed atomic.Int32
}

func newRedirectDiagOPFixture(t *testing.T, location, surface string, status int) *redirectDiagOPFixture {
	t.Helper()
	const prefix, secret = "/I6_CHECKED_SOURCE/vecnet", "vn_operator_I6_SYNTHETIC_ONLY"
	f := &redirectDiagOPFixture{path: "/v1/admin/peers"}
	method := http.MethodGet
	if surface == "discovery" {
		f.path = "/v1/blob-capabilities"
	}
	if redirectDiagOPUpload(surface) {
		method, f.path = http.MethodPut, "/v1/blobs"
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("source request did not use its synthetic credential")
		}
		if r.Method == http.MethodGet && r.URL.Path == prefix+"/v1/blob-capabilities" && surface != "discovery" {
			f.capabilities.Add(1)
			http.NotFound(w, r) // Actual legacy-disk discovery, not a mocked result.
			return
		}
		if r.Method != method || r.URL.Path != prefix+f.path {
			f.destinations.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.requests.Add(1)
		if redirectDiagOPUpload(surface) {
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "abc" || r.URL.RawQuery != "ttl=86400" {
				t.Error("raw upload changed its exact body/query")
			}
		}
		w.Header().Set("Location", location)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "redirect fixture\n")
	}))
	t.Cleanup(source.Close)
	f.base = source.URL + prefix
	f.client = New(f.base, secret)
	wire := &http.Transport{} // Local fixture only; no ambient proxy.
	t.Cleanup(wire.CloseIdleConnections)
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(source.URL, "http://") {
			f.destinations.Add(1)
			return nil, errors.New("redirect fixture blocked a non-source destination")
		}
		resp, err := wire.RoundTrip(r)
		if err == nil && r.URL.Path == prefix+f.path && resp.StatusCode == status {
			resp.Body = &redirectDiagOPBody{ReadCloser: resp.Body, closed: &f.closed}
		}
		return resp, err
	})
	f.client.hc.Transport, f.client.uploadHC.Transport = rt, rt
	return f
}

// Actual stdlib reference, not a mocked Do/parser. Match the upload reader's
// replayability and method, and count this request separately from the API.
func (f *redirectDiagOPFixture) reference(t *testing.T, surface string) bool {
	t.Helper()
	method, target := http.MethodGet, f.base+f.path
	var body io.Reader
	if redirectDiagOPUpload(surface) {
		method, target, body = http.MethodPut, target+"?ttl=86400", strings.NewReader("abc")
		if surface == "upload-with-progress" {
			body = struct{ io.Reader }{body}
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.ContentLength = 3
	}
	req.Header.Set("Authorization", "Bearer "+f.client.secret)
	hc := &http.Client{Transport: f.client.hc.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if !errors.As(err, &ue) || ue.URL != target {
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

func TestRedirectResponseOperatorMalformedDiagnostics(t *testing.T) { runRedirectDiagOP(t, true) }
func TestRedirectResponseOperatorNoFollowControls(t *testing.T)     { runRedirectDiagOP(t, false) }

func runRedirectDiagOP(t *testing.T, malformed bool) {
	t.Helper()
	for _, tc := range redirectDiagOPCases {
		if tc.malformed != malformed {
			continue
		}
		for _, surface := range []string{"request", "discovery", "upload", "upload-with-progress"} {
			t.Run(tc.name+"/"+surface, func(t *testing.T) {
				status := http.StatusTemporaryRedirect
				if malformed {
					status = http.StatusFound
				}
				f := newRedirectDiagOPFixture(t, tc.location, surface, status)
				parseFailure, wantRequests := false, int32(1)
				if malformed {
					parseFailure, wantRequests = f.reference(t, surface), 2
				}
				var err error
				var progress atomic.Int64
				switch surface {
				case "request":
					_, err = f.client.Peers(context.Background())
				case "discovery", "upload":
					_, err = f.client.Upload(context.Background(), strings.NewReader("abc"), 3, "text/plain", "fixture.txt")
				case "upload-with-progress":
					_, err = f.client.UploadWithProgress(context.Background(), strings.NewReader("abc"), 3, "text/plain", "fixture.txt", func(n int64) { progress.Store(n) })
				}
				if err == nil {
					t.Fatal("redirect unexpectedly succeeded")
				}
				var api *Error
				if parseFailure {
					var ue *url.Error
					if errors.As(err, &api) || !errors.As(err, &ue) {
						t.Errorf("malformed Location lost transport/URL classification: %T", err)
					} else {
						wantURL := f.base + f.path
						if redirectDiagOPUpload(surface) {
							wantURL += "?ttl=86400"
						}
						if ue.URL != wantURL || ue.Err == nil || !errors.Is(err, ue.Err) {
							t.Error("checked-source URL or original transport cause was lost")
						}
					}
					if !redirectDiagOPUpload(surface) && !strings.Contains(err.Error(), f.base+f.path) {
						t.Error("public error dropped the original checked-source URL")
					}
					if redirectDiagOPUpload(surface) && err.Error() != "transport failure" {
						t.Error("query-bearing upload URL must use fixed transport prose")
					}
				} else {
					wantMessage := http.StatusText(status)
					if surface == "discovery" {
						wantMessage = "http_307"
					}
					if !errors.As(err, &api) || api.Status != status || api.Message != wantMessage {
						t.Error("parseable blocked redirect changed its typed API status/fallback message")
					}
				}
				if tc.canary != "" && (strings.Contains(err.Error(), tc.canary) || strings.Contains(err.Error(), tc.location)) {
					t.Errorf("received Location bytes reached the public error: %q", err.Error())
				}
				wantCaps := int32(0)
				if redirectDiagOPUpload(surface) {
					wantCaps = 1
				}
				if f.requests.Load() != wantRequests || f.capabilities.Load() != wantCaps || f.destinations.Load() != 0 {
					t.Errorf("source/capability/destination requests = %d/%d/%d, want %d/%d/0", f.requests.Load(), f.capabilities.Load(), f.destinations.Load(), wantRequests, wantCaps)
				}
				if f.closed.Load() < wantRequests {
					t.Error("real redirect response body was not closed")
				}
				if surface == "upload-with-progress" && progress.Load() != 3 {
					t.Error("redirect changed the completed raw-upload progress measurement")
				}
			})
		}
	}
}

func redirectDiagOPUpload(surface string) bool {
	return surface == "upload" || surface == "upload-with-progress"
}

// Query values come from received cursors and other API inputs. Checking the
// configured base does not admit the entire outer request URL for display.
func TestRedirectDiagnosticQueryURL(t *testing.T) {
	f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "NR_QUERY_CURSOR" {
			t.Error("request cursor changed")
		}
		w.Header().Set("Location", "https://[NR_LOCATION")
		w.WriteHeader(http.StatusFound)
	})
	_, err := f.c.Threads(context.Background(), opapi.ThreadQuery{Cursor: "NR_QUERY_CURSOR"})
	var ue *url.Error
	if !errors.As(err, &ue) || !strings.Contains(ue.URL, "NR_QUERY_CURSOR") || !errors.Is(err, ue.Err) {
		t.Fatalf("original query-bearing URL chain changed: %v", err)
	}
	if err.Error() != "transport failure" {
		t.Errorf("unchecked request query or Location displayed: %s", err)
	}
	f.check(t, 1)
}

func TestRedirectTailRetriesWithoutEffects(t *testing.T) {
	f := newNoticeResponseFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/I47/v1/events" {
			t.Error("tail performed an effect")
		}
		w.Header().Set("Location", "https://[NR_LOCATION")
		w.WriteHeader(http.StatusFound)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var states []string
	pauses := 0
	f.c.sleep = func(ctx context.Context, d time.Duration) error {
		pauses++
		if d < time.Second*4/5 || d > time.Second*6/5 {
			t.Error("retry classification changed")
		}
		cancel()
		return ctx.Err()
	}
	err := f.c.TailWithAck(ctx, func(Batch) error { t.Error("redirect reached handler"); return nil }, func(s string) {
		states = append(states, s)
	}, func(string) { t.Error("redirect acknowledged") })
	if !errors.Is(err, context.Canceled) || pauses != 1 || strings.Join(states, ",") != "connecting,offline" {
		t.Errorf("tail retry/cancel changed: err=%v pauses=%d states=%v", err, pauses, states)
	}
	f.check(t, 1)
}
