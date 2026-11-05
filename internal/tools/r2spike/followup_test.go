//go:build spike

package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFollowupPreservesWitnessesAndTTLEvidence(t *testing.T) {
	t.Chdir(t.TempDir())
	os.MkdirAll(filepath.Dir(reportPath), 0755)
	id := "20261105T025511Z-4cecd40c6f14cf36"
	old := report{Started: "original-start", Finished: stamp(), RunID: id, Profile: "sha256", Fixtures: []string{"attachments/v1/exp/1h/spike-" + id + "/lifecycle-exp-1h", "attachments/v1/keep/spike-" + id + "/lifecycle-keep"}, Gates: map[string]string{"get_600_after_expiry": "PASS", "cors_range_exposure": "FAIL"}, Observations: []observation{{Name: "get_600_after_expiry", Status: "PASS"}}}
	data, _ := json.Marshal(old)
	os.WriteFile(reportPath, data, 0644)
	r := runner{report: report{RunID: "new-run", Gates: map[string]string{"cors_range_exposure": "PASS"}}}
	if !r.loadBaseline() {
		t.Fatal("valid baseline rejected")
	}
	merged := r.combinedReport()
	if !reflect.DeepEqual(merged.Fixtures, old.Fixtures) || merged.RunID != old.RunID || merged.Finished != old.Finished || merged.Gates["get_600_after_expiry"] != "PASS" || merged.Gates["cors_range_exposure"] != "PASS" || len(merged.Followups) != 1 || merged.Observations[0].Name != "get_600_after_expiry" {
		t.Fatal("follow-up lost original lifecycle/TTL evidence or current gates")
	}
	if r.baseline.Gates["cors_range_exposure"] != "FAIL" {
		t.Fatal("merge mutated baseline evidence")
	}
	old.Fixtures[1] = "attachments/v1/keep/foreign/object"
	data, _ = json.Marshal(old)
	os.WriteFile(reportPath, data, 0644)
	if r.loadBaseline() {
		t.Fatal("foreign baseline fixture accepted")
	}
}

func TestLoopbackGrantsAreSameOriginOnly(t *testing.T) {
	const marker = "synthetic-capability-marker"
	handler := grantHandler(browserGrant{Put: marker}, "/grant-random")
	for _, tt := range []struct {
		method, path, origin string
		want                 int
	}{
		{"GET", "/grant-random", "", 200},
		{"GET", "/grant-random", "http://localhost:45752", 200},
		{"GET", "/grant-random", "https://foreign.invalid", 403},
		{"OPTIONS", "/grant-random", "https://foreign.invalid", 403},
		{"GET", "/unknown", "", 404},
		{"GET", "/", "", 200},
	} {
		req := httptest.NewRequest(tt.method, "http://localhost:45752"+tt.path, nil)
		req.Header.Set("Origin", tt.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tt.want || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("loopback grant boundary incorrect")
		}
		if (tt.want != 200 || tt.path != "/grant-random") && strings.Contains(w.Body.String(), marker) {
			t.Fatal("grant leaked outside protected endpoint")
		}
	}
}
