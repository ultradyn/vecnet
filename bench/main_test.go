//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

// Exercise the harness against real HTTP handlers, migrations and SQLite,
// including spill, event consumption, ack, receipts and presence fan-out.
func TestHarnessSmoke(t *testing.T) {
	env := testhub.Start(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bench/storage":
			s, e := readStorage(env.Hub.DB, env.Hub.Cfg.DataDir)
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(s)
		case "/bench/snapshot":
			_ = json.NewEncoder(w).Encode(readSnapshot())
		default:
			proxyReq := r.Clone(r.Context())
			proxyReq.RequestURI = ""
			proxyReq.URL.Scheme = "http"
			proxyReq.URL.Host = env.URL[len("http://"):]
			resp, e := http.DefaultClient.Do(proxyReq)
			if e != nil {
				t.Error(e)
				w.WriteHeader(502)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			var body json.RawMessage
			if resp.StatusCode != 204 {
				if e = json.NewDecoder(resp.Body).Decode(&body); e == nil {
					_, _ = w.Write(body)
				}
			}
		}
	}))
	defer srv.Close()
	c := &client{srv.URL, env.OperatorSecret, &http.Client{Timeout: 10 * time.Second}}
	v, e := measureSends(c, 16384, 2, 2, time.Nanosecond, 6)
	if e != nil {
		t.Fatal(e)
	}
	got := v.(map[string]any)
	n := got["accepted"].(int)
	if n < 6 || got["spilled"].(int) != n {
		t.Fatalf("accepted/spilled = %v/%v", got["accepted"], got["spilled"])
	}
	delta := got["storage"].(storage)
	if delta.BlobBytes != int64(n*16384) || delta.Rows["deliveries"] != int64(n*2) {
		t.Fatalf("blob bytes %d, deliveries %d", delta.BlobBytes, delta.Rows["deliveries"])
	}
	if got["presence"].(map[string]any)["events"].(int64) < 200 {
		t.Fatal("presence fanout missing")
	}
	var processed int
	if e = env.Hub.DB.QueryRow("SELECT count(*) FROM deliveries WHERE state='processed'").Scan(&processed); e != nil || processed != n*2 {
		t.Fatalf("processed %d, err %v", processed, e)
	}
}
