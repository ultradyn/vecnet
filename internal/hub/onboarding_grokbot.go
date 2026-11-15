package hub

// Grok Bot onboarding (Decisions §§3–5). One transaction creates the peer,
// the sealed route, the connector descriptor, the setup thread and one
// hub-authored delivery. The push worker sends it after commit.
// PROTOCOL §§2.2, 3.3, 5.1–5.3, 12, 13.3, 14.3 and 16.3.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/onboarding"
)

const grokSetupTitle = "vecnet incoming messages"

// grokDescriptor is the per-peer connector record. It names one credential
// mode. It does not hold a routine URL or key.
type grokDescriptor struct {
	V              int    `json:"v"`
	Name           string `json:"name"`
	PeerID         string `json:"peer_id"`
	CredentialMode string `json:"credential_mode"`
}

// grokbotOnboard is the grokbot_setup body. Validation and sealing happen
// before any insert. The caller saves the receipt in this same transaction
// and publishes notifications after commit. createdAt is the setup
// message's created_at; the activity feed uses that same value.
func grokbotOnboard(ctx context.Context, h *Hub, tx *sql.Tx, n onboarding.Normalized) (onboarding.SubmitResult, []string, string, error) {
	sealed, err := h.prepareGrokbotRoute(n.WebhookURL, n.WebhookKey, true)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	operator, err := grokOperatorPeer(ctx, tx)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := grokConnectorFree(ctx, tx, n.ConnectorName); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	mode := onboarding.CredentialHostedOAuth
	if n.SkipMCP {
		mode = onboarding.CredentialCLIBearer
	}
	peerID := ids.New(ids.Peer)
	prnID := ids.New(ids.Prn)
	thrID := ids.New(ids.Thr)
	msgID := ids.New(ids.Msg)
	hubURL := strings.TrimRight(h.Cfg.PublicURL, "/")
	text, err := grokSetupText(n, mode, hubURL, peerID, thrID)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	nowT := h.Now()
	now := envelope.FormatTime(nowT)

	if err := insertGrokDescriptor(ctx, tx, n.ConnectorName, peerID, mode); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := insertPrincipal(ctx, tx, prnID, n.Name, now); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := h.insertPeer(ctx, tx, peerID, prnID, n.Name, n.Kind, now, snapshot); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := h.DirectoryUpdated(ctx, tx, peerID, "enrolled", enrolledFields); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := audit.Record(ctx, tx, operator, "peer_add", []string{peerID, prnID}, map[string]any{
		"kind": n.Kind, "request_id": n.RequestID, "credential_mode": mode,
	}); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	routeID, _, err := h.insertRoute(ctx, tx, newRoute{
		ownerKind: "peer", ownerID: peerID, kind: RouteKindGrokbot, url: n.WebhookURL,
		keyEnc: sealed, verified: true, grokbotTransport: n.GrokbotTransport,
	})
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}

	title := grokSetupTitle
	thr := &threadRow{
		ID: thrID, Title: &title, CreatedBy: HubSender, HistoryAccess: "from_join",
		participants:  []string{operator, peerID},
		isParticipant: map[string]bool{operator: true, peerID: true},
	}
	if b := preferenceNumber(snapshot, "agents.thread_budget"); b > 0 {
		thr.Budget = &b
	}
	expires := envelope.FormatTime(nowT.Add(preferenceDuration(snapshot, "agents.default_ttl")))
	msg := envelope.Message{
		SchemaVersion: envelope.SchemaVersion,
		MessageID:     msgID,
		ThreadID:      thrID,
		Kind:          "message",
		Sender:        HubSender,
		SenderInfo:    &envelope.SenderInfo{Name: HubSender, Kind: HubSender},
		Recipients:    []string{peerID},
		Priority:      "wake_idle",
		CreatedAt:     now,
		Content:       envelope.Content{Text: text},
		Loop: envelope.Loop{
			MaxDepth:    preferenceNumber(snapshot, "agents.default_max_depth"),
			ExpiresAt:   expires,
			SenderChain: []string{HubSender},
		},
	}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", fmt.Errorf("hub: encode setup message: %w", err)
	}
	if strings.Contains(string(msgJSON), n.WebhookKey) || strings.Contains(string(msgJSON), n.WebhookURL) {
		return onboarding.SubmitResult{}, nil, "", errors.New("hub: setup message would contain a routine secret")
	}
	if err := insertThread(ctx, tx, thr, now); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := addParticipants(ctx, tx, thrID, []string{operator, peerID}, 0, now); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := h.insertMessage(ctx, tx, thr, &msg, msgJSON, "", "", false); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	refs, notify, err := h.fanOut(ctx, tx, thr, &msg, msgJSON, []target{{peer: peerID}}, nil, "hub", expires, nowT, snapshot)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if len(refs) != 1 || refs[0].Recipient != peerID {
		return onboarding.SubmitResult{}, nil, "", errors.New("hub: setup delivery was not queued for the bot")
	}
	operators, err := h.noteOperatorView(ctx, tx, "sent", thrID)
	if err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	if err := audit.Record(ctx, tx, HubSender, "grokbot_setup", []string{peerID, thrID, msgID, refs[0].DeliveryID}, map[string]any{
		"request_id": n.RequestID, "credential_mode": mode, "connector_name": n.ConnectorName, "deliveries": 1,
	}); err != nil {
		return onboarding.SubmitResult{}, nil, "", err
	}
	res := onboarding.SubmitResult{
		GrokbotTransport: n.GrokbotTransport,
		PeerID:           peerID, PrincipalID: prnID, RouteID: routeID, ConnectorName: n.ConnectorName,
		AuthState: onboarding.AuthUnlinked,
		Setup: &onboarding.SetupInfo{
			ThreadID: thrID, MessageID: msgID, DeliveryID: refs[0].DeliveryID, State: onboarding.SetupQueued,
		},
	}
	return res, uniqueNotify(append(append(notify, prnID), operators...)), now, nil
}

func grokSetupText(n onboarding.Normalized, mode, hubURL, peerID, thrID string) (string, error) {
	values := map[string]string{
		"hub_url": hubURL, "peer_id": peerID, "thread_id": thrID, "credential_mode": mode,
		"grokbot_transport": n.GrokbotTransport,
	}
	if mode == onboarding.CredentialHostedOAuth {
		values["connector_name"] = n.ConnectorName
	} else {
		values["profile"] = onboarding.Slug(n.Name)
	}
	text, err := onboarding.RenderPrompt(n.Kind, onboarding.PhaseSetup, values)
	if err != nil {
		return "", onboardError(err)
	}
	if strings.Contains(text, n.WebhookKey) || strings.Contains(text, n.WebhookURL) {
		return "", errors.New("hub: setup message would contain a routine secret")
	}
	return text, nil
}

func grokOperatorPeer(ctx context.Context, tx *sql.Tx) (string, error) {
	var peer string
	err := tx.QueryRowContext(ctx, `
		SELECT p.peer_id FROM peers p
		JOIN credentials c ON c.subject_id = p.prn_id
		WHERE p.kind = 'operator' AND c.kind = 'operator' AND c.revoked_at IS NULL
		ORDER BY c.created_at, p.peer_id
		LIMIT 1`).Scan(&peer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", E(http.StatusForbidden, "forbidden", "operator credential required")
	}
	if err != nil {
		return "", fmt.Errorf("hub: onboarding operator: %w", err)
	}
	return peer, nil
}

func grokConnectorKey(name string) string {
	return "onboarding.v1/connector/" + name
}

func grokConnectorFree(ctx context.Context, tx *sql.Tx, name string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM meta WHERE key=?`, grokConnectorKey(name)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hub: connector descriptor: %w", err)
	}
	return grokConnectorConflict()
}

func insertGrokDescriptor(ctx context.Context, tx *sql.Tx, name, peerID, mode string) error {
	if mode != onboarding.CredentialHostedOAuth && mode != onboarding.CredentialCLIBearer {
		return errors.New("hub: credential mode must be hosted_oauth or cli_bearer")
	}
	body, err := json.Marshal(grokDescriptor{V: 1, Name: name, PeerID: peerID, CredentialMode: mode})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)`, grokConnectorKey(name), string(body)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: meta.key") {
			return grokConnectorConflict()
		}
		return fmt.Errorf("hub: connector descriptor: %w", err)
	}
	return nil
}

func grokConnectorConflict() error {
	return onboardError(onboarding.ConnectorCollision("taken", []string{"taken"}))
}

func uniqueNotify(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
