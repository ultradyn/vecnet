package hub

import (
	"context"
	"database/sql"
	"errors"

	"github.com/XertroV/vecnet/internal/audit"
)

// SetThreadArchived changes only the operator view; peer access is unaffected.
func (h *Hub) SetThreadArchived(ctx context.Context, id string, archived bool) error {
	var notify []string
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var at sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT archived_at FROM threads WHERE thr_id=?`, id).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			e := E(404, "unknown_thread", "no such thread")
			e.Details["thread_id"] = id
			return e
		}
		if err != nil {
			return err
		}
		if at.Valid == archived {
			return nil
		}
		action := "thread_unarchive"
		if archived {
			action = "thread_archive"
			_, err = tx.ExecContext(ctx, `UPDATE threads SET archived_at=?,archived_by='operator' WHERE thr_id=?`, h.nowStr(), id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE threads SET archived_at=NULL,archived_by=NULL,unarchived_at=? WHERE thr_id=?`, h.nowStr(), id)
		}
		if err != nil {
			return err
		}
		if err = audit.Record(ctx, tx, "operator", action, []string{id}, nil); err != nil {
			return err
		}
		notify, err = h.noteOperatorView(ctx, tx, "archive", id)
		return err
	})
	if err == nil {
		h.Log.Notify(notify...)
	}
	return err
}
