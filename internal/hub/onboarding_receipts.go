package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/XertroV/vecnet/internal/onboarding"
)

// onboardDigestInfo domain-separates the receipt MAC from sealing and from
// blob tokens, both of which also use meta.hub_key.
const onboardDigestInfo = "vecnet onboarding.v1 request"

// onboardRecord is the namespaced meta value. It holds ids, the keyed
// digest and nonsecret options. It does not hold a raw request, a routine
// URL or key, or one-time human output.
type onboardRecord struct {
	V               int    `json:"v"`
	Subject         string `json:"subject"`
	RequestID       string `json:"request_id"`
	Kind            string `json:"kind"`
	SpecVersion     string `json:"spec_version"`
	Digest          string `json:"digest"`
	PeerID          string `json:"peer_id"`
	PrincipalID     string `json:"principal_id"`
	NameOpen        bool   `json:"name_open"`
	EnrolTTLSeconds int    `json:"enrol_ttl_seconds,omitempty"`
	ConnectorName   string `json:"connector_name,omitempty"`
	RouteID         string `json:"route_id,omitempty"`
	ThreadID        string `json:"thread_id,omitempty"`
	MessageID       string `json:"message_id,omitempty"`
	DeliveryID      string `json:"delivery_id,omitempty"`
}

// onboardCanonical is the byte string the keyed digest covers, including
// secret inputs. It is never stored.
type onboardCanonical struct {
	ConnectorName   string `json:"connector_name"`
	EnrolTTLSeconds int    `json:"enrol_ttl_seconds"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	NameOpen        bool   `json:"name_open"`
	PrincipalID     string `json:"principal_id"`
	RequestID       string `json:"request_id"`
	SkipMCP         bool   `json:"skip_mcp"`
	SpecVersion     string `json:"spec_version"`
	WebhookKey      string `json:"webhook_key"`
	WebhookURL      string `json:"webhook_url"`
}

func onboardMetaKey(subject, requestID string) string {
	return "onboarding.v1/" + subject + "/" + requestID
}

func (h *Hub) onboardDigest(n onboarding.Normalized) (string, error) {
	key := h.hubKey()
	if len(key) < 32 {
		return "", errors.New("hub: meta.hub_key is not loaded")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(onboardDigestInfo))
	derived := mac.Sum(nil)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	body := onboardCanonical{
		ConnectorName: n.ConnectorName, EnrolTTLSeconds: n.EnrolTTLSeconds, Kind: n.Kind,
		Name: n.Name, NameOpen: n.NameOpen, PrincipalID: n.PrincipalID, RequestID: n.RequestID,
		SkipMCP: n.SkipMCP, SpecVersion: n.SpecVersion, WebhookKey: n.WebhookKey, WebhookURL: n.WebhookURL,
	}
	if err := enc.Encode(body); err != nil {
		return "", err
	}
	mac = hmac.New(sha256.New, derived)
	mac.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func digestEqual(stored, got string) bool {
	return subtle.ConstantTimeCompare([]byte(stored), []byte(got)) == 1
}

func (h *Hub) loadOnboardReceipt(ctx context.Context, tx *sql.Tx, subject, requestID string) (onboardRecord, bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, onboardMetaKey(subject, requestID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return onboardRecord{}, false, nil
	}
	if err != nil {
		return onboardRecord{}, false, fmt.Errorf("hub: onboarding receipt: %w", err)
	}
	var rec onboardRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return onboardRecord{}, false, fmt.Errorf("hub: onboarding receipt: %w", err)
	}
	if rec.Subject != subject || rec.RequestID != requestID {
		return onboardRecord{}, false, errors.New("hub: onboarding receipt subject mismatch")
	}
	return rec, true, nil
}

func (h *Hub) saveOnboardReceipt(ctx context.Context, tx *sql.Tx, subject string, n onboarding.Normalized, digest string, res onboarding.SubmitResult) error {
	rec := onboardRecord{
		V: 1, Subject: subject, RequestID: n.RequestID, Kind: n.Kind, SpecVersion: n.SpecVersion,
		Digest: digest, PeerID: res.PeerID, PrincipalID: res.PrincipalID, NameOpen: n.NameOpen,
		EnrolTTLSeconds: n.EnrolTTLSeconds, ConnectorName: n.ConnectorName, RouteID: res.RouteID,
	}
	if res.Setup != nil {
		rec.ThreadID = res.Setup.ThreadID
		rec.MessageID = res.Setup.MessageID
		rec.DeliveryID = res.Setup.DeliveryID
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	text := string(body)
	if n.WebhookKey != "" && strings.Contains(text, n.WebhookKey) || n.WebhookURL != "" && strings.Contains(text, n.WebhookURL) {
		return errors.New("hub: onboarding receipt would store a routine secret")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)`, onboardMetaKey(subject, n.RequestID), text); err != nil {
		return fmt.Errorf("hub: onboarding receipt: %w", err)
	}
	return nil
}

func (h *Hub) resultFromReceipt(ctx context.Context, tx *sql.Tx, rec onboardRecord, status string) (onboarding.SubmitResult, error) {
	spec, _ := onboarding.Lookup(rec.Kind)
	state, err := h.deriveAuthState(ctx, tx, rec.PeerID)
	if err != nil {
		return onboarding.SubmitResult{}, err
	}
	setup, err := h.deriveSetup(ctx, tx, rec)
	if err != nil {
		return onboarding.SubmitResult{}, err
	}
	return onboarding.SubmitResult{
		RequestID: rec.RequestID, Kind: rec.Kind, PeerID: rec.PeerID, PrincipalID: rec.PrincipalID,
		RouteID: rec.RouteID, ConnectorName: rec.ConnectorName, Setup: setup, AuthState: state,
		SendDefaults: copyDefaults(spec), Status: status,
	}, nil
}

func (h *Hub) deriveAuthState(ctx context.Context, tx *sql.Tx, peerID string) (string, error) {
	now := h.nowStr()
	var cred, token, link, oauth, peer int
	err := tx.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM peers WHERE peer_id=?),
		  (SELECT count(*) FROM credentials WHERE kind='peer' AND subject_id=? AND revoked_at IS NULL),
		  (SELECT count(*) FROM enrol_tokens WHERE peer_id=? AND used_at IS NULL AND expires_at>?),
		  (SELECT count(*) FROM dot_links WHERE peer_id=? AND used_at IS NULL AND expires_at>?),
		  (SELECT count(*) FROM oauth_tokens t JOIN peers p ON p.prn_id=t.principal_id
		    WHERE p.peer_id=? AND t.revoked_at IS NULL AND t.expires_at>?)`,
		peerID, peerID, peerID, now, peerID, now, peerID, now).Scan(&peer, &cred, &token, &link, &oauth)
	if err != nil {
		return "", fmt.Errorf("hub: onboarding auth state: %w", err)
	}
	if peer == 0 {
		return onboarding.AuthUnlinked, nil
	}
	if cred > 0 || oauth > 0 {
		return onboarding.AuthVerified, nil
	}
	if token > 0 || link > 0 {
		return onboarding.AuthPending, nil
	}
	return onboarding.AuthUnlinked, nil
}

func (h *Hub) deriveSetup(ctx context.Context, tx *sql.Tx, rec onboardRecord) (*onboarding.SetupInfo, error) {
	if rec.DeliveryID == "" && rec.ThreadID == "" && rec.MessageID == "" {
		return nil, nil
	}
	info := &onboarding.SetupInfo{ThreadID: rec.ThreadID, MessageID: rec.MessageID, DeliveryID: rec.DeliveryID}
	if rec.DeliveryID == "" {
		info.State = onboarding.SetupQueued
		return info, nil
	}
	var state, priority, thread, message string
	var held int
	err := tx.QueryRowContext(ctx, `SELECT state, priority, held, thr_id, msg_id FROM deliveries WHERE dly_id=?`, rec.DeliveryID).
		Scan(&state, &priority, &held, &thread, &message)
	if errors.Is(err, sql.ErrNoRows) {
		info.State = onboarding.SetupFailed
		return info, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hub: onboarding setup: %w", err)
	}
	if thread != "" {
		info.ThreadID = thread
	}
	if message != "" {
		info.MessageID = message
	}
	info.State = setupState(state, priority, held != 0)
	return info, nil
}

func setupState(state, priority string, held bool) string {
	switch state {
	case onboarding.SetupAccepted, onboarding.SetupProcessed, onboarding.SetupFailed, onboarding.SetupExpired, onboarding.SetupDeadLetter:
		return state
	case "queued", "pushed":
		if held || priority == "hold" {
			return onboarding.SetupHeld
		}
		return onboarding.SetupQueued
	default:
		return onboarding.SetupFailed
	}
}

func onboardMetaConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "meta.key")
}
