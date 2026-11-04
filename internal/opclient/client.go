// Package opclient is the operator HTTP client. It depends only on the wire
// contract, and can be linked into the CLI without bringing in the hub.
package opclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Error retains the hub's structured error, including recovery details.
type Error struct {
	Status        int
	Code, Message string
	Details       map[string]any
}

func (e *Error) Error() string {
	return fmt.Sprintf("operator API %d %s: %s", e.Status, e.Code, e.Message)
}

// Client may be shared by callers. Its credential is sent only as a bearer.
type Client struct {
	base, secret string
	hc, uploadHC *http.Client
	// sleep is a clock seam for backoff tests; nil uses a context-aware timer.
	sleep func(context.Context, time.Duration) error
}

func New(base, secret string) *Client {
	// Do not redirect authenticated operator operations to another endpoint.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		base: strings.TrimRight(base, "/"), secret: secret,
		hc:       &http.Client{Timeout: 35 * time.Second, CheckRedirect: noRedirect},
		uploadHC: &http.Client{Timeout: 0, CheckRedirect: noRedirect},
	}
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost && (path == "/v1/messages" || path == "/v1/reactions") {
		req.Header.Set("X-Vecnet-Ingress", "operator_ui")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	return checkedResponse(resp)
}

// checkedResponse preserves the hub's structured errors for both JSON requests
// and raw uploads, and closes error responses before returning them.
func checkedResponse(resp *http.Response) (*http.Response, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var wire struct {
			Error struct {
				Code    string         `json:"code"`
				Message string         `json:"message"`
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		// Error pages are untrusted and may be HTML. Preserve the HTTP status even
		// when a proxy did not send the protocol's JSON envelope.
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire)
		e := &Error{Status: resp.StatusCode, Code: wire.Error.Code, Message: wire.Error.Message, Details: wire.Error.Details}
		if e.Message == "" {
			e.Message = http.StatusText(e.Status)
		}
		return nil, e
	}
	return resp, nil
}
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func queryPath(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
func pageQuery(limit int, key, value string) url.Values {
	q := url.Values{}
	if limit != 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if value != "" {
		q.Set(key, value)
	}
	return q
}
func threadPath(id string) string { return "/v1/admin/threads/" + url.PathEscape(id) }

func (c *Client) Inbox(ctx context.Context, q opapi.InboxQuery) (out opapi.Inbox, err error) {
	params := pageQuery(q.Limit, "before", q.Before)
	if q.UnreadOnly {
		params.Set("unread", "1")
	}
	err = c.do(ctx, "GET", queryPath("/v1/admin/inbox", params), nil, &out)
	return
}
func (c *Client) ReadThread(ctx context.Context, id string) (out opapi.ReadResult, err error) {
	err = c.do(ctx, "POST", threadPath(id)+"/read", nil, &out)
	return
}
func (c *Client) Threads(ctx context.Context, q opapi.ThreadQuery) (out opapi.ThreadPage, err error) {
	params := pageQuery(q.Limit, "cursor", q.Cursor)
	if q.Participant != "" {
		params.Set("participant", q.Participant)
	}
	if q.ExcludeParticipant != "" {
		params.Set("exclude_participant", q.ExcludeParticipant)
	}
	err = c.do(ctx, "GET", queryPath("/v1/admin/threads", params), nil, &out)
	return
}
func (c *Client) Thread(ctx context.Context, id string) (out opapi.ThreadDetail, err error) {
	err = c.do(ctx, "GET", threadPath(id), nil, &out)
	return
}
func (c *Client) History(ctx context.Context, id, after string, limit int) (out opapi.History, err error) {
	err = c.do(ctx, "GET", queryPath("/v1/threads/"+url.PathEscape(id)+"/messages", pageQuery(limit, "after", after)), nil, &out)
	return
}
func (c *Client) Matrix(ctx context.Context, id, after string, limit int) (out opapi.Matrix, err error) {
	err = c.do(ctx, "GET", queryPath(threadPath(id)+"/deliveries", pageQuery(limit, "after", after)), nil, &out)
	return
}
func (c *Client) Peers(ctx context.Context) ([]opapi.PeerRow, error) {
	var out struct {
		Peers []opapi.PeerRow `json:"peers"`
	}
	err := c.do(ctx, "GET", "/v1/admin/peers", nil, &out)
	return out.Peers, err
}
func (c *Client) LoopCuts(ctx context.Context, hours int) (out opapi.LoopCuts, err error) {
	q := url.Values{}
	if hours != 0 {
		q.Set("hours", strconv.Itoa(hours))
	}
	err = c.do(ctx, "GET", queryPath("/v1/admin/loop-cuts", q), nil, &out)
	return
}
func (c *Client) DeliveryHealth(ctx context.Context) (out opapi.DeliveryHealth, err error) {
	err = c.do(ctx, "GET", "/v1/admin/delivery-health", nil, &out)
	return
}
func (c *Client) Subscriptions(ctx context.Context) (out []opapi.Subscription, err error) {
	err = c.do(ctx, "GET", "/v1/admin/subscriptions", nil, &out)
	return
}
func (c *Client) ResumeSubscription(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/admin/subscriptions/"+url.PathEscape(id)+"/resume", nil, nil)
}
func (c *Client) Audit(ctx context.Context, limit int, before string) (out opapi.AuditPage, err error) {
	err = c.do(ctx, "GET", queryPath("/v1/admin/audit", pageQuery(limit, "before", before)), nil, &out)
	return
}
func (c *Client) Counters(ctx context.Context) (out map[string]int, err error) {
	err = c.do(ctx, "GET", "/v1/admin/counters", nil, &out)
	return
}
func (c *Client) RetryDeadLetter(ctx context.Context, dly string) error {
	return c.do(ctx, "POST", "/v1/admin/dead-letters/"+url.PathEscape(dly)+"/retry", nil, nil)
}
func (c *Client) DiscardDeadLetter(ctx context.Context, dly string) error {
	return c.do(ctx, "POST", "/v1/admin/dead-letters/"+url.PathEscape(dly)+"/discard", nil, nil)
}
func (c *Client) Send(ctx context.Context, r envelope.SendRequest) (out opapi.SendResult, err error) {
	err = c.do(ctx, "POST", "/v1/messages", r, &out)
	return
}

// React adds or removes the operator's reaction (PROTOCOL §17.2). A no-op is
// also a success, with Changed false.
func (c *Client) React(ctx context.Context, req envelope.ReactionRequest) (out opapi.ReactionResult, err error) {
	err = c.do(ctx, http.MethodPost, "/v1/reactions", req, &out)
	return
}
func (c *Client) Answer(ctx context.Context, a opapi.OpenApproval, option int) error {
	if option < 0 || option >= len(a.Options) {
		return &Error{Status: 422, Code: "invalid_option", Message: "option is outside the approval's options"}
	}
	_, err := c.Send(ctx, envelope.SendRequest{
		IdempotencyKey: fmt.Sprintf("tui-apr-%s-%d", a.ApprovalID, option), ThreadID: &a.ThreadID, InReplyTo: &a.MessageID,
		Recipients: []string{a.Requester}, Kind: "approval_response", Priority: "wake", Content: envelope.SendContent{Text: a.Options[option]},
		ApprovalResponse: &envelope.ApprovalResponse{ApprovalID: a.ApprovalID, Decision: a.Options[option]},
	})
	return err
}
func (c *Client) SetPresence(ctx context.Context, state string, at time.Time) error {
	return c.do(ctx, "POST", "/v1/presence", struct {
		State string `json:"state"`
		At    string `json:"at"`
	}{state, envelope.FormatTime(at)}, nil)
}
func (c *Client) Ack(ctx context.Context, cursor string) error {
	return c.do(ctx, "POST", "/v1/ack", struct {
		Cursor string `json:"cursor"`
	}{cursor}, nil)
}

// Upload streams bytes under the caller's context, without the short timeout
// used for JSON requests. Unreferenced uploads expire after one day.
func (c *Client) Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error) {
	if size < 0 {
		return envelope.Attachment{}, fmt.Errorf("upload size must be nonnegative")
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	if size == 0 {
		r = http.NoBody // An empty upload must also have an exact Content-Length.
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/v1/blobs?ttl=86400", r)
	if err != nil {
		return envelope.Attachment{}, err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mediaType)
	resp, err := c.uploadHC.Do(req)
	if err != nil {
		return envelope.Attachment{}, err
	}
	resp, err = checkedResponse(resp)
	if err != nil {
		return envelope.Attachment{}, err
	}
	defer resp.Body.Close()
	var uploaded struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		return envelope.Attachment{}, err
	}
	return envelope.Attachment{SHA256: uploaded.SHA256, Size: uploaded.Size, MediaType: mediaType, Name: name}, nil
}

// Blob enforces max even when the server sends a chunked response or lies
// about Content-Length. It never returns partial bytes as a successful blob.
func (c *Client) Blob(ctx context.Context, sha string, max int64) ([]byte, string, error) {
	if max < 0 {
		return nil, "", fmt.Errorf("blob limit must be nonnegative")
	}
	resp, err := c.request(ctx, "GET", "/v1/blobs/"+url.PathEscape(sha), nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	tooLarge := func() ([]byte, string, error) { return nil, "", fmt.Errorf("blob exceeds %d bytes", max) }
	if resp.ContentLength > max {
		return tooLarge()
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, "", err
	}
	var extra [1]byte
	n, err := io.ReadFull(resp.Body, extra[:])
	if n != 0 {
		return tooLarge()
	}
	if err != nil && err != io.EOF {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}
