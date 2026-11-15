package cli

// These tests cover the routeStats and cmdRouteStats helpers directly.
// Public command dispatch is covered separately in route_stats_dispatch_test.go.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/XertroV/vecnet/internal/opapi"
)

const t3StatsPeer = "peer_00000000000000000000000001"
const t3StatsRoute = "route_00000000000000000000000011"
const t3StatsSub = "sub_00000000000000000000000013"
const t3StatsBearer = "vn_operator_task3_synthetic_bearer_canary"
const t3StatsURLCanary = "https://userinfo-canary:password-canary@failure.example:9443/private-path?query-canary#fragment-canary"

// Independent literal oracle: contract 3.1 and immutable Task2 DTO wire keys.
// Mixed/principal, measured zero/peer, and empty/subscription populations are
// deliberately different from the separate retained-delivery population.
const t3StatsReport = `{
  "subject":{"kind":"peer","id":"peer_00000000000000000000000001"},
  "window":{"since":"2030-01-03T12:00:00.000Z","until":"2030-01-10T12:00:00.000Z","max_rows":1000,"max_age_hours":168},
  "retained_deliveries":{"peer_ids":["peer_00000000000000000000000001"],"total":901,"with_push_evidence":500,"accepted_without_push_evidence":2},
  "routes":[
    {"route_id":"route_00000000000000000000000011","kind":"http","host":"stats.example","owner_kind":"principal","owner_id":"prn_00000000000000000000000010","attempt_scope":"route",
     "requests":300,"coalesced":0,"ok":298,"fail":1,"refused":1,"distinct_ids":299,
     "last_used_at":"2030-01-10T11:59:00.000Z","last_success_at":"2030-01-10T11:59:00.000Z","last_error":"http_429","last_error_at":"2030-01-10T11:58:00.000Z","last_status":202,
     "body_bytes":{"avg":150.5,"p95":285,"p99":297,"samples":300},"latency_ms":{"avg":12.347,"p95":25,"p99":40,"samples":300}},
    {"route_id":"route_00000000000000000000000012","kind":"grokbot","host":"api2.cursor.sh","owner_kind":"peer","owner_id":"peer_00000000000000000000000001","attempt_scope":"route",
     "requests":1,"coalesced":6,"ok":1,"fail":0,"refused":0,"distinct_ids":1,
     "last_used_at":"2030-01-10T11:57:00.000Z","last_success_at":"2030-01-10T11:57:00.000Z","last_error":null,"last_error_at":null,"last_status":204,
     "body_bytes":{"avg":0,"p95":0,"p99":0,"samples":1},"latency_ms":{"avg":0,"p95":0,"p99":0,"samples":1}},
    {"route_id":"sub_00000000000000000000000013","kind":"events","host":"events.example","owner_kind":"peer","owner_id":"peer_00000000000000000000000001","attempt_scope":"route",
     "requests":0,"coalesced":0,"ok":0,"fail":0,"refused":0,"distinct_ids":0,
     "last_used_at":null,"last_success_at":null,"last_error":null,"last_error_at":null,"last_status":null,
     "body_bytes":{"avg":null,"p95":null,"p99":null,"samples":0},"latency_ms":{"avg":null,"p95":null,"p99":null,"samples":0}}
  ]
}`

const t3StatsEmptyReport = `{
  "subject":{"kind":"peer","id":"peer_00000000000000000000000001"},
  "window":{"since":"2030-01-03T12:00:00.000Z","until":"2030-01-10T12:00:00.000Z","max_rows":1000,"max_age_hours":168},
  "retained_deliveries":{"peer_ids":["peer_00000000000000000000000001"],"total":0,"with_push_evidence":0,"accepted_without_push_evidence":0},
  "routes":[]
}`

// A retained principal route with no current member peers still has a row,
// while its context peer_ids is []. A peer report always includes its peer.
const t3StatsNoContextReport = `{
  "subject":{"kind":"route","id":"route_00000000000000000000000011"},
  "window":{"since":"2030-01-03T12:00:00.000Z","until":"2030-01-10T12:00:00.000Z","max_rows":1000,"max_age_hours":168},
  "retained_deliveries":{"peer_ids":[],"total":0,"with_push_evidence":0,"accepted_without_push_evidence":0},
  "routes":[{"route_id":"route_00000000000000000000000011","kind":"http","host":"stats.example","owner_kind":"principal","owner_id":"prn_00000000000000000000000010","attempt_scope":"route",
    "requests":0,"coalesced":0,"ok":0,"fail":0,"refused":0,"distinct_ids":0,
    "last_used_at":null,"last_success_at":null,"last_error":null,"last_error_at":null,"last_status":null,
    "body_bytes":{"avg":null,"p95":null,"p99":null,"samples":0},"latency_ms":{"avg":null,"p95":null,"p99":null,"samples":0}}]
}`

type t3StatsRoundTripper func(*http.Request) (*http.Response, error)

func (f t3StatsRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type t3StatsBody struct {
	io.Reader
	err    error
	closed bool
}

func (b *t3StatsBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	return b.Reader.Read(p)
}

func (b *t3StatsBody) Close() error {
	b.closed = true
	return nil
}

func t3StatsApp(t *testing.T, jsonMode bool) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errs bytes.Buffer
	a := &app{
		ctx: context.Background(), stdin: strings.NewReader(""),
		stdout: &out, stderr: &errs, json: jsonMode, net: "selected-synthetic-operator",
		as: "peer_must_not_become_an_extra_query",
		getenv: func(string) string {
			t.Fatal("helper attempted config/environment resolution outside selected-client seam")
			return ""
		},
	}
	return a, &out, &errs
}

func t3StatsRequest(t *testing.T, r *http.Request, id string) {
	t.Helper()
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["id"]) != 1 || q.Get("id") != id {
		t.Error("stats request must contain exactly one unchanged encoded id and no other query keys")
	}
	if r.Method != http.MethodGet || r.URL.Path != "/v1/admin/route-stats" || r.URL.RawQuery != (url.Values{"id": {id}}).Encode() {
		t.Error("stats request method/path/encoding changed")
	}
	if r.Header.Get("Authorization") != "Bearer "+t3StatsBearer || r.Header.Get("Accept") != "application/json" || r.Header.Get("X-Vecnet-Client") != "task3-selected-client" {
		t.Error("stats request did not use selected client's bearer and headers")
	}
	if r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil || len(raw) != 0 {
			t.Error("stats GET must have no body")
		}
	}
}

func t3StatsSelected(c *client) func() (*client, error) {
	return func() (*client, error) { return c, nil }
}

func t3StatsNoLeak(t *testing.T, text string, extra ...string) {
	t.Helper()
	canaries := append([]string{
		t3StatsBearer, "userinfo-canary", "password-canary", "failure.example",
		"private-path", "query-canary", "fragment-canary", "selector-canary",
		"envelope-code-canary", "envelopecodecanary", "remote_private_code_canary",
		"decode-field-canary", "908070605040302010909090", "https://", "http://",
	}, extra...)
	for _, canary := range canaries {
		if canary != "" && strings.Contains(text, canary) {
			t.Fatal("returned or printed error leaked synthetic private text")
		}
	}
}

func t3StatsJSON(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal("fixture/output is not one complete JSON value")
	}
	return value
}

func t3StatsServe(t *testing.T, id, report string, jsonMode bool) (string, string) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t3StatsRequest(t, r, id)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, report)
	}))
	defer srv.Close()
	c := newClient(srv.URL, t3StatsBearer)
	c.clientHeader = "task3-selected-client"
	a, out, errs := t3StatsApp(t, jsonMode)
	selected := 0
	err := a.routeStats(opts{pos: []string{id}}, func() (*client, error) {
		selected++
		return c, nil
	})
	if code := a.exit(err); code != exitOK || errs.Len() != 0 || selected != 1 || calls.Load() != 1 {
		t.Fatalf("helper success exit=%d selections=%d GETs=%d stderr=%q", code, selected, calls.Load(), errs.String())
	}
	return out.String(), errs.String()
}

func TestRouteStatsHelperRequestAndErrors(t *testing.T) {
	for _, id := range []string{t3StatsPeer, t3StatsRoute, t3StatsSub} {
		t.Run("selected-client-"+strings.SplitN(id, "_", 2)[0], func(t *testing.T) {
			report := t3StatsNoContextReport
			if id == t3StatsPeer {
				report = t3StatsEmptyReport
			}
			if id == t3StatsSub {
				var fixture opapi.RouteStats
				if err := json.Unmarshal([]byte(report), &fixture); err != nil {
					t.Fatal(err)
				}
				fixture.Subject = opapi.RouteStatsSubject{Kind: "subscription", ID: id}
				fixture.RetainedDeliveries.PeerIDs = []string{t3StatsPeer}
				fixture.Routes[0].RouteID, fixture.Routes[0].Kind = id, "events"
				fixture.Routes[0].OwnerKind, fixture.Routes[0].OwnerID = "peer", t3StatsPeer
				raw, err := json.Marshal(fixture)
				if err != nil {
					t.Fatal(err)
				}
				report = string(raw)
			}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				t3StatsRequest(t, r, id)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, report)
			}))
			defer srv.Close()
			c := newClient(srv.URL, t3StatsBearer)
			c.clientHeader = "task3-selected-client"
			a, out, errs := t3StatsApp(t, true)
			err := a.routeStats(opts{pos: []string{id}}, t3StatsSelected(c))
			if code := a.exit(err); code != exitOK || calls.Load() != 1 || out.Len() == 0 || errs.Len() != 0 {
				t.Fatalf("helper exit=%d GETs=%d", code, calls.Load())
			}
		})
	}
	t.Run("wrong-arity-before-selection", func(t *testing.T) {
		for _, args := range [][]string{nil, {t3StatsPeer, t3StatsRoute}} {
			a, out, errs := t3StatsApp(t, false)
			selected := 0
			err := a.routeStats(opts{pos: args}, func() (*client, error) {
				selected++
				return nil, errors.New("must not select")
			})
			var ue *usageError
			if !errors.As(err, &ue) || a.exit(err) != exitUsage || selected != 0 || out.Len() != 0 || !strings.Contains(errs.String(), "usage") {
				t.Fatal("wrong arity must be local usage, with no selection/HTTP/output")
			}
			// Adapter must also validate before a.connect; getenv is a tripwire.
			a, out, errs = t3StatsApp(t, false)
			if code := a.exit(a.cmdRouteStats(args)); code != exitUsage || out.Len() != 0 || !strings.Contains(errs.String(), "usage") {
				t.Fatal("adapter wrong arity must precede profile resolution")
			}
		}
	})
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"unauthenticated", 401, "unauthenticated"},
		{"forbidden", 403, "forbidden"},
		{"not-found", 404, "not_found"},
		{"invalid-selector", 422, "invalid_value"},
		{"internal", 500, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately malformed single selector tests literal query encoding
			// and service error propagation, not local alias/syntax resolution.
			id := "selector-canary +&id=second?#/"
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				t3StatsRequest(t, r, id)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
					"code": tc.code, "message": t3StatsURLCanary + id,
					"details": map[string]any{"field": "id", "private": t3StatsBearer},
				}})
			}))
			defer srv.Close()
			c := newClient(srv.URL, t3StatsBearer)
			c.clientHeader = "task3-selected-client"
			a, out, errs := t3StatsApp(t, true)
			err := a.routeStats(opts{pos: []string{id}}, t3StatsSelected(c))
			var he *hubError
			if !errors.As(err, &he) || he.Status != tc.status || he.Code != tc.code {
				t.Fatal("helper lost fixed hub status/code")
			}
			if a.exit(err) != exitHub || calls.Load() != 1 || out.Len() != 0 || !strings.HasPrefix(errs.String(), tc.code+":") {
				t.Fatal("hub failure must keep exit 2 and no partial output")
			}
			t3StatsNoLeak(t, err.Error()+errs.String(), id)
			encoded, encodeErr := json.Marshal(he)
			if encodeErr != nil {
				t.Fatal("cannot inspect sanitized hub error")
			}
			t3StatsNoLeak(t, string(encoded), id)
		})
	}
	for _, tc := range []struct {
		name             string
		status           int
		body             string
		readFailure      bool
		transportFailure bool
		wantExit         int
		wantCode         string
	}{
		{"URL-bearing-roundtripper", 0, "", false, true, exitNetwork, ""},
		{"URL-bearing-body-reader", 200, "", true, false, exitNetwork, ""},
		{"malformed-success", 200, `{"subject":`, false, false, exitHub, "bad_response"},
		{"malformed-field-with-URL-value", 200, `{"routes":[{"last_status":"` + t3StatsURLCanary + `"}]}`, false, false, exitHub, "bad_response"},
		// UnmarshalTypeError.Value may include the raw overflowing number.
		// A string-to-int error need not include its original string value.
		{"decode-number-canary", 200, `{"routes":[{"last_status":908070605040302010909090}]}`, false, false, exitHub, "bad_response"},
		{"malformed-array", 200, `{"retained_deliveries":{"peer_ids":{"decode-field-canary":"private"}}}`, false, false, exitHub, "bad_response"},
		{"malformed-error-body", 502, "<html>" + t3StatsURLCanary + t3StatsBearer + "</html>", false, false, exitHub, "http_502"},
		{"untrusted-envelope-code", 500, `{"error":{"code":"envelope-code-canary","message":"` + t3StatsURLCanary + `","details":{"private":"` + t3StatsBearer + `"}}}`, false, false, exitHub, "http_error"},
		{"safe-looking-untrusted-envelope-code", 500, `{"error":{"code":"remote_private_code_canary","message":"` + t3StatsURLCanary + `","details":{"private":"` + t3StatsBearer + `"}}}`, false, false, exitHub, "http_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "route_selector-canary"
			body := &t3StatsBody{Reader: strings.NewReader(tc.body)}
			if tc.readFailure {
				body.err = errors.New(t3StatsURLCanary + id + t3StatsBearer)
			}
			calls := 0
			c := newClient("https://selected.example", t3StatsBearer)
			c.clientHeader = "task3-selected-client"
			c.hc.Transport = t3StatsRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				t3StatsRequest(t, r, id)
				if r.URL.Host != "selected.example" {
					t.Error("helper replaced selected hub")
				}
				if tc.transportFailure {
					return nil, errors.New(t3StatsURLCanary + id + t3StatsBearer)
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: body, Request: r}, nil
			})
			a, out, errs := t3StatsApp(t, true)
			err := a.routeStats(opts{pos: []string{id}}, t3StatsSelected(c))
			if err == nil {
				t.Fatal("fixture must return error, not partial success")
			}
			if a.exit(err) != tc.wantExit || calls != 1 || out.Len() != 0 || errs.Len() == 0 {
				t.Fatal("failed helper lost exit class, retried, or printed partial data")
			}
			if tc.wantExit == exitNetwork {
				var ne *netError
				if !errors.As(err, &ne) {
					t.Fatal("sanitization must retain network exit type")
				}
				for cause := error(ne); cause != nil; cause = errors.Unwrap(cause) {
					t3StatsNoLeak(t, cause.Error(), id, "selected.example")
				}
			} else {
				var he *hubError
				if !errors.As(err, &he) || he.Status != tc.status || he.Code != tc.wantCode {
					t.Fatal("response error must preserve HTTP status and safe fixed code")
				}
				if !strings.HasPrefix(errs.String(), tc.wantCode+":") {
					t.Fatal("generic exit printer lost the fixed safe response code")
				}
				encoded, encodeErr := json.Marshal(he)
				if encodeErr != nil {
					t.Fatal("cannot inspect sanitized hub error")
				}
				t3StatsNoLeak(t, string(encoded), id)
			}
			t3StatsNoLeak(t, err.Error()+errs.String(), id, "selected.example")
			if !tc.transportFailure && !body.closed {
				t.Fatal("response body leaked")
			}
		})
	}
	t.Run("selection-errors-and-interruption", func(t *testing.T) {
		for _, tc := range []struct {
			err  error
			want int
		}{
			{&netError{err: errors.New(t3StatsURLCanary)}, exitNetwork},
			{context.Canceled, exitUsage},
			{context.DeadlineExceeded, exitUsage},
		} {
			a, out, errs := t3StatsApp(t, false)
			selected := 0
			err := a.routeStats(opts{pos: []string{t3StatsPeer}}, func() (*client, error) {
				selected++
				return nil, tc.err
			})
			if err == nil || a.exit(err) != tc.want || selected != 1 || out.Len() != 0 {
				t.Fatal("selection failure exit changed")
			}
			if errors.Is(tc.err, context.Canceled) || errors.Is(tc.err, context.DeadlineExceeded) {
				if !errors.Is(err, tc.err) || !strings.Contains(errs.String(), "interrupted") {
					t.Fatal("interruption semantics changed")
				}
			}
			t3StatsNoLeak(t, err.Error()+errs.String())
		}
	})
}

func TestRouteStatsHelperJSONParity(t *testing.T) {
	for _, tc := range []struct{ name, id, report string }{
		{"all-fields-nulls-and-zero", t3StatsPeer, t3StatsReport},
		{"empty-routes-array", t3StatsPeer, t3StatsEmptyReport},
		{"empty-context-array", t3StatsRoute, t3StatsNoContextReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := t3StatsServe(t, tc.id, tc.report, true)
			if !reflect.DeepEqual(t3StatsJSON(t, out), t3StatsJSON(t, tc.report)) {
				t.Fatal("CLI JSON differs from complete frozen HTTP-shaped fixture")
			}
			// The same fixed oracle must also round-trip through the immutable
			// DTO; prevents drifting to a CLI-only shape or silently lost fields.
			var dto opapi.RouteStats
			if err := json.Unmarshal([]byte(tc.report), &dto); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(dto)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(t3StatsJSON(t, string(encoded)), t3StatsJSON(t, out)) {
				t.Fatal("CLI JSON lost shared DTO fields or null/empty-array semantics")
			}
		})
	}
}

func t3StatsLine(t *testing.T, text, marker string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, marker) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one line containing %q, got %d", marker, len(found))
	}
	return found[0]
}

func t3StatsContains(t *testing.T, text string, tokens ...string) {
	t.Helper()
	for _, token := range tokens {
		if !strings.Contains(text, token) {
			t.Errorf("human report missing %q", token)
		}
	}
}

// Compare complete whitespace-delimited field values on the selected line.
// A correct prefix, or a duplicate key with a different value, is insufficient.
func t3StatsTokens(t *testing.T, line string, tokens ...string) {
	t.Helper()
	for _, token := range tokens {
		key, want, ok := strings.Cut(token, "=")
		if !ok {
			t.Fatal("test oracle requires key=value tokens")
		}
		matches, got := 0, ""
		for _, field := range strings.Fields(line) {
			fieldKey, value, hasValue := strings.Cut(field, "=")
			if hasValue && fieldKey == key {
				matches++
				got = value
			}
		}
		if matches != 1 || got != want {
			t.Errorf("human report requires exactly one complete token %q", token)
		}
	}
}

func TestRouteStatsHumanOutputPopulations(t *testing.T) {
	out, _ := t3StatsServe(t, t3StatsPeer, t3StatsReport, false)
	lower := strings.ToLower(out)
	t3StatsContains(t, lower, "sample: newest 1000 per route / 168h", "route-wide", "shared principal", "retained delivery evidence", "separate population", "updated_at", "claims")
	t3StatsContains(t, out, "subject=peer:"+t3StatsPeer, "2030-01-03T12:00:00.000Z", "2030-01-10T12:00:00.000Z")
	contextLine := t3StatsLine(t, lower, "retained delivery evidence")
	t3StatsContains(t, contextLine, t3StatsPeer)
	t3StatsTokens(t, contextLine, "total=901", "with_push_evidence=500", "accepted_without_push_evidence=2")
	principal := t3StatsLine(t, out, t3StatsRoute)
	t3StatsContains(t, principal, "owner=principal:prn_00000000000000000000000010", "kind=http", "host=stats.example", "attempt_scope=route",
		"last_used_at=2030-01-10T11:59:00.000Z", "last_success_at=2030-01-10T11:59:00.000Z", "last_error=http_429", "last_error_at=2030-01-10T11:58:00.000Z")
	t3StatsTokens(t, principal, "requests=300", "coalesced=0", "ok=298", "fail=1", "refused=1", "distinct_ids=299", "last_status=202",
		"body_bytes_avg=150.5", "body_bytes_p95=285", "body_bytes_p99=297", "body_bytes_samples=300", "latency_ms_avg=12.347", "latency_ms_p95=25", "latency_ms_p99=40", "latency_ms_samples=300")
	zero := t3StatsLine(t, out, "route_00000000000000000000000012")
	t3StatsContains(t, zero, "owner=peer:"+t3StatsPeer, "kind=grokbot", "host=api2.cursor.sh", "last_error=-", "last_error_at=-")
	t3StatsTokens(t, zero, "requests=1", "coalesced=6", "last_status=204")
	empty := t3StatsLine(t, out, t3StatsSub)
	t3StatsContains(t, empty, "owner=peer:"+t3StatsPeer, "kind=events", "host=events.example", "last_used_at=-", "last_success_at=-", "last_error=-", "last_error_at=-", "last_status=-")
	t3StatsTokens(t, empty, "requests=0", "coalesced=0", "distinct_ids=0")
	for _, prefix := range []string{"body_bytes", "latency_ms"} {
		for _, metric := range []string{"avg", "p95", "p99"} {
			key := prefix + "_" + metric
			if !regexp.MustCompile(`(?:^|\s)` + key + `=0(?:\.0+)?(?:\s|$)`).MatchString(zero) {
				t.Errorf("measured zero lost for %s", key)
			}
			t3StatsTokens(t, empty, key+"=-")
		}
		t3StatsTokens(t, zero, prefix+"_samples=1")
		t3StatsTokens(t, empty, prefix+"_samples=0")
	}
	for _, bad := range []string{"total traffic", "total_traffic", "pulled", "processed=", "replied=", "https://", ":9443", "/private-path"} {
		if strings.Contains(lower, bad) {
			t.Errorf("human report contains forbidden traffic claim or URL material %q", bad)
		}
	}
	t.Run("untrusted-fields-stay-on-one-safe-line", func(t *testing.T) {
		// Test every string location independently so outputting one unsanitized
		// metadata/last/context field cannot hide behind another safe field.
		mutations := []struct {
			name string
			set  func(*opapi.RouteStats, string)
		}{
			{"subject-kind", func(v *opapi.RouteStats, s string) { v.Subject.Kind = s }},
			{"subject-id", func(v *opapi.RouteStats, s string) { v.Subject.ID = s }},
			{"since", func(v *opapi.RouteStats, s string) { v.Window.Since = s }},
			{"until", func(v *opapi.RouteStats, s string) { v.Window.Until = s }},
			{"context-peer", func(v *opapi.RouteStats, s string) { v.RetainedDeliveries.PeerIDs[0] = s }},
			{"route-id", func(v *opapi.RouteStats, s string) { v.Routes[0].RouteID = s }},
			{"kind", func(v *opapi.RouteStats, s string) { v.Routes[0].Kind = s }},
			{"host", func(v *opapi.RouteStats, s string) { v.Routes[0].Host = s }},
			{"owner-kind", func(v *opapi.RouteStats, s string) { v.Routes[0].OwnerKind = s }},
			{"owner-id", func(v *opapi.RouteStats, s string) { v.Routes[0].OwnerID = s }},
			{"scope", func(v *opapi.RouteStats, s string) { v.Routes[0].AttemptScope = s }},
			{"last-used", func(v *opapi.RouteStats, s string) { v.Routes[0].LastUsedAt = &s }},
			{"last-success", func(v *opapi.RouteStats, s string) { v.Routes[0].LastSuccessAt = &s }},
			{"last-error", func(v *opapi.RouteStats, s string) { v.Routes[0].LastError = &s }},
			{"last-error-at", func(v *opapi.RouteStats, s string) { v.Routes[0].LastErrorAt = &s }},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				var dto opapi.RouteStats
				if err := json.Unmarshal([]byte(t3StatsReport), &dto); err != nil {
					t.Fatal(err)
				}
				mutation.set(&dto, "visible\r\nINJECTED-LINE\t\x1b]0;title\a\x7f\u009b\u2028\u2029\u202e\u2066end")
				raw, err := json.Marshal(dto)
				if err != nil {
					t.Fatal(err)
				}
				human, _ := t3StatsServe(t, t3StatsPeer, string(raw), false)
				if len(strings.Split(human, "\n")) != len(strings.Split(out, "\n")) {
					t.Fatal("untrusted field injected a terminal line")
				}
				for _, line := range strings.Split(human, "\n") {
					for _, r := range line {
						if (unicode.IsControl(r) && r != '\t') || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
							t.Fatal("human output contains terminal control or invisible format rune")
						}
					}
					if strings.HasPrefix(line, "INJECTED-LINE") {
						t.Fatal("untrusted field forged a report line")
					}
				}
			})
		}
	})
}

func TestCoalescedCLIInt64AndOldServer(t *testing.T) {
	for _, tc := range []struct{ name, report, token string }{
		{"int64", strings.Replace(t3StatsReport, `"coalesced":6`, `"coalesced":4294967296`, 1), "coalesced=4294967296"},
		{"old-server-omission", strings.ReplaceAll(strings.ReplaceAll(t3StatsReport, `"coalesced":0,`, ""), `"coalesced":6,`, ""), "coalesced=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := t3StatsServe(t, t3StatsPeer, tc.report, false)
			t3StatsTokens(t, t3StatsLine(t, out, "route_00000000000000000000000012"), tc.token)
			out, _ = t3StatsServe(t, t3StatsPeer, tc.report, true)
			var dto opapi.RouteStats
			if err := json.Unmarshal([]byte(out), &dto); err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if tc.name == "int64" {
				want = 4294967296
			}
			if dto.Routes[1].Coalesced != want || dto.Routes[0].Coalesced != 0 || dto.Routes[2].Coalesced != 0 {
				t.Fatalf("CLI decoded coverage=%+v", dto.Routes)
			}
		})
	}
}
