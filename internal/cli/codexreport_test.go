package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

type capturedCodexReport struct {
	method, authorization string
	query                 url.Values
	body                  map[string]any
	decodeErr             error
}
type codexReportCapture struct {
	mu       sync.Mutex
	requests []capturedCodexReport
}

// Accept every report: an unintended POST must not appear to be a refusal just
// because the fixture returned 404. Install a new server before binding rather
// than replacing a running server's Handler. Captures are immutable after append.
func newCodexReportEndpoint(f *bindFixture, hook func(*http.Request)) *codexReportCapture {
	f.t.Helper()
	capture := &codexReportCapture{}
	identity := f.server.Config.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtime-status" {
			if hook != nil {
				hook(r)
			}
			identity.ServeHTTP(w, r)
			return
		}
		report := capturedCodexReport{method: r.Method, authorization: r.Header.Get("Authorization"), query: r.URL.Query()}
		report.decodeErr = json.NewDecoder(r.Body).Decode(&report.body)
		capture.mu.Lock()
		capture.requests = append(capture.requests, report)
		capture.mu.Unlock()
		if hook != nil {
			hook(r)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	f.t.Cleanup(server.Close)
	f.hub = server.URL
	f.config(f.hub)
	return capture
}
func (c *codexReportCapture) snapshot() []capturedCodexReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedCodexReport(nil), c.requests...)
}
func reportArgs(f *bindFixture) []string {
	f.t.Helper()
	b := f.entries()[0]
	return []string{"report-status", "--thread", bindValue[string](f.t, b, "thread_id"), "--codex-home", f.home,
		"--expect-generation", bindValue[string](f.t, b, "generation"), "--expect-hub", f.hub, "--expect-caller", bindValue[string](f.t, b, "caller")}
}
func reportRunContext(ctx context.Context, f *bindFixture, input string, args []string) (string, error) {
	var out bytes.Buffer
	a := &app{ctx: ctx, stdin: strings.NewReader(input), stdout: &out, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
	rest, _, err := a.parseGlobal(args)
	if err == nil {
		now := f.now
		err = a.cmdCodex(rest, func() time.Time { return now })
	}
	return out.String(), err
}
func reportRun(f *bindFixture, input string, args []string) (string, error) {
	return reportRunContext(f.t.Context(), f, input, args)
}
func assertNoCodexReport(t *testing.T, capture *codexReportCapture, out string, err error) {
	t.Helper()
	if got := capture.snapshot(); len(got) != 0 {
		t.Fatalf("refused report reached accepting endpoint: %+v; out=%q err=%v", got, out, err)
	}
	if err == nil || strings.Contains(out, `"reported":true`) {
		t.Fatalf("report was not refused: out=%q err=%v", out, err)
	}
}
func assertReportLeavesBinding(t *testing.T, f *bindFixture, before []byte) {
	t.Helper()
	if !bytes.Equal(before, f.raw()) {
		t.Fatal("report changed binding authority")
	}
	state, err := statePath(func(k string) string { return f.env[k] }, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("report created delivery state: %v", err)
	}
	markers, err := filepath.Glob(f.path + ".*.pending")
	if err != nil || len(markers) != 0 {
		t.Fatalf("report created pending send: %v, %v", markers, err)
	}
}

const validCodexReport = `{"state":"idle","delivery":"ready","version":"0.169.0"}`

func TestCodexReportExactOwnerAndBoundedLease(t *testing.T) {
	f := newBindFixture(t)
	capture := newCodexReportEndpoint(f, nil)
	f.bind("p", bindT1)
	before, args := f.raw(), reportArgs(f)
	out, err := reportRun(f, validCodexReport, args)
	if err != nil || out != "{\"reported\":true}\n" {
		t.Fatalf("report: out=%q err=%v", out, err)
	}
	reports := capture.snapshot()
	if len(reports) != 1 {
		t.Fatalf("reports: %+v", reports)
	}
	want := map[string]any{"runtime": "codex", "state": "idle", "delivery": "ready", "version": "0.169.0", "ttl_seconds": float64(90)}
	if reports[0].decodeErr != nil || !reflect.DeepEqual(reports[0].body, want) {
		t.Fatalf("public report: %+v", reports[0])
	}
	if reports[0].method != http.MethodPost || len(reports[0].query) != 0 || reports[0].authorization != "Bearer vn_peer_test" {
		t.Fatalf("report request: %+v", reports[0])
	}
	assertReportLeavesBinding(t, f, before)
	expires := f.now.Add(24 * time.Hour)
	f.now = expires.Add(-4 * time.Second)
	if _, err := reportRun(f, `{"state":"offline","delivery":"unavailable","reason":"stopped"}`, args); err != nil {
		t.Fatal(err)
	}
	reports = capture.snapshot()
	if len(reports) != 2 || reports[1].body["ttl_seconds"] != float64(1) {
		t.Fatalf("lease exceeded: %+v", reports)
	}
	for _, remaining := range []time.Duration{3 * time.Second, 0} {
		f.now = expires.Add(-remaining)
		out, err := reportRun(f, validCodexReport, args)
		if err == nil || strings.Contains(out, `"reported":true`) || len(capture.snapshot()) != 2 {
			t.Fatalf("owner with %v remaining reported: out=%q err=%v", remaining, out, err)
		}
	}
	assertReportLeavesBinding(t, f, before)
}
func TestCodexReportRefusesReplacementAndInvalidSnapshots(t *testing.T) {
	f := newBindFixture(t)
	capture := newCodexReportEndpoint(f, nil)
	f.bind("p", bindT1)
	before, args := f.raw(), reportArgs(f)
	for _, tc := range []struct{ name, input string }{
		{"null", `null`},
		{"unknown field", `{"state":"idle","delivery":"ready","note":"private"}`},
		{"offline ready", `{"state":"offline","delivery":"ready"}`},
		{"offline unsupported", `{"state":"offline","delivery":"unsupported"}`},
		{"blocked ready", `{"state":"blocked","delivery":"ready"}`},
		{"duplicate state", `{"state":"idle","state":"working","delivery":"ready"}`},
		{"case folded state", `{"State":"idle","delivery":"ready"}`},
		{"unknown state", `{"state":"unknown","delivery":"ready"}`},
		{"unknown delivery", `{"state":"idle","delivery":"unknown"}`},
		{"private reason", `{"state":"blocked","delivery":"unavailable","reason":"/private/thread"}`},
		{"empty reason", `{"state":"idle","delivery":"ready","reason":""}`},
		{"null reason", `{"state":"idle","delivery":"ready","reason":null}`},
		{"bad version", `{"state":"idle","delivery":"ready","version":"bogus"}`},
		{"leading zero prerelease", `{"state":"idle","delivery":"ready","version":"1.2.3-01"}`},
		{"empty version", `{"state":"idle","delivery":"ready","version":""}`},
		{"null version", `{"state":"idle","delivery":"ready","version":null}`},
		{"trailing document", `{"state":"idle","delivery":"ready"} {}`},
		{"oversize", strings.Repeat(" ", 1100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Give each negative case an empty capture, even when a production bug in
			// an earlier case publishes a report. The mutex also covers HTTP handlers.
			capture.mu.Lock()
			capture.requests = nil
			capture.mu.Unlock()
			out, err := reportRun(f, tc.input, args)
			assertNoCodexReport(t, capture, out, err)
			assertReportLeavesBinding(t, f, before)
		})
	}
	capture.mu.Lock()
	capture.requests = nil
	capture.mu.Unlock()
	f.peer.Store("peer_replaced")
	out, err := reportRun(f, validCodexReport, args)
	assertNoCodexReport(t, capture, out, err)
	assertReportLeavesBinding(t, f, before)
}
func TestCodexReportPrincipalTargetsOnlyBoundPeer(t *testing.T) {
	f := newBindFixture(t)
	capture := newCodexReportEndpoint(f, nil)
	if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{"p": {Hub: f.hub, Secret: "vn_principal_test"}}}); err != nil {
		t.Fatal(err)
	}
	f.bind("p", bindT1, "--as", "peer_scoped")
	args := reportArgs(f)
	for _, scope := range [][]string{nil, {"--as", "peer_other"}} {
		out, err := reportRun(f, validCodexReport, append(append([]string{}, args...), scope...))
		assertNoCodexReport(t, capture, out, err)
	}
	out, err := reportRun(f, validCodexReport, append(args, "--net", "p", "--as", "peer_scoped"))
	if err != nil || !strings.Contains(out, `"reported":true`) {
		t.Fatalf("principal report: out=%q err=%v", out, err)
	}
	reports := capture.snapshot()
	if len(reports) != 1 || reports[0].decodeErr != nil || reports[0].method != http.MethodPost || !reflect.DeepEqual(reports[0].query, url.Values{"as": {"peer_scoped"}}) || reports[0].authorization != "Bearer vn_principal_test" {
		t.Fatalf("principal report did not target its peer: %+v", reports)
	}
}
func TestCodexReportRejectsObsoleteGenerationAfterHandoff(t *testing.T) {
	f := newBindFixture(t)
	capture := newCodexReportEndpoint(f, nil)
	f.bind("p", bindT1)
	oldArgs := reportArgs(f)
	generation := bindValue[string](t, f.entries()[0], "generation")
	f.bind("p", bindT2, "--expect-generation", generation)
	before := f.raw()
	out, err := reportRun(f, validCodexReport, oldArgs)
	assertNoCodexReport(t, capture, out, err)
	assertReportLeavesBinding(t, f, before)
	// Returning to the same thread must not revive its obsolete generation.
	generation = bindValue[string](t, f.entries()[0], "generation")
	f.bind("p", bindT1, "--expect-generation", generation)
	before = f.raw()
	out, err = reportRun(f, validCodexReport, oldArgs)
	assertNoCodexReport(t, capture, out, err)
	assertReportLeavesBinding(t, f, before)
	if _, err := reportRun(f, validCodexReport, reportArgs(f)); err != nil {
		t.Fatalf("current owner could not report after handoff: %v", err)
	}
	if len(capture.snapshot()) != 1 {
		t.Fatal("current owner did not publish exactly once")
	}
}
func TestCodexReportWaitsForBindingLock(t *testing.T) {
	f := newBindFixture(t)
	capture := newCodexReportEndpoint(f, nil)
	f.bind("p", bindT1)
	before, args := f.raw(), reportArgs(f)
	unlock, err := lockPath(t.Context(), f.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	out, err := reportRunContext(ctx, f, validCodexReport, args)
	assertNoCodexReport(t, capture, out, err)
	if !errors.Is(err, errStateBusy) {
		t.Fatalf("report did not wait for binding lock: %v", err)
	}
	assertReportLeavesBinding(t, f, before)
}

// This context is passed only to cmdCodex's real binding lock, never to HTTP.
// Both lockPath's Unix retry and platform.Lock's Windows retry consult Done
// only after an OS lock attempt reports contention. All cancellation behavior
// is delegated unchanged to the command's context.
type codexReportLockContext struct {
	context.Context
	contended chan struct{}
	once      sync.Once
}

func (c *codexReportLockContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.contended) })
	return c.Context.Done()
}

func TestCodexReportHoldsBindingLockUntilPostCompletes(t *testing.T) {
	f := newBindFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	identityEntered, identityRelease := make(chan struct{}), make(chan struct{})
	var handoffStarted atomic.Bool
	var postOnce, identityOnce, releaseIdentityOnce sync.Once
	releaseIdentity := func() { releaseIdentityOnce.Do(func() { close(identityRelease) }) }
	capture := newCodexReportEndpoint(f, func(r *http.Request) {
		if r.URL.Path == "/v1/runtime-status" {
			postOnce.Do(func() { close(entered); <-release })
		}
		if r.URL.Path == "/v1/peers/me" && handoffStarted.Load() {
			identityOnce.Do(func() { close(identityEntered) })
			<-identityRelease
		}
	})
	defer close(release)
	defer releaseIdentity()
	f.bind("p", bindT1)
	before, args := f.raw(), reportArgs(f)
	generation := bindValue[string](t, f.entries()[0], "generation")

	done := make(chan error, 1)
	go func() { _, err := reportRun(f, validCodexReport, args); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("report ended before POST: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("report never reached endpoint")
	}
	// Keep the native HTTP/setup allowance separate from lock observations.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	lockCtx := &codexReportLockContext{Context: ctx, contended: make(chan struct{})}
	a := &app{ctx: ctx, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
	handoffDone := make(chan error, 1)
	handoffStarted.Store(true)
	go func() {
		handoffDone <- a.cmdCodexWithBindingLockContext([]string{"bind", "--thread", bindT2, "--expect-generation", generation}, func() time.Time { return f.now }, lockCtx)
	}()
	select {
	case <-identityEntered:
	case err := <-handoffDone:
		t.Fatalf("handoff ended before authentication: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Intentionally withhold the identity body throughout the independent 80ms
	// OS-lock probe. Endpoint entry cannot count as completed authentication or
	// as a waiting handoff, however long identity resolution takes.
	probeCtx, probeCancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	unlock, err := lockPath(probeCtx, f.path+".lock")
	probeCancel()
	if err == nil {
		unlock()
	}
	if !errors.Is(err, errStateBusy) {
		t.Fatalf("report released binding lock before POST completed: %v", err)
	}
	select {
	case <-lockCtx.contended:
		t.Fatal("handoff reached binding lock before identity response was released")
	case err := <-handoffDone:
		t.Fatalf("handoff ended with identity response withheld: %v", err)
	default:
	}
	assertReportLeavesBinding(t, f, before)
	t.Log("phase: POST blocked, independent OS lock busy, identity response withheld")
	releaseIdentity()
	// This signal comes from a failed acquisition by this command's actual
	// binding lock, after codexBindCaller has decoded and validated identity.
	// A bypassed lock ends the handoff instead of satisfying this phase.
	select {
	case <-lockCtx.contended:
	case err := <-handoffDone:
		t.Fatalf("handoff ended before real binding lock contention: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-handoffDone:
		t.Fatalf("handoff passed an in-flight report: %v", err)
	default:
	}
	assertReportLeavesBinding(t, f, before)
	t.Log("phase: authenticated handoff contended on real binding lock, POST still blocked")
	// Release the one blocked handler; the deferred close also covers failures.
	select {
	case release <- struct{}{}:
	case <-time.After(3 * time.Second):
		t.Fatal("report handler did not accept release")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("report did not finish after POST release")
	}
	select {
	case err := <-handoffDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	entries := f.entries()
	if len(entries) != 1 || bindValue[string](t, entries[0], "thread_id") != bindT2 || bindValue[string](t, entries[0], "generation") == generation || bindValue[string](t, entries[0], "caller") != "peer_A" {
		t.Fatalf("waiting handoff did not replace the exact owner: %s", f.raw())
	}
	before = f.raw()
	out, err := reportRun(f, validCodexReport, args)
	if err == nil || strings.Contains(out, `"reported":true`) || len(capture.snapshot()) != 1 {
		t.Fatalf("old owner reported after serialized handoff: out=%q err=%v reports=%+v", out, err, capture.snapshot())
	}
	assertReportLeavesBinding(t, f, before)
	t.Log("phase: same waiting handoff completed after POST release; obsolete report refused without authority changes")
}
func TestCodexReportRechecksLeaseAfterAuthentication(t *testing.T) {
	f := newBindFixture(t)
	var expire atomic.Bool
	var clock atomic.Int64
	clock.Store(f.now.UnixNano())
	capture := newCodexReportEndpoint(f, func(r *http.Request) {
		if r.URL.Path == "/v1/peers/me" && expire.Load() {
			clock.Store(f.now.Add(24 * time.Hour).UnixNano())
		}
	})
	f.bind("p", bindT1)
	args, before := reportArgs(f), f.raw()
	expire.Store(true)
	var out bytes.Buffer
	a := &app{ctx: t.Context(), stdin: strings.NewReader(validCodexReport), stdout: &out, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
	err := a.cmdCodex(args, func() time.Time { return time.Unix(0, clock.Load()).UTC() })
	assertNoCodexReport(t, capture, out.String(), err)
	assertReportLeavesBinding(t, f, before)
}

func TestCodexReportAcceptsBlockedUnsupportedAndAbsentOptionals(t *testing.T) {
	f := newBindFixture(t)
	capture := newCodexReportEndpoint(f, nil)
	f.bind("p", bindT1)
	args := reportArgs(f)
	for _, input := range []string{
		`{"state":"idle","delivery":"ready"}`,
		`{"state":"blocked","delivery":"unsupported","reason":"refused_version","version":"1.2.3-dev.1+build.5"}`,
	} {
		out, err := reportRun(f, input, args)
		if err != nil || !strings.Contains(out, `"reported":true`) {
			t.Fatalf("valid report %s: out=%q err=%v", input, out, err)
		}
	}
	reports := capture.snapshot()
	if len(reports) != 2 || reports[0].decodeErr != nil || reports[1].decodeErr != nil {
		t.Fatalf("reports: %+v", reports)
	}
	if _, exists := reports[0].body["reason"]; exists {
		t.Fatal("absent reason was invented")
	}
	if _, exists := reports[0].body["version"]; exists {
		t.Fatal("absent version was invented")
	}
	if reports[1].body["state"] != "blocked" || reports[1].body["delivery"] != "unsupported" || reports[1].body["reason"] != "refused_version" || reports[1].body["version"] != "1.2.3-dev.1+build.5" {
		t.Fatalf("unsupported runtime report changed: %+v", reports[1])
	}
}

func TestCodexReportDispatchDoesNotFollowSignedRelocation(t *testing.T) {
	for _, stage := range []string{"authentication", "post"} {
		t.Run(stage, func(t *testing.T) {
			f := newBindFixture(t)
			// Run exercises real dispatch and its real clock, unlike f.run.
			f.now = time.Now().UTC()
			key := testIdentityKey(t, "1")
			var destinationCalls, sourceReports atomic.Int32
			destination := httptest.NewUnstartedServer(nil)
			destination.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls.Add(1)
				if r.URL.Path == "/v1/hub/identity" {
					sig, err := key.SignChallenge(destination.URL, r.URL.Query().Get("challenge"))
					if err != nil {
						t.Error(err)
					}
					_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: destination.URL, ChallengeSig: sig})
					return
				}
				if r.URL.Path == "/v1/runtime-status" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": "peer_A"})
			})
			destination.Start()
			t.Cleanup(destination.Close)
			var moving atomic.Bool
			var notice envelope.HubNotice
			source := httptest.NewUnstartedServer(nil)
			source.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/hub/identity" {
					sig, err := key.SignChallenge(source.URL, r.URL.Query().Get("challenge"))
					if err != nil {
						t.Error(err)
					}
					_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: source.URL, ChallengeSig: sig})
					return
				}
				if r.URL.Path == "/v1/runtime-status" {
					sourceReports.Add(1)
				}
				movedHere := stage == "authentication" && r.URL.Path == "/v1/peers/me" || stage == "post" && r.URL.Path == "/v1/runtime-status"
				if moving.Load() && movedHere {
					w.WriteHeader(http.StatusGone)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": "moved", "details": map[string]any{"notice": notice}}})
					return
				}
				if r.URL.Path == "/v1/peers/me" {
					_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": "peer_A"})
					return
				}
				if r.URL.Path == "/v1/runtime-status" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				http.NotFound(w, r)
			})
			source.Start()
			t.Cleanup(source.Close)
			notice = envelope.HubNotice{NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: source.URL, NewURL: destination.URL, Message: "move", IssuedAt: envelope.FormatTime(f.now.Add(-time.Second)), ExpiresAt: envelope.FormatTime(f.now.Add(time.Hour))}
			if err := key.SignNotice(&notice); err != nil {
				t.Fatal(err)
			}
			f.hub = source.URL
			if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{"p": {Hub: source.URL, HubPubkey: key.Pubkey(), Secret: "vn_peer_test"}}}); err != nil {
				t.Fatal(err)
			}
			f.bind("p", bindT1)
			args, bindingBefore := reportArgs(f), f.raw()
			configBefore := pinRead(t, f.env["VECNET_CONFIG"])
			pinsFile, err := pinsPath(func(k string) string { return f.env[k] })
			if err != nil {
				t.Fatal(err)
			}
			pinsBefore, pinsErr := os.ReadFile(pinsFile)
			if pinsErr != nil && !os.IsNotExist(pinsErr) {
				t.Fatal(pinsErr)
			}
			moving.Store(true)
			var out, errs bytes.Buffer
			code := Run(append([]string{"codex"}, args...), strings.NewReader(validCodexReport), &out, &errs, func(k string) string { return f.env[k] })
			if code != 2 || !strings.Contains(errs.String(), "hub_moved") || strings.Contains(out.String(), `"reported":true`) {
				t.Fatalf("dispatch: code=%d out=%q stderr=%q", code, out.String(), errs.String())
			}
			if destinationCalls.Load() != 0 || !bytes.Equal(configBefore, pinRead(t, f.env["VECNET_CONFIG"])) {
				t.Fatalf("exact owner followed relocation: destination calls=%d", destinationCalls.Load())
			}
			pinsAfter, afterErr := os.ReadFile(pinsFile)
			if !bytes.Equal(pinsBefore, pinsAfter) || os.IsNotExist(pinsErr) != os.IsNotExist(afterErr) || afterErr != nil && !os.IsNotExist(afterErr) {
				t.Fatalf("report changed trust sidecar: before=%v after=%v", pinsErr, afterErr)
			}
			wantPosts := int32(0)
			if stage == "post" {
				wantPosts = 1
			}
			if sourceReports.Load() != wantPosts {
				t.Fatalf("source reports=%d, want %d", sourceReports.Load(), wantPosts)
			}
			assertReportLeavesBinding(t, f, bindingBefore)
		})
	}
}
