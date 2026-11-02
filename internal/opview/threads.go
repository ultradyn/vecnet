package opview

import (
	"context"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

func (r *Reader) Threads(ctx context.Context, q opapi.ThreadQuery) (opapi.ThreadPage, error) {
	return opapi.ThreadPage{}, hub.E(501, "not_implemented", "operator thread list is not implemented")
}

func (r *Reader) Thread(ctx context.Context, c auth.Caller, thrID string) (opapi.ThreadDetail, error) {
	return opapi.ThreadDetail{}, hub.E(501, "not_implemented", "operator thread detail is not implemented")
}

func (r *Reader) Matrix(ctx context.Context, thrID, after string, limit int) (opapi.Matrix, error) {
	return opapi.Matrix{}, hub.E(501, "not_implemented", "operator delivery matrix is not implemented")
}
