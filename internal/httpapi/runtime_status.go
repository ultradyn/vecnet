package httpapi

import (
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
)

// A valid report is only a few hundred bytes; reject oversized bodies using
// the normal middleware/error envelope, including unknown-length uploads.
const runtimeStatusBodyLimit = 1024

// postRuntimeStatus is POST /v1/runtime-status, or ?as= for a principal.
func (s *server) postRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	top, err := decodeObject(raw, []string{"runtime", "state", "delivery", "reason", "version", "ttl_seconds"}, []string{"runtime", "state", "delivery", "ttl_seconds"})
	if err != nil {
		writeError(w, err)
		return
	}
	var p hub.RuntimeStatusReport
	for _, f := range []struct {
		name  string
		value *string
	}{{"runtime", &p.Runtime}, {"state", &p.State}, {"delivery", &p.Delivery}} {
		*f.value, err = stringField(top, f.name)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	for _, f := range []struct {
		name  string
		value **string
	}{{"reason", &p.Reason}, {"version", &p.Version}} {
		if _, ok := top[f.name]; !ok {
			continue
		}
		v, err := stringField(top, f.name)
		if err != nil {
			writeError(w, err)
			return
		}
		*f.value = &v
	}
	p.TTLSeconds, err = intField(top, "ttl_seconds")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetRuntimeStatus(r.Context(), callerFrom(r.Context()), r.URL.Query().Get("as"), p); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
