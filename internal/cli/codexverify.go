package cli

import (
	"context"
	"time"
)

// Verify authenticates readiness without requiring or creating a delivery claim.
// The send path still takes its own guard immediately before each RPC.
func (a *app) cmdCodexVerify(args []string, nowTime func() time.Time) error {
	o, err := parseFlags("codex verify", args, map[string]flagKind{"thread": strFlag, "codex-home": strFlag, "expect-generation": strFlag, "expect-hub": strFlag, "expect-caller": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("codex verify: unexpected arguments")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	worker := *a
	worker.ctx = ctx
	worker.inHook = true // Verification must not perform first-use trust writes.
	_, unlock, err := worker.lockCodexOwner()
	if err != nil {
		return err
	}
	defer unlock()
	b, err := worker.codexOwner(o, nowTime())
	if err != nil {
		return err
	}
	c, _, _, err := worker.connect()
	if err != nil {
		return err
	}
	caller, err := worker.codexBindCaller(c)
	if err != nil {
		return err
	}
	if normalizeHub(c.base) != b.Hub || caller != b.Caller {
		return usageErr("Codex identity changed")
	}
	if _, err := worker.codexOwner(o, nowTime()); err != nil {
		return err
	}
	return hookWrite(ctx, a.stdout, []byte("{\"verified\":true}\n"))
}
