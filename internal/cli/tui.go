package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui"
)

// cmdTUI is `vecnet tui`: the operator's terminal client on the --net profile.
func (a *app) cmdTUI(args []string) error {
	o, err := parseFlags("tui", args, map[string]flagKind{})
	if err != nil {
		return err
	}
	if len(o.pos) > 0 {
		return usageErr("usage: vecnet tui")
	}
	out, ok := a.stdout.(*os.File)
	if !ok || !term.IsTerminal(out.Fd()) {
		return usageErr("vecnet tui needs a terminal on stdout")
	}
	c, profile, _, err := a.connect()
	if err != nil {
		return err
	}
	beatEvery, warning := tuiBeatOverride(a.getenv)
	if warning != "" {
		fmt.Fprintln(a.stderr, warning)
	}
	err = tui.Run(a.ctx, tui.Options{BeatEvery: beatEvery, API: opclient.New(c.base, c.secret), Profile: profile, Hub: c.base, In: a.stdin, Out: out})
	if errors.Is(err, opclient.ErrForbidden) {
		return &hubError{Status: http.StatusForbidden, Code: "forbidden", Message: err.Error()}
	}
	return err
}

// tuiBeatOverride lets the screen harness exercise the real presence heartbeat
// without changing the production cadence.
func tuiBeatOverride(getenv func(string) string) (time.Duration, string) {
	value := getenv("VECNET_TEST_TUI_BEAT")
	if value == "" {
		return 0, ""
	}
	beat, err := time.ParseDuration(value)
	if err != nil || beat < 5*time.Second || beat > 20*time.Minute {
		return 0, fmt.Sprintf("ignoring VECNET_TEST_TUI_BEAT=%s: want a duration from 5s to 20m", value)
	}
	return beat, ""
}
