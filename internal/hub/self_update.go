package hub

import (
	"context"
	"database/sql"

	"github.com/XertroV/vecnet/internal/audit"
)

// AuditSelfUpdate records that the operator started a one-click update from
// one version to another (POST /v1/admin/update).
func (h *Hub) AuditSelfUpdate(ctx context.Context, from, to string) error {
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		return audit.Record(ctx, tx, "operator", "hub_update", []string{}, map[string]any{"from": from, "to": to})
	})
}
