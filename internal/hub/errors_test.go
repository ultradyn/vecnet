package hub_test

import (
	"context"
	"fmt"
	"io"
	"net"
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

// TestEgressErrDropsReceiverText: a receiver controls bytes the HTTP client
// quotes in its errors (a malformed Location, a malformed status line). It
// could reflect delivered message text or a credential there, so EgressErr
// reduces every transport failure to an allowlisted reason (PROTOCOL §12
// Logging, §14 inv. 6). The fixtures are offline loopback servers.
func TestEgressErrDropsReceiverText(t *testing.T) {
	const marker = "PRIVATEMESSAGEMARKER"
	const credential = "whsec_syntheticcredentialmarker"
	allowed := map[string]bool{
		"timeout": true, "canceled": true, "redirect refused": true, "destination refused": true,
		"scheme refused": true, "userinfo refused": true, "invalid url": true,
		"network error": true, "network error: protocol error": true, "network error: connection closed": true,
		"network error: connection refused": true, "network error: connection reset": true,
		"network error: dns lookup failed": true, "network error: tls failure": true,
	}
	for name, response := range map[string]string{
		"malformed location": "HTTP/1.1 302 Found\r\nLocation: /" + marker + "%zz-" + credential + "\r\nContent-Length: 0\r\n\r\n",
		"malformed response": marker + "-" + credential + "\r\n\r\n",
		"malformed status":   "HTTP/1.1 " + marker + " " + credential + "\r\n\r\n",
		"malformed header":   "HTTP/1.1 200 OK\r\n" + marker + " " + credential + "\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						defer conn.Close()
						conn.SetDeadline(time.Now().Add(5 * time.Second))
						buf := make([]byte, 4096)
						conn.Read(buf)
						io.WriteString(conn, response)
					}()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/hook", nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				t.Fatal("precondition: malformed response was accepted")
			}
			if !strings.Contains(err.Error(), marker) {
				t.Fatalf("precondition: raw error %q does not quote the receiver's bytes", err)
			}
			got := hub.EgressErr(fmt.Errorf("push: verify rte_x: %w", err))
			if strings.Contains(got, marker) || strings.Contains(got, credential) || strings.Contains(got, "whsec_") || !allowed[got] {
				t.Fatalf("EgressErr = %q, want an allowlisted reason without receiver text", got)
			}
		})
	}
}
