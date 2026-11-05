package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"

	"github.com/XertroV/vecnet/internal/egress"
)

// APIError is a failure the HTTP layer renders as the §2 error envelope:
// {"error":{"code":Code,"message":Message,"details":Details}} with Status.
// Code is one of the reason codes PROTOCOL.md names.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

// E returns an APIError with empty details.
func E(status int, code, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg, Details: map[string]any{}}
}

// EgressErr describes a failed outbound request (push, notify, verify) for
// logs, deliveries.last_error and API details. Transport failures reduce to
// an allowlisted reason ("timeout", "redirect refused", "network error:
// protocol error", ...): the target URL may carry a credential, and the
// HTTP client quotes receiver-controlled bytes (a malformed Location or
// status line) that could reflect message text or secrets (PROTOCOL §12
// Logging, §14 inv. 6). Errors without a transport cause are hub-authored
// (for example a refused challenge echo) and pass through, bounded.
func EgressErr(err error) string {
	if err == nil {
		return ""
	}
	if reason, ok := egressReason(err); ok {
		return reason
	}
	msg := err.Error()
	if len(msg) > maxEgressErr {
		msg = msg[:maxEgressErr]
	}
	return msg
}

// maxEgressErr bounds a passed-through hub-authored error.
const maxEgressErr = 256

// egressReason classifies a transport failure without reading its text.
func egressReason(err error) (string, bool) {
	for _, refused := range []struct {
		err    error
		reason string
	}{
		{egress.ErrRedirect, "redirect refused"},
		{egress.ErrPrivateAddress, "destination refused"},
		{egress.ErrScheme, "scheme refused"},
		{egress.ErrUserinfo, "userinfo refused"},
		{egress.ErrInvalidURL, "invalid url"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
	} {
		if errors.Is(err, refused.err) {
			return refused.reason, true
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout", true
	}
	var dnsErr *net.DNSError
	var recordErr tls.RecordHeaderError
	var alertErr tls.AlertError
	var verifyErr *tls.CertificateVerificationError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	var opErr *net.OpError
	var ue *url.Error
	switch {
	case errors.As(err, &dnsErr):
		return "network error: dns lookup failed", true
	case errors.Is(err, syscall.ECONNREFUSED):
		return "network error: connection refused", true
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "network error: connection reset", true
	case errors.As(err, &recordErr), errors.As(err, &alertErr), errors.As(err, &verifyErr),
		errors.As(err, &authorityErr), errors.As(err, &hostnameErr), errors.As(err, &invalidErr):
		return "network error: tls failure", true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "network error: connection closed", true
	case errors.As(err, &opErr):
		return "network error", true
	case errors.As(err, &ue):
		// Malformed responses and redirects, HTTP/2 errors: their text can
		// quote the receiver.
		return "network error: protocol error", true
	}
	return "", false
}
