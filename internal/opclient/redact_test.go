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

func TestRequestAndUploadKeepCheckedHubURL(t *testing.T) {
	const (
		user  = "USERSECRET"
		path  = "PATHSECRET"
		query = "QUERYSECRET"
		frag  = "FRAG"
	)
	ipv6 := "https://[2001:db8::7]/" + path
	secrets := []string{"alice:" + user, user, query, frag}
	var calls int
	dial := func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connect %s", r.URL.String())}
	}
	c := New(ipv6, "vn_operator_ok")
	c.hc.Transport = roundTripFunc(dial)
	c.uploadHC.Transport = c.hc.Transport
	_, err := c.Peers(context.Background())
	if err == nil || !strings.Contains(err.Error(), ipv6) {
		t.Fatalf("error %v, want the full hub URL", err)
	}
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" {
		t.Fatalf("dial classification lost: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) || !strings.Contains(ue.URL, "[2001:db8::7]") {
		t.Fatalf("url.Error lost or IPv6 mangled: %v", err)
	}

	c.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, &tls.RecordHeaderError{Msg: "handshake " + r.URL.String()}
	})
	c.uploadHC.Transport = c.hc.Transport
	_, err = c.Upload(context.Background(), strings.NewReader("hi"), 2, "text/plain", "hi.txt")
	if err == nil || !strings.Contains(err.Error(), ipv6) {
		t.Fatalf("upload error %v", err)
	}
	var rec *tls.RecordHeaderError
	if !errors.As(err, &rec) {
		t.Fatalf("tls classification lost: %v", err)
	}

	for _, raw := range []string{
		"https://alice:" + user + "@hidden-host.example/" + path + "?token=" + query + "#" + frag,
		"https://hidden-host.example/" + path + "?token=" + query,
		"https://hidden-host.example/" + path + "#" + frag,
		"https://hidden-host.example/" + path + "\n",
	} {
		before := calls
		refused := New(raw, "vn_operator_ok")
		refused.hc.Transport = roundTripFunc(dial)
		refused.uploadHC.Transport = refused.hc.Transport
		_, err = refused.Peers(context.Background())
		_, err2 := refused.Upload(context.Background(), strings.NewReader("hi"), 2, "text/plain", "hi.txt")
		if calls != before {
			t.Fatalf("%q made a request", raw)
		}
		for _, got := range []error{err, err2} {
			if got == nil {
				t.Fatalf("%q accepted", raw)
			}
			for _, secret := range secrets {
				if strings.Contains(got.Error(), secret) {
					t.Fatalf("error contains %q: %s", secret, got.Error())
				}
			}
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
