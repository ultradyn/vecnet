package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
)

// receiptStates are the states a client may post (PROTOCOL §2.5); the hub
// alone records accepted, expired and dead_letter.
var receiptStates = map[string]bool{"processed": true, "read": true, "failed": true}

// receiptFrom lists the delivery states a receipt state moves from. read
// never changes the delivery; failed, expired and dead_letter are terminal
// (PROTOCOL §5.1). processed is not terminal in §5.1, so a later failed
// (e.g. a job that fails after processing began) may still move it.
var receiptFrom = map[string]string{
	"processed": `'queued', 'pushed', 'accepted'`,
	"failed":    `'queued', 'pushed', 'accepted', 'processed'`,
}

// MaxReceiptNote is the receipt note cap in characters ($defs/receipt).
const MaxReceiptNote = 1024

// Receipt records the recipient's progress on a Delivery (PROTOCOL §2.5,
// §5.1). state must be processed, read or failed, else 422 invalid_state.
// Only the Delivery's recipient, or a principal (or operator) credential of
// the recipient's principal, may post; anyone else, and an unknown
// delivery, is 403 not_recipient. processed and failed move the delivery's
// state (from a non-terminal state); read leaves it. A receipt Event, by the
// recipient, is appended to the sender's log when the sender is a peer
// (hub-authored and source messages have no log to receive it), and the
// receipt is audited (without its note). A processed or failed receipt that
// moves nothing (the delivery is terminal, or already in that state) is a
// no-op: 204, no Event and no audit row, so a retried POST is harmless and
// the sender never sees, say, expired then processed.
func (h *Hub) Receipt(ctx context.Context, c auth.Caller, deliveryID, state, note string) error {
	if !receiptStates[state] {
		e := E(http.StatusUnprocessableEntity, "invalid_state", "receipt state must be processed, read or failed")
		e.Details["state"] = state
		return e
	}
	if utf8.RuneCountInString(note) > MaxReceiptNote {
		return &envelope.SchemaError{Path: "/note", Message: fmt.Sprintf("note exceeds %d characters", MaxReceiptNote)}
	}
	var sender string
	notify := false
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		notify = false
		var msg, thr, recipient, prn string
		var senderIsPeer bool
		err := tx.QueryRowContext(ctx,
			`SELECT d.msg_id, d.thr_id, d.recipient, p.prn_id, m.sender,
			        EXISTS (SELECT 1 FROM peers s WHERE s.peer_id = m.sender)
			 FROM deliveries d JOIN peers p ON p.peer_id = d.recipient JOIN messages m ON m.msg_id = d.msg_id
			 WHERE d.dly_id = ?`, deliveryID).Scan(&msg, &thr, &recipient, &prn, &sender, &senderIsPeer)
		if errors.Is(err, sql.ErrNoRows) {
			return notRecipient(deliveryID)
		}
		if err != nil {
			return fmt.Errorf("hub: receipt delivery: %w", err)
		}
		if !mayActFor(c, recipient, prn) {
			return notRecipient(deliveryID)
		}
		nowT := h.Now()
		now := envelope.FormatTime(nowT)
		if from, ok := receiptFrom[state]; ok {
			res, err := tx.ExecContext(ctx,
				`UPDATE deliveries SET state = ?, held = 0, updated_at = ? WHERE dly_id = ? AND state IN (`+from+`)`,
				state, now, deliveryID)
			if err != nil {
				return fmt.Errorf("hub: receipt state: %w", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("hub: receipt state: %w", err)
			}
			if n == 0 {
				return nil // terminal or repeated: nothing to report
			}
		}
		if state == "read" {
			res, err := tx.ExecContext(ctx,
				`UPDATE deliveries SET read_at = ? WHERE dly_id = ? AND read_at IS NULL`, now, deliveryID)
			if err != nil {
				return fmt.Errorf("hub: receipt read: %w", err)
			}
			if n, err := res.RowsAffected(); err != nil {
				return fmt.Errorf("hub: receipt read: %w", err)
			} else if n == 0 {
				return nil // already read: one read receipt per delivery
			}
		}
		if senderIsPeer {
			r := envelope.Receipt{
				DeliveryID: deliveryID, MessageID: msg, ThreadID: thr,
				State: state, By: recipient, At: now, Note: note,
			}
			if _, err := h.Log.Append(ctx, tx, sender, "receipt", nowT, r); err != nil {
				return err
			}
			notify = true
		}
		return audit.Record(ctx, tx, callerID(c), "receipt", []string{deliveryID, msg},
			map[string]any{"state": state, "by": recipient})
	})
	if err != nil {
		return err
	}
	if notify {
		h.Log.Notify(sender)
	}
	return nil
}

// mayActFor reports whether c is the peer itself or a principal/operator
// credential of the peer's principal.
func mayActFor(c auth.Caller, peer, prn string) bool {
	switch c.Kind {
	case "peer":
		return c.PeerID == peer
	case "principal", "operator":
		return c.PrincipalID != "" && c.PrincipalID == prn
	}
	return false
}

func notRecipient(deliveryID string) *APIError {
	e := E(http.StatusForbidden, "not_recipient", "only the delivery's recipient or its principal may post a receipt")
	e.Details["delivery_id"] = deliveryID
	return e
}
