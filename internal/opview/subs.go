package opview

import (
	"context"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

func (r *Reader) Subscriptions(ctx context.Context) ([]opapi.Subscription, error) {
	return nil, hub.E(501, "not_implemented", "operator subscriptions are not implemented")
}

// Attention is an empty seam until subscription health is implemented; the
// inbox must remain usable before that task lands.
func (r *Reader) Attention(ctx context.Context) ([]opapi.Attention, error) {
	return nil, nil
}
