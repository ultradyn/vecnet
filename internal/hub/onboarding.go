package hub

// Caller-aware onboarding. Kinds, validation and prompt text come from
// internal/onboarding. This file dispatches the legacy invite and link
// actions. grokbot_setup is grokbotOnboard, registered below.
// PROTOCOL §§2.12–2.13, 4.5, 8, 12, 16.2 and 20.1.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/onboarding"
)

const (
	onboardCreated  = "created"
	onboardReplayed = "replayed"
)

// requestIDRE matches onboarding's request id rule. The registry package
// does not export it.
var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// grokbotSetup is the grokbot_setup transaction body. init registers
// grokbotOnboard. Nil fails closed: OnboardAgent returns unsupported_action
// and writes nothing. The string result is the setup message created_at,
// which the activity feed must use (PROTOCOL §2.13).
var grokbotSetup func(ctx context.Context, h *Hub, tx *sql.Tx, n onboarding.Normalized) (onboarding.SubmitResult, []string, string, error)

func init() {
	grokbotSetup = grokbotOnboard
}

type onboardQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type onboardOutcome struct {
	result   onboarding.SubmitResult
	human    string
	notify   []string
	unlock   bool
	activity *ActivityEvent
}

// OnboardingKinds lists the Add agent specs for an operator. Listing does
// not grant write authority and does not write.
func (h *Hub) OnboardingKinds(ctx context.Context, c auth.Caller) ([]onboarding.KindSpec, error) {
	if _, _, err := h.operatorAuthorized(ctx, h.DB, c); err != nil {
		return nil, err
	}
	return onboarding.Specs(), nil
}

// OnboardAgent validates req and, for a legacy kind, creates the peer and
// its safe receipt in one transaction. The first commit returns one-time
// human output; a replay of the same request returns the same ids and omits
// that output. grokbot_setup fails closed until grokbotSetup is registered.
func (h *Hub) OnboardAgent(ctx context.Context, c auth.Caller, req onboarding.SubmitRequest) (onboarding.SubmitResult, error) {
	if _, _, err := h.operatorAuthorized(ctx, h.DB, c); err != nil {
		return onboarding.SubmitResult{}, err
	}
	n, err := onboarding.Normalize(req)
	if err != nil {
		return onboarding.SubmitResult{}, onboardError(err)
	}
	spec, ok := onboarding.Lookup(n.Kind)
	if !ok {
		return onboarding.SubmitResult{}, onboardError(&onboarding.FieldError{Code: onboarding.CodeUnknownKind, Field: "kind", Message: "unknown peer kind"})
	}
	if spec.SubmitAction == onboarding.ActionGrokbotSetup && grokbotSetup == nil {
		return onboarding.SubmitResult{}, onboardUnavailable()
	}
	res, err := h.onboardCommit(ctx, c, n, spec)
	if onboardMetaConflict(err) {
		return h.onboardReplay(ctx, c, n)
	}
	return res, err
}

// OnboardingStatus returns the safe receipt for the caller's subject and
// requestID. Human output is never included. Setup state and auth_state are
// read from current deliveries and credentials.
func (h *Hub) OnboardingStatus(ctx context.Context, c auth.Caller, requestID string) (onboarding.SubmitResult, error) {
	if !requestIDRE.MatchString(requestID) {
		return onboarding.SubmitResult{}, invalidValue("request_id", "request_id must be 1 to 128 characters: letters, digits, and . _ : -")
	}
	var res onboarding.SubmitResult
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		subject, _, err := h.operatorAuthorized(ctx, tx, c)
		if err != nil {
			return err
		}
		rec, found, err := h.loadOnboardReceipt(ctx, tx, subject, requestID)
		if err != nil {
			return err
		}
		if !found {
			return onboardNotFound(requestID)
		}
		res, err = h.resultFromReceipt(ctx, tx, rec, "")
		return err
	})
	return res, err
}

func (h *Hub) onboardCommit(ctx context.Context, c auth.Caller, n onboarding.Normalized, spec onboarding.KindSpec) (onboarding.SubmitResult, error) {
	var out onboardOutcome
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = h.onboardTx(ctx, tx, c, n, spec)
		return err
	})
	if err != nil {
		return onboarding.SubmitResult{}, err
	}
	if out.unlock {
		h.linkGuard.unlock()
	}
	if out.result.Status == onboardCreated {
		if len(out.notify) > 0 {
			h.Log.Notify(out.notify...)
		}
		h.notifyEnabled(ctx)
		if out.activity != nil {
			h.Activity.Publish(*out.activity)
		}
		out.result.HumanOutput = out.human
	}
	return out.result, nil
}

func (h *Hub) onboardTx(ctx context.Context, tx *sql.Tx, c auth.Caller, n onboarding.Normalized, spec onboarding.KindSpec) (onboardOutcome, error) {
	subject, actor, err := h.operatorAuthorized(ctx, tx, c)
	if err != nil {
		return onboardOutcome{}, err
	}
	digest, err := h.onboardDigest(n)
	if err != nil {
		return onboardOutcome{}, err
	}
	rec, found, err := h.loadOnboardReceipt(ctx, tx, subject, n.RequestID)
	if err != nil {
		return onboardOutcome{}, err
	}
	if found {
		if !digestEqual(rec.Digest, digest) {
			return onboardOutcome{}, idempotencyConflict(n.RequestID)
		}
		res, err := h.resultFromReceipt(ctx, tx, rec, onboardReplayed)
		return onboardOutcome{result: res}, err
	}
	switch spec.SubmitAction {
	case onboarding.ActionInvite:
		return h.onboardInvite(ctx, tx, n, spec, subject, actor, digest)
	case onboarding.ActionOAuthLink:
		return h.onboardLink(ctx, tx, n, spec, subject, actor, digest)
	case onboarding.ActionGrokbotSetup:
		return h.onboardGrok(ctx, tx, n, spec, subject, digest)
	default:
		return onboardOutcome{}, onboardUnavailable()
	}
}

func (h *Hub) onboardReplay(ctx context.Context, c auth.Caller, n onboarding.Normalized) (onboarding.SubmitResult, error) {
	var res onboarding.SubmitResult
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		subject, _, err := h.operatorAuthorized(ctx, tx, c)
		if err != nil {
			return err
		}
		digest, err := h.onboardDigest(n)
		if err != nil {
			return err
		}
		rec, found, err := h.loadOnboardReceipt(ctx, tx, subject, n.RequestID)
		if err != nil {
			return err
		}
		if !found || !digestEqual(rec.Digest, digest) {
			return idempotencyConflict(n.RequestID)
		}
		res, err = h.resultFromReceipt(ctx, tx, rec, onboardReplayed)
		return err
	})
	return res, err
}

func (h *Hub) onboardInvite(ctx context.Context, tx *sql.Tx, n onboarding.Normalized, spec onboarding.KindSpec, subject, actor, digest string) (onboardOutcome, error) {
	peerID := ids.New(ids.Peer)
	name := n.Name
	if n.NameOpen && name == "" {
		name = "invited-" + strings.ToLower(peerID[len(peerID)-6:])
	}
	opts := AddPeerOpts{
		Kind: n.Kind, Name: n.Name, PrincipalID: n.PrincipalID,
		TTL: time.Duration(n.EnrolTTLSeconds) * time.Second, NameOpen: n.NameOpen,
	}
	prn, token, exp, err := h.addPeerTx(ctx, tx, peerID, opts, name, actor, map[string]any{"request_id": n.RequestID})
	if err != nil {
		return onboardOutcome{}, err
	}
	human, err := h.inviteHuman(ctx, tx, n.Kind, n.NameOpen, n.Name, name, token, exp)
	if err != nil {
		return onboardOutcome{}, err
	}
	res, err := h.finishOnboard(ctx, tx, n, spec, subject, digest, peerID, prn, "")
	if err != nil {
		return onboardOutcome{}, err
	}
	return onboardOutcome{result: res, human: human, notify: []string{prn}}, nil
}

func (h *Hub) onboardLink(ctx context.Context, tx *sql.Tx, n onboarding.Normalized, spec onboarding.KindSpec, subject, actor, digest string) (onboardOutcome, error) {
	peerID := ids.New(ids.Peer)
	opts := AddPeerOpts{Kind: n.Kind, Name: n.Name, PrincipalID: n.PrincipalID}
	prn, _, _, err := h.addPeerTx(ctx, tx, peerID, opts, n.Name, actor, map[string]any{"request_id": n.RequestID})
	if err != nil {
		return onboardOutcome{}, err
	}
	code, shown, err := newDotLinkCode()
	if err != nil {
		return onboardOutcome{}, err
	}
	if err := h.insertDotLinkTx(ctx, tx, peerID, code, actor, map[string]any{"request_id": n.RequestID}); err != nil {
		return onboardOutcome{}, err
	}
	res, err := h.finishOnboard(ctx, tx, n, spec, subject, digest, peerID, prn, "")
	if err != nil {
		return onboardOutcome{}, err
	}
	return onboardOutcome{result: res, human: shown, notify: []string{prn}, unlock: true}, nil
}

func (h *Hub) onboardGrok(ctx context.Context, tx *sql.Tx, n onboarding.Normalized, spec onboarding.KindSpec, subject, digest string) (onboardOutcome, error) {
	if grokbotSetup == nil {
		return onboardOutcome{}, onboardUnavailable()
	}
	res, notify, createdAt, err := grokbotSetup(ctx, h, tx, n)
	if err != nil {
		return onboardOutcome{}, err
	}
	res.RequestID = n.RequestID
	res.Kind = n.Kind
	res.GrokbotTransport = n.GrokbotTransport
	if res.AuthState == "" {
		res.AuthState = onboarding.AuthUnlinked
	}
	if res.SendDefaults == nil {
		res.SendDefaults = copyDefaults(spec)
	}
	res.Status = onboardCreated
	res.HumanOutput = ""
	if err := h.saveOnboardReceipt(ctx, tx, subject, n, digest, res); err != nil {
		return onboardOutcome{}, err
	}
	var activity *ActivityEvent
	if res.Setup != nil && res.Setup.MessageID != "" {
		if createdAt == "" {
			return onboardOutcome{}, errors.New("hub: setup activity missing message created_at")
		}
		ev := messageActivity(res.Setup.ThreadID, res.Setup.MessageID, HubSender, "message", createdAt)
		activity = &ev
	}
	return onboardOutcome{result: res, notify: notify, activity: activity}, nil
}

func (h *Hub) finishOnboard(ctx context.Context, tx *sql.Tx, n onboarding.Normalized, spec onboarding.KindSpec, subject, digest, peerID, prn, routeID string) (onboarding.SubmitResult, error) {
	state, err := h.deriveAuthState(ctx, tx, peerID)
	if err != nil {
		return onboarding.SubmitResult{}, err
	}
	res := onboarding.SubmitResult{
		RequestID: n.RequestID, Kind: n.Kind, PeerID: peerID, PrincipalID: prn,
		RouteID: routeID, AuthState: state, SendDefaults: copyDefaults(spec), Status: onboardCreated,
	}
	if err := h.saveOnboardReceipt(ctx, tx, subject, n, digest, res); err != nil {
		return onboarding.SubmitResult{}, err
	}
	return res, nil
}

func (h *Hub) inviteHuman(ctx context.Context, tx *sql.Tx, kind string, open bool, suggestion, storedName, token string, exp time.Time) (string, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT p.name FROM principals p JOIN credentials c ON c.subject_id = p.prn_id
		WHERE c.kind = 'operator' AND c.revoked_at IS NULL ORDER BY c.created_at LIMIT 1`).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	values := map[string]string{
		"owner": owner, "hub_url": strings.TrimRight(h.Cfg.PublicURL, "/"),
		"profile": onboarding.Slug(storedName), "token": token, "expires": envelope.FormatTime(exp),
	}
	if open {
		values["open"] = "true"
		if suggestion != "" {
			values["name"] = suggestion
		}
	}
	return onboarding.RenderPrompt(kind, onboarding.PhaseInvite, values)
}

// operatorAuthorized is the operator check used before and inside onboarding
// transactions. The receipt subject is the verified principal, never a
// caller-supplied as. actor is that principal's operator peer.
func (h *Hub) operatorAuthorized(ctx context.Context, q onboardQuerier, c auth.Caller) (subject, actor string, err error) {
	if c.Kind != "operator" || (c.PeerKind != "" && c.PeerKind != "operator") || c.PrincipalID == "" {
		return "", "", E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	var n int
	if c.CredentialID != "" {
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE cred_id=? AND kind='operator' AND subject_id=? AND revoked_at IS NULL`, c.CredentialID, c.PrincipalID).Scan(&n); err != nil {
			return "", "", err
		}
		if n == 0 {
			return "", "", E(http.StatusUnauthorized, "unauthenticated", "unauthenticated")
		}
	} else if err = q.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE kind='operator' AND subject_id=? AND revoked_at IS NULL`, c.PrincipalID).Scan(&n); err != nil {
		return "", "", err
	} else if n == 0 {
		return "", "", E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	err = q.QueryRowContext(ctx, `SELECT peer_id FROM peers WHERE prn_id=? AND kind='operator' ORDER BY peer_id LIMIT 1`, c.PrincipalID).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	if err != nil {
		return "", "", err
	}
	if c.PeerID != "" && c.PeerID != actor {
		return "", "", E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	return c.PrincipalID, actor, nil
}

func onboardError(err error) error {
	var fe *onboarding.FieldError
	if !errors.As(err, &fe) {
		return err
	}
	status := http.StatusUnprocessableEntity
	switch fe.Code {
	case onboarding.CodeUnknownKind, onboarding.CodeUnknownField, onboarding.CodeUnsupportedAction:
		status = http.StatusBadRequest
	case onboarding.CodeStaleSpec, onboarding.CodeConnectorNameConflict:
		status = http.StatusConflict
	}
	e := E(status, fe.Code, fe.Message)
	if fe.Field != "" {
		e.Details["field"] = fe.Field
	}
	return e
}

func onboardUnavailable() error {
	return E(http.StatusUnprocessableEntity, "unsupported_action", "this submit action is not available")
}

func idempotencyConflict(requestID string) error {
	e := E(http.StatusConflict, "idempotency_conflict", "request_id was already used with a different request")
	e.Details["request_id"] = requestID
	return e
}

func onboardNotFound(requestID string) error {
	e := E(http.StatusNotFound, "not_found", "no such onboarding request")
	e.Details["request_id"] = requestID
	return e
}

func copyDefaults(spec onboarding.KindSpec) map[string]string {
	if len(spec.SendDefaults) == 0 {
		return nil
	}
	out := make(map[string]string, len(spec.SendDefaults))
	for k, v := range spec.SendDefaults {
		out[k] = v
	}
	return out
}
