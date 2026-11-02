package main

import (
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
)

func TestDemoControls(t *testing.T) {
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
	go func() { err := run(ctx, args, writer); writer.CloseWithError(err); done <- err }()
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
	info, err := os.Stat(filepath.Join(dir, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential manifest is not private")
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
			URL string `json:"url"`
		} `json:"routes"`
	}
	err = json.NewDecoder(response.Body).Decode(&routes)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || len(routes.Routes) != 1 || routes.Routes[0].URL != second.URL+"/__demo/sink/push" {
		t.Fatal("saved push route does not point to the reopened demo")
	}
	stop()
	if first.OperatorPeer != second.OperatorPeer || first.OperatorSecret != second.OperatorSecret || first.Threads["hark"] != second.Threads["hark"] {
		t.Fatal("saved scenario was regenerated")
	}
	third, stop := startDemo(t, dir, "--reset")
	stop()
	if third.OperatorPeer == first.OperatorPeer || third.OperatorSecret == first.OperatorSecret {
		t.Fatal("reset did not create a new scenario")
	}
}

func TestDemoRefusesUnownedDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.db"), []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--data-dir", dir, "--reset"}, io.Discard); err == nil {
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
