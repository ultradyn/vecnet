package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// Inbound webhook sources (PROTOCOL §10). A source is not a peer. It is its
// own message sender, with its own rate limit, and it joins no thread and
// receives nothing. Threads and redeliveries are keyed per source (R09).

// kindSource is a source's directory kind (PROTOCOL §8).
const kindSource = "source"

// defaultSourcePriority is a source Message's priority unless the operator
// configures another (R09).
const defaultSourcePriority = "wake_idle"

// sourceKinds are the kinds POST /v1/ingress can verify. The schema also
// reserves standard_webhooks and token (PROTOCOL §10); a source of those
// kinds could not be verified yet, so it cannot be created.
var sourceKinds = map[string]bool{"github": true}

// SourceInfo is one source as the operator sees it; it never carries the
// secret.
type SourceInfo struct {
	SourceID       string   `json:"source_id"`
	Kind           string   `json:"kind"`
	Name           string   `json:"name"`
	Target         string   `json:"target"`
	EventAllow     []string `json:"event_allow"`
	Priority       string   `json:"priority"`
	SendsPerMin    int      `json:"sends_per_min"`
	ThreadsPerHour int      `json:"threads_per_hour"`
	CreatedAt      string   `json:"created_at"`
	Disabled       bool     `json:"disabled"`
}

// SourceMessage is one verified event, mapped to a Message by IngestSource.
// ProviderDeliveryID is the redelivery dedupe key; OriginThreadID the
// external conversation, which picks the thread. EventName is matched
// against the source's allowlist and travels as metadata vecnet.event; Title
// names a thread created for this event. Raw is stored verbatim as a hosted
// attachment of type MediaType.
type SourceMessage struct {
	SrcID              string
	ProviderDeliveryID string
	OriginThreadID     string
	OriginEventID      string
	EventName          string
	Title              string
	Summary            string
	Raw                []byte
	MediaType          string
}

// sourceActor is a source send in flight: the actor of sendTx.
type sourceActor struct {
	id, target         string
	providerID, origin string
}

func unknownSource(srcID string) *APIError {
	e := E(http.StatusNotFound, "not_found", "no such source")
	e.Details["source_id"] = srcID
	return e
}

// AddSource creates a source of kind routing every event to targetID, a
// peer_, team_ or thr_ id (404 unknown_recipient when it does not exist).
// eventAllow, when non-empty, limits it to those events; priority "" means
// wake_idle. It returns the source id, the ingress URL and the verification
// secret, 32 random bytes in hex; the secret is sealed at rest and is
// returned only here.
func (h *Hub) AddSource(ctx context.Context, kind, name, targetID string, eventAllow []string, priority string) (srcID, ingressURL, secret string, err error) {
	if !sourceKinds[kind] {
		return "", "", "", invalidValue("kind", "unknown source kind")
	}
	if name == "" {
		return "", "", "", invalidValue("name", "source name is required")
	}
	if priority == "" {
		priority = defaultSourcePriority
	}
	switch priority {
	case "wake", "wake_idle", "hold":
	default:
		return "", "", "", invalidValue("priority", "priority must be wake, wake_idle or hold")
	}
	var allow any // NULL allows everything
	if len(eventAllow) > 0 {
		b, err := json.Marshal(eventAllow)
		if err != nil {
			return "", "", "", fmt.Errorf("hub: encode event_allow: %w", err)
		}
		allow = string(b)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", "", "", fmt.Errorf("hub: source secret: %w", err)
	}
	secret = hex.EncodeToString(key)
	sealed, err := h.Seal([]byte(secret))
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256([]byte(secret))
	perMin, perHour := h.Cfg.SendsPerMin, h.Cfg.ThreadsPerHour
	if perMin <= 0 {
		perMin = 60
	}
	if perHour <= 0 {
		perHour = 20
	}

	srcID = ids.New(ids.Src)
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := sourceTargetExists(ctx, tx, targetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sources(src_id, kind, name, target_id, secret_hash, secret_enc, event_allow, priority,
			                     sends_per_min, threads_per_hour, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			srcID, kind, name, targetID, hex.EncodeToString(sum[:]), sealed, allow, priority,
			perMin, perHour, h.nowStr()); err != nil {
			return fmt.Errorf("hub: insert source: %w", err)
		}
		if err := h.DirectoryUpdated(ctx, tx, srcID, "enrolled", enrolledFields); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "source_add", []string{srcID, targetID}, map[string]any{"kind": kind})
	})
	if err != nil {
		return "", "", "", err
	}
	h.notifyEnabled(ctx)
	return srcID, h.Cfg.PublicURL + "/v1/ingress/" + srcID, secret, nil
}

// sourceTargetExists is nil when id names an existing peer, team or thread,
// else 404 unknown_recipient.
func sourceTargetExists(ctx context.Context, tx *sql.Tx, id string) error {
	var q string
	switch {
	case ids.Is(ids.Peer, id):
		q = `SELECT 1 FROM peers WHERE peer_id = ?`
	case ids.Is(ids.Team, id):
		q = `SELECT 1 FROM teams WHERE team_id = ?`
	case ids.Is(ids.Thr, id):
		q = `SELECT 1 FROM threads WHERE thr_id = ?`
	default:
		return unknownRecipient(id)
	}
	var one int
	err := tx.QueryRowContext(ctx, q, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return unknownRecipient(id)
	}
	if err != nil {
		return fmt.Errorf("hub: find source target: %w", err)
	}
	return nil
}

// ListSources returns every source, enabled or not, ordered by id.
func (h *Hub) ListSources(ctx context.Context) ([]SourceInfo, error) {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT src_id, kind, name, target_id, event_allow, priority, sends_per_min, threads_per_hour, created_at, disabled
		 FROM sources ORDER BY src_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: list sources: %w", err)
	}
	defer rows.Close()
	out := []SourceInfo{}
	for rows.Next() {
		var s SourceInfo
		var allow sql.NullString
		var disabled int
		if err := rows.Scan(&s.SourceID, &s.Kind, &s.Name, &s.Target, &allow, &s.Priority, &s.SendsPerMin, &s.ThreadsPerHour, &s.CreatedAt, &disabled); err != nil {
			return nil, fmt.Errorf("hub: list sources: %w", err)
		}
		if s.EventAllow, err = decodeEventAllow(allow); err != nil {
			return nil, err
		}
		s.Disabled = disabled != 0
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: list sources: %w", err)
	}
	return out, nil
}

func decodeEventAllow(allow sql.NullString) ([]string, error) {
	if !allow.Valid {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(allow.String), &out); err != nil {
		return nil, fmt.Errorf("hub: decode event_allow: %w", err)
	}
	return out, nil
}

// DisableSource stops srcID accepting events and removes it from the
// directory; its threads stay. 404 not_found for an unknown source.
func (h *Hub) DisableSource(ctx context.Context, srcID string) error {
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sources SET disabled = 1 WHERE src_id = ? AND disabled = 0`, srcID)
		if err != nil {
			return fmt.Errorf("hub: disable source: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("hub: disable source: %w", err)
		}
		if n == 0 {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM sources WHERE src_id = ?`, srcID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return unknownSource(srcID)
			}
			return err // already disabled: nothing changes
		}
		if err := h.DirectoryUpdated(ctx, tx, srcID, "removed", []string{}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "source_disable", []string{srcID}, nil)
	})
	if err != nil {
		return err
	}
	h.notifyEnabled(ctx)
	return nil
}

// directorySources renders the enabled sources as directory entries.
func (h *Hub) directorySources(ctx context.Context) ([]DirectoryEntry, error) {
	rows, err := h.DB.QueryContext(ctx, `SELECT src_id, name, created_at FROM sources WHERE disabled = 0 ORDER BY src_id`)
	if err != nil {
		return nil, fmt.Errorf("hub: directory sources: %w", err)
	}
	defer rows.Close()
	var out []DirectoryEntry
	for rows.Next() {
		e := DirectoryEntry{
			Kind: kindSource, Capabilities: []string{}, Quota: map[string]any{},
			Presence: "unknown", Reachability: "unknown", Teams: []string{},
		}
		var created string
		if err := rows.Scan(&e.PeerID, &e.Name, &created); err != nil {
			return nil, fmt.Errorf("hub: directory sources: %w", err)
		}
		e.EnrolledAt = &created
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: directory sources: %w", err)
	}
	return out, nil
}

// sourceRow is the part of a sources row ingestion needs.
type sourceRow struct {
	id, kind, target, priority, sealed string
	allow                              []string
}

// loadSource reads srcID; unknown is 404 not_found and disabled 403
// forbidden.
func (h *Hub) loadSource(ctx context.Context, srcID string) (*sourceRow, error) {
	s := &sourceRow{id: srcID}
	var allow sql.NullString
	var disabled int
	err := h.DB.QueryRowContext(ctx,
		`SELECT kind, target_id, priority, secret_enc, event_allow, disabled FROM sources WHERE src_id = ?`, srcID).
		Scan(&s.kind, &s.target, &s.priority, &s.sealed, &allow, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, unknownSource(srcID)
	}
	if err != nil {
		return nil, fmt.Errorf("hub: load source: %w", err)
	}
	if disabled != 0 {
		e := E(http.StatusForbidden, "forbidden", "this source is disabled")
		e.Details["source_id"] = srcID
		return nil, e
	}
	if s.allow, err = decodeEventAllow(allow); err != nil {
		return nil, err
	}
	return s, nil
}

// SourceSecret returns srcID's kind and verification secret, for the
// ingress handler; the secret must never reach a response or a log. Unknown
// is 404 not_found and disabled 403 forbidden.
func (h *Hub) SourceSecret(ctx context.Context, srcID string) (kind, secret string, err error) {
	s, err := h.loadSource(ctx, srcID)
	if err != nil {
		return "", "", err
	}
	plain, err := h.Unseal(s.sealed)
	if err != nil {
		return "", "", fmt.Errorf("hub: source %s secret: %w", srcID, err)
	}
	return s.kind, string(plain), nil
}

// allows reports whether event passes the source's allowlist. An entry
// matches the whole name ("issue_comment.created") or its event
// ("issue_comment"); an empty list allows everything.
func (s *sourceRow) allows(event string) bool {
	if len(s.allow) == 0 {
		return true
	}
	base, _, _ := strings.Cut(event, ".")
	for _, a := range s.allow {
		if a == event || a == base {
			return true
		}
	}
	return false
}

// IngestSource turns one verified event into a Message from the source
// (PROTOCOL §10). A provider delivery id already accepted returns its
// original message id and creates nothing. An event outside the allowlist
// returns dropped true. Otherwise the raw payload is stored as a hosted
// attachment and the Message goes to the source's target in the thread of
// (source, OriginThreadID), which the first event creates. A source over its
// own limits gets 429 rate_limited (PROTOCOL §16.2), and an event over a
// byte cap of §2.2 step 9 gets 413 payload_too_large. The caller kicks the
// push worker.
func (h *Hub) IngestSource(ctx context.Context, m SourceMessage) (msgID string, dropped bool, err error) {
	src, err := h.loadSource(ctx, m.SrcID)
	if err != nil {
		return "", false, err
	}
	if m.ProviderDeliveryID == "" {
		return "", false, invalidValue("provider_delivery_id", "a provider delivery id is required")
	}
	if !src.allows(m.EventName) {
		return "", true, nil
	}
	req := &envelope.SendRequest{
		ThreadTitle: m.Title,
		Priority:    src.priority,
		Content:     envelope.SendContent{Text: m.Summary},
		Origin:      &envelope.Origin{System: src.kind, ThreadID: m.OriginThreadID, EventID: m.OriginEventID},
		Metadata: map[string]any{
			"vecnet.source_kind": src.kind,
			"vecnet.event":       m.EventName,
		},
	}
	// The payload chooses EventName, and this request skips Send's checks.
	raw, err := json.Marshal(req)
	if err != nil {
		return "", false, fmt.Errorf("hub: encode source message: %w", err)
	}
	if err := checkPayloadCaps(raw); err != nil {
		return "", false, err
	}

	// Answer redeliveries and rate-limited events before storing a blob they
	// would leave behind. sendTx repeats both checks in the transaction that
	// counts: this read may be stale by the time it runs.
	var prev string
	var cut *APIError
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var e error
		if prev, e = sourceDelivered(ctx, tx, src.id, m.ProviderDeliveryID); e != nil || prev != "" {
			return e
		}
		cut, e = h.sourceRateCut(ctx, tx, src.id, m.OriginThreadID, h.Now())
		return e
	})
	if err != nil {
		return "", false, err
	}
	if prev != "" {
		return prev, false, nil
	}
	if cut != nil {
		return "", false, cut
	}

	media := m.MediaType
	if media == "" {
		media = "application/json"
	}
	meta, err := h.putBlobAs(ctx, src.id, bytes.NewReader(m.Raw), media, 0)
	if err != nil {
		return "", false, err
	}
	req.Attachments = []envelope.Attachment{{
		SHA256: meta.SHA256, Size: meta.Size, MediaType: media, Name: src.kind + "-event.json",
	}}
	resp, _, err := h.sendAs(ctx, actor{src: &sourceActor{
		id: src.id, target: src.target, providerID: m.ProviderDeliveryID, origin: m.OriginThreadID,
	}}, "inbound_webhook", req, "")
	if err != nil {
		return "", false, err
	}
	return resp.MessageID, false, nil
}

// sourceDelivered returns the message id recorded for (srcID, providerID),
// or "".
func sourceDelivered(ctx context.Context, tx *sql.Tx, srcID, providerID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT msg_id FROM source_deliveries WHERE src_id = ? AND provider_id = ?`,
		srcID, providerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hub: source delivery lookup: %w", err)
	}
	return id, nil
}

// sourceThread returns the thread of (srcID, origin), or "" before the
// first event of that conversation.
func sourceThread(ctx context.Context, tx *sql.Tx, srcID, origin string) (string, error) {
	var thr string
	err := tx.QueryRowContext(ctx, `SELECT thr_id FROM source_threads WHERE src_id = ? AND origin_thread_id = ?`,
		srcID, origin).Scan(&thr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hub: source thread lookup: %w", err)
	}
	return thr, nil
}

// sourceRateCut enforces the per-source limits of PROTOCOL §16.2 for an
// event of conversation origin: a non-nil result is the 429 rate_limited to
// return once tx, holding its counter and audit row, commits.
func (h *Hub) sourceRateCut(ctx context.Context, tx *sql.Tx, srcID, origin string, now time.Time) (*APIError, error) {
	var perMin, perHour int
	if err := tx.QueryRowContext(ctx, `SELECT sends_per_min, threads_per_hour FROM sources WHERE src_id = ?`, srcID).
		Scan(&perMin, &perHour); err != nil {
		return nil, fmt.Errorf("hub: rate limits of %s: %w", srcID, err)
	}
	thr, err := sourceThread(ctx, tx, srcID, origin)
	if err != nil {
		return nil, err
	}
	e, err := rateVerdict(ctx, tx, "per-source", srcID, perMin, perHour, thr == "", now)
	if err != nil || e == nil {
		return nil, err
	}
	if err := h.countRateCut(ctx, tx, srcID, e); err != nil {
		return nil, err
	}
	return e, nil
}

// sourceSendStart is the source half of the head of sendTx. A redelivery
// returns its original response (only the message id is known). Otherwise
// it returns the sender and a copy of req with the thread and recipients
// the event routes to: the source's thread for this origin when it has one,
// addressed to the source's target; a thread target addresses that thread's
// current participants, and its events keep their own per-origin thread
// (R09). A rate cut is returned as a *cutError.
func (h *Hub) sourceSendStart(ctx context.Context, tx *sql.Tx, src *sourceActor, req *envelope.SendRequest, now time.Time) (sender, *envelope.SendRequest, *SendResponse, error) {
	snd := sender{peer: src.id, kind: kindSource}
	if prev, err := sourceDelivered(ctx, tx, src.id, src.providerID); err != nil {
		return snd, nil, nil, err
	} else if prev != "" {
		return snd, nil, &SendResponse{MessageID: prev}, nil
	}
	if cut, err := h.sourceRateCut(ctx, tx, src.id, src.origin, now); err != nil {
		return snd, nil, nil, err
	} else if cut != nil {
		return snd, nil, nil, &cutError{cut}
	}

	r := *req
	thr, err := sourceThread(ctx, tx, src.id, src.origin)
	if err != nil {
		return snd, nil, nil, err
	}
	if thr != "" {
		r.ThreadID = &thr
	}
	r.Recipients = []string{src.target}
	if ids.Is(ids.Thr, src.target) {
		if r.Recipients, err = threadParticipants(ctx, tx, src.target); err != nil {
			return snd, nil, nil, err
		}
		if len(r.Recipients) == 0 {
			e := E(http.StatusUnprocessableEntity, "no_recipients", "the target thread has no participants")
			e.Details["thread_id"] = src.target
			return snd, nil, nil, e
		}
	}
	return snd, &r, nil, nil
}

// recordSourceSend stores the dedupe row of a sent event and, when the send
// created thr, its mapping, in the send's transaction: a redelivery can
// never see a half-sent event.
func recordSourceSend(ctx context.Context, tx *sql.Tx, src *sourceActor, thr *threadRow, msgID, now string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO source_deliveries(src_id, provider_id, msg_id, at) VALUES (?, ?, ?, ?)`,
		src.id, src.providerID, msgID, now); err != nil {
		return fmt.Errorf("hub: record source delivery: %w", err)
	}
	if !thr.isNew {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO source_threads(src_id, origin_thread_id, thr_id) VALUES (?, ?, ?)`,
		src.id, src.origin, thr.ID); err != nil {
		return fmt.Errorf("hub: record source thread: %w", err)
	}
	return nil
}
