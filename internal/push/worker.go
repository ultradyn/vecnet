package push

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/webhook"
)

// Worker defaults.
const (
	DefaultInterval = time.Second      // Run's poll interval
	ClientTimeout   = 30 * time.Second // per push request
	ClaimBatch      = 16               // deliveries claimed per Run iteration
	// HeaderAttempt carries the attempt number on every push (§3.1).
	HeaderAttempt = "X-Vecnet-Attempt"
)

// ErrChallenge reports a verification challenge the receiver did not
// answer with 2xx and the same challenge (PROTOCOL §3.2).
var ErrChallenge = errors.New("push: route did not echo the verification challenge")

// NewClient returns the push HTTP client: it never follows a redirect (a
// 3xx is a terminal result, §3.3, §14.7) and times out after 30 s.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: ClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Worker pushes due deliveries. Zero fields take defaults: Client
// NewClient(), Now Hub.Now, Interval DefaultInterval. A caller-supplied
// Client must not follow redirects.
type Worker struct {
	Hub      *hub.Hub
	Client   *http.Client
	Now      func() time.Time
	Interval time.Duration

	once   sync.Once
	kick   chan struct{}
	client *http.Client
}

func (w *Worker) init() {
	w.once.Do(func() {
		w.kick = make(chan struct{}, 1)
		w.client = w.Client
		if w.client == nil {
			w.client = NewClient()
		}
	})
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return w.Hub.Now()
}

// Kick wakes Run now instead of at its next interval; it never blocks.
func (w *Worker) Kick() {
	w.init()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Run claims due deliveries (up to 16 at a time), pushes each in its own
// goroutine, and sleeps Interval or until Kick, until ctx ends. It returns
// nil once ctx is done and in-flight pushes have finished. Claim errors are
// logged and retried at the next interval.
func (w *Worker) Run(ctx context.Context) error {
	w.init()
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-w.kick:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		due, err := w.Hub.ClaimDue(ctx, w.now(), ClaimBatch)
		if err != nil && ctx.Err() == nil {
			slog.Error("push: claim due", "err", err)
		}
		for _, d := range due {
			wg.Go(func() { w.Push(ctx, d) })
		}
		timer.Reset(interval)
	}
}

// Step claims every delivery due now (in batches of 16), pushes each batch
// concurrently and waits for the results; it returns how many it pushed.
// Tests and operator tools drive the state machine with it.
func (w *Worker) Step(ctx context.Context) (int, error) {
	w.init()
	total := 0
	for {
		due, err := w.Hub.ClaimDue(ctx, w.now(), ClaimBatch)
		if err != nil {
			return total, err
		}
		if len(due) == 0 {
			return total, nil
		}
		var wg sync.WaitGroup
		for _, d := range due {
			wg.Go(func() { w.Push(ctx, d) })
		}
		wg.Wait()
		total += len(due)
		if len(due) < ClaimBatch {
			return total, nil
		}
	}
}

// Push POSTs d.Body exactly to d.URL with the §3.1 headers (webhook-id =
// delivery_id, webhook-timestamp, webhook-signature by every active route
// key, X-Vecnet-Attempt), reads at most 4 KiB of the response, and records
// the result with Hub.PushResult. It then kicks Run, since the result may
// unblock the next delivery of the thread (§3.4).
func (w *Worker) Push(ctx context.Context, d hub.DueDelivery) {
	w.init()
	status, retryAfter, netErr := w.post(ctx, d.URL, d.DeliveryID, d.Keys, d.Body, d.Attempt, nil)
	if ctx.Err() != nil && netErr != nil {
		// Shutting down: leave the lease to lapse rather than burn an attempt.
		return
	}
	if err := w.Hub.PushResult(context.WithoutCancel(ctx), d, status, retryAfter, netErr, w.now()); err != nil {
		slog.Error("push: record result", "delivery", d.DeliveryID, "err", err)
	}
	w.Kick()
}

// post signs and sends one webhook. When sink is non-nil the response body
// (≤ 4 KiB) is copied into it; otherwise it is discarded.
func (w *Worker) post(ctx context.Context, url, id string, keys []string, body []byte, attempt int, sink *bytes.Buffer) (status int, retryAfter time.Duration, err error) {
	if len(body) > hub.MaxPushBody {
		return 0, 0, fmt.Errorf("push: body is %d bytes, over the %d-byte cap", len(body), hub.MaxPushBody)
	}
	if len(keys) == 0 {
		return 0, 0, errors.New("push: route has no active key")
	}
	now := w.now()
	sig, err := webhook.SignAll(keys, id, now, body)
	if err != nil {
		return 0, 0, fmt.Errorf("push: sign: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("push: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webhook.HeaderID, id)
	req.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(webhook.HeaderSignature, sig)
	if attempt > 0 {
		req.Header.Set(HeaderAttempt, strconv.Itoa(attempt))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	dst := io.Discard
	if sink != nil {
		dst = sink
	}
	io.Copy(dst, io.LimitReader(resp.Body, hub.PushResponseCap))
	return resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After"), w.now()), nil
}

// Verify sends routeID its verification challenge (PROTOCOL §3.2):
// {"type":"verification","challenge":<43-char base64url>}, signed like a
// push. A 2xx answer of {"challenge":<the same>} marks the route verified
// (Hub.MarkRouteVerified) and kicks Run, so deliveries queued for it go
// out; anything else returns an error wrapping ErrChallenge and leaves the
// route unverified.
func (w *Worker) Verify(ctx context.Context, routeID string) error {
	w.init()
	url, err := w.Hub.RouteURL(ctx, routeID)
	if err != nil {
		return err
	}
	keys, err := w.Hub.RouteKeys(ctx, routeID, w.now())
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("push: challenge: %w", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(raw) // 43 characters
	body, err := json.Marshal(map[string]string{"type": "verification", "challenge": challenge})
	if err != nil {
		return fmt.Errorf("push: challenge: %w", err)
	}
	var answer bytes.Buffer
	status, _, err := w.post(ctx, url, ids.New(verifyPrefix), keys, body, 0, &answer)
	if err != nil {
		return fmt.Errorf("push: verify %s: %w", routeID, err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrChallenge, status)
	}
	var echo struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(answer.Bytes(), &echo); err != nil || echo.Challenge != challenge {
		return fmt.Errorf("%w: wrong or missing echo", ErrChallenge)
	}
	if err := w.Hub.MarkRouteVerified(ctx, routeID); err != nil {
		return err
	}
	w.Kick()
	return nil
}

// verifyPrefix tags the webhook-id of a verification challenge.
const verifyPrefix ids.Prefix = "vrf"
