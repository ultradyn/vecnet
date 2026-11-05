package hub_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/hub"
)

// TestEgressErrHidesURL: a transport error's text names the request URL,
// which may carry a credential; EgressErr drops it.
func TestEgressErrHidesURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:1/hooks/SECRETPATH?token=SECRETQUERY", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = http.DefaultClient.Do(req)
	if err == nil {
		t.Skip("something answered on 127.0.0.1:1")
	}
	if !strings.Contains(err.Error(), "SECRETQUERY") {
		t.Fatalf("precondition: raw error %q does not name the URL", err)
	}
	got := hub.EgressErr(err)
	if strings.Contains(got, "SECRET") || got == "" {
		t.Fatalf("EgressErr = %q, want the cause without the URL", got)
	}
	if hub.EgressErr(nil) != "" {
		t.Fatal("EgressErr(nil) != \"\"")
	}
}

// TestEgressErrIsAFixedReasonCode: a malformed redirect Location is quoted
// whole in net/http's error (before CheckRedirect runs), so the receiver
// controls that text; EgressErr reports only a fixed reason code.
func TestEgressErrIsAFixedReasonCode(t *testing.T) {
	const loc = "http://SECRETUSER:SECRETPW@bad host/SECRETBODY?token=SECRETTOKEN"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	_, err := http.Get(srv.URL + "/hook?token=SECRETQUERY")
	if err == nil || !strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("precondition: raw error %v does not quote the Location", err)
	}
	if got := hub.EgressErr(err); got != "request_failed" {
		t.Fatalf("EgressErr(malformed Location) = %q, want request_failed", got)
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrap SECRET: %w", egress.ErrRedirect), "redirect_refused"},
		{fmt.Errorf("wrap SECRET: %w", context.DeadlineExceeded), "timeout"},
		{hub.HTTPStatusError(503), "HTTP 503"},
		{&hub.OutboundError{Reason: "no_active_key", Err: errors.New("SECRET")}, "no_active_key"},
		{errors.New("SECRET body text"), "request_failed"},
	} {
		if got := hub.EgressErr(tc.err); got != tc.want {
			t.Errorf("EgressErr(%T) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
