package hub

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
)

// ErrPeerDisabled reports a stream narrowed to a disabled peer; GET
// /v1/events answers it with 204 so the client stops reconnecting
// (PROTOCOL §2.3).
var ErrPeerDisabled = errors.New("hub: peer disabled")

// StreamOwners resolves the event-log owners a caller reads and the
// (owner, scope) its acked cursor lives under (PROTOCOL §2.3, R05):
//
//   - peer credential: [peer], (peer, "*"); peerFilter must be "" or the peer;
//   - principal or operator credential: all its peers, (prn, "*");
//   - principal or operator with peerFilter: [peer], (prn, peer), where peer
//     must belong to the principal.
//
// A filter naming another principal's peer (or no peer) is 403
// not_your_peer; a narrowed stream to a disabled peer is ErrPeerDisabled.
func (h *Hub) StreamOwners(ctx context.Context, c auth.Caller, peerFilter string) (owners []string, ackOwner, ackScope string, err error) {
	notYours := func() error {
		e := E(http.StatusForbidden, "not_your_peer", "peer names a peer the credential does not own")
		e.Details["peer"] = peerFilter
		return e
	}
	switch c.Kind {
	case "peer":
		if peerFilter != "" && peerFilter != c.PeerID {
			return nil, "", "", notYours()
		}
		return []string{c.PeerID}, c.PeerID, "*", nil
	case "principal", "operator":
	default:
		return nil, "", "", E(http.StatusForbidden, "forbidden", "this credential has no event stream")
	}
	if peerFilter != "" {
		var prn string
		var disabled int
		err := h.DB.QueryRowContext(ctx, `SELECT prn_id, disabled FROM peers WHERE peer_id = ?`, peerFilter).Scan(&prn, &disabled)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && prn != c.PrincipalID) {
			return nil, "", "", notYours()
		}
		if err != nil {
			return nil, "", "", fmt.Errorf("hub: stream peer: %w", err)
		}
		if disabled != 0 {
			return nil, "", "", ErrPeerDisabled
		}
		return []string{peerFilter}, c.PrincipalID, peerFilter, nil
	}
	rows, err := h.DB.QueryContext(ctx, `SELECT peer_id FROM peers WHERE prn_id = ? ORDER BY peer_id`, c.PrincipalID)
	if err != nil {
		return nil, "", "", fmt.Errorf("hub: stream peers: %w", err)
	}
	defer rows.Close()
	owners = []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, "", "", fmt.Errorf("hub: stream peers: %w", err)
		}
		owners = append(owners, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", "", fmt.Errorf("hub: stream peers: %w", err)
	}
	return owners, c.PrincipalID, "*", nil
}

// Ack acknowledges the caller's stream up to and including cur (PROTOCOL
// §2.4, §5.1) in one transaction: the acked cursor of (ackOwner, ackScope)
// advances; every delivery of the stream's owners at or before cur that is
// still queued or pushed becomes accepted (held cleared), and each such
// change appends an accepted receipt Event, by the recipient, to the
// sender's log (none for a hub-authored message, whose sender is no peer). A cursor past the newest event is 422 invalid_value; one
// from another epoch is eventlog.ErrCursorPurged. Acking the same cursor
// twice changes nothing the second time.
func (h *Hub) Ack(ctx context.Context, c auth.Caller, peerFilter string, cur eventlog.Cursor) error {
	owners, ackOwner, ackScope, err := h.StreamOwners(ctx, c, peerFilter)
	if errors.Is(err, ErrPeerDisabled) {
		return E(http.StatusForbidden, "forbidden", "the peer is disabled")
	}
	if err != nil {
		return err
	}
	var notify []string
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		notify = nil
		if cur.Epoch == h.Log.Epoch() {
			var high int64
			if err := tx.QueryRowContext(ctx,
				`SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&high); err != nil {
				return fmt.Errorf("hub: ack high-water: %w", err)
			}
			if cur.Seq > high {
				e := E(http.StatusUnprocessableEntity, "invalid_value", "cursor is past the newest event")
				e.Details["field"] = "cursor"
				return e
			}
		}
		if err := h.Log.Ack(ctx, tx, ackOwner, ackScope, cur); err != nil {
			return err
		}
		accepted, err := acceptDeliveries(ctx, tx, owners, cur.Seq, h.nowStr())
		if err != nil {
			return err
		}
		nowT := h.Now()
		at := envelope.FormatTime(nowT)
		for _, d := range accepted {
			if !d.senderIsPeer {
				continue // a hub-authored message: no peer log to receive it
			}
			r := envelope.Receipt{
				DeliveryID: d.dly, MessageID: d.msg, ThreadID: d.thr,
				State: "accepted", By: d.recipient, At: at,
			}
			if _, err := h.Log.Append(ctx, tx, d.sender, "receipt", nowT, r); err != nil {
				return err
			}
			notify = append(notify, d.sender)
		}
		return audit.Record(ctx, tx, callerID(c), "ack", []string{ackOwner},
			map[string]any{"scope": ackScope, "cursor": cur.String(), "accepted": len(accepted)})
	})
	if err != nil {
		return err
	}
	if len(notify) > 0 {
		h.Log.Notify(notify...)
	}
	return nil
}

// acceptedDelivery is one delivery an ack moved to accepted.
type acceptedDelivery struct {
	dly, msg, thr, recipient, sender string
	senderIsPeer                     bool // false for hub-authored messages
}

// acceptDeliveries moves the owners' queued or pushed deliveries with
// event_seq <= seq to accepted and returns them with their senders.
func acceptDeliveries(ctx context.Context, tx *sql.Tx, owners []string, seq int64, now string) ([]acceptedDelivery, error) {
	if len(owners) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(owners)+2)
	args = append(args, now)
	for _, o := range owners {
		args = append(args, o)
	}
	args = append(args, seq)
	rows, err := tx.QueryContext(ctx,
		`UPDATE deliveries SET state = 'accepted', updated_at = ?, held = 0
		 WHERE recipient IN (`+strings.TrimSuffix(strings.Repeat("?,", len(owners)), ",")+`) AND event_seq <= ? AND state IN ('queued', 'pushed')
		 RETURNING dly_id, msg_id, thr_id, recipient, event_seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("hub: accept deliveries: %w", err)
	}
	type row struct {
		d   acceptedDelivery
		seq int64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.d.dly, &r.d.msg, &r.d.thr, &r.d.recipient, &r.seq); err != nil {
			rows.Close()
			return nil, fmt.Errorf("hub: accept deliveries: %w", err)
		}
		got = append(got, r)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("hub: accept deliveries: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: accept deliveries: %w", err)
	}
	// RETURNING order is unspecified; receipts follow the recipients' log order.
	slices.SortFunc(got, func(a, b row) int {
		if a.seq != b.seq {
			return cmp.Compare(a.seq, b.seq)
		}
		return strings.Compare(a.d.dly, b.d.dly)
	})
	out := make([]acceptedDelivery, len(got))
	for i, r := range got {
		if err := tx.QueryRowContext(ctx,
			`SELECT m.sender, EXISTS (SELECT 1 FROM peers s WHERE s.peer_id = m.sender) FROM messages m WHERE m.msg_id = ?`,
			r.d.msg).Scan(&r.d.sender, &r.d.senderIsPeer); err != nil {
			return nil, fmt.Errorf("hub: delivery sender: %w", err)
		}
		out[i] = r.d
	}
	return out, nil
}

// callerID is the audit actor for c: its peer, else its principal.
func callerID(c auth.Caller) string {
	if c.Kind == "peer" && c.PeerID != "" {
		return c.PeerID
	}
	return c.PrincipalID
}
