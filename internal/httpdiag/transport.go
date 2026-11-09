// Package httpdiag provides safe presentation of HTTP transport failures.
package httpdiag

import (
	"errors"
	"fmt"
	"net"
	"net/url"
)

// TransportError wraps a failed http.Client.Do after its request URL was
// checked by the caller. Only the outer request URL and operation are shown;
// nested errors can contain untrusted response data. A nil error stays nil.
func TransportError(err error) error {
	if err == nil {
		return nil
	}
	return &transportError{err: err}
}

type transportError struct{ err error }

func (e *transportError) Error() string {
	if ue, ok := e.err.(*url.Error); ok && ue != nil {
		return fmt.Sprintf("%s %q: transport failure", ue.Op, ue.URL)
	}
	return "transport failure"
}

func (e *transportError) Unwrap() error { return e.err }

func (e *transportError) Timeout() bool {
	var ne net.Error
	return errors.As(e.err, &ne) && ne.Timeout()
}

func (e *transportError) Temporary() bool {
	var ne net.Error
	return errors.As(e.err, &ne) && ne.Temporary()
}
