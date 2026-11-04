package httpapi

import (
	"net/http"
	"strconv"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/mcpdiag"
)

// Diagnostics may contain identities and client metadata. Require the operator
// explicitly, since this endpoint is outside the /v1/admin middleware prefix.
func (s *server) getMCPDiag(w http.ResponseWriter, r *http.Request) {
	if callerFrom(r.Context()).Kind != "operator" {
		writeError(w, hub.E(http.StatusForbidden, "forbidden", "operator credential required"))
		return
	}
	q := r.URL.Query()
	limit := 100
	if q.Has("limit") {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > mcpdiag.Capacity {
			writeError(w, hub.E(http.StatusBadRequest, "invalid_query", "limit must be between 1 and 1000"))
			return
		}
		limit = n
	}
	var after uint64
	if q.Has("after") {
		n, e := strconv.ParseUint(q.Get("after"), 10, 64)
		if e != nil {
			writeError(w, hub.E(http.StatusBadRequest, "invalid_query", "after must be a sequence number"))
			return
		}
		after = n
	}
	records := mcpdiag.Snapshot(mcpdiag.Filter{Peer: q.Get("peer"), Method: q.Get("method"), Limit: limit, After: after, AfterSet: q.Has("after")})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}
