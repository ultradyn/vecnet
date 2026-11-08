package httpapi

import (
	"net/http"
	"net/url"

	"github.com/XertroV/vecnet/internal/hub"
)

// Keep method handling in a stdlib mux for this exact endpoint. The parent
// mux's methodless / fallback would otherwise turn unsupported methods into
// 404; this preserves GET's implicit HEAD and the read endpoint's 405/Allow.
func (s *server) routeStatsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/route-stats", s.adminRouteStats)
	return mux
}

// adminRouteStats accepts exactly one id. Query errors contain only a fixed
// field name; selector semantics and read authority also live in the hub.
func (s *server) adminRouteStats(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, routeStatsQueryError("query"))
		return
	}
	for key := range query {
		if key != "id" {
			writeError(w, routeStatsQueryError("query"))
			return
		}
	}
	ids := query["id"]
	if len(ids) != 1 || ids[0] == "" || len(ids[0]) > 80 {
		writeError(w, routeStatsQueryError("id"))
		return
	}
	stats, err := s.Hub.RouteStats(r.Context(), callerFrom(r.Context()), ids[0])
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, stats)
}

func routeStatsQueryError(field string) *hub.APIError {
	e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "route statistics require one id and no other query fields")
	e.Details["field"] = field
	return e
}
