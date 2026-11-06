package opclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRequestAndUploadOmitHubURL(t *testing.T) {
	const (
		user  = "USERSECRET"
		path  = "PATHSECRET"
		query = "QUERYSECRET"
		host  = "hidden-host.example"
	)
	raw := "https://alice:" + user + "@" + host + "/" + path + "?token=" + query
	secrets := []string{"alice:" + user, user, path, query}
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, secret := range secrets {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("returned error contains %q: %s", secret, err.Error())
			}
		}
		if !strings.Contains(err.Error(), "https://"+host) {
			t.Fatalf("error %q dropped the host", err.Error())
		}
	}
	dial := func(r *http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connect %s", r.URL.String())}
	}
	tlsFail := func(r *http.Request) (*http.Response, error) {
		return nil, &tls.RecordHeaderError{Msg: "handshake " + r.URL.String()}
	}
	c := New(raw, "vn_operator_redact")
	c.hc.Transport = roundTripFunc(dial)
	c.uploadHC.Transport = c.hc.Transport
	_, err := c.Peers(context.Background())
	check(t, err)
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" {
		t.Fatalf("dial classification lost: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("url.Error lost: %v", err)
	}

	c.hc.Transport = roundTripFunc(tlsFail)
	c.uploadHC.Transport = c.hc.Transport
	_, err = c.Upload(context.Background(), strings.NewReader("hi"), 2, "text/plain", "hi.txt")
	check(t, err)
	var rec *tls.RecordHeaderError
	if !errors.As(err, &rec) {
		t.Fatalf("tls classification lost: %v", err)
	}

	broken := New(raw+"\n", "vn_operator_redact")
	_, err = broken.Peers(context.Background())
	check(t, err)
	_, err = broken.Upload(context.Background(), strings.NewReader("hi"), 2, "text/plain", "hi.txt")
	check(t, err)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
