package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Secrets planted in a hub URL must not come back in a validation error.
// Scheme and public-HTTP refusals may name the scheme and host only.
// A URL with userinfo is refused without naming any part of it.
func TestCheckHubURLOmitsSecrets(t *testing.T) {
	const (
		userSecret  = "pwUSERSECRET"
		pathSecret  = "PATHSECRET"
		querySecret = "QUERYSECRET"
		host        = "hidden-host.example"
	)
	secrets := []string{userSecret, pathSecret, querySecret, "alice", host}

	cases := []struct {
		name    string
		raw     string
		allow   bool
		want    string
		keep    []string
		hideAll bool
	}{
		{
			name: "malformed",
			raw:  "http://[::1" + userSecret,
			want: "the hub is not a URL like https://hub.example",
		},
		{
			name: "unsupported scheme",
			raw:  "ftp://" + host + "/" + pathSecret + "?token=" + querySecret,
			want: "ftp://" + host + ": the scheme must be https (or http on the LAN)",
			keep: []string{"ftp", host},
		},
		{
			name:    "unsupported scheme with userinfo",
			raw:     "ftp://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret,
			want:    "a hub URL must not include credentials",
			hideAll: true,
		},
		{
			name: "public http",
			raw:  "http://" + host + "/" + pathSecret + "?token=" + querySecret,
			want: "http://" + host + " is plain http on a public host",
			keep: []string{"http", host, "https://" + host},
		},
		{
			name:    "public http with userinfo",
			raw:     "http://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret,
			want:    "a hub URL must not include credentials",
			hideAll: true,
		},
		{
			name:    "https with userinfo",
			raw:     "https://alice:" + userSecret + "@" + host + "/" + pathSecret + "?token=" + querySecret,
			want:    "a hub URL must not include credentials",
			hideAll: true,
		},
		{
			name:    "https with userinfo even when insecure http is allowed",
			raw:     "https://alice:" + userSecret + "@" + host + "/" + pathSecret + "?access_token=" + querySecret,
			allow:   true,
			want:    "a hub URL must not include credentials",
			hideAll: true,
		},
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
			banned := secrets
			if !tc.hideAll {
				banned = []string{userSecret, pathSecret, querySecret, "alice"}
			}
			for _, secret := range banned {
				if strings.Contains(msg, secret) {
					t.Fatalf("error contains %q: %q", secret, msg)
				}
			}
			for _, keep := range tc.keep {
				if !strings.Contains(msg, keep) {
					t.Fatalf("error %q does not name %q", msg, keep)
				}
			}
			if strings.Contains(msg, "?") || strings.Contains(msg, pathSecret) {
				t.Fatalf("error includes a query or path: %q", msg)
			}
		})
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
