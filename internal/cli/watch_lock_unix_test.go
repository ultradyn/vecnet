//go:build unix

package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestWatchDefersWhenLocked(t *testing.T) {
	env := testhub.Start(t)
	recipient, secret := env.NewPeer("session", "Watcher")
	_, senderSecret := env.NewPeer("session", "Sender")
	target, err := url.Parse(env.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	opened := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events" && r.Header.Get("Accept") == "text/event-stream" {
			defer func() {
				select {
				case closed <- struct{}{}:
				default:
				}
			}()
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
	lockInfo, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	out, stop := startWatch(t, u, "watch", "--for-monitor")
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("watch never opened its live stream")
	}
	sent := sender.send(recipient, "locked live delivery", "--hold")
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("locked monitor did not stop its transport at the retained delivery")
	}
	if strings.Contains(out.String(), "locked live delivery") {
		t.Fatal("held shared lock authorized monitor output")
	}
	select {
	case <-opened:
		t.Fatal("monitor reopened transport past a locked delivery")
	default:
	}
	state := filepath.Join(filepath.Dir(path), "p.json")
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("busy lock wrote state: %v", err)
	}
	if current, err := os.Stat(path); err != nil || !os.SameFile(lockInfo, current) {
		t.Fatal("deferral replaced the shared lock inode")
	}
	// No state writer, event or hub notification assists this fresh watch.
	// Releasing the exact held lock must allow its initial state bootstrap
	// and retained delivery to complete locally exactly once.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	waitLine(t, out, regexp.MustCompile(`locked live delivery$`), 10*time.Second)
	stop()
	if strings.Count(out.String(), "locked live delivery") != 1 {
		t.Fatal("unlock skipped or duplicated the retained delivery")
	}
	if current, err := os.Stat(path); err != nil || !os.SameFile(lockInfo, current) {
		t.Fatal("unlock/retry replaced the shared lock inode")
	}
	raw, err := os.ReadFile(state)
	var proof struct {
		Caller    string   `json:"caller"`
		Displayed []string `json:"displayed"`
	}
	if err != nil || json.Unmarshal(raw, &proof) != nil || proof.Caller != recipient {
		t.Fatal("unlocked monitor did not establish its verified caller state")
	}
	found := false
	for _, id := range proof.Displayed {
		if id == sent.Deliveries[0].DeliveryID {
			found = true
		}
	}
	if !found {
		t.Fatal("unlocked monitor failed to publish exact successful display proof")
	}
}
