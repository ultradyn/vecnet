package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReceivedRedirectDiagnostics(t *testing.T) {
	for _, location := range []string{"https://bad.example:T2_LOCATION_PORT/", "https://[T2_LOCATION_IPV6/", "https://bad.example/T2_LOCATION_ESCAPE%zz"} {
		t.Run(location, func(t *testing.T) {
			var source, destinations atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				source.Add(1)
				w.Header().Set("Location", location)
				w.WriteHeader(302)
			}))
			defer srv.Close()
			c := newClient(srv.URL, "")
			wire := &http.Transport{}
			defer wire.CloseIdleConnections()
			c.hc.Transport = receivedRoundTrip(func(r *http.Request) (*http.Response, error) {
				if strings.HasPrefix(r.URL.String(), srv.URL+"/") {
					return wire.RoundTrip(r)
				}
				destinations.Add(1)
				return nil, fmt.Errorf("unexpected destination")
			})
			err := c.do(context.Background(), "GET", "/v1/peers", nil, nil, nil)
			var ne *netError
			var ue *url.Error
			if !errors.As(err, &ne) || !errors.As(err, &ue) {
				t.Fatalf("lost typed transport cause: %T %v", err, err)
			}
			a, out, errs, _ := receivedApp(t, srv.URL, false)
			if code := a.exit(err); code != 3 {
				t.Errorf("exit=%d", code)
			}
			now := time.Unix(100, 0)
			s := reconnectStatus{out: errs, name: "watch", now: func() time.Time { return now }}
			s.failed(err.Error())
			now = now.Add(time.Minute)
			s.failed(err.Error())
			s.live()
			if strings.Contains(out.String()+errs.String(), "T2_LOCATION") || !strings.Contains(errs.String(), "reconnecting") || destinations.Load() != 0 || source.Load() != 1 {
				t.Errorf("unsafe/retry output: %s %s calls=%d/%d", out.String(), errs.String(), source.Load(), destinations.Load())
			}
			_, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\n", srv.URL))
			for _, args := range [][]string{{"peers"}, {"--json", "prefs", "list"}} {
				code, output, diagnostic := pinRun(env, args...)
				if code != 3 || strings.Contains(output+diagnostic, "T2_LOCATION") {
					t.Errorf("%v exit=%d %s %s", args, code, output, diagnostic)
				}
				if args[0] == "--json" && !json.Valid([]byte(output)) {
					t.Errorf("invalid prefs error JSON: %s", output)
				}
			}
		})
	}
}
func TestReceivedTransportClassification(t *testing.T) {
	cause := &net.DNSError{Err: "T2_NESTED_TRANSPORT", Name: "synthetic.example", IsTimeout: true, IsTemporary: true}
	original := &url.Error{Op: "Get", URL: "https://source.example/path?cursor=T2_CURSOR", Err: cause}
	err := &netError{err: original}
	var got *net.DNSError
	if !errors.Is(err, cause) || !errors.As(err, &got) || !got.Timeout() || !got.Temporary() {
		t.Fatal("lost network identity")
	}
	if strings.Contains(err.Error(), "T2_") {
		t.Error("transport diagnostic leaked nested/query value")
	}
	canceled := &netError{err: context.Canceled}
	if !errors.Is(canceled, context.Canceled) {
		t.Fatal("lost cancellation")
	}
}
func TestReceivedEnrolRelocation(t *testing.T) {
	for _, tc := range append(receivedURLCases("https://next.example/Vecnet/%2Fexact/"), receivedURLCase{"safe", "https://next.example/Vecnet/%2Fexact/", ""}) {
		t.Run(tc.name, func(t *testing.T) {
			var destinations atomic.Int32
			dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinations.Add(1) }))
			defer dst.Close()
			details := map[string]any{"new_url": tc.value, "unknown": "T2_UNKNOWN"}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(410)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": dst.URL + "?token=T2_MESSAGE_DIFFERENT", "details": details}})
			}))
			defer srv.Close()
			path, env := pinTestEnv(t, fmt.Sprintf("[profiles.p]\nhub=%q\nsecret=\"vn_operator_synthetic\"\n", srv.URL))
			before := pinRead(t, path)
			code, out, errs := pinRun(env, "enrol", "--hub", srv.URL, "--token", "synthetic-invite", "--profile", "new")
			if code != 2 || !strings.Contains(errs, "hub_moved") || strings.Contains(out+errs, "T2_") || destinations.Load() != 0 || !bytes.Equal(before, pinRead(t, path)) {
				t.Errorf("enrol exit=%d output=%s error=%s destination=%d", code, out, errs, destinations.Load())
			}
			if tc.name == "safe" && !strings.Contains(errs, tc.value.(string)) {
				t.Error("lost checked recovery hint")
			}
			err := newClient(srv.URL, "").do(context.Background(), "GET", "/v1/test", nil, nil, nil)
			var he *hubError
			if !errors.As(err, &he) || he.Status != 410 || he.Code != "hub_moved" {
				t.Fatal("lost relocation classification")
			}
			if tc.name != "safe" && he.Details != nil {
				t.Error("invalid recovery retained")
			}
			if tc.name == "safe" && (he.Message != "hub moved" || he.Details["new_url"] != tc.value || len(he.Details) != 1) {
				t.Errorf("unexpected safe projection: %+v", he)
			}
		})
	}
	// Unrelated rich errors retain their existing public contract.
	resp := &http.Response{StatusCode: 409, Body: http.NoBody}
	resp.Body = io.NopCloser(strings.NewReader(`{"error":{"code":"rich","message":"ordinary rich prose","details":{"keep":42}}}`))
	var he *hubError
	if !errors.As(readHubError(resp, false), &he) || he.Message != "ordinary rich prose" || he.Details["keep"] != float64(42) {
		t.Fatal("changed unrelated rich error")
	}
}
