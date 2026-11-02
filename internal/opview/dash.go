package opview

import (
	"context"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

func (r *Reader) Peers(ctx context.Context) ([]opapi.PeerRow, error) {
	return nil, hub.E(501, "not_implemented", "operator peer list is not implemented")
}

func (r *Reader) LoopCuts(ctx context.Context, hours int) (opapi.LoopCuts, error) {
	return opapi.LoopCuts{}, hub.E(501, "not_implemented", "operator loop cuts are not implemented")
}

func (r *Reader) DeliveryHealth(ctx context.Context) (opapi.DeliveryHealth, error) {
	return opapi.DeliveryHealth{}, hub.E(501, "not_implemented", "operator delivery health is not implemented")
}
