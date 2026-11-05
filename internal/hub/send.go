package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/eventpayload"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/prefs"
)

// SendResponse is the body of a 202 (or replayed 200) POST /v1/messages
// (PROTOCOL §2.2 Response, R04).
type SendResponse struct {
	MessageID  string        `json:"message_id"`
	ThreadID   string        `json:"thread_id"`
	Deliveries []DeliveryRef `json:"deliveries"`
	JobID      string        `json:"job_id,omitempty"`
	Spilled    bool          `json:"spilled"`
}

// DeliveryRef is one entry of SendResponse.deliveries.
type DeliveryRef struct {
	DeliveryID string  `json:"delivery_id"`
	Recipient  string  `json:"recipient"`
	Via        *string `json:"via"`
	State      string  `json:"state"`
}

// IdempotencyWindow is how long an idempotency_key is remembered per sender
// (PROTOCOL §2.2, R03).
const IdempotencyWindow = 24 * time.Hour

// PushAttempts is the attempt budget of a new Delivery (PROTOCOL §3.3).
const PushAttempts = 8

// ingresses are the Delivery.ingress values [SCH].
var ingresses = map[string]bool{
	"http_send": true, "mcp_tool": true, "inbound_webhook": true,
	"connector": true, "operator_ui": true, "hub": true,
}

// deliveryWire is a Delivery as it is appended to the recipient's log and
// pushed: the thread summary and the Message are embedded as pre-serialised
// bytes, so every Delivery of a Message carries byte-identical message JSON.
type deliveryWire struct {
	SchemaVersion int             `json:"schema_version"`
	DeliveryID    string          `json:"delivery_id"`
	Recipient     string          `json:"recipient"`
	Via           *string         `json:"via"`
	AlsoVia       []string        `json:"also_via,omitempty"`
	Attempt       int             `json:"attempt"`
	Ingress       string          `json:"ingress"`
	Thread        json.RawMessage `json:"thread"`
	Message       json.RawMessage `json:"message"`
}

// target is one recipient peer of a send after group fan-out.
type target struct {
	peer    string
	via     *string  // first group that resolved it; nil for a direct address
	alsoVia []string // further groups
}

// sender is the resolved originating actor: a peer, or for a source send
// its src_ id with prn "" and kind "source". Authority must key on
// actor.src, never on this kind: peers.kind accepts "source" too.
type sender struct {
	peer, prn, kind string
	name            string // directory name, snapshotted into Message.sender_info
}

// info is the Message.sender_info snapshot of s (PROTOCOL §2.2 step 12).
func (s sender) info() *envelope.SenderInfo {
	return &envelope.SenderInfo{Name: s.name, Kind: s.kind}
}

// actor is who a send runs as. Exactly one field is set. A credential is
// resolved to its peer inside the send transaction; a source is its own
// sender, joins no thread and answers to its own limits (PROTOCOL §10).
type actor struct {
	caller *auth.Caller
	src    *sourceActor
	// client is the operator CLI label of a credential send, recorded as
	// metadata["vecnet.client"] only when the caller is the operator
	// (PROTOCOL §2.2 step 11). nil for every other send.
	client *envelope.OperatorClient
}

// Send runs the send pipeline of PROTOCOL §2.2 for caller c over the raw
// SendRequest body, inside one transaction. ingress is the Delivery.ingress
// value ("http_send", "operator_ui", "connector", ...). replayed is true
// when the (sender, idempotency_key) pair was already used with the same
// body: the original response is returned and nothing is sent. After commit
// it wakes the recipients' log readers; callers kick the push worker.
func (h *Hub) Send(ctx context.Context, c auth.Caller, raw []byte, ingress string) (resp *SendResponse, replayed bool, err error) {
	return h.SendVia(ctx, c, raw, ingress, nil)
}

// SendVia is Send with the operator CLI label of an X-Vecnet-Client header
// (PROTOCOL §2.2 step 11). client must already be cleaned; it is ignored
// unless c is the operator credential.
func (h *Hub) SendVia(ctx context.Context, c auth.Caller, raw []byte, ingress string, client *envelope.OperatorClient) (resp *SendResponse, replayed bool, err error) {
	if c.Kind != "operator" {
		client = nil
	}
	if !ingresses[ingress] {
		return nil, false, fmt.Errorf("hub: send: unknown ingress %q", ingress)
	}
	// Every later check reads one value per member name; a repeated name
	// would let the struct decoder merge an unchecked earlier value back in.
	if err := checkRepeatedNames(raw); err != nil {
		return nil, false, err
	}
	if err := PrecheckSend(raw); err != nil {
		return nil, false, err
	}
	if err := envelope.Validate("sendRequest", raw); err != nil {
		return nil, false, err
	}
	req, err := decodeSendRequest(raw)
	if err != nil {
		return nil, false, err
	}
	if err := checkKindPayload(req); err != nil {
		return nil, false, err
	}
	for k := range req.Metadata {
		if strings.HasPrefix(k, "vecnet.") {
			e := E(http.StatusUnprocessableEntity, "reserved_metadata", "metadata keys starting with \"vecnet.\" belong to the hub")
			e.Details["key"] = k
			return nil, false, e
		}
	}
	bodySHA, err := canonicalSHA256(raw)
	if err != nil {
		return nil, false, err
	}
	return h.sendAs(ctx, actor{caller: &c, client: client}, ingress, req, bodySHA)
}

// sendAs runs the transactional send of req for act and, after commit,
// wakes the recipients' log readers and fires the operator notification
// webhooks. Send has validated req; IngestSource builds it. bodySHA keys the
// idempotency check of a credential; a source dedupes on its provider
// delivery id instead and passes "".
func (h *Hub) sendAs(ctx context.Context, act actor, ingress string, req *envelope.SendRequest, bodySHA string) (resp *SendResponse, replayed bool, err error) {
	var notify []string
	var cut *APIError
	var pub ActivityEvent // set by a sendTx that stored a message
	// BEGIN IMMEDIATE (store DSN) serialises the idempotency lookup and
	// insert; the (sender, key) primary key is the backstop, and a send that
	// loses on it re-runs once to replay the winner's response. A send that
	// must auto-spill text not yet in the blob store rolls back, stores it
	// (the blob store writes in its own transaction) and re-runs once.
	idemRetried, spillStored := false, false
	for {
		notify, cut, pub = nil, nil, ActivityEvent{}
		err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
			var e error
			resp, replayed, notify, e = h.sendTx(ctx, tx, act, req, bodySHA, ingress, &pub)
			var ce *cutError
			if errors.As(e, &ce) {
				// Commit the cut's counter, audit row and events; the send
				// itself wrote nothing.
				cut = ce.api
				return nil
			}
			return e
		})
		var ns *needSpillError
		if errors.As(err, &ns) && !spillStored {
			spillStored = true
			if err = h.putSpill(ctx, ns.text); err != nil {
				return nil, false, err
			}
			continue
		}
		if err != nil && !idemRetried && isIdempotencyConflict(err) {
			idemRetried = true
			continue
		}
		break
	}
	if err != nil {
		return nil, false, err
	}
	if len(notify) > 0 {
		h.Log.Notify(notify...)
	}
	if cut == nil && !replayed && pub.ThreadID != "" {
		h.Activity.Publish(pub) // committed: the transaction above returned nil
	}
	if act.caller != nil && req.As != "" && req.As != act.caller.PeerID {
		// The send was authorised for req.As: a principal acting for a peer
		// is an inbound call by that peer (PROTOCOL §8, §13.6).
		h.TouchReachability(ctx, req.As, "reachable")
	}
	if cut != nil {
		return nil, false, cut
	}
	if !replayed && resp != nil {
		// Operator notification webhooks (PROTOCOL §5.2, T15): fire and forget.
		h.notifyOperatorDeliveries(ctx, resp.MessageID)
	}
	return resp, replayed, nil
}

// PrecheckSend runs the checks of Send that come before schema validation,
// so a caller that validates a SendRequest itself reaches the same reason
// codes in the same order: a display name in recipients is
// name_not_an_address and an oversize field is payload_too_large, not a
// schema violation (PROTOCOL §2.2).
func PrecheckSend(raw []byte) error {
	if err := checkRecipientNames(raw); err != nil {
		return err
	}
	return checkPayloadCaps(raw)
}

func isIdempotencyConflict(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed: idempotency.")
}

// sendTx is the transactional body of sendAs.
// pub receives the activity feed entry for the stored message; sendAs
// publishes it only after commit.
func (h *Hub) sendTx(ctx context.Context, tx *sql.Tx, act actor, req *envelope.SendRequest, bodySHA, ingress string, pub *ActivityEvent) (*SendResponse, bool, []string, error) {
	snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
	if err != nil {
		return nil, false, nil, err
	}
	nowT := h.Now()
	now := envelope.FormatTime(nowT)

	var snd sender
	var c auth.Caller // zero for a source: it reaches no job_status check
	if act.src != nil {
		var prev *SendResponse
		var err error
		if snd, req, prev, err = h.sourceSendStart(ctx, tx, act.src, req, nowT); err != nil || prev != nil {
			return prev, prev != nil, nil, err
		}
	} else {
		c = *act.caller
		var err error
		if snd, err = resolveSender(ctx, tx, c, req.As); err != nil {
			return nil, false, nil, err
		}

		// Idempotency (PROTOCOL §2.2, R03).
		if prev, ok, err := h.idempotencyLookup(ctx, tx, snd.peer, req.IdempotencyKey, bodySHA, nowT); err != nil || ok {
			return prev, ok, nil, err
		}

		// Per-peer rate limits (PROTOCOL §16.2), before anything is written.
		if apiErr, err := h.checkRate(ctx, tx, snd.peer, req.ThreadID == nil, nowT); err != nil {
			return nil, false, nil, err
		} else if apiErr != nil {
			if err := h.countRateCut(ctx, tx, snd.peer, apiErr); err != nil {
				return nil, false, nil, err
			}
			return nil, false, nil, &cutError{apiErr}
		}
	}
	// Thread: load an existing one, or prepare a new one.
	var thr *threadRow
	if req.ThreadID != nil {
		if thr, err = loadThread(ctx, tx, *req.ThreadID); err != nil {
			return nil, false, nil, err
		}
		if act.src == nil && !thr.isParticipant[snd.peer] && snd.kind != "operator" {
			e := E(http.StatusForbidden, "not_participant", "the sender is not a participant of this thread")
			e.Details["thread_id"] = thr.ID
			return nil, false, nil, e
		}
	}

	// Recipients: as addressed, or the thread's participants minus the sender.
	addressed := req.Recipients
	if addressed == nil {
		for _, p := range thr.participants {
			if p != snd.peer {
				addressed = append(addressed, p)
			}
		}
		if len(addressed) == 0 {
			e := E(http.StatusUnprocessableEntity, "no_recipients", "the thread has no participants besides the sender")
			e.Details["thread_id"] = thr.ID
			return nil, false, nil, e
		}
	}
	targets, err := h.resolveTargets(ctx, tx, snd, addressed)
	if err != nil {
		return nil, false, nil, err
	}
	if len(targets) == 0 {
		return nil, false, nil, E(http.StatusUnprocessableEntity, "no_recipients", "the recipients resolve to no peer besides the sender")
	}

	// Participants: who joins with this send.
	var joiners []string
	if thr == nil {
		thr = &threadRow{
			ID:            ids.New(ids.Thr),
			CreatedBy:     snd.peer,
			HistoryAccess: "from_join",
			isParticipant: map[string]bool{},
		}
		if req.ThreadTitle != "" {
			title := req.ThreadTitle
			thr.Title = &title
		}
		// §16.3: the creator sets both at creation; ignored afterwards.
		if req.HistoryAccess != "" {
			thr.HistoryAccess = req.HistoryAccess
		}
		if req.LockedParticipants != nil {
			thr.Locked = *req.LockedParticipants
		}
		if ids.IsGroup(addressed[0]) {
			g := addressed[0]
			thr.Group = &g
		}
		if b := preferenceNumber(snapshot, "agents.thread_budget"); b > 0 {
			thr.Budget = &b
		}
		thr.isNew = true
	}
	if act.src == nil && !thr.isParticipant[snd.peer] {
		joiners = append(joiners, snd.peer) // a source is never a participant (§10)
		thr.isParticipant[snd.peer] = true
	}
	for _, t := range targets {
		if !thr.isParticipant[t.peer] {
			joiners = append(joiners, t.peer)
			thr.isParticipant[t.peer] = true
		}
	}
	if !thr.isNew && len(joiners) > 0 && thr.Locked && snd.peer != thr.CreatedBy && snd.kind != "operator" {
		e := E(http.StatusForbidden, "participants_locked", "only the thread creator or the operator may add participants")
		e.Details["thread_id"] = thr.ID
		return nil, false, nil, e
	}
	if len(thr.participants)+len(joiners) > MaxParticipants {
		e := E(http.StatusUnprocessableEntity, "thread_full", "a thread holds at most 64 participants")
		e.Details["limit"] = MaxParticipants
		return nil, false, nil, e
	}

	// in_reply_to and the loop parent. A send into an existing thread that
	// names no in_reply_to continues the thread's loop from its latest
	// message (PROTOCOL §6): omitting in_reply_to must not reset depth,
	// expires_at or sender_chain, or two bots could loop forever by
	// leaving it out. Only a new thread starts fresh; the operator resets
	// depth and expiry for its own subtree (§16.1, buildLoop); and every
	// event of a source starts a loop of its own (§10), whatever came before
	// it in the issue's thread.
	var parent *envelope.Message
	if req.InReplyTo != nil {
		m, mThr, found, err := loadMessage(ctx, tx, *req.InReplyTo)
		if err != nil {
			return nil, false, nil, err
		}
		ok := found && mThr == thr.ID
		if found && thr.isNew {
			// A new thread may point at a message in another thread the
			// sender can see (PROTOCOL §7).
			if ok, err = isParticipant(ctx, tx, mThr, snd.peer); err != nil {
				return nil, false, nil, err
			}
		}
		if !ok {
			e := E(http.StatusUnprocessableEntity, "not_in_thread", "in_reply_to is not a message of this thread")
			e.Details["in_reply_to"] = *req.InReplyTo
			return nil, false, nil, e
		}
		if m.Kind == "reaction" {
			e := E(http.StatusUnprocessableEntity, "not_replyable", "reaction messages cannot be replied to")
			e.Details["in_reply_to"] = *req.InReplyTo
			return nil, false, nil, e
		}
		parent = m
	} else if !thr.isNew && act.src == nil {
		if parent, err = latestMessage(ctx, tx, thr.ID); err != nil {
			return nil, false, nil, err
		}
	}

	// Kind-specific checks: jobs (§2.2 Jobs, §5.6) and approvals (§9).
	effect, err := h.prepareKind(ctx, tx, c, snd, thr, req, addressed, nowT)
	if err != nil {
		return nil, false, nil, err
	}
	if effect == nil {
		effect = &kindEffect{}
	}

	// Loop control (PROTOCOL §2.2 step 5, §6, §16.1): depth, max_depth and
	// expiry, then the thread budget. A cut is counted, audited and
	// committed; the send writes nothing else.
	loop, apiErr := h.buildLoopSnapshot(snapshot, parent, snd.peer, snd.kind, req, nowT)
	var cutNotify []string
	if apiErr == nil {
		if apiErr, err = h.checkBudget(ctx, tx, thr, nowT); err != nil {
			return nil, false, nil, err
		}
		if apiErr != nil {
			cutNotify = thr.participants
		}
	}
	if apiErr != nil {
		if err := h.countCut(ctx, tx, CounterLoopCut, snd.peer, []string{thr.ID},
			map[string]any{"reason": apiErr.Code}); err != nil {
			return nil, false, nil, err
		}
		return nil, false, cutNotify, &cutError{apiErr}
	}

	// Build and serialise the Message once. Recipients defaulted from a
	// large thread's participants may exceed $defs/message's 32: every
	// target still gets a Delivery, but the Message lists the first 32.
	msgRecipients := addressed
	if len(msgRecipients) > maxMessageRecipients {
		msgRecipients = msgRecipients[:maxMessageRecipients]
	}
	kind := req.Kind
	if kind == "" {
		kind = "message"
	}
	msg := envelope.Message{
		SchemaVersion:    envelope.SchemaVersion,
		MessageID:        ids.New(ids.Msg),
		ThreadID:         thr.ID,
		InReplyTo:        req.InReplyTo,
		Kind:             kind,
		Sender:           snd.peer,
		SenderInfo:       snd.info(), // the directory entry as of now, stored with the Message
		Recipients:       msgRecipients,
		Priority:         req.Priority,
		CreatedAt:        now,
		Content:          envelope.Content{Text: req.Content.Text},
		Attachments:      stripBlobURLs(req.Attachments),
		Metadata:         req.Metadata,
		Loop:             loop,
		JobRequest:       req.JobRequest,
		JobStatus:        req.JobStatus,
		ApprovalRequest:  req.ApprovalRequest,
		ApprovalResponse: req.ApprovalResponse,
	}
	if ingress == "connector" || act.src != nil {
		msg.Origin = req.Origin // honoured only for connectors (§2.2 step 8); a source's is hub-built (§10)
	}
	if effect.jobRequest {
		// Every Delivery carries the hub-set job id (§2.2 Response, R04).
		if err := setHubMetadata(&msg, JobIDMetadataKey, effect.jobID); err != nil {
			return nil, false, nil, err
		}
	}
	if act.client != nil && c.Kind == "operator" {
		// The operator CLI label (§2.2 step 11): provenance for display.
		if err := setHubMetadata(&msg, envelope.ClientMetadataKey, *act.client); err != nil {
			return nil, false, nil, err
		}
	}
	// Attachments (§2.2 step 7) and auto-spill (step 6). hosted holds every
	// hash whose bytes the hub serves: those are pinned and linked per
	// Delivery.
	hosted, err := h.checkAttachments(ctx, tx, snd, msg.Attachments)
	if err != nil {
		return nil, false, nil, err
	}
	if err := h.applySpill(ctx, tx, &msg, req.Content.Text); err != nil {
		return nil, false, nil, err
	}
	if msg.Content.Truncated {
		hosted[msg.Attachments[len(msg.Attachments)-1].SHA256] = true
	}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		return nil, false, nil, fmt.Errorf("hub: encode message: %w", err)
	}

	// Thread row, sequence, participants.
	if thr.isNew {
		if err := insertThread(ctx, tx, thr, now); err != nil {
			return nil, false, nil, err
		}
	}
	var lastSeq int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM messages WHERE thr_id = ?`, thr.ID).Scan(&lastSeq); err != nil {
		return nil, false, nil, fmt.Errorf("hub: last seq: %w", err)
	}
	if err := addParticipants(ctx, tx, thr.ID, joiners, lastSeq, now); err != nil {
		return nil, false, nil, err
	}
	thr.participants = append(thr.participants, joiners...)
	if err := h.insertMessage(ctx, tx, thr, &msg, msgJSON, effect.jobID, effect.approvalID, true); err != nil {
		return nil, false, nil, err
	}
	if effect.apply != nil {
		if err := effect.apply(ctx, tx, msg.MessageID, now); err != nil {
			return nil, false, nil, err
		}
	}
	for sha := range hosted {
		if err := h.Blobs.Pin(ctx, tx, sha, msg.MessageID); err != nil {
			return nil, false, nil, err
		}
	}
	for _, r := range addressed {
		if err := touchGroup(ctx, tx, r, now); err != nil {
			return nil, false, nil, err
		}
	}

	// One Delivery per recipient peer, each appended to that peer's log.
	refs, notify, err := h.fanOut(ctx, tx, thr, &msg, msgJSON, targets, hosted, ingress, msg.Loop.ExpiresAt, nowT, snapshot)
	if err != nil {
		return nil, false, nil, err
	}
	resp := &SendResponse{MessageID: msg.MessageID, ThreadID: thr.ID, Deliveries: refs, Spilled: msg.Content.Truncated}
	if effect.jobRequest {
		resp.JobID = effect.jobID
	}

	respJSON, err := json.Marshal(resp)
	if err != nil {
		return nil, false, nil, fmt.Errorf("hub: encode response: %w", err)
	}
	if act.src != nil {
		if err := recordSourceSend(ctx, tx, act.src, thr, msg.MessageID, now); err != nil {
			return nil, false, nil, err
		}
	} else if _, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency(sender, key, body_sha256, response, created_at) VALUES (?, ?, ?, ?, ?)`,
		snd.peer, req.IdempotencyKey, bodySHA, string(respJSON), now); err != nil {
		return nil, false, nil, fmt.Errorf("hub: store idempotency: %w", err)
	}
	if err := audit.Record(ctx, tx, snd.peer, "send", []string{msg.MessageID, thr.ID},
		map[string]any{"ingress": ingress, "deliveries": len(targets), "new_thread": thr.isNew}); err != nil {
		return nil, false, nil, err
	}
	if snd.kind == "operator" {
		operators, err := h.noteOperatorView(ctx, tx, "sent", thr.ID)
		if err != nil {
			return nil, false, nil, err
		}
		notify = append(notify, operators...)
	}
	*pub = messageActivity(thr.ID, msg.MessageID, msg.Sender, msg.Kind, msg.CreatedAt)
	return resp, false, notify, nil
}

// maxMessageRecipients is $defs/message's cap on recipients.
const maxMessageRecipients = 32

// insertMessage stores msg (serialised as msgJSON) as the next message of
// thr, with its job and approval ids ("" for none), and updates the
// thread's message_count, last_message_at and, when countBudget, its
// budget_remaining (hub-authored messages count against no budget, R20).
// thr's counters are updated in place so its summary reflects the message.
func (h *Hub) insertMessage(ctx context.Context, tx *sql.Tx, thr *threadRow, msg *envelope.Message, msgJSON []byte, jobID, aprID string, countBudget bool) error {
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) + 1 FROM messages WHERE thr_id = ?`, thr.ID).Scan(&seq); err != nil {
		return fmt.Errorf("hub: next seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages(msg_id, thr_id, seq, sender, kind, created_at, body, job_id, approval_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.MessageID, thr.ID, seq, msg.Sender, msg.Kind, msg.CreatedAt, string(msgJSON),
		nullIfEmpty(jobID), nullIfEmpty(aprID)); err != nil {
		return fmt.Errorf("hub: insert message: %w", err)
	}
	thr.MessageCount++
	if countBudget && thr.Budget != nil && *thr.Budget > 0 {
		b := *thr.Budget - 1
		thr.Budget = &b
	}
	var budget any
	if thr.Budget != nil {
		budget = *thr.Budget
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE threads SET message_count = ?, budget_remaining = ?, last_message_at = ? WHERE thr_id = ?`,
		thr.MessageCount, budget, msg.CreatedAt, thr.ID); err != nil {
		return fmt.Errorf("hub: update thread: %w", err)
	}
	return nil
}

// fanOut writes one Delivery of msg (stored as msgJSON) per target: a
// delivery Event in the target's log carrying thr's summary, and a
// deliveries row queued for push. hosted holds the attachment hashes the
// hub serves (per-recipient blob_urls); nil for none. dlyExpires is the
// deliveries' expires_at: msg.Loop.ExpiresAt for a sent Message; a
// hub-authored Message passes its own so it stays deliverable without
// raising the thread's loop.expires_at. It returns the response refs and
// the peers to wake.
func (h *Hub) fanOut(ctx context.Context, tx *sql.Tx, thr *threadRow, msg *envelope.Message, msgJSON []byte,
	targets []target, hosted map[string]bool, ingress, dlyExpires string, nowT time.Time, snapshots ...prefs.Snapshot) ([]DeliveryRef, []string, error) {
	var snapshot prefs.Snapshot
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	} else {
		snapshot = h.Preferences.Current()
	}
	now := envelope.FormatTime(nowT)
	// Latch replay immutability in the very transaction publishing references.
	// Disabling new writes later must never unfreeze previously replayable bodies.
	if h.Cfg.DeliveryReferences {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES ('event_payload_refs_enabled', '1')
         ON CONFLICT(key) DO UPDATE SET value = '1' WHERE meta.value != '1'`); err != nil {
			return nil, nil, fmt.Errorf("hub: enable delivery references: %w", err)
		}
	}
	threadJSON, err := json.Marshal(thr.summary())
	if err != nil {
		return nil, nil, fmt.Errorf("hub: encode thread summary: %w", err)
	}
	refs := []DeliveryRef{}
	notify := make([]string, 0, len(targets))
	for _, t := range targets {
		dly := ids.New(ids.Dly)
		// The stored Message bytes, plus per-recipient blob_urls when the
		// hub hosts any attachment.
		dlyMsg := json.RawMessage(msgJSON)
		if len(hosted) > 0 {
			if dlyMsg, err = json.Marshal(h.deliveryBlobURLs(*msg, t.peer, hosted)); err != nil {
				return nil, nil, fmt.Errorf("hub: encode delivery message: %w", err)
			}
		}
		data, err := json.Marshal(deliveryWire{
			SchemaVersion: envelope.SchemaVersion,
			DeliveryID:    dly,
			Recipient:     t.peer,
			Via:           t.via,
			AlsoVia:       t.alsoVia,
			Attempt:       1,
			Ingress:       ingress,
			Thread:        threadJSON,
			Message:       dlyMsg,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("hub: encode delivery: %w", err)
		}
		route, err := routeFor(ctx, tx, t.peer)
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(data) // the complete frozen Delivery, never the reference
		var cur eventlog.Cursor
		if h.Cfg.DeliveryReferences {
			p, err := eventpayload.EncodeDelivery(ctx, tx, msg.MessageID, data)
			if err != nil {
				return nil, nil, err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO events(owner, type, at, data, payload_format, msg_id, snapshot_id)
             VALUES (?, 'delivery', ?, ?, ?, ?, NULLIF(?, ''))`, t.peer, now, string(p.Data), p.Format, p.MsgID, p.SnapshotID)
			if err != nil {
				return nil, nil, fmt.Errorf("hub: append delivery reference: %w", err)
			}
			seq, err := res.LastInsertId()
			if err != nil {
				return nil, nil, fmt.Errorf("hub: delivery reference seq: %w", err)
			}
			cur = eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: seq}
		} else {
			cur, err = h.Log.Append(ctx, tx, t.peer, "delivery", nowT, json.RawMessage(data))
			if err != nil {
				return nil, nil, err
			}
		}
		alsoVia, _ := json.Marshal(append([]string{}, t.alsoVia...))
		gate := delivery{recipient: t.peer, priority: msg.Priority}
		if err := h.gateWakeIdleSnapshot(ctx, tx, &gate, snapshot, nowT); err != nil {
			return nil, nil, err
		}
		held := 0
		if gate.held {
			held = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO deliveries(dly_id, msg_id, thr_id, recipient, via, also_via, priority, state, attempt,
			                        attempts_left, route_id, held, expires_at, created_at, updated_at, event_seq, body_sha256)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dly, msg.MessageID, thr.ID, t.peer, t.via, string(alsoVia), msg.Priority,
			PushAttempts, route, held, dlyExpires, now, now, cur.Seq, hex.EncodeToString(sum[:])); err != nil {
			return nil, nil, fmt.Errorf("hub: insert delivery: %w", err)
		}
		refs = append(refs, DeliveryRef{DeliveryID: dly, Recipient: t.peer, Via: t.via, State: "queued"})
		notify = append(notify, t.peer)
	}
	return refs, notify, nil
}

// resolveSender derives the sending peer from the credential and `as`
// (PROTOCOL §2.2 step 1, §14.3).
func resolveSender(ctx context.Context, tx *sql.Tx, c auth.Caller, as string) (sender, error) {
	forbidden := func() error {
		e := E(http.StatusForbidden, "forbidden", "as must name a peer of the caller's principal")
		e.Details["as"] = as
		return e
	}
	var peer string
	switch c.Kind {
	case "peer":
		if as != "" && as != c.PeerID {
			return sender{}, forbidden()
		}
		peer = c.PeerID
	case "principal", "operator":
		peer = as
		if peer == "" {
			peer = c.PeerID // the operator's or an OAuth principal's peer; else ""
		}
		if peer == "" {
			return sender{}, E(http.StatusUnprocessableEntity, "as_required", "a principal credential must name the sending peer with as")
		}
	default:
		return sender{}, E(http.StatusForbidden, "forbidden", "this credential cannot send")
	}
	s := sender{peer: peer}
	var disabled int
	err := tx.QueryRowContext(ctx, `SELECT prn_id, kind, name, disabled FROM peers WHERE peer_id = ?`, peer).Scan(&s.prn, &s.kind, &s.name, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (disabled != 0 || s.prn != c.PrincipalID)) {
		return sender{}, forbidden()
	}
	if err != nil {
		return sender{}, fmt.Errorf("hub: resolve sender: %w", err)
	}
	return s, nil
}

// idempotencyLookup returns the stored response for (sender, key) when the
// body matches (ok true), 409 idempotency_conflict when it differs, and
// (nil, false, nil) when the key is unused or its row is older than the
// window (the stale row is removed).
func (h *Hub) idempotencyLookup(ctx context.Context, tx *sql.Tx, sender, key, bodySHA string, now time.Time) (*SendResponse, bool, error) {
	var sha, respJSON, created string
	err := tx.QueryRowContext(ctx, `SELECT body_sha256, response, created_at FROM idempotency WHERE sender = ? AND key = ?`,
		sender, key).Scan(&sha, &respJSON, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("hub: idempotency lookup: %w", err)
	}
	if at, perr := envelope.ParseTime(created); perr != nil || now.Sub(at) >= IdempotencyWindow {
		if _, err := tx.ExecContext(ctx, `DELETE FROM idempotency WHERE sender = ? AND key = ?`, sender, key); err != nil {
			return nil, false, fmt.Errorf("hub: idempotency expire: %w", err)
		}
		return nil, false, nil
	}
	if sha != bodySHA {
		e := E(http.StatusConflict, "idempotency_conflict", "idempotency_key was already used with a different body")
		e.Details["idempotency_key"] = key
		return nil, false, e
	}
	var resp SendResponse
	if err := json.Unmarshal([]byte(respJSON), &resp); err != nil {
		return nil, false, fmt.Errorf("hub: decode stored response: %w", err)
	}
	return &resp, true, nil
}

// resolveTargets fans the addressed ids out to recipient peers: one target
// per distinct peer, in address order; via is the first group that reached
// it and alsoVia the rest (PROTOCOL §5.3, R17). The sender is skipped when a
// group reaches it. Every addressed id, and every member a group reaches, is
// checked against the sender's scopes.
func (h *Hub) resolveTargets(ctx context.Context, tx *sql.Tx, snd sender, addressed []string) ([]target, error) {
	var out []target
	index := map[string]int{}
	add := func(peer string, via *string) {
		i, seen := index[peer]
		if !seen {
			index[peer] = len(out)
			out = append(out, target{peer: peer, via: via})
			return
		}
		t := &out[i]
		if via == nil || (t.via != nil && *t.via == *via) {
			return
		}
		for _, g := range t.alsoVia {
			if g == *via {
				return
			}
		}
		t.alsoVia = append(t.alsoVia, *via)
	}
	scopes, err := h.Auth.LoadScopes(ctx, tx, snd.peer)
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, r := range addressed {
		if done[r] {
			continue
		}
		done[r] = true
		switch {
		case ids.Is(ids.Peer, r):
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM peers WHERE peer_id = ?`, r).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, unknownRecipient(r)
			}
			if err != nil {
				return nil, fmt.Errorf("hub: find recipient: %w", err)
			}
			if !scopes.Allows(r) {
				return nil, scopeDenied(r, "")
			}
			add(r, nil)
		case ids.IsGroup(r):
			members, err := groupMembers(ctx, tx, r)
			if err != nil {
				return nil, err
			}
			// A group is checked per member, as if each were addressed
			// directly (PROTOCOL §7, §12): a deny on the group or any member
			// refuses the send; under allow-list scopes each member must be
			// allowed, unless the group itself is.
			if scopes.Denied(r) {
				return nil, scopeDenied(r, "")
			}
			groupAllowed := !scopes.AllowListed() || scopes.Allows(r)
			g := r
			for _, m := range members {
				if m == snd.peer {
					continue
				}
				if scopes.Denied(m) || (!groupAllowed && !scopes.Allows(m)) {
					return nil, scopeDenied(m, r)
				}
				add(m, &g)
			}
		default:
			return nil, unknownRecipient(r)
		}
	}
	return out, nil
}

// scopeDenied is 403 scope_denied for recipient, reached via group when
// group is not "".
func scopeDenied(recipient, group string) *APIError {
	e := E(http.StatusForbidden, "scope_denied", "send scopes forbid this recipient")
	e.Details["recipient"] = recipient
	if group != "" {
		e.Details["via"] = group
	}
	return e
}

func (h *Hub) mustMaySend(ctx context.Context, tx *sql.Tx, from, to string) error {
	ok, err := h.Auth.MaySend(ctx, tx, from, to)
	if err != nil {
		return err
	}
	if !ok {
		e := E(http.StatusForbidden, "scope_denied", "send scopes forbid this recipient")
		e.Details["recipient"] = to
		return e
	}
	return nil
}

// routeFor returns the push route of peer: its own route, else its
// principal's, else nil (pull only).
func routeFor(ctx context.Context, tx *sql.Tx, peer string) (any, error) {
	var route sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT coalesce(p.route_id,
		   (SELECT route_id FROM routes WHERE owner_kind = 'peer' AND owner_id = p.peer_id ORDER BY created_at, route_id LIMIT 1),
		   (SELECT route_id FROM routes WHERE owner_kind = 'principal' AND owner_id = p.prn_id ORDER BY created_at, route_id LIMIT 1))
		 FROM peers p WHERE p.peer_id = ?`, peer).Scan(&route)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, unknownRecipient(peer)
	}
	if err != nil {
		return nil, fmt.Errorf("hub: route for %s: %w", peer, err)
	}
	if !route.Valid {
		return nil, nil
	}
	return route.String, nil
}

// checkRecipientNames rejects a recipient string that is not a vecnet id
// with 422 name_not_an_address (PROTOCOL §2.2 step 2, §14.4), and a source id
// with 404 unknown_recipient (§10). It runs before schema validation, which
// would otherwise report the same strings as pattern violations (400
// invalid_body). Anything it cannot read is left to the schema.
func checkRecipientNames(raw []byte) error {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	var rs []json.RawMessage
	if json.Unmarshal(top["recipients"], &rs) != nil {
		return nil
	}
	for i, r := range rs {
		var s string
		if json.Unmarshal(r, &s) != nil {
			continue
		}
		prefix, _, err := ids.Parse(s)
		if err != nil {
			e := E(http.StatusUnprocessableEntity, "name_not_an_address",
				"recipients are peer_, team_ or grp_ ids; a display name is never an address")
			e.Details["path"] = fmt.Sprintf("/recipients/%d", i)
			return e
		}
		if prefix == ids.Src {
			return unknownRecipient(s) // sources cannot receive messages (§10)
		}
	}
	return nil
}

// pointerEscape escapes one JSON Pointer reference token (RFC 6901).
var pointerEscape = strings.NewReplacer("~", "~0", "/", "~1")

// checkRepeatedNames refuses a body that repeats a member name in any
// object. The byte caps, the schema and the idempotency hash see a repeated
// name's last value, but encoding/json's struct decoder merges an earlier
// object's map entries and fields into the result, so oversized or extra
// metadata could pass the checks and still be stored. A body that is not
// JSON is left to the checks that follow.
func checkRepeatedNames(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func(path string) error
	walk = func(path string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, _ := key.(string)
				member := path + "/" + pointerEscape.Replace(name)
				if seen[name] {
					return &envelope.SchemaError{Path: member, Message: fmt.Sprintf("member name %q is repeated", name)}
				}
				seen[name] = true
				if err := walk(member); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for i := 0; dec.More(); i++ {
				if err := walk(path + "/" + strconv.Itoa(i)); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	var schemaErr *envelope.SchemaError
	if err := walk(""); errors.As(err, &schemaErr) {
		return err
	}
	return nil
}

// decodeSendRequest decodes a schema-valid body. Numbers in free-form
// objects stay json.Number so metadata round-trips without float rounding.
func decodeSendRequest(raw []byte) (*envelope.SendRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var req envelope.SendRequest
	if err := dec.Decode(&req); err != nil {
		return nil, &envelope.SchemaError{Path: "", Message: "invalid body: " + err.Error()}
	}
	return &req, nil
}

// canonicalSHA256 hashes raw after canonical re-encoding (sorted keys, no
// insignificant whitespace, numbers verbatim), so the idempotency check
// compares bodies, not their formatting.
func canonicalSHA256(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", &envelope.SchemaError{Path: "", Message: "invalid JSON: " + err.Error()}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("hub: canonical body: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// stripBlobURLs copies atts without any sender-supplied blob_url: that field
// is hub-set per Delivery (deliveryBlobURLs) and never stored.
func stripBlobURLs(atts []envelope.Attachment) []envelope.Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]envelope.Attachment, len(atts))
	for i, a := range atts {
		a.BlobURL = ""
		out[i] = a
	}
	return out
}

// setHubMetadata adds one hub-owned key to msg's metadata, copying the
// sender's map rather than writing into it; a map already at the schema's
// key cap is payload_too_large.
func setHubMetadata(msg *envelope.Message, key string, value any) error {
	if len(msg.Metadata) >= maxMetadataKeys {
		e := E(http.StatusRequestEntityTooLarge, "payload_too_large", "metadata has no room for the hub-set "+key)
		e.Details["field"] = "metadata"
		e.Details["limit"] = maxMetadataKeys - 1
		return e
	}
	md := make(map[string]any, len(msg.Metadata)+1)
	for k, v := range msg.Metadata {
		md[k] = v
	}
	md[key] = value
	msg.Metadata = md
	return nil
}
