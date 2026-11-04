package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// cmdHookRebind hands a profile's hook to another Claude Code session
// (PROTOCOL §13.9). Run from a session's Bash tool, CLAUDE_CODE_SESSION_ID
// names that session, so it binds directly. Without it (an older Claude
// Code, or a plain terminal) it opens a hookRebindWindow window and the
// next hook run from any session takes the binding; from Bash that is the
// same session's PostToolUse right after this command.
func (a *app) cmdHookRebind(args []string) error {
	o, err := parseFlags("hook rebind", args, map[string]flagKind{"profile": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet hook rebind [--profile P]")
	}
	if o.has("profile") {
		a.net = o.str("profile")
	}
	want := a.namedProfile()
	if want == "" {
		return usageErr("hook rebind: name the agent's profile with --profile P (or --net P, or $VECNET_PROFILE)")
	}
	path, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	name, _, err := cfg.profile(want)
	if err != nil {
		return err
	}
	if v := strings.TrimSpace(a.getenv("VECNET_PROFILE")); v != "" && v != name {
		return usageErr(fmt.Sprintf("hook rebind: this session has VECNET_PROFILE=%s, so its hook ignores profile %s; binding %s here would silence it in every session", inert(v, 64), inert(name, 64), inert(name, 64)))
	}
	sp, err := statePath(a.getenv, name, a.as)
	if err != nil {
		return err
	}
	session := hookSession(a.getenv("CLAUDE_CODE_SESSION_ID"))
	now := hookNow()
	var prev string
	var prevAt time.Time
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	err = stateFile{path: sp}.update(ctx, func(s *hookState) error {
		prev, prevAt = s.Session, s.SessionAt
		if session != "" {
			s.bindSession(hookOwner{session: session, proc: currentProc(a.getenv)}, bindRebind, now)
		} else {
			s.RebindUntil = now.Add(hookRebindWindow).UTC()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("hook rebind: %w", err)
	}
	was := "it was unbound"
	if prev != "" {
		was = fmt.Sprintf("it was bound to session %s since %s", inert(prev, 64), prevAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	p := inert(name, 64)
	if session != "" {
		if prev == session {
			_, err = fmt.Fprintf(a.stdout, "vecnet: profile %s's hook is already bound to this Claude Code session (%s).\n", p, session)
			return err
		}
		_, err = fmt.Fprintf(a.stdout, "vecnet: profile %s's hook is now bound to this Claude Code session (%s); %s.\n", p, session, was)
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "vecnet: CLAUDE_CODE_SESSION_ID is not set here, so profile %s's hook will bind to the next Claude Code session whose hook runs before %s (%s); %s. From a session's Bash tool, that session's own PostToolUse hook runs next.\n", p, now.Add(hookRebindWindow).UTC().Format("15:04 UTC"), hookRebindWindow, was)
	return err
}
