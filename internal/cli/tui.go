package cli

import (
	"errors"
	"net/http"
	"os"

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
	err = tui.Run(a.ctx, tui.Options{API: opclient.New(c.base, c.secret), Profile: profile, Hub: c.base, In: a.stdin, Out: out})
	if errors.Is(err, opclient.ErrForbidden) {
		return &hubError{Status: http.StatusForbidden, Code: "forbidden", Message: err.Error()}
	}
	return err
}
