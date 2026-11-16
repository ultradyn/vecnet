package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/platform"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

func TestPrefFlag(t *testing.T) {
	p := prefFlags{}
	if err := p.Set("events.refresh_interval=900@VECNET_EVENTS_REFRESH_INTERVAL"); err != nil {
		t.Fatal(err)
	}
	e := p["events.refresh_interval"]
	if n, _ := e.Value.Int64(); n != 900 || e.Source != prefs.SourceFlag || e.Origin != "VECNET_EVENTS_REFRESH_INTERVAL" {
		t.Fatalf("got %+v", e)
	}
	for _, bad := range []string{"events.refresh_interval=900", "no.such_key=1@X", "events.refresh_interval=x@X", "blobs.expiry=1@X"} {
		if p.Set(bad) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestExitOnStdinEOF(t *testing.T) { testStdinEOF(t, true) }

func TestStdinIgnoredWithoutFlag(t *testing.T) { testStdinEOF(t, false) }

func testStdinEOF(t *testing.T, exitOnEOF bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdin, input := io.Pipe()
	defer stdin.Close()
	defer input.Close()
	output, writer := io.Pipe()
	defer output.Close()
	done := make(chan error, 1)
	args := []string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}
	if exitOnEOF {
		args = append(args, "--exit-on-stdin-eof")
	}
	go func() {
		err := run(ctx, args, stdin, writer)
		writer.CloseWithError(err)
		done <- err
	}()
	var m manifest
	ready := make(chan error, 1)
	go func() { ready <- json.NewDecoder(output).Decode(&m) }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("manifest startup timeout")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if !exitOnEOF {
		select {
		case err := <-done:
			t.Fatalf("stdin EOF stopped demo without flag: %v", err)
		case <-time.After(time.Second):
		}
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(m.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health after stdin EOF = %d", resp.StatusCode)
		}
		cancel()
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("demo did not shut down within 7s")
	}
}

func TestDemoControls(t *testing.T) {
	t.Run("wire contract", func(t *testing.T) {
		for _, tc := range []struct {
			name, method, path, body, code string
			status                         int
		}{
			{"method", "GET", "/__demo/advance", "", "method_not_allowed", 405},
			{"unknown endpoint", "POST", "/__demo/missing", `{}`, "not_found", 404},
			{"invalid duration", "POST", "/__demo/advance", `{"by":"invalid"}`, "invalid_body", 400},
			{"zero duration", "POST", "/__demo/stall", `{"for":"0s"}`, "invalid_body", 400},
			{"excessive duration", "POST", "/__demo/stall", `{"for":"169h"}`, "invalid_body", 400},
			{"invalid prefix", "POST", "/__demo/fail", `{"prefix":"/ui/","status":500,"for":"1h"}`, "invalid_body", 400},
			{"invalid status", "POST", "/__demo/fail", `{"prefix":"/v1/peers","status":200,"for":"1h"}`, "invalid_body", 400},
			{"invalid clear", "POST", "/__demo/clear", `{"control":"unknown"}`, "invalid_body", 400},
			{"unknown advance field", "POST", "/__demo/advance", `{"by":"1s","unexpected":true}`, "invalid_body", 400},
			{"unknown stall field", "POST", "/__demo/stall", `{"for":"1s","unexpected":true}`, "invalid_body", 400},
			{"unknown fail field", "POST", "/__demo/fail", `{"prefix":"/v1/peers","status":500,"for":"1h","unexpected":true}`, "invalid_body", 400},
			{"unknown clear field", "POST", "/__demo/clear", `{"control":"stall","unexpected":true}`, "invalid_body", 400},
			{"unknown status field", "POST", "/__demo/status", `{"unexpected":true}`, "invalid_body", 400},
			{"wrong endpoint field", "POST", "/__demo/advance", `{"by":"1s","for":"1s"}`, "invalid_body", 400},
			{"trailing object", "POST", "/__demo/advance", `{"by":"1s"} {}`, "invalid_body", 400},
			{"trailing garbage", "POST", "/__demo/advance", `{"by":"1s"} garbage`, "invalid_body", 400},
			{"malformed", "POST", "/__demo/status", `{`, "invalid_body", 400},
			{"empty", "POST", "/__demo/status", ``, "invalid_body", 400},
			{"null", "POST", "/__demo/status", `null`, "invalid_body", 400},
			{"array", "POST", "/__demo/status", `[]`, "invalid_body", 400},
			{"wrong type", "POST", "/__demo/advance", `{"by":123}`, "invalid_body", 400},
			{"oversize", "POST", "/__demo/status", `{"padding":"` + strings.Repeat("x", 4096) + `"}`, "invalid_body", 400},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c := &controls{}
				handler := c.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("control reached next handler") }))
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
				if w.Code != tc.status {
					t.Errorf("status = %d, want %d", w.Code, tc.status)
				}
				if got := w.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
				var body struct {
					Error struct {
						Code    string         `json:"code"`
						Message string         `json:"message"`
						Details map[string]any `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("invalid error JSON: %v", err)
				}
				if body.Error.Code != tc.code || body.Error.Message == "" || body.Error.Details == nil {
					t.Errorf("incomplete error envelope: %+v", body.Error)
				}
				if c.offset.Load() != 0 || !c.stallUntil.IsZero() || !c.failUntil.IsZero() {
					t.Error("rejected request changed control state")
				}
			})
		}
		for _, tc := range []struct{ path, body, want string }{
			{"/__demo/advance", "{\"by\":\"1s\"} \n\t", "2026-11-02T01:00:01.123Z"},
			{"/__demo/stall", `{"for":"1s"}`, "2026-11-02T01:00:00.123Z"},
			{"/__demo/fail", `{"prefix":"/v1/peers","status":503,"for":"1h"}`, "2026-11-02T01:00:00.123Z"},
		} {
			t.Run(tc.path+" timestamp", func(t *testing.T) {
				c := &controls{wallNow: func() time.Time {
					return time.Date(2026, 11, 2, 12, 0, 0, 123456789, time.FixedZone("test", 11*60*60))
				}}
				w := httptest.NewRecorder()
				c.handler(nil).ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
				var body struct {
					Now string `json:"now"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
					t.Fatalf("invalid success response: status=%d, err=%v", w.Code, err)
				}
				if _, err := envelope.ParseTime(body.Now); err != nil {
					t.Errorf("now is not a wire timestamp: %v", err)
				}
				if body.Now != tc.want {
					t.Errorf("now = %q, want %q", body.Now, tc.want)
				}
			})
		}
	})
	base := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	type waiting struct {
		duration time.Duration
		tick     chan struct{}
	}
	waits := make(chan waiting, 1)
	c := &controls{
		wallNow: func() time.Time { return base.Add(time.Duration(elapsed.Load())) },
		wait: func(ctx context.Context, d time.Duration, release <-chan struct{}) {
			tick := make(chan struct{})
			waits <- waiting{d, tick}
			select {
			case <-tick:
			case <-release:
			case <-ctx.Done():
			}
		},
	}
	handler := c.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	call := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	check := func(path, body string, status int) {
		t.Helper()
		if w := call(path, body); w.Code != status {
			t.Fatalf("%s: got %d, want %d", path, w.Code, status)
		}
	}
	before := c.now()
	check("/__demo/advance", `{"by":"15m"}`, 200)
	if c.now().Sub(before) != 15*time.Minute {
		t.Fatal("incorrect clock advance")
	}
	check("/__demo/advance", `{"by":"oops"}`, 400)
	check("/__demo/fail", `{"prefix":"/v1/peers","status":503,"for":"40ms"}`, 200)
	check("/v1/peers", "", 503)
	check("/v1/messages", "", 204)
	elapsed.Add(int64(39 * time.Millisecond))
	check("/v1/peers", "", 503)
	elapsed.Add(int64(time.Millisecond))
	check("/v1/peers", "", 204)

	check("/__demo/stall", `{"for":"1h"}`, 200)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call("/v1/peers", "") }()
	wait := <-waits
	if wait.duration != time.Hour {
		t.Fatal("incorrect stall duration")
	}
	var status struct {
		Stalled int `json:"stalled_requests"`
	}
	if err := json.Unmarshal(call("/__demo/status", `{}`).Body.Bytes(), &status); err != nil || status.Stalled != 1 {
		t.Fatal("stall was not observed", err)
	}
	select {
	case <-done:
		t.Fatal("stall released early")
	default:
	}
	check("/__demo/clear", `{"control":"stall"}`, 200)
	if w := <-done; w.Code != 204 {
		t.Fatal("cleared request failed")
	}
	check("/v1/peers", "", 204)

	check("/__demo/stall", `{"for":"40ms"}`, 200)
	go func() { done <- call("/v1/peers", "") }()
	wait = <-waits
	if wait.duration != 40*time.Millisecond {
		t.Fatal("incorrect timer duration")
	}
	elapsed.Add(int64(40 * time.Millisecond))
	close(wait.tick)
	if w := <-done; w.Code != 204 {
		t.Fatal("expired stall failed")
	}
	check("/v1/peers", "", 204)
	check("/__demo/fail", `{"prefix":"/ui/","status":500,"for":"1s"}`, 400)
	check("/__demo/clear", `{"control":"unknown"}`, 400)
	check("/__demo/stall", `{"for":"1h"}`, 200)
	go func() { done <- call("/v1/peers", "") }()
	<-waits
	cancel()
	if w := <-done; w.Code == 204 {
		t.Fatal("cancelled stalled request reached the handler")
	}
}

func startDemo(t *testing.T, dir string, extra ...string) (manifest, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir}, extra...)
	go func() { err := run(ctx, args, strings.NewReader(""), writer); writer.CloseWithError(err); done <- err }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			reader.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("shutdown timeout")
			}
		})
	}
	t.Cleanup(stop)
	var m manifest
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(reader).Decode(&m) }()
	select {
	case err := <-decoded:
		if err != nil {
			cancel()
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("startup timeout")
	}

	return m, stop
}

func TestDemoReopensWithStableCredentials(t *testing.T) {
	dir := t.TempDir()
	first, stop := startDemo(t, dir)
	request, err := http.NewRequest("GET", first.URL+"/v1/peers", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+first.OperatorSecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	stop()
	if err := platform.PrivateFile(filepath.Join(dir, "demo.json")); err != nil {
		t.Fatalf("credential manifest is not private: %v", err)
	}
	second, stop := startDemo(t, dir)
	// Reopening on port 0 must repoint the seeded push receiver to this server.
	request, err = http.NewRequest("GET", second.URL+"/v1/admin/routes", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+second.OperatorSecret)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var routes struct {
		Routes []struct {
			Host string `json:"host"` // the list shows hosts only (PROTOCOL §2.12)
		} `json:"routes"`
	}
	err = json.NewDecoder(response.Body).Decode(&routes)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || len(routes.Routes) != 1 || routes.Routes[0].Host != strings.TrimPrefix(second.URL, "http://") {
		t.Fatal("saved push route does not point to the reopened demo")
	}
	stop()
	// The API deliberately redacts paths; inspect the stored callback after
	// shutdown so a correct host cannot hide a missing or changed sink path.
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	var callbackURL string
	err = db.QueryRow(`SELECT url FROM routes`).Scan(&callbackURL)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if want := second.URL + "/__demo/sink/push"; callbackURL != want {
		t.Fatalf("saved push callback = %q, want %q", callbackURL, want)
	}
	if first.OperatorPeer != second.OperatorPeer || first.OperatorSecret != second.OperatorSecret || first.Threads["hark"] != second.Threads["hark"] {
		t.Fatal("saved scenario was regenerated")
	}
	third, stop := startDemo(t, dir, "--reset")
	stop()
	if third.OperatorPeer == first.OperatorPeer || third.OperatorSecret == first.OperatorSecret {
		t.Fatal("reset did not create a new scenario")
	}
}

func TestWriteDemoManifestPrivateReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	if err := os.WriteFile(path, []byte("public old manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"first private credentials\n", "replacement credentials\n"} {
		if err := writeDemoManifest(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if err := platform.PrivateFile(path); err != nil {
			t.Fatalf("published manifest is not private: %v", err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != data {
			t.Fatalf("manifest bytes = %q, %v", got, err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "demo.json" {
			t.Fatalf("manifest publication leaked a temporary file: %v, %v", entries, err)
		}
	}
}

func TestWriteDemoManifestFailureCleansTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeDemoManifest(path, []byte("private credentials")); err == nil {
		t.Fatal("replaced a directory with a credential manifest")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed manifest publication leaked credentials: %v, %v", entries, err)
	}
}

func TestDemoRefusesUnownedDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.db"), []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--data-dir", dir, "--reset"}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("reset accepted an unowned database")
	}
	content, err := os.ReadFile(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "do not change" {
		t.Fatal("existing database changed")
	}
}

func TestDemoControlsCanClearFailure(t *testing.T) {
	c := &controls{wallNow: func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }}
	handler := c.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, step := range []struct {
		path, body string
		status     int
	}{
		{"/__demo/fail", `{"prefix":"/v1/peers","status":503,"for":"1h"}`, 200},
		{"/v1/peers", ``, 503},
		{"/__demo/clear", `{"control":"fail"}`, 200},
		{"/v1/peers", ``, 204},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", step.path, strings.NewReader(step.body)))
		if w.Code != step.status {
			t.Fatalf("%s: got %d, want %d", step.path, w.Code, step.status)
		}
	}
}

func TestDemoRestartControl(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output, writer := io.Pipe()
	defer output.Close()
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		err := run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dir}, strings.NewReader(""), writer)
		writer.CloseWithError(err)
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("demo shutdown timeout")
		}
	})
	var m manifest
	ready := make(chan error, 1)
	go func() { ready <- json.NewDecoder(output).Decode(&m) }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("demo startup timeout")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	open := func() (*http.Response, *bufio.Reader) {
		req, _ := http.NewRequest("GET", m.URL+"/v1/admin/activity", nil)
		req.Header.Set("Authorization", "Bearer "+m.OperatorSecret)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != 200 {
			t.Fatalf("activity = %d", resp.StatusCode)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	frame := func(br *bufio.Reader) string {
		var out strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			out.WriteString(line)
			if line == "\n" {
				return out.String()
			}
		}
	}
	_, br := open()
	if got := frame(br); got != "retry: 3000\n\n" {
		t.Fatalf("retry = %q", got)
	}
	if got := frame(br); got != "event: ready\ndata: {}\n\n" {
		t.Fatalf("ready = %q", got)
	}
	resp, err := client.Post(m.URL+"/__demo/restart", "application/json", strings.NewReader(`{"retry_after_ms":1500}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("restart control = %d %s %v", resp.StatusCode, body, err)
	}
	if string(body) != "{\"streams\":1}\n" {
		t.Fatalf("restart result = %s", body)
	}
	if got := frame(br); got != "event: hub_restarting\nretry: 1500\ndata: {\"retry_after_ms\":1500}\n\n" {
		t.Fatalf("restart frame = %q", got)
	}
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("after restart = %q %v", rest, err)
	}
	_, br = open()
	if got := frame(br); got != "retry: 3000\n\n" {
		t.Fatalf("new retry = %q", got)
	}
	if got := frame(br); got != "event: ready\ndata: {}\n\n" {
		t.Fatalf("new stream = %q", got)
	}
}
