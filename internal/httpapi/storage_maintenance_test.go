package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestStorageMaintenanceOperatorDiagnostics(t *testing.T) {
	env := testhub.Start(t)
	env.Hub.Cfg.StorageMaintenanceEnabled = true
	env.Hub.Tick(t.Context(), true)
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/storage/maintenance", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got store.MaintenanceResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.After.PageSize != 4096 || got.After.Synchronous != 2 || got.ElapsedNanos <= 0 {
		t.Fatalf("missing maintenance metrics: %+v", got)
	}
	if strings.Contains(string(raw), env.Hub.Cfg.DataDir) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("diagnostics leaked path or may be cached")
	}
	_, secret := env.NewPeer("session", "nonoperator")
	resp, _ = env.Do(secret, "GET", "/v1/admin/storage/maintenance", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("nonoperator status=%d", resp.StatusCode)
	}
}
