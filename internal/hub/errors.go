package hub

import "fmt"

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
