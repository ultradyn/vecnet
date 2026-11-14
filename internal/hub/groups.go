package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// GroupIdleTTL is how long an ad hoc group may go without activity before
// ArchiveIdleGroups archives it (PROTOCOL §7).
const GroupIdleTTL = 30 * 24 * time.Hour

// GroupInfo is one entry of GET /v1/groups (PROTOCOL §2.9).
type GroupInfo struct {
	GroupID   string   `json:"group_id"`
	Members   []string `json:"members"`
	CreatedBy string   `json:"created_by"`
	Archived  bool     `json:"archived"`
}

// TeamInfo is one operator-managed team (PROTOCOL §7, §2.12).
type TeamInfo struct {
	TeamID  string   `json:"team_id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// groupMembers resolves a team_ or grp_ id to its member peers (sorted by
// peer id) inside tx. An unknown id is 404 unknown_recipient; an archived ad
// hoc group is 410 group_archived (PROTOCOL §7, R21).
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

// groupActor is the peer that creates or edits an ad hoc group for c,
// resolved inside tx like a sender (PROTOCOL §2.1 "peer / principal"): a
// peer's own id, the operator's peer, or for a principal the peer named by
// as (422 as_required without it; 403 forbidden for a peer outside the
// principal or a disabled one).
func (h *Hub) groupActor(ctx context.Context, tx *sql.Tx, c auth.Caller, as string) (string, error) {
	snd, err := h.resolveSender(ctx, tx, c, as)
	if err != nil {
		return "", err
	}
	return snd.peer, nil
}

// checkMembers validates a members list: every entry a peer_ id (422
// invalid_value) of an enrolled peer (404 unknown_recipient). When actor is
// non-empty, actor's send scopes must allow each member (403 scope_denied,
// PROTOCOL §2.9); actor itself is skipped. It returns the distinct members
// in request order.
func (h *Hub) checkMembers(ctx context.Context, tx *sql.Tx, actor string, members []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for i, m := range members {
		if !ids.Is(ids.Peer, m) {
			e := invalidValue("members", "members are peer_ ids")
			e.Details["path"] = fmt.Sprintf("/members/%d", i)
			return nil, e
		}
		if seen[m] || m == actor {
			continue
		}
		seen[m] = true
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM peers WHERE peer_id = ?`, m).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, unknownRecipient(m)
		}
		if err != nil {
			return nil, fmt.Errorf("hub: find member: %w", err)
		}
		if actor != "" {
			if err := h.mustMaySend(ctx, tx, actor, m); err != nil {
				return nil, err
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// CreateGroup creates an ad hoc group (PROTOCOL §2.9, §7) of the caller's
// peer (for a principal, the peer named by as) plus members, each of which the creator's send scopes must allow
// (403 scope_denied). The creator is always a member. Audited.
func (h *Hub) CreateGroup(ctx context.Context, c auth.Caller, as string, members []string) (string, error) {
	grp := ids.New(ids.Grp)
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		creator, err := h.groupActor(ctx, tx, c, as)
		if err != nil {
			return err
		}
		ms, err := h.checkMembers(ctx, tx, creator, members)
		if err != nil {
			return err
		}
		now := h.nowStr()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO groups(grp_id, created_by, created_at, last_active_at) VALUES (?, ?, ?, ?)`,
			grp, creator, now, now); err != nil {
			return fmt.Errorf("hub: insert group: %w", err)
		}
		if err := insertMembers(ctx, tx, "group_members", "grp_id", grp, append([]string{creator}, ms...)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, creator, "group_create", append([]string{grp}, ms...), nil)
	})
	if err != nil {
		return "", err
	}
	return grp, nil
}

// AddGroupMembers adds members to ad hoc group grpID (PROTOCOL §2.9). The
// caller's peer (for a principal, the peer named by as) must be a member (403 not_member; the operator may edit any
// group) and its send scopes must allow each new member (403 scope_denied).
// An unknown group is 404 unknown_recipient; an archived one is 410
// group_archived. Existing members are ignored. Adding counts as activity
// for the idle auto-archive. Audited.
func (h *Hub) AddGroupMembers(ctx context.Context, c auth.Caller, as, grpID string, members []string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		actor, err := h.groupActor(ctx, tx, c, as)
		if err != nil {
			return err
		}
		var archived sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT archived_at FROM groups WHERE grp_id = ?`, grpID).Scan(&archived)
		if errors.Is(err, sql.ErrNoRows) {
			return unknownRecipient(grpID)
		}
		if err != nil {
			return fmt.Errorf("hub: find group: %w", err)
		}
		if c.Kind != "operator" {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM group_members WHERE grp_id = ? AND peer_id = ?`, grpID, actor).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				e := E(http.StatusForbidden, "not_member", "only a member may add members to a group")
				e.Details["group_id"] = grpID
				return e
			}
			if err != nil {
				return fmt.Errorf("hub: group member check: %w", err)
			}
		}
		if archived.Valid {
			e := E(http.StatusGone, "group_archived", "the group is archived; the operator can unarchive it")
			e.Details["group_id"] = grpID
			return e
		}
		ms, err := h.checkMembers(ctx, tx, actor, members)
		if err != nil {
			return err
		}
		if err := insertMembers(ctx, tx, "group_members", "grp_id", grpID, ms); err != nil {
			return err
		}
		if err := touchGroup(ctx, tx, grpID, h.nowStr()); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, "group_add_members", append([]string{grpID}, ms...), nil)
	})
}

// insertMembers inserts peers into a membership table (group_members or
// team_members), ignoring existing rows.
func insertMembers(ctx context.Context, tx *sql.Tx, table, col, id string, peers []string) error {
	for _, p := range peers {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO `+table+`(`+col+`, peer_id) VALUES (?, ?)`, id, p); err != nil {
			return fmt.Errorf("hub: insert %s: %w", table, err)
		}
	}
	return nil
}

// ListGroups lists the caller's ad hoc groups (PROTOCOL §2.9): those its
// peer belongs to, or for a principal those any of its peers belongs to.
// The operator sees every group (§7). Ordered by creation.
func (h *Hub) ListGroups(ctx context.Context, c auth.Caller) ([]GroupInfo, error) {
	all := c.Kind == "operator"
	prn := ""
	if c.Kind == "principal" {
		prn = c.PrincipalID
	}
	out := []GroupInfo{}
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT g.grp_id, g.created_by, g.archived_at IS NOT NULL FROM groups g
			 WHERE ?1 OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.grp_id = g.grp_id AND gm.peer_id IN
			       (SELECT peer_id FROM peers WHERE (?2 != '' AND peer_id = ?2) OR (?3 != '' AND prn_id = ?3)))
			 ORDER BY g.created_at, g.grp_id`,
			all, c.PeerID, prn)
		if err != nil {
			return fmt.Errorf("hub: list groups: %w", err)
		}
		for rows.Next() {
			var g GroupInfo
			if err := rows.Scan(&g.GroupID, &g.CreatedBy, &g.Archived); err != nil {
				rows.Close()
				return fmt.Errorf("hub: list groups: %w", err)
			}
			out = append(out, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: list groups: %w", err)
		}
		for i := range out {
			if out[i].Members, err = queryIDs(ctx, tx, `SELECT peer_id FROM group_members WHERE grp_id = ? ORDER BY peer_id`, out[i].GroupID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryIDs runs a one-column id query with one argument; never nil.
func queryIDs(ctx context.Context, tx *sql.Tx, q, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("hub: members: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("hub: members: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: members: %w", err)
	}
	return out, nil
}

// ArchiveIdleGroups archives every unarchived ad hoc group whose
// last_active_at is more than GroupIdleTTL (30 days) before now (PROTOCOL
// §7) and returns how many it archived. Teams never archive. Run by the
// hub's tickers (Task 17).
func (h *Hub) ArchiveIdleGroups(ctx context.Context, now time.Time) (int, error) {
	cutoff := envelope.FormatTime(now.Add(-GroupIdleTTL))
	at := envelope.FormatTime(now)
	n := 0
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		idle, err := queryIDs(ctx, tx,
			`SELECT grp_id FROM groups WHERE archived_at IS NULL AND last_active_at < ? ORDER BY grp_id`, cutoff)
		if err != nil {
			return err
		}
		if len(idle) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE groups SET archived_at = ? WHERE archived_at IS NULL AND last_active_at < ?`, at, cutoff); err != nil {
			return fmt.Errorf("hub: archive groups: %w", err)
		}
		n = len(idle)
		return audit.Record(ctx, tx, "hub", "group_archive", idle, map[string]any{"idle_days": int(GroupIdleTTL / (24 * time.Hour))})
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// operatorOnly is 403 forbidden unless c is the operator credential.
func operatorOnly(c auth.Caller) error {
	if c.Kind != "operator" {
		return E(http.StatusForbidden, "forbidden", "teams are managed by the operator")
	}
	return nil
}

// CreateTeam creates an operator-managed team (PROTOCOL §7) named name with
// members (enrolled peer_ ids). Operator only (403 forbidden); an empty name
// is 422 invalid_value. Audited. The HTTP admin route is Task 17.
func (h *Hub) CreateTeam(ctx context.Context, c auth.Caller, name string, members []string) (string, error) {
	if err := operatorOnly(c); err != nil {
		return "", err
	}
	if name == "" {
		return "", invalidValue("name", "team name is required")
	}
	team := ids.New(ids.Team)
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		ms, err := h.checkMembers(ctx, tx, "", members)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams(team_id, name, created_at) VALUES (?, ?, ?)`,
			team, name, h.nowStr()); err != nil {
			return fmt.Errorf("hub: insert team: %w", err)
		}
		if err := insertMembers(ctx, tx, "team_members", "team_id", team, ms); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "team_create", append([]string{team}, ms...), nil)
	})
	if err != nil {
		return "", err
	}
	return team, nil
}

// AddTeamMembers adds members to team teamID. Operator only (403
// forbidden); an unknown team is 404 unknown_recipient. Existing members
// are ignored. Audited.
func (h *Hub) AddTeamMembers(ctx context.Context, c auth.Caller, teamID string, members []string) error {
	if err := operatorOnly(c); err != nil {
		return err
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE team_id = ?`, teamID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return unknownRecipient(teamID)
		}
		if err != nil {
			return fmt.Errorf("hub: find team: %w", err)
		}
		ms, err := h.checkMembers(ctx, tx, "", members)
		if err != nil {
			return err
		}
		if err := insertMembers(ctx, tx, "team_members", "team_id", teamID, ms); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "team_add_members", append([]string{teamID}, ms...), nil)
	})
}

// ListTeams lists every team with its members. Operator only (403
// forbidden). Ordered by creation.
func (h *Hub) ListTeams(ctx context.Context, c auth.Caller) ([]TeamInfo, error) {
	if err := operatorOnly(c); err != nil {
		return nil, err
	}
	out := []TeamInfo{}
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT team_id, name FROM teams ORDER BY created_at, team_id`)
		if err != nil {
			return fmt.Errorf("hub: list teams: %w", err)
		}
		for rows.Next() {
			var t TeamInfo
			if err := rows.Scan(&t.TeamID, &t.Name); err != nil {
				rows.Close()
				return fmt.Errorf("hub: list teams: %w", err)
			}
			out = append(out, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("hub: list teams: %w", err)
		}
		for i := range out {
			if out[i].Members, err = queryIDs(ctx, tx, `SELECT peer_id FROM team_members WHERE team_id = ? ORDER BY peer_id`, out[i].TeamID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
