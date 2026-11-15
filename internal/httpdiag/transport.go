// Package httpdiag provides safe presentation of HTTP transport failures.
package httpdiag

import (
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/XertroV/vecnet/internal/hubid"
)

// TransportError hides nested response/transport prose while retaining its
// original cause. It displays an outer request URL only if CheckHubURL accepts
// it (public HTTP policy remains the caller's). Generated queries, including
// received cursors, are not checked hub URLs. A nil error stays nil.
func TransportError(err error) error {
	if err == nil {
		return nil
	}
	return &transportError{err: err}
}

type transportError struct{ err error }

func (e *transportError) Error() string {
	if ue, ok := e.err.(*url.Error); ok && ue != nil {
		err := hubid.CheckHubURL(ue.URL)
		if err == nil || errors.Is(err, hubid.ErrPlainHTTP) {
			return fmt.Sprintf("request to %q: transport failure", ue.URL)
		}
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
