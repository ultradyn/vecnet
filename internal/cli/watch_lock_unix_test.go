//go:build unix

package cli_test

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestWatchFailsOpenWhenLocked(t *testing.T) {
	env := testhub.Start(t)
	recipient, secret := env.NewPeer("session", "Watcher")
	_, senderSecret := env.NewPeer("session", "Sender")
	target, err := url.Parse(env.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	opened := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events" && r.Header.Get("Accept") == "text/event-stream" {
			select {
			case opened <- struct{}{}:
			default:
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u := profileUser(t, srv.URL, secret)
	u.env["XDG_CONFIG_HOME"] = filepath.Join(filepath.Dir(u.conf), "xdg")
	sender := profileUser(t, env.URL, senderSecret)
	path := filepath.Join(filepath.Dir(u.conf), "state", "p.json.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	out, stop := startWatch(t, u, "watch", "--for-monitor")
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("watch never opened its live stream")
	}
	sender.send(recipient, "locked live delivery", "--hold")
	waitLine(t, out, regexp.MustCompile(`locked live delivery$`), 10*time.Second)
	stop()
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "p.json")); !os.IsNotExist(err) {
		t.Fatalf("busy lock wrote state: %v", err)
	}
}
