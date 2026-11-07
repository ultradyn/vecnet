package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// History paging limits (PROTOCOL §2.8).
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
	// maxThreadTitleQuery is the longest q substring ListThreads accepts,
	// counted in runes. A longer q is 422 invalid_value.
	maxThreadTitleQuery = 128
)

// visibleSQL is the PROTOCOL §16.3 rule for whether message m of thread t is
// visible to participant row tp: the whole thread under history_access
// 'full', otherwise only messages appended after the participant joined.
// ThreadMessages and CanSeeBlob share it.
const visibleSQL = `(t.history_access = 'full' OR m.seq > tp.joined_after_seq)`

// pageLimit applies the §2.8 limit rule: ≤ 0 means the default, and anything
// above the cap is clamped to it.
func pageLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	}
	return limit
}

func invalidValue(field, msg string) *APIError {
	e := E(http.StatusUnprocessableEntity, "invalid_value", msg)
	e.Details["field"] = field
	return e
}

// ListThreads lists the threads a peer participates in (PROTOCOL §2.8),
// newest activity first (last_message_at desc, then thr_id desc).
//
// participant selects whose threads: "" or "me" is the caller's own peer; a
// principal credential has no peer of its own and must name one of its
// peers (422 as_required for "me", 403 forbidden for another principal's
// peer), except an OAuth principal, whose own peer is its bound dot; the
// operator may name any peer. A peer credential naming another
// peer lists the caller's threads that peer also participates in. group (a
// team_ or grp_ id, "" for any) keeps threads whose ThreadSummary.group it
// is. q, when non-empty, keeps threads whose title contains that substring,
// case-insensitively (ASCII, via SQLite lower); it matches the title only,
// not participants or message text. q longer than maxThreadTitleQuery runes
// is 422 invalid_value. An empty q applies no title filter. A non-zero since
// keeps threads active at or after it. limit ≤ 0 is 50 and is capped at 200.
// pageTok is the next of a previous page; next is "" on the last page.
func (h *Hub) ListThreads(ctx context.Context, c auth.Caller, participant, group, q string, since time.Time, limit int, pageTok string) ([]envelope.ThreadSummary, string, error) {
	limit = pageLimit(limit)
	if group != "" && !ids.IsGroup(group) {
		return nil, "", invalidValue("group", "group must be a team_ or grp_ id")
	}
	if n := utf8.RuneCountInString(q); n > maxThreadTitleQuery {
		return nil, "", invalidValue("q", fmt.Sprintf("q must be at most %d characters", maxThreadTitleQuery))
	}
	var curAt, curID string
	if pageTok != "" {
		var ok bool
		if curAt, curID, ok = decodePageToken(pageTok); !ok {
			return nil, "", invalidValue("cursor", "cursor is not a paging token from this endpoint")
		}
	}
	sinceStr := ""
	if !since.IsZero() {
		sinceStr = envelope.FormatTime(since)
	}
	var out []envelope.ThreadSummary
	next := ""
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		viewer, with, err := threadViewer(ctx, tx, c, participant)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT t.thr_id, t.last_message_at FROM threads t
			 JOIN thread_participants tp ON tp.thr_id = t.thr_id AND tp.peer_id = ?1
			 WHERE (?2 = '' OR EXISTS (SELECT 1 FROM thread_participants w WHERE w.thr_id = t.thr_id AND w.peer_id = ?2))
			   AND (?3 = '' OR t.group_id = ?3)
			   AND (?4 = '' OR t.last_message_at >= ?4)
			   AND (?5 = '' OR t.last_message_at < ?5 OR (t.last_message_at = ?5 AND t.thr_id < ?6))
			   AND (?7 = '' OR instr(lower(t.title), lower(?7)) > 0)
			 ORDER BY t.last_message_at DESC, t.thr_id DESC
			 LIMIT ?8`,
			viewer, with, group, sinceStr, curAt, curID, q, limit+1)
		if err != nil {
			return fmt.Errorf("hub: list threads: %w", err)
		}
		type key struct{ id, at string }
		var keys []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.id, &k.at); err != nil {
				rows.Close()
				return fmt.Errorf("hub: list threads: %w", err)
			}
			keys = append(keys, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: list threads: %w", err)
		}
		if len(keys) > limit {
			keys = keys[:limit]
			last := keys[len(keys)-1]
			next = encodePageToken(last.at, last.id)
		}
		out = make([]envelope.ThreadSummary, 0, len(keys))
		for _, k := range keys {
			t, err := loadThread(ctx, tx, k.id)
			if err != nil {
				return err
			}
			out = append(out, t.summary())
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// threadViewer resolves ListThreads' participant parameter to the peer whose
// threads are listed and an optional co-participant filter.
func threadViewer(ctx context.Context, tx *sql.Tx, c auth.Caller, participant string) (viewer, with string, err error) {
	if participant != "" && participant != "me" && !ids.Is(ids.Peer, participant) {
		return "", "", invalidValue("participant", `participant must be "me" or a peer_ id`)
	}
	me := participant == "" || participant == "me"
	switch c.Kind {
	case "peer":
		if me || participant == c.PeerID {
			return c.PeerID, "", nil
		}
		return c.PeerID, participant, nil
	case "operator":
		if me {
			return c.PeerID, "", nil
		}
		return participant, "", nil
	case "principal":
		if me && c.PeerID != "" {
			return c.PeerID, "", nil // an OAuth principal acts as its bound dot peer
		}
		if me {
			return "", "", E(http.StatusUnprocessableEntity, "as_required",
				"a principal credential must name one of its peers with participant")
		}
		var prn string
		err := tx.QueryRowContext(ctx, `SELECT prn_id FROM peers WHERE peer_id = ?`, participant).Scan(&prn)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("hub: thread viewer: %w", err)
		}
		if err != nil || prn != c.PrincipalID {
			e := E(http.StatusForbidden, "forbidden", "participant must name a peer of the caller's principal")
			e.Details["participant"] = participant
			return "", "", e
		}
		return participant, "", nil
	}
	return "", "", E(http.StatusForbidden, "forbidden", "this credential has no threads")
}

// encodePageToken renders the ListThreads paging token: base64url of
// "<last_message_at> <thr_id>" of the last thread on the page.
func encodePageToken(at, thr string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at + " " + thr))
}

func decodePageToken(tok string) (at, thr string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return "", "", false
	}
	at, thr, found := strings.Cut(string(raw), " ")
	if !found || !ids.Is(ids.Thr, thr) {
		return "", "", false
	}
	if _, err := envelope.ParseTime(at); err != nil {
		return "", "", false
	}
	return at, thr, true
}

// ThreadMessages returns thread thrID's history for late joiners and cursor
// re-sync (PROTOCOL §2.8, §16.3): messages in append order after afterMsgID
// (exclusive; "" is the thread's start), at most limit (≤ 0 is 50, capped at
// 200). nextAfter is the last returned id when more follow, else nil.
//
// An unknown thread is 404 unknown_thread; a caller none of whose peers
// participates is 403 not_participant. The operator reads the whole thread.
// Under history_access from_join a participant sees only messages appended
// after it joined (seq > joined_after_seq); a principal sees what its
// best-placed participating peer sees. An afterMsgID that is not a message
// of this thread is 422 not_in_thread.
func (h *Hub) ThreadMessages(ctx context.Context, c auth.Caller, thrID, afterMsgID string, limit int) ([]envelope.Message, *string, error) {
	limit = pageLimit(limit)
	if afterMsgID != "" && !ids.Is(ids.Msg, afterMsgID) {
		return nil, nil, invalidValue("after", "after must be a msg_ id")
	}
	var out []envelope.Message
	var nextAfter *string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := loadThread(ctx, tx, thrID)
		if err != nil {
			return err
		}
		from, err := historyStart(ctx, tx, c, t)
		if err != nil {
			return err
		}
		if afterMsgID != "" {
			var seq int
			err := tx.QueryRowContext(ctx, `SELECT seq FROM messages WHERE msg_id = ? AND thr_id = ?`, afterMsgID, thrID).Scan(&seq)
			if errors.Is(err, sql.ErrNoRows) {
				e := E(http.StatusUnprocessableEntity, "not_in_thread", "after is not a message of this thread")
				e.Details["after"] = afterMsgID
				return e
			}
			if err != nil {
				return fmt.Errorf("hub: history after: %w", err)
			}
			from = max(from, seq)
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT body FROM messages WHERE thr_id = ? AND seq > ? AND kind != 'reaction' ORDER BY seq LIMIT ?`, thrID, from, limit+1)
		if err != nil {
			return fmt.Errorf("hub: history: %w", err)
		}
		defer rows.Close()
		out = make([]envelope.Message, 0, limit)
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return fmt.Errorf("hub: history: %w", err)
			}
			m, err := decodeStoredMessage(body)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: history: %w", err)
		}
		if len(out) > limit {
			out = out[:limit]
			id := out[limit-1].MessageID
			nextAfter = &id
		}
		if err := rows.Close(); err != nil {
			return err
		}
		msgIDs := make([]string, len(out))
		for i := range out {
			msgIDs[i] = out[i].MessageID
		}
		groups, err := reactionGroups(ctx, tx, msgIDs)
		if err != nil {
			return err
		}
		for i := range out {
			out[i].Reactions = groups[out[i].MessageID]
		}

		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, nextAfter, nil
}

// historyStart returns the seq after which c may read thread t (0 = the
// whole thread), or 403 not_participant.
func historyStart(ctx context.Context, tx *sql.Tx, c auth.Caller, t *threadRow) (int, error) {
	if c.Kind == "operator" {
		return 0, nil
	}
	prn := ""
	if c.Kind == "principal" {
		prn = c.PrincipalID
	}
	var from sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT min(tp.joined_after_seq) FROM thread_participants tp
		 WHERE tp.thr_id = ?1
		   AND tp.peer_id IN (SELECT peer_id FROM peers WHERE (?2 != '' AND peer_id = ?2) OR (?3 != '' AND prn_id = ?3))`,
		t.ID, c.PeerID, prn).Scan(&from)
	if err != nil {
		return 0, fmt.Errorf("hub: history access: %w", err)
	}
	if !from.Valid {
		e := E(http.StatusForbidden, "not_participant", "the caller is not a participant of this thread")
		e.Details["thread_id"] = t.ID
		return 0, e
	}
	if t.HistoryAccess == "full" {
		return 0, nil
	}
	return int(from.Int64), nil
}

// decodeStoredMessage decodes a stored Message body; numbers in free-form
// objects stay json.Number so metadata round-trips without float rounding.
func decodeStoredMessage(body string) (envelope.Message, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.UseNumber()
	var m envelope.Message
	if err := dec.Decode(&m); err != nil {
		return envelope.Message{}, fmt.Errorf("hub: decode stored message: %w", err)
	}
	return m, nil
}

// HostedAttachments returns, for each of msgs that has any, the sha256s of
// its attachments whose bytes the hub hosts: a blob_refs row for that
// message (written only for attachments hosted at send time) and bytes not
// since purged (PROTOCOL §2.8, §11). Each list follows the message's
// attachment order without repeats. A reference-only attachment (origin_url
// and no hosted bytes) is never listed; hosted bytes that also name an
// origin_url are. The stored Messages are not changed.
func (h *Hub) HostedAttachments(ctx context.Context, msgs []envelope.Message) (map[string][]string, error) {
	out := map[string][]string{}
	var ids []any
	for _, m := range msgs {
		if len(m.Attachments) > 0 {
			ids = append(ids, m.MessageID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := h.DB.QueryContext(ctx,
		`SELECT br.msg_id, br.sha256 FROM (SELECT r.msg_id,r.sha256 FROM blob_refs r JOIN blobs b ON b.sha256=r.sha256 WHERE b.purged_at IS NULL UNION SELECT r.msg_id,r.sha256 FROM durable_blob_refs r JOIN durable_blob_payloads p ON p.sha256=r.sha256 AND p.purpose=r.purpose WHERE NOT EXISTS(SELECT 1 FROM blob_purge_tombstones t WHERE t.sha256=r.sha256)) br
		 WHERE br.msg_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if err != nil {
		return nil, fmt.Errorf("hub: hosted attachments: %w", err)
	}
	defer rows.Close()
	hosted := map[[2]string]bool{}
	for rows.Next() {
		var msgID, sha string
		if err := rows.Scan(&msgID, &sha); err != nil {
			return nil, fmt.Errorf("hub: hosted attachments: %w", err)
		}
		hosted[[2]string{msgID, sha}] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: hosted attachments: %w", err)
	}
	for _, m := range msgs {
		for _, a := range m.Attachments {
			k := [2]string{m.MessageID, a.SHA256}
			if hosted[k] {
				out[m.MessageID] = append(out[m.MessageID], a.SHA256)
				delete(hosted, k) // listed once
			}
		}
	}
	return out, nil
}

// CanSeeBlob reports whether peerID may read blob sha through its bearer
// (PROTOCOL §2.7, §16.3): some message referencing sha (blob_refs) must be
// visible to peerID under the ThreadMessages rules. Per-recipient blob_url
// tokens are checked separately and are unaffected by this.
func (h *Hub) CanSeeBlob(ctx context.Context, peerID, sha string) (bool, error) {
	return h.blobVisible(ctx, peerID, "", sha)
}

// blobVisible is CanSeeBlob for peerID, or for any peer of principal prnID
// when prnID is non-empty.
func (h *Hub) blobVisible(ctx context.Context, peerID, prnID, sha string) (bool, error) {
	var n int
	err := h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM (SELECT msg_id,sha256 FROM blob_refs UNION SELECT msg_id,sha256 FROM durable_blob_refs) br
		 JOIN messages m ON m.msg_id = br.msg_id
		 JOIN threads t ON t.thr_id = m.thr_id
		 JOIN thread_participants tp ON tp.thr_id = m.thr_id
		 WHERE br.sha256 = ?1
		   AND tp.peer_id IN (SELECT peer_id FROM peers WHERE (?2 != '' AND peer_id = ?2) OR (?3 != '' AND prn_id = ?3))
		   AND `+visibleSQL,
		sha, peerID, prnID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("hub: blob visibility: %w", err)
	}
	return n > 0, nil
}
