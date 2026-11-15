// Package opclient is the operator HTTP client. It depends only on the wire
// contract, and can be linked into the CLI without bringing in the hub.
package opclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/blobclient"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpdiag"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/hubnotice"
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

// gate refuses a hub base that carries userinfo, a query or a fragment, or
// that does not parse, before any dial. Plain http on a public host is left
// to the caller that opted in. The error names the rule and no part of the URL.
func (c *Client) gate() error {
	if c == nil {
		return hubid.ErrHubForm
	}
	err := hubid.CheckHubURL(c.base)
	if err == nil || errors.Is(err, hubid.ErrPlainHTTP) {
		return nil
	}
	return err
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
	return c.requestMatch(ctx, method, path, body, "")
}

// requestMatch is request with one strong If-Match. An empty tag sends no
// precondition header. Callers pass the ETag from GET unchanged, quotes included.
func (c *Client) requestMatch(ctx context.Context, method, path string, body any, ifMatch string) (*http.Response, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
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
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	if method == http.MethodPost && (path == "/v1/messages" || path == "/v1/reactions") {
		req.Header.Set("X-Vecnet-Ingress", "operator_ui")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, httpdiag.TransportError(err)
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
		if e.Status == http.StatusGone && e.Code == "hub_moved" {
			e.Message, e.Details = hubnotice.ProjectRelocation(e.Details, true)
		}
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

// BlobStatus refreshes reference metadata without minting a transfer URL or
// consuming the download grant budget.
func (c *Client) BlobStatus(ctx context.Context, sha, id string) (out blobwire.State, err error) {
	err = c.do(ctx, "GET", queryPath("/v1/blobs/"+url.PathEscape(sha)+"/status", url.Values{"blob_id": {id}}), nil, &out)
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

// PeerLifecycle applies an operator lifecycle action to a peer.
func (c *Client) PeerLifecycle(ctx context.Context, id, action string) error {
	return c.do(ctx, "POST", "/v1/admin/peers/"+url.PathEscape(id)+"/"+action, nil, nil)
}

// Notices lists the hub's active notices (GET /v1/hub/notices), oldest first.
// Their messages are operator text, untrusted on every surface.
// Preferences is GET /v1/admin/prefs. ETag is the raw header value, including quotes.
func (c *Client) Preferences(ctx context.Context) (opapi.Preferences, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/admin/prefs", nil)
	if err != nil {
		return opapi.Preferences{}, err
	}
	var out opapi.Preferences
	etag, err := decodeJSON(resp, &out)
	if err != nil {
		return opapi.Preferences{}, err
	}
	out.ETag = etag
	return out, nil
}

func preconditionRequired() error {
	return &Error{Status: http.StatusPreconditionRequired, Code: "prefs_precondition_required", Message: "If-Match is required; read preferences first."}
}

// PreviewPreferences is the read-only POST /v1/admin/prefs/preview. It does not
// follow a stale tag with another request.
func (c *Client) PreviewPreferences(ctx context.Context, etag string, patch opapi.PreferencesPatch) (opapi.PreferencesPreview, error) {
	if strings.TrimSpace(etag) == "" {
		return opapi.PreferencesPreview{}, preconditionRequired()
	}
	resp, err := c.requestMatch(ctx, http.MethodPost, "/v1/admin/prefs/preview", patch, etag)
	if err != nil {
		return opapi.PreferencesPreview{}, err
	}
	var out opapi.PreferencesPreview
	if _, err := decodeJSON(resp, &out); err != nil {
		return opapi.PreferencesPreview{}, err
	}
	return out, nil
}

// PatchPreferences is one conditional PATCH /v1/admin/prefs. A 412 is returned
// as a structured error and is not retried.
func (c *Client) PatchPreferences(ctx context.Context, etag string, patch opapi.PreferencesPatch) (opapi.Preferences, error) {
	if strings.TrimSpace(etag) == "" {
		return opapi.Preferences{}, preconditionRequired()
	}
	resp, err := c.requestMatch(ctx, http.MethodPatch, "/v1/admin/prefs", patch, etag)
	if err != nil {
		return opapi.Preferences{}, err
	}
	var out opapi.Preferences
	next, err := decodeJSON(resp, &out)
	if err != nil {
		return opapi.Preferences{}, err
	}
	out.ETag = next
	return out, nil
}

func decodeJSON(resp *http.Response, out any) (string, error) {
	defer resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", err
	}
	return etag, nil
}

// Notices validates the complete received list before exposing any entries.
// Public HTTP permission is owned by the operator client's caller, as in gate.
func (c *Client) Notices(ctx context.Context) ([]envelope.HubNotice, error) {
	var out struct {
		Notices []json.RawMessage `json:"notices"`
	}
	if err := c.do(ctx, "GET", "/v1/hub/notices", nil, &out); err != nil {
		return nil, err
	}
	var notices []envelope.HubNotice
	if out.Notices != nil {
		notices = make([]envelope.HubNotice, 0, len(out.Notices))
	}
	for _, raw := range out.Notices {
		n, err := hubnotice.DecodeNotice(raw, true)
		if err != nil {
			return nil, err
		}
		notices = append(notices, n)
	}
	return notices, nil
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
	// Send accepts only caller-owned reference metadata; upload proof remains
	// available to the UI on the original attachment.
	if r.Attachments != nil {
		refs := make([]envelope.Attachment, len(r.Attachments))
		for i, att := range r.Attachments {
			refs[i] = envelope.Attachment{BlobID: att.BlobID, SHA256: att.SHA256, Size: att.Size, MediaType: att.MediaType, Name: att.Name, OriginURL: att.OriginURL, ExpiresAt: att.ExpiresAt, RetentionClass: att.RetentionClass}
		}
		r.Attachments = refs
	}
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

// Upload transfers under the caller's context. Disk uploads retain their
// one-day unreferenced TTL; R2 grants record their own retention class.
func (c *Client) Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error) {
	return c.UploadWithProgress(ctx, r, size, mediaType, name, nil)
}

// UploadWithProgress reports PUT bytes only, excluding hashing and spooling.
func (c *Client) UploadWithProgress(ctx context.Context, r io.Reader, size int64, mediaType, name string, progress func(int64)) (envelope.Attachment, error) {
	if err := c.gate(); err != nil {
		return envelope.Attachment{}, err
	}
	bc, transport := c.blobs()
	bc.Progress = progress
	caps, err := bc.Discover(ctx)
	if err != nil {
		return envelope.Attachment{}, transport.outcome(err)
	}
	if caps.Backend == "r2" {
		if size <= 0 || size > blobclient.MaxSize || size > caps.MaxSize {
			return envelope.Attachment{}, &Error{Status: 413, Code: "blob_too_large", Message: "blob exceeds upload limit"}
		}
		seeker, ok := r.(io.ReadSeeker)
		if !ok {
			f, err := os.CreateTemp("", "vecnet-upload-*")
			if err != nil {
				return envelope.Attachment{}, fmt.Errorf("cannot spool upload")
			}
			defer os.Remove(f.Name())
			defer f.Close()
			n, err := io.Copy(f, io.LimitReader(&contextReader{ctx: ctx, r: r}, size+1))
			if err != nil {
				if ctx.Err() != nil {
					return envelope.Attachment{}, ctx.Err()
				}
				return envelope.Attachment{}, transferError(&blobclient.Error{Code: "blob_read_failed"})
			}
			if n != size {
				return envelope.Attachment{}, fmt.Errorf("upload size changed")
			}
			seeker = f
		}
		// Keep this snapshot. Upload would discover again, and a 404 there is
		// legacy disk, which would PUT the file through the hub.
		ref, err := bc.UploadUsing(ctx, caps, seeker, size, mediaType, "")
		if err != nil {
			return envelope.Attachment{}, transport.outcome(err)
		}
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		return envelope.Attachment{BlobID: ref.BlobID, SHA256: ref.SHA256, Size: ref.Size, MediaType: mediaType, Name: name, ExpiresAt: ref.ExpiresAt, RetentionClass: ref.RetentionClass, SHA256Verified: ref.SHA256Verified, Integrity: ref.Integrity}, nil
	}
	if progress != nil {
		r = &uploadProgressReader{r: r, report: progress}
	}

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
		return envelope.Attachment{}, httpdiag.TransportError(err)
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

// BlobOptions adds generation identity without changing legacy disk callers.
type BlobOptions struct{ BlobID string }

func (c *Client) BlobWithOptions(ctx context.Context, sha string, max int64, opts BlobOptions) ([]byte, string, error) {
	if err := c.gate(); err != nil {
		return nil, "", err
	}
	if opts.BlobID == "" {
		return c.Blob(ctx, sha, max)
	}
	bc, transport := c.blobs()
	b, g, err := bc.Download(ctx, sha, opts.BlobID, max)
	return b, g.MediaType, transport.outcome(err)
}

// A transport belongs to one transfer operation. It retains only checked hub
// transport errors so callers keep errors.As classification from the operator
// client. Direct-provider errors never pass through this transport.
type blobTransport struct {
	c          *Client
	failure    error
	relocation *relocationBody
}

func (t *blobTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.c.secret)
	// Run Client.Do here so malformed Location parsing is captured before the
	// blob client replaces hub transport causes with its fixed reason code.
	// uploadHC refuses redirects and has no transfer deadline of its own.
	resp, err := t.c.uploadHC.Do(r)
	t.failure = httpdiag.TransportError(err)
	t.relocation = nil
	if err == nil && resp.StatusCode == http.StatusGone {
		// Retain the bounded raw response as the blob client consumes it. Its
		// typed moving notice erases null/type admission and other follow fields.
		t.relocation = &relocationBody{ReadCloser: resp.Body}
		resp.Body = t.relocation
	}
	return resp, err
}
func (t *blobTransport) outcome(err error) error {
	var e *blobclient.Error
	// A truncated or canceled body remains a transport/context failure even
	// if its partial bytes happen to contain a complete relocation JSON value.
	if errors.As(err, &e) && !e.HubTransport && t.relocation != nil && !t.relocation.overflow {
		_, projected := checkedResponse(&http.Response{
			StatusCode: http.StatusGone,
			Body:       io.NopCloser(bytes.NewReader(t.relocation.raw)),
		})
		var moved *Error
		if errors.As(projected, &moved) && moved.Code == "hub_moved" {
			return moved
		}
	}
	if errors.As(err, &e) && e.HubTransport && t.failure != nil {
		return t.failure
	}
	return transferError(err)
}

// relocationBody observes exactly the bytes consumed by blobclient, without
// changing read/close behavior or keeping more than its 64 KiB response limit.
type relocationBody struct {
	io.ReadCloser
	raw      []byte
	overflow bool
}

func (b *relocationBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	remaining := (64 << 10) - len(b.raw)
	if n > remaining {
		b.overflow = true
	}
	b.raw = append(b.raw, p[:min(n, remaining)]...)
	return n, err
}

func (c *Client) blobs() (*blobclient.Client, *blobTransport) {
	transport := &blobTransport{c: c}
	return blobclient.New(c.base, transport, nil, nil), transport
}
func transferError(err error) error {
	var e *blobclient.Error
	if errors.As(err, &e) {
		return &Error{Status: e.Status, Code: e.Code, Message: e.Error(), Details: map[string]any{"retry_after_seconds": float64(e.RetryAfterSeconds)}}
	}
	return err
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

type uploadProgressReader struct {
	r      io.Reader
	n      int64
	report func(int64)
}

func (r *uploadProgressReader) Read(b []byte) (int, error) {
	n, e := r.r.Read(b)
	r.n += int64(n)
	r.report(r.n)
	return n, e
}
