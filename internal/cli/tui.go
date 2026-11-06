package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui"
)

// pickOperator is the profile picker. Tests replace it; the TUI never prints a secret.
var pickOperator = func(ctx context.Context, choices []tui.ProfileChoice, in io.Reader, out io.Writer) (tui.PickResult, error) {
	return tui.Pick(ctx, choices, in, out)
}

// errTUICancel leaves the process with exit 0 and writes nothing. It is not a
// usage error.
var errTUICancel = errors.New("tui profile pick cancelled")

// cmdTUI is `vecnet tui`: the operator's terminal client on the --net profile.
func (a *app) cmdTUI(args []string) error {
	o, err := parseFlags("tui", args, map[string]flagKind{"pick": boolFlag})
	if err != nil {
		return err
	}
	if len(o.pos) > 0 {
		return usageErr("usage: vecnet tui [--pick]")
	}
	out, ok := a.stdout.(*os.File)
	if !ok || !term.IsTerminal(out.Fd()) {
		return usageErr("vecnet tui needs a terminal on stdout")
	}
	if err := a.chooseTUIProfile(o.has("pick")); err != nil {
		if errors.Is(err, errTUICancel) {
			return nil
		}
		return err
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

// chooseTUIProfile opens the picker for --pick, and for a bare human run whose
// config has no operator_profile. A named profile, or an operator_profile that
// is set (even when it is not an operator credential), keeps the existing
// connect path. Cancelling writes nothing. y rewrites only that one line;
// n uses the choice for this process.
func (a *app) chooseTUIProfile(pick bool) error {
	path, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	if err := cfg.checkDefault(); err != nil {
		return err
	}
	if !pick && (a.namedProfile() != "" || cfg.OperatorProfile != "") {
		return nil
	}
	if !pick {
		r := detectRunner(a.getenv, isTerminal(a.stdin), isTerminal(a.stdout))
		if r.agent {
			return bareAgentErr(r)
		}
	}
	choices := operatorProfiles(cfg)
	if len(choices) == 0 {
		return usageErr("vecnet tui: no operator profile in the config. A profile needs a vn_operator_ credential; pass --net <profile> or enrol one")
	}
	res, err := pickOperator(a.ctx, choices, a.stdin, a.stdout)
	if err != nil {
		return err
	}
	if res.Cancelled || res.Name == "" {
		return errTUICancel
	}
	if res.MakeDefault {
		if err := setOperatorProfile(path, res.Name); err != nil {
			return err
		}
	}
	a.net = res.Name
	return nil
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
