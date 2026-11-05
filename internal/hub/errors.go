package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
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

// OutboundError is an outbound failure that names its own fixed reason
// code. Reason must be a constant code or a status line ("HTTP 503"): it is
// what EgressErr reports, while Err (which may name the URL) is only for
// errors.Is/As.
type OutboundError struct {
	Reason string
	Err    error
}

func (e *OutboundError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Reason
}

func (e *OutboundError) Unwrap() error { return e.Err }

// HTTPStatusError is the OutboundError for a receiver that answered status.
func HTTPStatusError(status int) error {
	return &OutboundError{Reason: "HTTP " + strconv.Itoa(status)}
}

// EgressErr describes a failed outbound request (push, notify, verify,
// avatar fetch) for logs, deliveries.last_error and API details as a fixed
// reason code. It never renders the error's text: notification and push
// URLs often carry a credential in their path or query, and a transport
// error can quote the URL or a receiver-chosen header such as a malformed
// redirect Location (PROTOCOL §12 Logging, §14 inv. 6). Errors it cannot
// classify are "request_failed".
func EgressErr(err error) string {
	if err == nil {
		return ""
	}
	var oe *OutboundError
	if errors.As(err, &oe) && oe.Reason != "" {
		return oe.Reason
	}
	for _, c := range []struct {
		target error
		code   string
	}{
		{egress.ErrRedirect, "redirect_refused"},
		{egress.ErrPrivateAddress, "private_address"},
		{egress.ErrScheme, "scheme_refused"},
		{egress.ErrUserinfo, "userinfo_refused"},
		{egress.ErrInvalidURL, "invalid_url"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
	} {
		if errors.Is(err, c.target) {
			return c.code
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	var alertErr tls.AlertError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &recordErr) || errors.As(err, &alertErr) ||
		errors.As(err, &unknownAuthority) || errors.As(err, &hostnameErr) || errors.As(err, &invalidCert) {
		return "tls_error"
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return "connection_closed"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "network_error"
	}
	return "request_failed"
}
