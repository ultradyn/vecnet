package hub_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestStorageMaintenanceOptInAndStatus(t *testing.T) {
	env := testhub.StartFull(t, func(c *hub.Config) { c.StorageMaintenance = store.MaintenanceOptions{ReclaimPages: 8} })
	h := env.Hub
	ctx := context.Background()
	h.Tick(ctx, true)
	s, err := h.StorageMaintenanceStatus(auth.Caller{Kind: "operator"})
	if err != nil || s.ElapsedNanos != 0 {
		t.Fatalf("disabled maintenance ran: %+v %v", s, err)
	}
	h.Cfg.StorageMaintenanceEnabled = true
	h.Tick(ctx, false)
	s, err = h.StorageMaintenanceStatus(auth.Caller{Kind: "operator"})
	if err != nil || s.ElapsedNanos != 0 {
		t.Fatalf("fast tick ran maintenance: %+v %v", s, err)
	}
	h.Tick(ctx, true)
	s, err = h.StorageMaintenanceStatus(auth.Caller{Kind: "operator"})
	if err != nil || s.After.AutoVacuum != 2 || s.After.Synchronous != 2 || s.ElapsedNanos <= 0 {
		t.Fatalf("slow slice status: %+v %v", s, err)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), env.Hub.Cfg.DataDir) {
		t.Fatal("status exposed path")
	}
	if _, err = h.StorageMaintenanceStatus(auth.Caller{Kind: "peer"}); err == nil {
		t.Fatal("nonoperator read diagnostics")
	}
}
