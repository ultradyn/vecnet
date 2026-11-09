package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

func receivedProjectionCase(t *testing.T, details map[string]any, message string, want any) {
	t.Helper()
	f := newReceivedFixture(t, "https://source.example.invalid")
	f.reply(410, "hub_moved", "hostile "+receivedCanary, details)
	before := f.snapshot()
	receivedError(t, f.constructor("connect", false, nil), 410, "hub_moved", message, want)
	f.unchanged(before)
	source, destination, closed, read := f.counts()
	if source != 1 || destination != 0 || closed != 1 || read != len(f.body) {
		t.Errorf("source/destination/closed/read = %d/%d/%d/%d", source, destination, closed, read)
	}
}

func TestHubMovedDiagnosticProjection(t *testing.T) {
	t.Run("fixed-message-and-closed-recovery", func(t *testing.T) {
		f := newReceivedFixture(t, "https://source.example.invalid")
		n := f.notice()
		n.Hub, n.NewURL = "https://Source.EXAMPLE.invalid/API/%2F", "https://[2001:db8::1]:8443/Next/%2F"
		if err := f.key.SignNotice(&n); err != nil {
			t.Fatal(err)
		}
		original := receivedJSON(t, n)
		want := map[string]any{"new_url": receivedDestination, "notice": n}
		f.reply(410, "hub_moved", "unsafe "+receivedCanary, map[string]any{"new_url": receivedDestination, "notice": n, "hint": receivedCanary, "extra": map[string]any{"secret": receivedCanary}})
		before := f.snapshot()
		he := receivedError(t, f.constructor("connect", false, nil), 410, "hub_moved", "hub moved", want)
		var admitted envelope.HubNotice
		if err := json.Unmarshal(receivedJSON(t, he.Details["notice"]), &admitted); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, receivedJSON(t, admitted)) || !bytes.Equal(original, receivedJSON(t, n)) {
			t.Error("admitted signed fields or arbitrary message prose changed")
		}
		f.unchanged(before)
	})
	t.Run("absent-recovery", func(t *testing.T) {
		receivedProjectionCase(t, nil, "hub moved", nil)
	})
	t.Run("unknown-recovery-only", func(t *testing.T) {
		receivedProjectionCase(t, map[string]any{"unexpected": receivedCanary}, "hub moved", nil)
	})
	t.Run("top-target-shapes", func(t *testing.T) {
		for name, value := range map[string]any{"null": nil, "number": 7, "bool": true, "object": map[string]any{}, "array": []string{}, "empty": "", "public-http-default": "http://public.example.invalid"} {
			t.Run(name, func(t *testing.T) {
				receivedProjectionCase(t, map[string]any{"new_url": value}, "invalid hub relocation", nil)
			})
		}
	})
	badURLs := []struct{ name, raw string }{
		{"password", "https://user:" + receivedCanary + "@example.invalid"},
		{"username", "https://" + receivedCanary + "@example.invalid"},
		{"query", "https://example.invalid/?token=" + receivedCanary},
		{"bare-query", "https://example.invalid/?"},
		{"fragment", "https://example.invalid/#" + receivedCanary},
		{"bare-fragment", "https://example.invalid/#"},
		{"authority", "https://"}, {"ipv6", "https://[::1"},
		{"escape", "https://example.invalid/%zz"}, {"scheme", "file:///tmp/not-a-hub"},
		{"public-http-default", "http://public.example.invalid"},
	}
	for _, bad := range badURLs {
		t.Run("top-url/"+bad.name, func(t *testing.T) {
			receivedProjectionCase(t, map[string]any{"new_url": bad.raw}, "invalid hub relocation", nil)
		})
	}
	for _, field := range []string{"hub", "new_url"} {
		for _, kind := range []string{"moving", "maintenance", "cleared"} {
			for _, bad := range badURLs {
				t.Run("nested/"+field+"/"+kind+"/"+bad.name, func(t *testing.T) {
					n := map[string]any{"hub": "https://source.example.invalid", "new_url": receivedDestination, "kind": kind, "message": "arbitrary prose"}
					if kind == "cleared" {
						n["kind"], n["cleared_at"] = "restart", "2026-11-01T00:00:00.000Z"
					}
					n[field] = bad.raw
					receivedProjectionCase(t, map[string]any{"new_url": receivedDestination, "notice": n}, "invalid hub relocation", nil)
				})
			}
		}
	}
	t.Run("nested-structural", func(t *testing.T) {
		for name, value := range map[string]any{
			"null": nil, "array": []any{}, "string": receivedCanary, "number": 1,
			"missing-hub": map[string]any{"new_url": receivedDestination},
			"empty-hub":   map[string]any{"hub": ""}, "null-hub": map[string]any{"hub": nil},
			"typed-hub":     map[string]any{"hub": true},
			"null-new-url":  map[string]any{"hub": "https://source.example.invalid", "new_url": nil},
			"typed-new-url": map[string]any{"hub": "https://source.example.invalid", "new_url": 1},
		} {
			t.Run(name, func(t *testing.T) {
				receivedProjectionCase(t, map[string]any{"new_url": receivedDestination, "notice": value}, "invalid hub relocation", nil)
			})
		}
	})
	t.Run("valid-optional-and-nonmoving", func(t *testing.T) {
		for _, kind := range []string{"moving", "restart", "maintenance"} {
			for _, optional := range []string{"absent", "empty", "present"} {
				t.Run(kind+"/"+optional, func(t *testing.T) {
					n := map[string]any{"hub": "https://Source.EXAMPLE.invalid/API/%2F", "kind": kind, "message": "https://user:pass@example.invalid/?prose#prose", "sig": "unchanged-signature"}
					if optional != "absent" {
						n["new_url"] = ""
					}
					if optional == "present" {
						n["new_url"] = "https://[2001:db8::1]:443/Prefix/%2F"
					}
					var want envelope.HubNotice
					if err := json.Unmarshal(receivedJSON(t, n), &want); err != nil {
						t.Fatal(err)
					}
					receivedProjectionCase(t, map[string]any{"notice": n}, "hub moved", map[string]any{"notice": want})
				})
			}
		}
	})
	t.Run("unrelated-status-or-code", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			code   string
		}{{400, "hub_moved"}, {503, "hub_moved"}, {410, "unrelated"}} {
			t.Run(tc.code+http.StatusText(tc.status), func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				details := map[string]any{"new_url": "https://user:pass@example.invalid/?q", "field": "value", "extra": receivedCanary}
				message := "unrelated prose https://user:" + receivedCanary + "@example.invalid/?text#more"
				f.reply(tc.status, tc.code, message, details)
				before := f.snapshot()
				receivedError(t, f.constructor("connect", false, nil), tc.status, tc.code, message, details)
				f.unchanged(before)
			})
		}
	})
	t.Run("consumer-presentation", receivedPresentation)
	t.Run("signed-follow-and-refusal", receivedFollow)
	t.Run("body-close-limit-and-no-redirect", func(t *testing.T) {
		for _, large := range []bool{false, true} {
			t.Run(map[bool]string{false: "redirect", true: "bounded-body"}[large], func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				status, code, message := 302, "other", "preserved prose"
				f.reply(status, code, message, nil)
				wantRead := len(f.body)
				if large {
					f.status, f.body = 410, []byte(strings.Repeat("x", 70<<10))
					status, code, message, wantRead = 410, "http_410", "Gone", 64<<10
				}
				receivedError(t, f.constructor("connect", false, nil), status, code, message, nil)
				source, destination, closed, read := f.counts()
				if source != 1 || destination != 0 || closed != 1 || read != wantRead {
					t.Fatalf("source/destination/closed/read = %d/%d/%d/%d", source, destination, closed, read)
				}
			})
		}
	})
	t.Run("ordinary-malformed-error-envelope", func(t *testing.T) {
		for _, raw := range []string{`{"error":null}`, `not JSON`, `{"error":{"code":"hub_moved","details":[]}}`} {
			t.Run(raw, func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				f.body = []byte(raw)
				before := f.snapshot()
				receivedError(t, f.constructor("connect", false, nil), 410, "http_410", "Gone", nil)
				f.unchanged(before)
			})
		}
	})
	t.Run("transport-causes", receivedTransportCauses)
}

func receivedPresentation(t *testing.T) {
	for _, prefs := range []bool{false, true} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "normal", true: "prefs"}[prefs], map[bool]string{false: "human", true: "json"}[jsonMode]}, "/"), func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				f.reply(410, "hub_moved", receivedCanary, map[string]any{"new_url": "https://user:" + receivedCanary + "@example.invalid", "extra": receivedCanary})
				before := f.snapshot()
				a := f.app()
				a.json = jsonMode
				var err error
				if prefs {
					err = a.cmdPrefs([]string{"list"})
				} else {
					err = a.cmdPeers(nil)
				}
				if code := a.exit(err); code != 2 {
					t.Errorf("consumer exit=%d, error=%v", code, err)
				}
				out, errs := a.stdout.(*bytes.Buffer).String(), a.stderr.(*bytes.Buffer).String()
				if prefs && jsonMode {
					var value map[string]any
					if err := json.Unmarshal([]byte(out), &value); err != nil {
						t.Fatal(err)
					}
					want := map[string]any{"error": map[string]any{"code": "hub_moved", "message": "invalid hub relocation", "details": map[string]any{}}}
					if !bytes.Equal(receivedJSON(t, value), receivedJSON(t, want)) || errs != "" {
						t.Errorf("prefs JSON=%s stderr=%s", out, errs)
					}
				} else if out != "" || errs != "hub_moved: invalid hub relocation\n" {
					t.Errorf("consumer stdout=%q stderr=%q", out, errs)
				}
				f.unchanged(before)
				source, _, closed, _ := f.counts()
				if source != 1 || closed != 1 {
					t.Errorf("consumer source/closed=%d/%d", source, closed)
				}
			})
		}
	}
}

func receivedFollow(t *testing.T) {
	for _, mode := range []string{"valid", "bad-signature", "no-follow", "bad-top-with-signed-notice"} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/human", true: "/json"}[jsonMode], func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				n := f.notice()
				if mode == "bad-signature" {
					n.Message += "tampered"
				}
				details := map[string]any{"notice": n, "new_url": "https://different.example.invalid", "extra": receivedCanary}
				if mode == "bad-top-with-signed-notice" {
					details["new_url"] = "https://user:" + receivedCanary + "@example.invalid"
				}
				if mode == "no-follow" {
					f.env["VECNET_NO_FOLLOW"] = "1"
				}
				f.reply(410, "hub_moved", receivedCanary, details)
				before := f.snapshot()
				configBefore := pinRead(t, f.env["VECNET_CONFIG"])
				original := receivedJSON(t, n)
				args := []string{"--net", "p", "peers"}
				if jsonMode {
					args = append([]string{"--json"}, args...)
				}
				code, out, errs := f.run(args...)
				source, destination, closed, _ := f.counts()
				if source != 1 || closed != 1 || strings.Contains(out+errs, receivedCanary) || !bytes.Equal(original, receivedJSON(t, n)) {
					t.Errorf("source/closed=%d/%d stdout=%q stderr=%q", source, closed, out, errs)
				}
				if mode != "valid" {
					if code != 1 || destination != 0 || !strings.Contains(errs, "relocation_unverified") {
						t.Errorf("refused follow: code=%d destination=%d stderr=%s", code, destination, errs)
					}
					if mode == "bad-signature" && !strings.Contains(errs, "signature") || mode == "no-follow" && !strings.Contains(errs, "profile follow") {
						t.Errorf("wrong refusal: %s", errs)
					}
					f.unchanged(before)
					return
				}
				if code != 0 || destination != 2 || !strings.Contains(out, "peer_fixture") || !strings.Contains(errs, "verified") {
					t.Fatalf("valid follow: %d destination=%d stdout=%s stderr=%s", code, destination, out, errs)
				}
				f.mu.Lock()
				ordered := len(f.requests) == 3 && f.requests[1].path == "/v1/hub/identity" && f.requests[1].auth == "" && f.requests[2].path == "/v1/peers" && f.requests[2].auth == "Bearer vn_operator_fixture"
				f.mu.Unlock()
				if !ordered {
					t.Error("follow did not verify identity before the authenticated retry")
				}
				cfg, err := loadConfig(f.env["VECNET_CONFIG"])
				if err != nil || cfg.Profiles["p"].Hub != receivedDestination || cfg.Profiles["p"].HubPubkey != f.key.Pubkey() {
					t.Fatalf("followed config=%+v err=%v", cfg, err)
				}
				if !bytes.Equal(configBefore, pinRead(t, f.env["VECNET_CONFIG"]+".bak")) {
					t.Error("follow backup changed original config")
				}
				pp, err := pinsPath(func(k string) string { return f.env[k] })
				if err != nil {
					t.Fatal(err)
				}
				records, err := readPins(pp)
				if err != nil || records["p"].LastMove == nil || records["p"].LastMove.NoticeID != n.NoticeID || records["p"].LastMove.To != n.NewURL {
					t.Fatalf("move record=%+v err=%v", records, err)
				}
			})
		}
	}
}

type receivedNetworkCause struct{}

func (*receivedNetworkCause) Error() string   { return receivedCanary }
func (*receivedNetworkCause) Timeout() bool   { return true }
func (*receivedNetworkCause) Temporary() bool { return true }

type receivedFailBody struct {
	closed bool
	cause  error
}

func (b *receivedFailBody) Read([]byte) (int, error) { return 0, b.cause }
func (b *receivedFailBody) Close() error             { b.closed = true; return nil }

func receivedTransportCauses(t *testing.T) {
	cause := &receivedNetworkCause{}
	inner := &url.Error{Op: "read", URL: "https://user:" + receivedCanary + "@bad.invalid/?q", Err: cause}
	c := newClient("https://source.example.invalid", "fixture")
	c.hc.Transport = receivedRoundTrip(func(*http.Request) (*http.Response, error) { return nil, inner })
	err := c.do(context.Background(), "GET", "/probe", nil, nil, nil)
	var ne *netError
	var ue *url.Error
	var typed *receivedNetworkCause
	var capability interface {
		Timeout() bool
		Temporary() bool
	}
	if !errors.As(err, &ne) || !errors.As(err, &ue) || !errors.As(err, &typed) || typed != cause || !errors.Is(err, inner) || !errors.Is(err, cause) {
		t.Fatalf("transport cause identity lost: %T %v", err, err)
	}
	if ue.URL != "https://source.example.invalid/probe" || !errors.As(ne.Unwrap(), &capability) || !capability.Timeout() || !capability.Temporary() || strings.Contains(err.Error(), receivedCanary) {
		t.Errorf("transport diagnostics/capabilities changed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.hc.Transport = receivedRoundTrip(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	err = c.do(ctx, "GET", "/probe", nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation cause lost: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := c.do(ctx, "GET", "/probe", nil, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline cause lost: %v", err)
	}
	bodyCause := errors.New("body read cause")
	body := &receivedFailBody{cause: bodyCause}
	c.hc.Transport = receivedRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	})
	err = c.do(context.Background(), "GET", "/probe", nil, nil, new(json.RawMessage))
	if !errors.As(err, &ne) || !errors.Is(err, bodyCause) || !body.closed {
		t.Errorf("body read error/close changed: %v closed=%v", err, body.closed)
	}
}
