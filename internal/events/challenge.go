package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/webhook"
)

// HeaderSubscription names the subscription on every callback POST.
const HeaderSubscription = "X-MCP-Subscription-Id"

// Reasons of an ErrChallenge, the data.reason of a -32015 (PROTOCOL §4.4).
const (
	ReasonChallengeFailed = "challenge_failed"
	ReasonTimeout         = "timeout"
	ReasonInvalidURL      = "invalid_url"
)

// verifyPrefix tags the webhook-id of a verification challenge.
const verifyPrefix ids.Prefix = "vrf"

// ErrChallenge is a failed verification challenge.
type ErrChallenge struct{ Reason string }

func (e ErrChallenge) Error() string { return "events: challenge: " + e.Reason }

// Challenge verifies s's callback (PROTOCOL §4.4): it POSTs
// {"type":"verification","challenge":<32 random bytes, base64url>} signed with
// secret and carrying X-MCP-Subscription-Id, and wants a 2xx echoing
// {"challenge":<same>}. client must be an egress.Policy client, which refuses
// redirects; now is the hub's clock, for the signature. Any failure is an
// ErrChallenge.
func Challenge(ctx context.Context, client *http.Client, s Subscription, secret string, now time.Time) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("events: challenge: %w", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(raw)
	body, err := json.Marshal(map[string]string{"type": "verification", "challenge": challenge})
	if err != nil {
		return fmt.Errorf("events: challenge: %w", err)
	}
	id := ids.New(verifyPrefix)
	sig, err := webhook.Sign(secret, id, now, body)
	if err != nil {
		return fmt.Errorf("events: challenge: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return ErrChallenge{Reason: ReasonInvalidURL}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webhook.HeaderID, id)
	req.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(webhook.HeaderSignature, sig)
	req.Header.Set(HeaderSubscription, s.ID)
	resp, err := client.Do(req)
	if err != nil {
		return ErrChallenge{Reason: challengeReason(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrChallenge{Reason: ReasonChallengeFailed}
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, hub.PushResponseCap))
	if err != nil {
		return ErrChallenge{Reason: challengeReason(err)}
	}
	var echo struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(answer, &echo); err != nil ||
		subtle.ConstantTimeCompare([]byte(echo.Challenge), []byte(challenge)) != 1 {
		return ErrChallenge{Reason: ReasonChallengeFailed}
	}
	return nil
}

// challengeReason classifies a failed request: the guard's refusals are
// invalid_url, whether they come from the URL or from the addresses the host
// resolved to at dial time. A refused redirect is a failed challenge.
func challengeReason(err error) string {
	for _, refused := range []error{egress.ErrPrivateAddress, egress.ErrScheme, egress.ErrUserinfo, egress.ErrInvalidURL} {
		if errors.Is(err, refused) {
			return ReasonInvalidURL
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return ReasonTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	return ReasonChallengeFailed
}
