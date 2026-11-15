package httpdiag

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestTransportErrorCheckedOuterURL(t *testing.T) {
	for _, raw := range []string{"https://alice:CANARY@hub.example", "https://hub.example?CANARY", "https://hub.example#CANARY", "https://[CANARY", "/CANARY", ""} {
		t.Run(raw, func(t *testing.T) {
			cause := &url.Error{Op: "CANARY_OP", URL: raw, Err: errors.New("CANARY_CAUSE")}
			got := TransportError(cause)
			var ue *url.Error
			if got.Error() != "transport failure" || !errors.Is(got, cause) || !errors.As(got, &ue) || ue != cause {
				t.Fatalf("unsafe display or lost chain: %v", got)
			}
		})
	}
	for _, raw := range []string{"https://Safe.Example:8443/Prefix/%2Fexact/", "https://[2001:db8::7]:8443/Prefix/", "http://public.example/Prefix/"} {
		t.Run(raw, func(t *testing.T) {
			got := TransportError(&url.Error{Op: "CANARY_OP", URL: raw, Err: errors.New("CANARY_CAUSE")})
			if !strings.Contains(got.Error(), raw) || strings.Contains(got.Error(), "CANARY") {
				t.Fatal("safe URL changed or nested prose displayed")
			}
		})
	}
}

func TestTransportErrorClassification(t *testing.T) {
	if TransportError(nil) != nil {
		t.Fatal("nil changed")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		got := TransportError(cause)
		if got.Error() != "transport failure" || !errors.Is(got, cause) {
			t.Fatal("context cause changed")
		}
	}
	for _, timeout := range []bool{false, true} {
		for _, temporary := range []bool{false, true} {
			cause := &classifiedError{timeout, temporary}
			got := TransportError(&url.Error{Op: "Get", URL: "https://safe.example", Err: cause})
			var ne net.Error
			var original *classifiedError
			if !errors.As(got, &ne) || ne.Timeout() != timeout || ne.Temporary() != temporary || !errors.As(got, &original) || original != cause {
				t.Fatal("network classification changed")
			}
		}
	}
}

type classifiedError struct{ timeout, temporary bool }

func (e *classifiedError) Error() string   { return "CANARY" }
func (e *classifiedError) Timeout() bool   { return e.timeout }
func (e *classifiedError) Temporary() bool { return e.temporary }
