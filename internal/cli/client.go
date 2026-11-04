package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// requestTimeout bounds one ordinary request; uploads and streams set their
// own deadlines through the context.
const requestTimeout = 2 * time.Minute

// usageError is a bad invocation: exit 1.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErr(msg string) error { return &usageError{msg: msg} }

// refusedError is a refusal the CLI makes itself, before contacting the
// hub: exit 2 like a hub refusal, printed as "vecnet: msg".
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }

// hubError is a non-2xx answer from the hub: exit 2, printed as
// "code: message", then Hint on its own line when set.
type hubError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Hint    string
}

func (e *hubError) Error() string { return e.Code + ": " + e.Message }

// netError is a transport failure talking to the hub: exit 3.
type netError struct{ err error }

func (e *netError) Error() string { return "network: " + e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

// client talks HTTP to one hub with one bearer.
type client struct {
	base   string // hub origin without a trailing slash
	secret string // "" sends no Authorization header
	hc     *http.Client
	// clientHeader is the X-Vecnet-Client value of a bare human run
	// (operator.go), sent on every request; the hub reads it on sends.
	clientHeader string
}

func newClient(base, secret string) *client {
	return &client{base: strings.TrimRight(base, "/"), secret: secret, hc: &http.Client{}}
}

// rawBody is a request body sent as-is with its content type.
type rawBody struct {
	r           io.Reader
	contentType string
	length      int64 // -1 when unknown
}

// newRequest builds a request for path (with query q). body is nil, a
// rawBody, or a value sent as JSON.
func (c *client) newRequest(ctx context.Context, method, path string, q url.Values, body any) (*http.Request, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var r io.Reader
	ct := ""
	length := int64(-1)
	switch b := body.(type) {
	case nil:
	case rawBody:
		r, ct, length = b.r, b.contentType, b.length
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		r, ct, length = bytes.NewReader(j), "application/json", int64(len(j))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, usageErr("bad hub URL " + c.base + ": " + err.Error())
	}
	if length >= 0 {
		req.ContentLength = length
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if c.clientHeader != "" {
		req.Header.Set(envelope.ClientHeader, c.clientHeader)
	}
	return req, nil
}

// send performs req: a transport failure is a netError, a non-2xx status a
// hubError (with the body already read and closed).
func (c *client) send(req *http.Request) (*http.Response, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return nil, ctxErr
		}
		return nil, &netError{err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, readHubError(resp)
	}
	return resp, nil
}

// readHubError decodes the PROTOCOL §2 error envelope; a body that is not
// one becomes http_<status>.
func readHubError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var env struct {
		Error *struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Code != "" {
		return &hubError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message, Details: env.Error.Details}
	}
	return &hubError{Status: resp.StatusCode, Code: fmt.Sprintf("http_%d", resp.StatusCode), Message: http.StatusText(resp.StatusCode)}
}

// do sends one request and decodes a JSON response into out (nil discards
// it; a *json.RawMessage keeps the bytes).
func (c *client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return c.doNoTimeout(ctx, method, path, q, body, out)
}

func (c *client) doNoTimeout(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	req, err := c.newRequest(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &netError{err: err}
	}
	if out == nil {
		return nil
	}
	if rm, ok := out.(*json.RawMessage); ok {
		*rm = append((*rm)[:0], raw...)
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &hubError{Status: resp.StatusCode, Code: "bad_response", Message: "the hub answered with unexpected JSON: " + err.Error()}
	}
	return nil
}

// pathEscape escapes one path segment (an id from the command line).
func pathEscape(s string) string { return url.PathEscape(s) }
