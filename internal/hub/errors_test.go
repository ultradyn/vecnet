package hub_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

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
