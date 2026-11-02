package cli_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/cli"
)

func TestCmdTUINotATerminal(t *testing.T) {
	t.Run("buffer", func(t *testing.T) {
		u := profileUser(t, "http://127.0.0.1:1", "vn_op_secret")
		code, out, errs := u.run("tui")
		if code != 1 || out != "" || !strings.Contains(errs, "needs a terminal") {
			t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
		}
	})
	t.Run("pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		u := profileUser(t, "http://127.0.0.1:1", "vn_op_secret")
		var errb bytes.Buffer
		code := cli.Run([]string{"tui"}, strings.NewReader(""), w, &errb, u.getenv)
		if code != 1 || !strings.Contains(errb.String(), "needs a terminal") {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
	})
	t.Run("a device that is not a terminal", func(t *testing.T) {
		null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer null.Close()
		u := profileUser(t, "http://127.0.0.1:1", "vn_op_secret")
		var errb bytes.Buffer
		code := cli.Run([]string{"tui"}, strings.NewReader(""), null, &errb, u.getenv)
		if code != 1 || !strings.Contains(errb.String(), "needs a terminal") {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
	})
}

func TestCmdTUIUsage(t *testing.T) {
	u := profileUser(t, "http://127.0.0.1:1", "vn_op_secret")
	if out := u.mustRun("help"); !strings.Contains(out, "\n  tui ") {
		t.Fatalf("usage lacks tui:\n%s", out)
	}
	if code, _, errs := u.run("tui", "extra"); code != 1 || !strings.Contains(errs, "tui") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
}
