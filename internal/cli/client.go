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

	"github.com/XertroV/vecnet/internal/blobclient"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpdiag"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/hubnotice"
)

// requestTimeout bounds one ordinary request; uploads and streams set their
// own deadlines through the context.
const requestTimeout = 2 * time.Minute

// usageError is a bad invocation: exit 1.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErr(msg string) error { return &usageError{msg: msg} }

// refusedError is a refusal the CLI makes itself, before contacting the
// hub. The default mapping is exit 2, printed as "vecnet: msg". Preferences
// commands classify it as a confirmation refusal and may set code and details.
type refusedError struct {
	msg     string
	code    string
	details map[string]any
}

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

func (e *hubError) Error() string { return e.format("") }

// format projects only fixed public schema prose and recognized field paths.
// Validator messages, rejected values and arbitrary Details properties may
// carry credentials, URLs or terminal controls and are never presentation data.
func (e *hubError) format(command string) string {
	if e.Code != "invalid_body" {
		return e.Code + ": " + e.Message
	}
	field, expected := invalidBodyField(e.Details)
	if expected == "" {
		help := "vecnet --help"
		if _, known := idCommandFlags[command]; known {
			help = "vecnet " + command + " --help"
		}
		expected = "request must match this command's schema; check " + help
	} else {
		expected = "expected " + expected
	}
	return "invalid_body: value does not satisfy the schema or hub constraint; " + field + "; " + expected
}

// invalidBodyField is a deliberately closed subset of the published schema
// ($defs sendRequest, reactionRequest, attachmentReference and ID/cursor defs).
// Validate the entire pointer before displaying it; never truncate a received
// path to a safe-looking prefix. No allowed property contains RFC6901 escapes,
// so escaped tokens cannot name one of these fields.
func invalidBodyField(details map[string]any) (field, expected string) {
	path, ok := details["path"].(string)
	if !ok || len(path) > 256 || strings.Count(path, "/") > 16 {
		return "field unavailable", ""
	}
	if path == "" {
		return "field /", ""
	}
	exact := map[string]string{
		"/recipients":   "array of 1 to 32 peer_, team_ or grp_ IDs",
		"/thread_id":    "thr_ ID",
		"/in_reply_to":  "msg_ ID",
		"/as":           "peer_ ID",
		"/message_id":   "msg_ ID",
		"/delivery_id":  "dly_ ID",
		"/cursor":       "eN.N cursor",
		"/priority":     "wake, wake_idle or hold",
		"/content":      "object containing text",
		"/content/text": "text (at most 65536 characters)",
		"/attachments":  "array of at most 16 attachment references",
	}
	if shape, known := exact[path]; known {
		return "field " + path, shape
	}
	switch path {
	case "/<unknown>", "/content/<unknown>", "/metadata/<unknown>":
		return "field " + path, ""
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "" || !diagnosticArrayIndex(parts[2]) {
		return "field unavailable", ""
	}
	if parts[1] == "recipients" && len(parts) == 3 {
		return "field " + path, "peer_, team_ or grp_ ID"
	}
	if parts[1] == "attachments" {
		if len(parts) == 3 {
			return "field " + path, "attachment reference"
		}
		if len(parts) == 4 {
			if parts[3] == "<unknown>" {
				return "field " + path, ""
			}
			shape, known := map[string]string{
				"sha256":          "64 lowercase hexadecimal characters",
				"size":            "integer byte count from 0 to 26214400 (R2 requires at least 1)",
				"media_type":      "media type string (at most 128 characters)",
				"name":            "name string (at most 255 characters)",
				"origin_url":      "URI (at most 2048 characters)",
				"blob_url":        "URI (at most 2048 characters)",
				"blob_id":         "blo_ ID",
				"retention_class": "1h, 6h, 12h, 1d, 7d, 30d or keep",
				"expires_at":      "UTC timestamp, or null for keep",
			}[parts[3]]
			if known {
				return "field " + path, shape
			}
		}
	}
	return "field unavailable", ""
}

func diagnosticArrayIndex(s string) bool {
	if len(s) == 0 || len(s) > 10 || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := range s {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// netError is a transport failure talking to the hub: exit 3.
type netError struct{ err error }

func (e *netError) Error() string {
	if e == nil || e.err == nil {
		return "network: unreachable"
	}
	// Blob transfers already classify this failure with a fixed public code.
	if b, ok := e.err.(*blobclient.Error); ok && b.HubTransport && b.Code == "blob_unavailable" {
		return "network: blob_unavailable"
	}
	// Keep typed causes for classification, but never print their received prose.
	return "network: " + httpdiag.TransportError(e.err).Error()
}
func (e *netError) Unwrap() error { return e.err }

// client talks HTTP to one hub with one bearer.
type client struct {
	base      string // hub origin without a trailing slash
	secret    string // "" sends no Authorization header
	allowHTTP bool   // explicit selected-profile/environment policy for received URLs
	hc        *http.Client
	// clientHeader is the X-Vecnet-Client value of a bare human run
	// (operator.go), sent on every request; the hub reads it on sends.
	clientHeader string
}

func newClient(base, secret string) *client {
	return &client{base: strings.TrimRight(base, "/"), secret: secret, hc: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
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
	if err := hubid.CheckHubURL(c.base); err != nil && !errors.Is(err, hubid.ErrPlainHTTP) {
		return nil, usageErr(err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		// A URL that failed to build is not printed.
		return nil, usageErr(hubid.ErrHubForm.Error())
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
		return nil, readHubError(resp, c.allowHTTP)
	}
	return resp, nil
}

// readHubError decodes the PROTOCOL §2 error envelope; a body that is not
// one becomes http_<status>.
func readHubError(resp *http.Response, allowHTTP bool) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var env struct {
		Error *struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Code != "" {
		if resp.StatusCode == http.StatusGone && env.Error.Code == "hub_moved" {
			env.Error.Message, env.Error.Details = hubnotice.ProjectRelocation(env.Error.Details, allowHTTP)
		}
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
		return &netError{err: &url.Error{Op: "read", URL: req.URL.String(), Err: err}}
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
