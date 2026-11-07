// Package blobclient transfers attachments using authenticated hub metadata and
// a separate, credential-free HTTP transport. It never retains a transfer URL.
package blobclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
)

const MaxSize int64 = 25 << 20

// capabilitySkew is how far a presign deadline may sit past the nominal TTL
// on this clock. Honest hubs issue exactly a 5-minute PUT or a 10-minute GET,
// then truncate the wire time to milliseconds or whole seconds. A client one
// second behind otherwise treats that legal deadline as over-long and abandons
// a grant the hub has already reserved. Two seconds covers that truncation
// plus ordinary clock lag, and still rejects a capability that outlives the
// cap by more than the budget.
const capabilitySkew = 2 * time.Second

// MovingNotice carries only the signed relocation contract from a hub error.
// Callers must verify it against their pinned key before following NewURL.
// Provider errors never populate this field.
type MovingNotice struct {
	NoticeID   string `json:"notice_id"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	Hub        string `json:"hub"`
	IssuedAt   string `json:"issued_at"`
	At         string `json:"at,omitempty"`
	ExpiresAt  string `json:"expires_at"`
	NewURL     string `json:"new_url,omitempty"`
	Sig        string `json:"sig"`
	ClearedAt  string `json:"cleared_at,omitempty"`
	ClearedHow string `json:"cleared_how,omitempty"`
}

// Error renders only fixed reason codes and retry metadata, never provider text.
type Error struct {
	Status            int
	Code              string
	RetryAfterSeconds int
	MovingNotice      *MovingNotice
	// HubTransport distinguishes a failed hub request from HTTP responses,
	// local validation and direct-provider failures. The cause is discarded
	// because transport errors can contain credentials and capability URLs.
	HubTransport bool
	// refresh is set only when this capability's own deadline has already
	// passed. One download may mint again for that case. A hostile origin,
	// an over-long TTL, and a capability past the object deadline leave it
	// unset so the caller fails without a second mint.
	refresh bool
}

func (e *Error) Error() string {
	if e.RetryAfterSeconds > 0 {
		return fmt.Sprintf("%s (retry after %ds)", e.Code, e.RetryAfterSeconds)
	}
	return e.Code
}
func failure(code string) error { return &Error{Code: code} }

// newTransferRequest builds the credential-free transfer request. Tests replace
// it to prove a construction failure is not ignored.
var newTransferRequest = http.NewRequestWithContext

// Read the bounded body before parsing: a decoder can both hide wire errors
// behind JSON errors and accept a complete JSON value before a disconnect.
func hubJSON(r io.Reader, out any) error {
	const limit = 64 << 10
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return &Error{Code: "blob_unavailable", HubTransport: true}
	}
	if len(b) > limit || json.Unmarshal(b, out) != nil {
		return failure("bad_response")
	}
	return nil
}

// Client's transports must be independent: hub may add authentication; transfer
// must not. New disables redirects and supplies no cookie jar to either client.
// Configure Now before using the client; nil means the system clock.
type Client struct {
	base          string
	hub, transfer *http.Client
	origins       []string
	Progress      func(int64) // cumulative PUT bytes; never digest/spool reads
	Now           func() time.Time
}

func New(base string, hub, transfer http.RoundTripper, origins []string) *Client {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(base, "/"), hub: &http.Client{Transport: hub, CheckRedirect: noRedirect}, transfer: &http.Client{Transport: transfer, CheckRedirect: noRedirect}, origins: append([]string(nil), origins...)}
}
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Capabilities is discovered anew per operation, including after a hub changes
// backend or preferences. Only an old hub's 404 enables legacy disk behavior.
type Capabilities struct {
	Backend         string   `json:"backend"`
	MaxSize         int64    `json:"max_size"`
	ChecksumProfile string   `json:"checksum_profile"`
	DownloadOrigins []string `json:"download_origins"`
}

func (c *Client) Discover(ctx context.Context) (Capabilities, error) {
	var caps Capabilities
	err := c.json(ctx, "GET", "/v1/blob-capabilities", "", nil, &caps)
	var e *Error
	if errors.As(err, &e) && e.Status == 404 {
		return Capabilities{Backend: "disk", MaxSize: MaxSize}, nil
	}
	if err != nil {
		return caps, err
	}
	if caps.Backend != "disk" && caps.Backend != "r2" {
		return caps, failure("bad_response")
	}
	if caps.Backend == "r2" && caps.ChecksumProfile != "sha256" {
		return caps, failure("bad_response")
	}
	if caps.MaxSize <= 0 || caps.MaxSize > MaxSize {
		caps.MaxSize = MaxSize
	}
	return caps, nil
}

var reasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func responseError(resp *http.Response) error {
	defer resp.Body.Close()
	e := &Error{Status: resp.StatusCode, Code: fmt.Sprintf("http_%d", resp.StatusCode)}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Retry  int           `json:"retry_after_seconds"`
				Notice *MovingNotice `json:"notice"`
			} `json:"details"`
		} `json:"error"`
	}
	err := hubJSON(resp.Body, &env)
	var bodyErr *Error
	if errors.As(err, &bodyErr) && bodyErr.HubTransport {
		return err
	}
	if err == nil {
		if reasonCode.MatchString(env.Error.Code) {
			e.Code = env.Error.Code
		}
		if e.Status == http.StatusGone && e.Code == "hub_moved" {
			e.MovingNotice = env.Error.Details.Notice
		}
		if env.Error.Details.Retry > 0 {
			e.RetryAfterSeconds = env.Error.Details.Retry
		}
	}
	if e.RetryAfterSeconds == 0 {
		n, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if n > 0 {
			e.RetryAfterSeconds = n
		}
	}
	return e
}
func (c *Client) request(ctx context.Context, method, path string, r io.Reader) (*http.Response, error) {
	// Only locally constructed root-relative paths can receive hub credentials.
	if !strings.HasPrefix(path, "/v1/") || strings.ContainsAny(path, "\r\n") {
		return nil, failure("bad_response")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, failure("bad_response")
	}
	req.Header.Set("Accept", "application/json")
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hub.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Code: "blob_unavailable", HubTransport: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp)
	}
	return resp, nil
}
func (c *Client) json(ctx context.Context, method, path, idem string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return failure("bad_response")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return failure("bad_response")
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := c.hub.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Code: "blob_unavailable", HubTransport: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	defer resp.Body.Close()
	return hubJSON(resp.Body, out)
}
func validSHA(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}
func verified(sha string, b *bool, i *blobwire.Integrity) bool {
	return b != nil && *b && i != nil && i.Verified && i.Algorithm == "sha256" && i.Value == sha
}
func (c *Client) directURL(raw, deadline string, ttl time.Duration, origins []string, objectExpiry *string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, failure("bad_response")
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
			return nil, failure("bad_response")
		}
	}
	origin := u.Scheme + "://" + u.Host
	contains := func(xs []string) bool {
		for _, x := range xs {
			if x == origin {
				return true
			}
		}
		return false
	}
	if !contains(origins) || len(c.origins) > 0 && !contains(c.origins) {
		return nil, failure("bad_response")
	}
	now := c.now()
	end, err := time.Parse(time.RFC3339Nano, deadline)
	if err != nil {
		return nil, failure("blob_capability_expired")
	}
	// An over-long TTL is not an aged capability. Fail it without a re-mint.
	if end.After(now.Add(ttl + capabilitySkew)) {
		return nil, failure("blob_capability_expired")
	}
	if objectExpiry != nil {
		at, err := time.Parse(time.RFC3339Nano, *objectExpiry)
		// A capability past the object deadline, or an object that has already
		// ended, is a bad grant. Do not spend another mint on it.
		if err != nil || end.After(at) || !at.After(now) {
			return nil, failure("bad_response")
		}
	}
	if !end.After(now) {
		return nil, &Error{Code: "blob_capability_expired", refresh: true}
	}
	return u, nil
}

// Upload hashes once, rewinds, sends the exact signed body, and completes only
// after a successful PUT. A changed source cannot produce a completed reference.
func (c *Client) Upload(ctx context.Context, r io.ReadSeeker, size int64, media, idem string) (blobwire.Ref, error) {
	caps, err := c.Discover(ctx)
	if err != nil {
		return blobwire.Ref{}, err
	}
	return c.UploadUsing(ctx, caps, r, size, media, idem)
}

// UploadUsing transfers with a capability snapshot already returned by Discover.
// It does not discover again, so an observed R2 hub cannot fall back to a hub
// PUT when a later capabilities response is missing.
func (c *Client) UploadUsing(ctx context.Context, caps Capabilities, r io.ReadSeeker, size int64, media, idem string) (blobwire.Ref, error) {
	var zero blobwire.Ref
	var err error
	if caps.Backend != "disk" && caps.Backend != "r2" {
		return zero, failure("bad_response")
	}
	if caps.Backend == "r2" && caps.ChecksumProfile != "sha256" {
		return zero, failure("bad_response")
	}
	if caps.MaxSize <= 0 || caps.MaxSize > MaxSize {
		caps.MaxSize = MaxSize
	}
	// Size outside 1..max is not one code. Zero and negative are invalid
	// (hub 422 invalid_value). Only a size past the cap is payload_too_large.
	// Disk still accepts an empty body.
	if size < 0 || caps.Backend == "r2" && size == 0 {
		return zero, failure("invalid_value")
	}
	if size > caps.MaxSize {
		return zero, failure("payload_too_large")
	}
	if media == "" {
		media = "application/octet-stream"
	}
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return zero, failure("blob_read_failed")
	}
	if caps.Backend == "disk" {
		req, err := http.NewRequestWithContext(ctx, "PUT", c.base+"/v1/blobs?ttl=86400", io.LimitReader(r, size))
		if err != nil {
			return zero, failure("bad_response")
		}
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("Content-Type", media)
		req.Header.Set("Accept", "application/json")
		resp, err := c.hub.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, &Error{Code: "blob_unavailable", HubTransport: true}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return zero, responseError(resp)
		}
		defer resp.Body.Close()
		if err := hubJSON(resp.Body, &zero); err != nil {
			return blobwire.Ref{}, err
		}
		return zero, nil
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(r, size+1))
	if err != nil || n != size {
		return zero, failure("blob_read_failed")
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return zero, failure("blob_read_failed")
	}
	if idem == "" {
		var b [16]byte
		_, _ = rand.Read(b[:])
		idem = "blob-" + hex.EncodeToString(b[:])
	}
	var g blobwire.UploadGrant
	if err = c.json(ctx, "POST", "/v1/blobs/uploads", idem, blobwire.UploadRequest{Size: size, SHA256: digest, MediaType: media}, &g); err != nil {
		return zero, err
	}
	if g.BlobID == "" || g.SHA256 != digest || g.Size != size || g.Upload.Method != "PUT" {
		return zero, failure("bad_response")
	}
	u, err := c.directURL(g.Upload.URL, g.Upload.ExpiresAt, 5*time.Minute, caps.DownloadOrigins, g.ExpiresAt)
	if err != nil {
		return zero, err
	}
	want := map[string]string{"Content-Length": strconv.FormatInt(size, 10), "Content-Type": "application/octet-stream", "Content-Disposition": "attachment", "Cache-Control": "private, no-store", "If-None-Match": "*", "X-Amz-Checksum-Sha256": base64.StdEncoding.EncodeToString(hash.Sum(nil)), "X-Amz-Meta-Vecnet-Checksum-Profile": "sha256", "X-Amz-Meta-Vecnet-Checksum": base64.StdEncoding.EncodeToString(hash.Sum(nil))}
	headers := make(http.Header)
	for k, v := range g.Upload.Headers {
		key := http.CanonicalHeaderKey(k)
		if _, ok := want[key]; !ok || headers.Get(key) != "" {
			return zero, failure("bad_response")
		}
		headers.Set(key, v)
	}
	for k, v := range want {
		if headers.Get(k) != v {
			return zero, failure("bad_response")
		}
	}
	// Hash the PUT read too, without seeking or incrementing UI progress during
	// the initial digest pass. R2's enforced signed checksum remains authoritative.
	sentHash := sha256.New()
	req, err := http.NewRequestWithContext(ctx, "PUT", u.String(), &progressReader{r: io.TeeReader(io.LimitReader(r, size), sentHash), report: c.Progress})
	if err != nil {
		return zero, failure("bad_response")
	}
	req.Header = headers
	req.ContentLength = size
	resp, err := c.transfer.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, failure("blob_unavailable")
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, failure("blob_unavailable")
	}
	if hex.EncodeToString(sentHash.Sum(nil)) != digest {
		return zero, failure("blob_checksum_mismatch")
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	// Do not follow the grant's complete_url: construct the trusted endpoint.
	var ref blobwire.Ref
	if err = c.json(ctx, "POST", "/v1/blobs/uploads/"+url.PathEscape(g.BlobID)+"/complete", "", struct{}{}, &ref); err != nil {
		return zero, err
	}
	if ref.BlobID != g.BlobID || ref.SHA256 != digest || ref.Size != size || !verified(digest, ref.SHA256Verified, ref.Integrity) {
		return zero, failure("bad_response")
	}
	return ref, nil
}

// Download exchanges a stable reference for a fresh grant on every invocation.
// Errors return neither partial bytes nor a capability.
func (c *Client) Download(ctx context.Context, sha, id string, max int64) ([]byte, blobwire.DownloadGrant, error) {
	var b bytes.Buffer
	g, err := c.DownloadTo(ctx, &b, sha, id, max)
	if err != nil {
		return nil, blobwire.DownloadGrant{}, err
	}
	return b.Bytes(), g, nil
}

// DownloadTo streams into a caller-owned private temporary file. The caller must
// discard it on error and publish it only after successful integrity validation.
func (c *Client) DownloadTo(ctx context.Context, w io.Writer, sha, id string, max int64) (blobwire.DownloadGrant, error) {
	var zero blobwire.DownloadGrant
	if !validSHA(sha) || max < 0 {
		return zero, failure("invalid_blob")
	}
	if max > MaxSize {
		max = MaxSize
	}
	caps, err := c.Discover(ctx)
	if err != nil {
		return zero, err
	}
	if caps.MaxSize < max {
		max = caps.MaxSize
	}
	for attempt := 0; attempt < 2; attempt++ {
		path := "/v1/blobs/" + url.PathEscape(sha)
		q := url.Values{}
		if id != "" {
			q.Set("blob_id", id)
		}
		var g blobwire.DownloadGrant
		err = c.json(ctx, "GET", path+"/download?"+q.Encode(), "", nil, &g)
		var e *Error
		legacy := errors.As(err, &e) && e.Status == 404 && id == "" && caps.Backend == "disk"
		if err != nil && !legacy {
			return zero, err
		}
		var resp *http.Response
		hubBody := legacy || g.LocalPath != ""
		if hubBody {
			// Discovery selects the backend for new uploads. Grandfathered disk
			// attachments and durable text still use authenticated local grants.
			// An explicit R2 reference can never be downgraded to hub bytes.
			if !legacy && (id != "" || g.LocalPath != path || g.BlobID != "" || g.SHA256 != sha || g.URL != "") {
				return zero, failure("bad_response")
			}
			resp, err = c.request(ctx, "GET", path, nil)
		} else {
			if id == "" || g.BlobID != id || g.SHA256 != sha || g.Method != "GET" || g.Size < 0 || g.Size > max || !verified(sha, g.SHA256Verified, g.Integrity) {
				return zero, failure("bad_response")
			}
			u, uerr := c.directURL(g.URL, g.ExpiresAt, 600*time.Second, caps.DownloadOrigins, g.ObjectExpiresAt)
			if uerr != nil {
				var expired *Error
				if attempt == 0 && errors.As(uerr, &expired) && expired.refresh {
					continue
				}
				return zero, uerr
			}
			req, reqErr := newTransferRequest(ctx, "GET", u.String(), nil)
			if reqErr != nil {
				return zero, failure("bad_response")
			}
			resp, err = c.transfer.Do(req)
			if err == nil && resp.StatusCode == 403 && attempt == 0 {
				resp.Body.Close()
				continue
			}
			if err == nil && resp.StatusCode != 200 {
				resp.Body.Close()
				return zero, failure("blob_unavailable")
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			var e *Error
			if errors.As(err, &e) {
				return zero, err
			}
			return zero, failure("blob_unavailable")
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return zero, failure("blob_unavailable")
		}
		if resp.ContentLength > max {
			resp.Body.Close()
			return zero, failure("blob_too_large")
		}
		h := sha256.New()
		body := &bodyReader{Reader: resp.Body}
		n, copyErr := io.Copy(io.MultiWriter(w, h), io.LimitReader(body, max))
		var extra [1]byte
		extraN, extraErr := io.ReadFull(body, extra[:])
		resp.Body.Close()
		if copyErr != nil || extraErr != nil && extraErr != io.EOF {
			return zero, &Error{Code: "blob_unavailable", HubTransport: hubBody && body.err != nil}
		}
		if extraN != 0 {
			return zero, failure("blob_too_large")
		}
		if !legacy && n != g.Size || hex.EncodeToString(h.Sum(nil)) != sha {
			return zero, failure("blob_checksum_mismatch")
		}
		if legacy {
			g = blobwire.DownloadGrant{SHA256: sha, Size: n, MediaType: resp.Header.Get("Content-Type")}
		}
		return g, nil
	}
	return zero, failure("blob_capability_expired")
}

// Keep source read errors distinct from local destination write failures.
type bodyReader struct {
	io.Reader
	err error
}

func (r *bodyReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

type progressReader struct {
	r      io.Reader
	n      int64
	report func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.report != nil {
		p.report(p.n)
	}
	return n, err
}
