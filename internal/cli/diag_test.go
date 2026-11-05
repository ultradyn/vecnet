package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/mcpdiag"
)

func TestDiagMCPFiltersAndFollow(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "follow"}[follow], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A pre-identity hub: the CLI's TOFU probe gets 404 (PROTOCOL §21.1).
				if r.URL.Path == "/v1/hub/identity" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path != "/v1/diag/mcp" || r.URL.Query().Get("peer") != "peer_test" || r.URL.Query().Get("method") != "events/subscribe" || r.URL.Query().Get("limit") != "2" {
					t.Errorf("request query: %s", r.URL)
				}
				n := calls.Add(1)
				if n == 2 && r.URL.Query().Get("after") != "7" {
					t.Errorf("follow after=%q", r.URL.Query().Get("after"))
				}
				seq := 7
				if n > 1 {
					seq = 8
					defer cancel()
				}
				json.NewEncoder(w).Encode(map[string]any{"records": []any{map[string]any{"sequence": seq, "time": "2026-11-05T00:00:00Z", "kind": "events.refresh", "fields": map[string]any{"peer_id": "peer_test", "result": "refreshed"}}}})
			}))
			defer srv.Close()
			u := profileUser(t, srv.URL, "operator-test-credential")
			args := []string{"--json", "diag", "mcp", "--peer", "peer_test", "--method", "events/subscribe", "--limit", "2"}
			if follow {
				args = append(args, "--follow")
			}
			var out, errs bytes.Buffer
			code := cli.RunContext(ctx, args, strings.NewReader(""), &out, &errs, u.getenv)
			if code != 0 || !strings.Contains(out.String(), "events.refresh") {
				t.Fatalf("code=%d out=%s err=%s", code, out.String(), errs.String())
			}
			if follow && calls.Load() < 2 {
				t.Fatal("follow did not request next page")
			}
		})
	}
}

func TestDiagMCPInvalidLimit(t *testing.T) {
	for _, limit := range []string{"0", "1001", "bad"} {
		u := newUser(t)
		code, _, err := u.run("diag", "mcp", "--limit", limit)
		if code != 1 || !strings.Contains(err, "--limit") {
			t.Fatalf("limit %s: code=%d err=%s", limit, code, err)
		}
	}
}

type cancelDiagWriter struct {
	bytes.Buffer
	cancel       context.CancelFunc
	stopSequence uint64
}

func (w *cancelDiagWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	stop := w.stopSequence
	if stop == 0 {
		stop = 6
	}
	if strings.Contains(w.Buffer.String(), fmt.Sprintf(`"sequence":%d`, stop)) {
		w.cancel()
	}
	return n, err
}

func TestDiagMCPFollowDrainsBurstWithoutGaps(t *testing.T) {
	for _, initiallyEmpty := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially_empty_%v", initiallyEmpty), func(t *testing.T) {
			diagMCPFollowBurst(t, initiallyEmpty)
		})
	}
}

func diagMCPFollowBurst(t *testing.T, initiallyEmpty bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var ring mcpdiag.Buffer
	if !initiallyEmpty {
		ring.Emit("mcp.request", map[string]any{"method": "ping"})
	}
	var calls atomic.Int32
	var drainedAt []time.Time
	var drainMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A pre-identity hub: the CLI's TOFU probe gets 404 (PROTOCOL §21.1).
		if r.URL.Path == "/v1/hub/identity" {
			http.NotFound(w, r)
			return
		}
		call := calls.Add(1)
		if call == 2 {
			for i := 0; i < 5; i++ {
				ring.Emit("mcp.request", map[string]any{"method": "ping"})
			}
		}
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		if call == 1 && r.URL.Query().Has("after") {
			t.Error("initial snapshot must omit cursor")
		}
		if call == 2 && initiallyEmpty && (!r.URL.Query().Has("after") || after != 0) {
			t.Error("empty initial snapshot must continue with explicit after=0")
		}
		if r.URL.Query().Has("after") {
			drainMu.Lock()
			drainedAt = append(drainedAt, time.Now())
			drainMu.Unlock()
		}
		page := ring.Snapshot(mcpdiag.Filter{After: after, AfterSet: r.URL.Query().Has("after"), Limit: 2})
		json.NewEncoder(w).Encode(map[string]any{"records": page})
	}))
	defer srv.Close()
	u := profileUser(t, srv.URL, "operator-test-credential")
	stop := uint64(6)
	want := "[1 2 3 4 5 6]"
	if initiallyEmpty {
		stop = 5
		want = "[1 2 3 4 5]"
	}
	out := cancelDiagWriter{cancel: cancel, stopSequence: stop}
	var errs bytes.Buffer
	code := cli.RunContext(ctx, []string{"--json", "diag", "mcp", "--limit", "2", "--follow"}, strings.NewReader(""), &out, &errs, u.getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errs.String())
	}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	var got []uint64
	for dec.More() {
		var record mcpdiag.Record
		if err := dec.Decode(&record); err != nil {
			t.Fatal(err)
		}
		got = append(got, record.Sequence)
	}
	if fmt.Sprint(got) != want {
		t.Fatalf("follow skipped burst records: %v", got)
	}
	drainMu.Lock()
	defer drainMu.Unlock()
	if len(drainedAt) != 3 || drainedAt[2].Sub(drainedAt[0]) >= time.Second {
		t.Fatalf("full pages were not drained immediately: %v", drainedAt)
	}
}
