package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A refusal names the rule and no part of the input.
func TestCheckHubURLOmitsSecrets(t *testing.T) {
	const (
		userSecret  = "pwUSERSECRET"
		pathSecret  = "PATHSECRET"
		querySecret = "QUERYSECRET"
		host        = "hidden-host.example"
	)
	secrets := []string{userSecret, pathSecret, querySecret, "alice", host, "FRAG"}

	cases := []struct {
		name  string
		raw   string
		allow bool
		want  string
	}{
		{name: "malformed", raw: "http://[::1" + userSecret, want: "the hub is not a URL like https://hub.example"},
		{name: "unsupported scheme", raw: "ftp://" + host + "/" + pathSecret, want: "scheme must be https"},
		{name: "unsupported scheme with userinfo", raw: "ftp://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret, want: "must not include credentials"},
		{name: "query", raw: "https://" + host + "/" + pathSecret + "?token=" + querySecret, want: "contains a query"},
		{name: "fragment", raw: "https://" + host + "/" + pathSecret + "#FRAG", want: "contains a fragment"},
		{name: "public http", raw: "http://" + host + "/" + pathSecret, want: "plain http on a public host"},
		{name: "public http with userinfo", raw: "http://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret, want: "must not include credentials"},
		{name: "https with userinfo", raw: "https://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret, want: "must not include credentials"},
		{name: "https with userinfo even when insecure http is allowed", raw: "https://alice:" + userSecret + "@" + host + "/" + pathSecret + "?access_token=" + querySecret, allow: true, want: "must not include credentials"},
		{name: "query even when insecure http is allowed", raw: "https://" + host + "?token=" + querySecret, allow: true, want: "contains a query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkHubURL(tc.raw, tc.allow)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error %q, want it to contain %q", msg, tc.want)
			}
			for _, secret := range secrets {
				if strings.Contains(msg, secret) {
					t.Fatalf("error contains %q: %q", secret, msg)
				}
			}
		})
	}
	if err := checkHubURL("https://"+host+"/"+pathSecret, false); err != nil {
		t.Fatalf("path prefix refused: %v", err)
	}
	const ipv6 = "https://[2001:db8::7]/vecnet"
	if err := checkHubURL(ipv6, false); err != nil {
		t.Fatalf("ipv6 refused: %v", err)
	}
	if got, ok := printableHub(ipv6, false); !ok || got != ipv6 {
		t.Fatalf("printable ipv6 = %q %v", got, ok)
	}
}

func TestNewRequestDoesNotEchoHub(t *testing.T) {
	const (
		userSecret  = "pwUSERSECRET"
		pathSecret  = "PATHSECRET"
		querySecret = "QUERYSECRET"
	)
	c := newClient("https://alice:"+userSecret+"@hidden-host.example/"+pathSecret+"?token="+querySecret, "")
	c.base += "\n"
	_, err := c.newRequest(context.Background(), http.MethodGet, "/v1/peers", nil, nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	for _, secret := range []string{userSecret, pathSecret, querySecret, "alice", "hidden-host.example"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("request error contains %q: %q", secret, msg)
		}
	}
}
