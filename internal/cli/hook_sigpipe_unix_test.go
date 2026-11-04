//go:build unix

package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

// A closed native stdout or stderr must not kill the hook with SIGPIPE:
// the write fails with EPIPE and the hook still exits 0.
func TestHookClosedOutputExitsZero(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if stream == "stderr" {
					// A purged cursor produces a stderr diagnostic.
					w.WriteHeader(409)
					fmt.Fprint(w, `{"error":{"code":"cursor_purged","details":{}}}`)
					return
				}
				hookPage(w, []envelope.Event{hookEvent(1, "closed stdout")}, "e1.1")
			})
			seedHook(t, path, "e1.0")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = reader.Close() // no reader: every write gets EPIPE
			defer writer.Close()
			cmd := hookProcess(t, env)
			if stream == "stdout" {
				cmd.Stdout = writer
			} else {
				cmd.Stderr = writer
			}
			err = cmd.Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) || err != nil {
				t.Fatalf("closed %s: %v", stream, err)
			}
			if stream == "stdout" {
				if s := readHookState(t, path); s.PollCursor != "e1.0" || len(s.Shown) != 0 {
					t.Fatalf("failed write consumed delivery: %+v", s)
				}
			}
		})
	}
}
