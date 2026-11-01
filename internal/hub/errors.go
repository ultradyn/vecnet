package hub

import (
	"errors"
	"fmt"
	"net/url"
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
// logs, deliveries.last_error and API details without the target URL:
// notification and push URLs often carry a credential in their path or
// query (PROTOCOL §12 Logging, §14 inv. 6). A *url.Error is reduced to its
// cause ("timeout", or "network error: <dial/TLS error>"); other errors,
// which never carry the URL, pass through.
func EgressErr(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timeout"
		}
		return "network error: " + ue.Err.Error()
	}
	return err.Error()
}
