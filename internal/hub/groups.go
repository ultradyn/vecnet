package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/XertroV/vecnet/internal/ids"
)

// groupMembers resolves a team_ or grp_ id to its member peers (sorted by
// peer id) inside tx. An unknown id is 404 unknown_recipient; an archived ad
// hoc group is 410 group_archived (PROTOCOL §7, R21). The groups API itself
// is Task 15.
func groupMembers(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	var members string
	switch {
	case ids.Is(ids.Team, id):
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE team_id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, unknownRecipient(id)
		}
		if err != nil {
			return nil, fmt.Errorf("hub: find team: %w", err)
		}
		members = `SELECT peer_id FROM team_members WHERE team_id = ? ORDER BY peer_id`
	case ids.Is(ids.Grp, id):
		var archived sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT archived_at FROM groups WHERE grp_id = ?`, id).Scan(&archived)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, unknownRecipient(id)
		}
		if err != nil {
			return nil, fmt.Errorf("hub: find group: %w", err)
		}
		if archived.Valid {
			e := E(http.StatusGone, "group_archived", "the group is archived; the operator can unarchive it")
			e.Details["recipient"] = id
			return nil, e
		}
		members = `SELECT peer_id FROM group_members WHERE grp_id = ? ORDER BY peer_id`
	default:
		return nil, unknownRecipient(id)
	}
	rows, err := tx.QueryContext(ctx, members, id)
	if err != nil {
		return nil, fmt.Errorf("hub: group members: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("hub: group members: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: group members: %w", err)
	}
	return out, nil
}

// touchGroup records activity on an ad hoc group for the 30-day idle
// auto-archive (PROTOCOL §7). Teams never archive.
func touchGroup(ctx context.Context, tx *sql.Tx, id, now string) error {
	if !ids.Is(ids.Grp, id) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE groups SET last_active_at = ? WHERE grp_id = ?`, now, id); err != nil {
		return fmt.Errorf("hub: touch group: %w", err)
	}
	return nil
}

func unknownRecipient(id string) *APIError {
	e := E(http.StatusNotFound, "unknown_recipient", "recipient is not an enrolled peer, team or group")
	e.Details["recipient"] = id
	return e
}
