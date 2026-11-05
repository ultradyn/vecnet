package push_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// badLocation is a redirect target net/http cannot parse (a space in the
// host). The client reports the parse failure with the raw header quoted,
// before the egress policy's CheckRedirect ever runs, so a receiver can
// reflect a credential-bearing URL or the pushed Message text into the
// transport error.
const badLocation = "http://SECRETUSER:SECRETPW@bad host/SECRETBODY?token=SECRETTOKEN"

// lockedLog collects slog output for the duration of a test.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureSlog(t *testing.T) *lockedLog {
	t.Helper()
	l := &lockedLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

func assertNoReflection(t *testing.T, where, got string) {
	t.Helper()
	for _, leak := range []string{"SECRET", "bad host", "http://", "token=", "@"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%s carries %q from the malformed Location: %q", where, leak, got)
		}
	}
}

// TestMalformedRedirectLocationNeverReachesDiagnostics: a push receiver that
// answers with an unparseable Location must not get the header text (URL,
// credentials, reflected Message body) into deliveries.last_error, the
// verification API error, or the log.
func TestMalformedRedirectLocationNeverReachesDiagnostics(t *testing.T) {
	logs := captureSlog(t)
	f := setup(t, func(int, hit) reply {
		return reply{Status: http.StatusFound, Header: map[string]string{"Location": badLocation}}
	})
	msg := f.msg("wake")
	msg["content"] = map[string]any{"text": "SECRETBODY"}
	sr := f.send(f.aSec, msg)
	if f.step() != 1 {
		t.Fatal("the delivery was not pushed")
	}
	dly := sr.Deliveries[0].DeliveryID
	var lastErr *string
	if err := f.env.Hub.DB.QueryRow(`SELECT last_error FROM deliveries WHERE dly_id = ?`, dly).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if lastErr == nil || *lastErr != "request_failed" {
		t.Fatalf("last_error = %v, want the fixed reason request_failed", lastErr)
	}
	resp, raw := f.env.Do(f.env.OperatorSecret, "GET", "/v1/admin/delivery-health", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"request_failed"`) {
		t.Fatalf("delivery-health = %d %s, want the blocked delivery with its fixed reason", resp.StatusCode, raw)
	}
	assertNoReflection(t, "delivery-health", string(raw))

	// Verification of a route whose receiver answers the same way.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", badLocation)
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	route, _, err := f.env.Hub.AddRoute(f.ctx, "peer", f.a, srv.URL+"/hook")
	if err != nil {
		t.Fatal(err)
	}
	resp, raw = f.env.Do(f.env.OperatorSecret, "POST", "/v1/admin/routes/"+route+"/verify", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), `"reason":"request_failed"`) {
		t.Fatalf("verify = %d %s, want 422 with reason request_failed", resp.StatusCode, raw)
	}
	assertNoReflection(t, "verify API error", string(raw))
	assertNoReflection(t, "log", logs.String())
}
