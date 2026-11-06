package cli

import (
	"fmt"
	"io"
)

// routeStats is GET /v1/admin/route-stats. Fields match the hub readout.
type routeStats struct {
	Subject struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"subject"`
	Window struct {
		Since       string `json:"since"`
		Until       string `json:"until"`
		MaxRows     int    `json:"max_rows"`
		MaxAgeHours int    `json:"max_age_hours"`
	} `json:"window"`
	Deliveries struct {
		Pushed int `json:"pushed"`
		Pulled int `json:"pulled"`
	} `json:"deliveries"`
	Routes []routeStat `json:"routes"`
}

type routeStat struct {
	RouteID       string  `json:"route_id"`
	Kind          string  `json:"kind"`
	Host          string  `json:"host"`
	Requests      int     `json:"requests"`
	OK            int     `json:"ok"`
	Fail          int     `json:"fail"`
	Refused       int     `json:"refused"`
	Pushed        int     `json:"pushed"`
	LastError     *string `json:"last_error"`
	LastUsedAt    *string `json:"last_used_at"`
	LastSuccessAt *string `json:"last_success_at"`
	BodyBytes     dist    `json:"body_bytes"`
	LatencyMS     dist    `json:"latency_ms"`
}

type dist struct {
	Avg     float64 `json:"avg"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
	Samples int     `json:"samples"`
}

func printRouteStats(w io.Writer, s routeStats) error {
	fmt.Fprintf(w, "%s %s  window %s .. %s  max %d rows / %dh\n",
		s.Subject.Kind, s.Subject.ID, s.Window.Since, s.Window.Until, s.Window.MaxRows, s.Window.MaxAgeHours)
	fmt.Fprintf(w, "deliveries pushed=%d pulled=%d\n", s.Deliveries.Pushed, s.Deliveries.Pulled)
	if len(s.Routes) == 0 {
		fmt.Fprintln(w, "no routes")
		return nil
	}
	for _, r := range s.Routes {
		errText := "-"
		if r.LastError != nil {
			errText = *r.LastError
		}
		fmt.Fprintf(w, "%s kind=%s host=%s requests=%d ok=%d fail=%d refused=%d pushed=%d last_error=%s\n",
			r.RouteID, r.Kind, r.Host, r.Requests, r.OK, r.Fail, r.Refused, r.Pushed, errText)
		fmt.Fprintf(w, "  body avg=%.3f p95=%.3f p99=%.3f  latency_ms avg=%.3f p95=%.3f p99=%.3f  samples=%d\n",
			r.BodyBytes.Avg, r.BodyBytes.P95, r.BodyBytes.P99, r.LatencyMS.Avg, r.LatencyMS.P95, r.LatencyMS.P99, r.BodyBytes.Samples)
	}
	return nil
}
