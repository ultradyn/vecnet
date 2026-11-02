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
	"testing"
	"time"
)

func TestDemoControls(t *testing.T) {
	c := &controls{}
	handler := c.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}
	before := c.now()
	if r := call("/__demo/advance", `{"by":"15m"}`); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if d := c.now().Sub(before); d < 15*time.Minute || d > 15*time.Minute+time.Second {
		t.Fatal("incorrect clock advance")
	}
	if r := call("/__demo/advance", `{"by":"oops"}`); r.Code != 400 {
		t.Fatal("invalid duration accepted")
	}
	if r := call("/__demo/fail", `{"prefix":"/v1/peers","status":503,"for":"40ms"}`); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if r := call("/v1/peers", ""); r.Code != 503 {
		t.Fatal("prefix failure not applied")
	}
	if r := call("/v1/messages", ""); r.Code != 204 {
		t.Fatal("unrelated endpoint failed")
	}
	time.Sleep(60 * time.Millisecond)
	if r := call("/v1/peers", ""); r.Code != 204 {
		t.Fatal("failure did not expire")
	}
	if r := call("/__demo/stall", `{"for":"40ms"}`); r.Code != 200 {
		t.Fatal(r.Code)
	}
	start := time.Now()
	call("/v1/peers", "")
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("stall not applied")
	}
	if r := call("/__demo/fail", `{"prefix":"/ui/","status":500,"for":"1s"}`); r.Code != 400 {
		t.Fatal("failure may only affect /v1")
	}
}

func startDemo(t *testing.T, dir string, extra ...string) (manifest, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir}, extra...)
	go func() { err := run(ctx, args, writer); writer.CloseWithError(err); done <- err }()
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
	stop := func() {
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
