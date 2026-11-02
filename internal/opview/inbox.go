package opview

import (
	"context"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Inbox is filled by the operator inbox query task.
func (r *Reader) Inbox(ctx context.Context, c auth.Caller, q opapi.InboxQuery) (opapi.Inbox, error) {
	return opapi.Inbox{}, hub.E(501, "not_implemented", "operator inbox is not implemented")
}

func (r *Reader) MarkThreadRead(ctx context.Context, c auth.Caller, thrID string) (opapi.ReadResult, error) {
	return opapi.ReadResult{}, hub.E(501, "not_implemented", "operator read state is not implemented")
}
