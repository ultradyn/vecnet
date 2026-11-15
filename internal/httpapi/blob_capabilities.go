package httpapi

import (
	"context"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"net/http"
)

func (s *server) blobCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.blobOriginAllowed(w, r) {
		return
	}
	backend := s.Hub.Cfg.BlobBackend
	if backend == "" {
		backend = "disk"
	}
	result := map[string]any{"backend": backend, "upload_mode": "legacy", "max_size": s.Hub.Blobs.MaxSize, "daily_quota": s.Hub.Blobs.DailyQuota, "mcp_transfer_mode": "base64", "mcp_remote_bytes": false}
	// Retained remote references remain readable when new uploads use disk.
	if origins := s.r2Origins(); origins != nil {
		result["download_origins"] = origins
	}
	if backend == "r2" {
		snapshot, err := s.Hub.Preferences.LoadSnapshot(r.Context(), nil)
		if err != nil {
			writeError(w, hub.E(503, "blob_preferences_unavailable", "preferences unavailable"))
			return
		}
		effective, err := snapshot.Get("blobs.expiry")
		if err != nil {
			writeError(w, hub.E(503, "blob_preferences_unavailable", "preferences unavailable"))
			return
		}
		value, ok := effective.Value.Enum()
		if !ok {
			writeError(w, hub.E(503, "blob_preferences_unavailable", "preferences unavailable"))
			return
		}
		now := s.Hub.Now()
		_, expiry, err := blob.R2Retention(value, now)
		if err != nil {
			writeError(w, hub.E(503, "blob_preferences_unavailable", "preferences unavailable"))
			return
		}
		var seconds any
		if expiry != nil {
			seconds = int64(expiry.Sub(now).Seconds())
		}
		result["retention"] = map[string]any{"value": value, "source": effective.Source, "revision": effective.Revision, "allowed_values": []string{"1h", "6h", "12h", "1d", "7d", "30d", "off"}, "seconds": seconds}
		result["upload_mode"] = "direct"
		result["checksum_profile"] = "sha256"
		result["mcp_transfer_mode"] = "signed_urls"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (s *server) blobStorageValues(ctx context.Context) (map[string]int64, error) {
	var retained, reserved int64
	if err := s.Hub.DB.QueryRowContext(ctx, `SELECT retained_bytes,storage_reserved_bytes FROM blob_r2_storage WHERE singleton=1`).Scan(&retained, &reserved); err != nil {
		return nil, hub.E(503, "blob_unavailable", "storage unavailable")
	}
	return map[string]int64{"retained_bytes": retained, "reserved_bytes": reserved, "used_bytes": retained + reserved, "limit_bytes": s.Hub.Cfg.R2.StorageQuotaBytes}, nil
}
func (s *server) blobStorage(w http.ResponseWriter, r *http.Request) {
	storage, err := s.blobStorageValues(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"storage": storage})
}

// Keep the prefs-owned snapshot and ETag intact; aggregate usage is visible
// only through the operator settings surface and its dedicated live counter.
func (s *server) writePreferencesSnapshot(w http.ResponseWriter, r *http.Request, snapshot prefs.Snapshot) {
	result := map[string]any{
		"revision":    snapshot.Revision,
		"preferences": snapshot.Descriptors(),
		"runtime":     map[string]bool{"event_retention_enabled": s.Hub.Cfg.EventRetentionEnabled},
	}
	if s.Hub.Blobs.Transfers != nil {
		storage, err := s.blobStorageValues(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		result["storage"] = storage
	}
	writeJSON(w, 200, result)
}

// r2Origins is the exact R2 S3 origin clients transfer with, while R2
// transfers are installed (retained remote references stay readable after a
// switch back to disk). Capabilities advertise it, and the operator UI's
// CSP connect-src allows exactly it (internal/ui.Policy).
func (s *server) r2Origins() []string {
	if s.Hub == nil || s.Hub.Blobs == nil || s.Hub.Blobs.Transfers == nil {
		return nil
	}
	return []string{"https://" + s.Hub.Cfg.R2.AccountID + ".r2.cloudflarestorage.com"}
}
