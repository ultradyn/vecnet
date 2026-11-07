package hub

import (
	"context"
	"errors"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/store"
)

// MaintainStorage runs an explicitly requested bounded slice. The ticker calls
// it only with StorageMaintenanceEnabled; reference writes/backfill do not
// implicitly authorize physical reclaim. Concurrent callers yield.
func (h *Hub) MaintainStorage(ctx context.Context) (store.MaintenanceResult, error) {
	if !h.maintenanceMu.TryLock() {
		return store.MaintenanceResult{PausedReason: "lock_pressure"}, nil
	}
	defer h.maintenanceMu.Unlock()
	r, err := h.DB.Maintain(ctx, h.Cfg.StorageMaintenance)
	h.maintenanceLast = r
	return r, err
}

// StorageMaintenanceStatus returns the latest in-process slice's ids-only
// diagnostics. No SQL, filesystem path, body, URL or token is included.
func (h *Hub) StorageMaintenanceStatus(c auth.Caller) (store.MaintenanceResult, error) {
	if c.Kind != "operator" {
		return store.MaintenanceResult{}, errors.New("maintenance: operator_required")
	}
	h.maintenanceMu.Lock()
	defer h.maintenanceMu.Unlock()
	return h.maintenanceLast, nil
}
