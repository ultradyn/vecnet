package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// ReactResponse is the response of POST /v1/reactions, including no-op retries.
type ReactResponse struct {
	MessageID  string                   `json:"message_id"`
	ThreadID   string                   `json:"thread_id"`
	Emoji      string                   `json:"emoji"`
	Op         string                   `json:"op"`
	Changed    bool                     `json:"changed"`
	Reactions  []envelope.ReactionGroup `json:"reactions"`
	Superseded []string                 `json:"superseded"`
	Wake       *ReactWake               `json:"wake"`
}

// ReactWake identifies a Delivery created by the reaction's optional wake layer.
type ReactWake struct {
	MessageID  string `json:"message_id"`
	DeliveryID string `json:"delivery_id"`
	Recipient  string `json:"recipient"`
	State      string `json:"state"`
}

// React changes a visible target's reaction in one transaction. It publishes
// event-log notifications only after commit; callers kick the push worker.
func (h *Hub) React(ctx context.Context, c auth.Caller, req envelope.ReactionRequest, ingress string) (*ReactResponse, error) {
	emoji, ok := envelope.CanonicalEmoji(req.Emoji)
	if !ok {
		e := E(422, "emoji_not_allowed", "emoji is not in the allowed reaction set")
		e.Details["emoji"] = req.Emoji
		return nil, e
	}
	req.Emoji = emoji
	if req.Op != "add" && req.Op != "remove" {
		return nil, invalidValue("op", "op must be add or remove")
	}
	switch req.Priority {
	case "":
		req.Priority = "hold"
	case "hold", "wake", "wake_idle":
	default:
		return nil, invalidValue("priority", "priority must be wake, wake_idle or hold")
	}
	if !ingresses[ingress] {
		return nil, fmt.Errorf("hub: react: unknown ingress %q", ingress)
	}
	var resp *ReactResponse
	var owners []string
	var cut *APIError
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var e error
		resp, owners, e = h.reactTx(ctx, tx, c, req, ingress)
		var ce *cutError
		if errors.As(e, &ce) {
			cut = ce.api
			return nil
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	if len(owners) > 0 {
		h.Log.Notify(owners...)
	}
	if cut != nil {
		return nil, cut
	}
	if resp != nil && resp.Wake != nil {
		h.notifyOperatorDeliveries(ctx, resp.Wake.MessageID)
	}
	return resp, nil
}

func (h *Hub) reactTx(ctx context.Context, tx *sql.Tx, c auth.Caller, req envelope.ReactionRequest, ingress string) (*ReactResponse, []string, error) {
	snd, err := resolveSender(ctx, tx, c, req.As)
	if err != nil {
		return nil, nil, err
	}
	msg, thr, seq, found, err := loadTarget(ctx, tx, req.MessageID)
	if err != nil {
		return nil, nil, err
	}
	visible := false
	if found {
		visible, err = messageVisible(ctx, tx, snd, thr, seq)
		if err != nil {
			return nil, nil, err
		}
	}
	if !visible {
		e := E(404, "not_found", "message not found")
		e.Details["message_id"] = req.MessageID
		return nil, nil, e
	}
	if msg.Kind == "reaction" {
		return nil, nil, E(422, "not_replyable", "reaction messages cannot be reacted to")
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM reactions WHERE msg_id=? AND peer_id=? AND emoji=?`, req.MessageID, snd.peer, req.Emoji).Scan(&one)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	changed := (req.Op == "add") != (err == nil)
	resp := &ReactResponse{MessageID: req.MessageID, ThreadID: thr, Emoji: req.Emoji, Op: req.Op, Changed: changed, Reactions: []envelope.ReactionGroup{}, Superseded: []string{}}
	var owners []string
	if changed {
		now := h.Now()
		api, err := h.checkRate(ctx, tx, snd.peer, false, now)
		if err != nil {
			return nil, nil, err
		}
		if api != nil {
			if err := h.countRateCut(ctx, tx, snd.peer, api); err != nil {
				return nil, nil, err
			}
			return nil, nil, &cutError{api}
		}
		// Eligibility, scopes and loop refusals precede every reaction write.
		var targets []target
		var loop envelope.Loop
		if req.Op == "add" && req.Priority != "hold" && ids.Is(ids.Peer, msg.Sender) && msg.Sender != snd.peer {
			eligible := true
			if snd.kind != "operator" {
				if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM reaction_wakes WHERE msg_id=? AND peer_id=? AND emoji=?)`, req.MessageID, snd.peer, req.Emoji).Scan(&eligible); err != nil {
					return nil, nil, err
				}
			}
			if eligible {
				targets, err = h.resolveTargets(ctx, tx, snd, []string{msg.Sender})
				if err != nil {
					return nil, nil, err
				}
				var apiErr *APIError
				loop, apiErr = h.buildLoop(msg, snd.peer, snd.kind, &envelope.SendRequest{}, now)
				if apiErr != nil {
					if err := h.countCut(ctx, tx, CounterLoopCut, snd.peer, []string{thr}, map[string]any{"reason": apiErr.Code, "via": "reaction"}); err != nil {
						return nil, nil, err
					}
					return nil, nil, &cutError{apiErr}
				}
			}
		}
		at := envelope.FormatTime(now)
		if req.Op == "add" {
			_, err = tx.ExecContext(ctx, `INSERT INTO reactions(msg_id,peer_id,emoji,created_at) VALUES(?,?,?,?)`, req.MessageID, snd.peer, req.Emoji, at)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM reactions WHERE msg_id=? AND peer_id=? AND emoji=?`, req.MessageID, snd.peer, req.Emoji)
		}
		if err != nil {
			return nil, nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO reaction_ops(peer_id,at) VALUES(?,?)`, snd.peer, at); err != nil {
			return nil, nil, err
		}
		if req.Op == "remove" || len(targets) > 0 {
			targetID, emoji := "", ""
			if req.Op == "remove" {
				targetID, emoji = req.MessageID, req.Emoji
			}
			resp.Superseded, err = supersedeReactionDeliveries(ctx, tx, msg.Sender, snd.peer, thr, at, targetID, emoji)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(targets) > 0 {
			thread, err := loadThread(ctx, tx, thr)
			if err != nil {
				return nil, nil, err
			}
			wake := envelope.Message{
				SchemaVersion: envelope.SchemaVersion, MessageID: ids.New(ids.Msg), ThreadID: thr,
				Kind: "reaction", InReplyTo: &req.MessageID, Sender: snd.peer,
				Recipients: []string{msg.Sender}, Priority: req.Priority, CreatedAt: at,
				Content: envelope.Content{Text: req.Emoji}, Reaction: &envelope.ReactionRef{Target: req.MessageID, Emoji: req.Emoji},
				Attachments: []envelope.Attachment{}, Metadata: map[string]any{}, Loop: loop,
			}
			msgJSON, err := json.Marshal(wake)
			if err != nil {
				return nil, nil, err
			}
			if err := insertReactionMessage(ctx, tx, thread, &wake, msgJSON); err != nil {
				return nil, nil, err
			}
			if snd.kind != "operator" {
				if _, err := tx.ExecContext(ctx, `INSERT INTO reaction_wakes(msg_id,peer_id,emoji,created_at) VALUES(?,?,?,?)`, req.MessageID, snd.peer, req.Emoji, at); err != nil {
					return nil, nil, err
				}
			}
			refs, notify, err := h.fanOut(ctx, tx, thread, &wake, msgJSON, targets, nil, ingress, wake.Loop.ExpiresAt, now)
			if err != nil {
				return nil, nil, err
			}
			owners = append(owners, notify...)
			resp.Wake = &ReactWake{MessageID: wake.MessageID, DeliveryID: refs[0].DeliveryID, Recipient: refs[0].Recipient, State: refs[0].State}
		}
		audience, err := reactionAudience(ctx, tx, thr, seq, snd.peer)
		if err != nil {
			return nil, nil, err
		}
		owners = append(owners, audience...)
		data := envelope.ReactionEvent{MessageID: req.MessageID, ThreadID: thr, PeerID: snd.peer, Emoji: req.Emoji, Op: req.Op, At: at}
		except := make(map[string]bool, len(owners))
		for _, peer := range audience {
			if _, err = h.Log.Append(ctx, tx, peer, "reaction", now, data); err != nil {
				return nil, nil, err
			}
			except[peer] = true
		}
		operators, err := h.noteOperatorView(ctx, tx, "reaction", thr, except)
		if err != nil {
			return nil, nil, err
		}
		owners = append(owners, operators...)
		if err = audit.Record(ctx, tx, snd.peer, "react", []string{req.MessageID, thr}, map[string]any{"emoji": req.Emoji, "op": req.Op, "priority": req.Priority}); err != nil {
			return nil, nil, err
		}
	}
	groups, err := reactionGroups(ctx, tx, []string{req.MessageID})
	if err != nil {
		return nil, nil, err
	}
	if g := groups[req.MessageID]; len(g) > 0 {
		resp.Reactions = g
	}
	return resp, owners, nil
}

func loadTarget(ctx context.Context, tx *sql.Tx, msgID string) (msg *envelope.Message, thr string, seq int, found bool, err error) {
	var body string
	err = tx.QueryRowContext(ctx, `SELECT thr_id,seq,body FROM messages WHERE msg_id=?`, msgID).Scan(&thr, &seq, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", 0, false, nil
	}
	if err != nil {
		return nil, "", 0, false, err
	}
	m, err := decodeStoredMessage(body)
	if err != nil {
		return nil, "", 0, false, err
	}
	return &m, thr, seq, true, nil
}

func messageVisible(ctx context.Context, tx *sql.Tx, snd sender, thr string, seq int) (bool, error) {
	if snd.kind == "operator" {
		return true, nil
	}
	var visible bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM thread_participants tp JOIN threads t ON t.thr_id=tp.thr_id WHERE tp.thr_id=? AND tp.peer_id=? AND (t.history_access='full' OR tp.joined_after_seq < ?))`, thr, snd.peer, seq).Scan(&visible)
	return visible, err
}

func reactionAudience(ctx context.Context, tx *sql.Tx, thr string, seq int, reactor string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tp.peer_id AS peer_id FROM thread_participants tp JOIN threads t ON t.thr_id=tp.thr_id JOIN peers p ON p.peer_id=tp.peer_id WHERE tp.thr_id=? AND (p.kind='operator' OR t.history_access='full' OR tp.joined_after_seq < ?) UNION SELECT ? ORDER BY peer_id`, thr, seq, reactor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			return nil, err
		}
		out = append(out, peer)
	}
	return out, rows.Err()
}

// reactionGroups loads an entire history page in one query. Emoji follow the
// protocol table; peer order is reaction timestamp followed by peer id.
func reactionGroups(ctx context.Context, tx *sql.Tx, msgIDs []string) (map[string][]envelope.ReactionGroup, error) {
	out := make(map[string][]envelope.ReactionGroup)
	if len(msgIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(msgIDs))
	for i, id := range msgIDs {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, `SELECT msg_id,emoji,peer_id FROM reactions WHERE msg_id IN (?`+strings.Repeat(",?", len(args)-1)+`) ORDER BY created_at,peer_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]map[string][]string{}
	for rows.Next() {
		var id, emoji, peer string
		if err := rows.Scan(&id, &emoji, &peer); err != nil {
			return nil, err
		}
		if groups[id] == nil {
			groups[id] = map[string][]string{}
		}
		groups[id][emoji] = append(groups[id][emoji], peer)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id, byEmoji := range groups {
		for _, emoji := range envelope.ReactionEmoji {
			if peers := byEmoji[emoji]; len(peers) > 0 {
				out[id] = append(out[id], envelope.ReactionGroup{Emoji: emoji, Peers: peers})
			}
		}
	}
	return out, nil
}

// insertReactionMessage assigns a sequence without changing activity or budget.
func insertReactionMessage(ctx context.Context, tx *sql.Tx, thr *threadRow, msg *envelope.Message, msgJSON []byte) error {
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0)+1 FROM messages WHERE thr_id=?`, thr.ID).Scan(&seq); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES(?,?,?,?,?,?,?)`, msg.MessageID, thr.ID, seq, msg.Sender, msg.Kind, msg.CreatedAt, string(msgJSON))
	return err
}

// supersedeReactionDeliveries withdraws only queued wakes. A nonempty target
// narrows a remove to that emoji and target; a new wake coalesces the thread.
func supersedeReactionDeliveries(ctx context.Context, tx *sql.Tx, recipient, reactor, thr, at, targetID, emoji string) ([]string, error) {
	query := `UPDATE deliveries SET state='superseded',held=0,next_attempt_at=NULL,updated_at=?
 WHERE recipient=? AND thr_id=? AND state='queued'
 AND msg_id IN (SELECT msg_id FROM messages WHERE kind='reaction' AND sender=? AND thr_id=?`
	args := []any{at, recipient, thr, reactor, thr}
	if targetID != "" {
		query += ` AND json_extract(body,'$.reaction.target')=? AND json_extract(body,'$.reaction.emoji')=?`
		args = append(args, targetID, emoji)
	}
	query += `) RETURNING dly_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
