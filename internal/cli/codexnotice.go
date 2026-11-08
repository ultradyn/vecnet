package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Only known bounded memory writers and independently nonblocking OS sinks
// qualify. An arbitrary io.Writer or regular filesystem can block forever.
var codexNoticeWriteFD = func(*os.File, string) {}

func writeCodexNotice(w io.Writer, message string) {
	if len(message) > 4096 {
		return
	}
	switch sink := w.(type) {
	case *bytes.Buffer:
		_, _ = sink.WriteString(message)
	case *os.File:
		codexNoticeWriteFD(sink, message)
	}
}

// Reasons contain no upstream error text. Wrapping preserves the existing
// fallback error and control flow at the narrow refusal sites.
type codexInactiveReason string

func (r codexInactiveReason) Error() string { return errCodexDelivery.Error() }
func (r codexInactiveReason) Unwrap() error { return errCodexDelivery }
func (r codexInactiveReason) action() string {
	switch r {
	case "node_missing", "node_too_old":
		return "install Node.js 22.18+ (22.x), 23.6+ (23.x), or 24+, then retry a normal vecnet action"
	case "node_unavailable":
		return "check that Node.js can run and report its version, then retry a normal vecnet action"
	case "assets_missing":
		return "reinstall the vecnet launcher assets, then retry a normal vecnet action"
	case "manager_refused":
		return "check the user delivery service, then retry a normal vecnet action"
	case "ownership_ambiguous":
		return "inspect the existing delivery owner before retrying"
	case "other_thread_live":
		return "use `vecnet codex bindings` to inspect the binding before explicit takeover"
	default:
		return ""
	}
}

type codexNoticeState struct {
	Version int                               `json:"version"`
	Hub     string                            `json:"hub"`
	Reason  codexInactiveReason               `json:"reason"`
	Noticed map[codexInactiveReason]time.Time `json:"noticed,omitempty"`
}

// Unsupported platforms fail closed without needing Linux ownership helpers.
// Linux installs descriptor-relative, private, bounded metadata access.
var codexNoticeAccess = func(a *app, hub string, ctx context.Context, update func(*codexNoticeState) bool) (codexNoticeState, error) {
	return codexNoticeState{}, errCodexDelivery
}

func (a *app) persistCodexReason(ctx context.Context, hub string, err error) {
	var reason codexInactiveReason
	if err != nil && (!errors.As(err, &reason) || reason.action() == "") {
		return
	}
	// Background writers may wait briefly for ordinary marker contention,
	// within the caller's remaining lifetime. Foreground acquisition never waits.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, _ = codexNoticeAccess(a, hub, ctx, func(s *codexNoticeState) bool {
		if s.Reason == reason {
			return false
		}
		s.Reason = reason
		return true
	})
}

// Notice opt-outs use a bounded read on supported platforms. A malformed
// bindings file cannot authorize displaying a stale fallback notice.
var codexNoticeBindings = func(a *app) (codexBindingsFile, error) {
	return codexBindingsFile{}, errCodexDelivery
}

func (a *app) cmdCodexStatus(args []string) error {
	o, err := parseFlags("codex status", args, map[string]flagKind{})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("codex status: unexpected arguments")
	}
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
	name, p, err := cfg.profile(a.namedProfile())
	if err != nil {
		return err
	}
	if err := a.useStoredHub(name, p); err != nil {
		return err
	}
	hub, _, err := codexDeliveryHub(p.Hub)
	if err != nil {
		return err
	}
	state := "unknown (no trusted inactive reason recorded)"
	action := "retry a normal vecnet action to refresh delivery state"
	bindings, bindErr := codexNoticeBindings(a)
	if a.getenv("VECNET_CODEX_AUTO_DELIVERY") == "0" || (bindErr == nil && bindings.disabled(name, a.as)) {
		state, action = "disabled by explicit opt-out", "enable automatic delivery when desired"
	} else if bindErr == nil {
		if s, err := codexNoticeAccess(a, hub, nil, nil); err == nil && s.Reason.action() != "" {
			state, action = string(s.Reason), s.Reason.action()
		}
	}
	_, err = fmt.Fprintf(a.stdout, "Codex auto-delivery for %s: %s\nAction: %s\n", codexNoticeHubDisplay(hub), state, action)
	return err
}

func (a *app) codexNoticeMessage(hub string, now time.Time) string {
	announced := false
	s, err := codexNoticeAccess(a, hub, nil, func(s *codexNoticeState) bool {
		if a.ctx.Err() != nil || s.Reason.action() == "" {
			return false
		}
		last := s.Noticed[s.Reason]
		if !last.IsZero() && now.Before(last.Add(24*time.Hour)) {
			return false
		}
		if s.Noticed == nil {
			s.Noticed = make(map[codexInactiveReason]time.Time)
		}
		s.Noticed[s.Reason] = now.UTC()
		announced = true
		return true
	})
	if err != nil || !announced {
		return ""
	}
	guidance := "select --net and --as explicitly before running `vecnet codex status`"
	name := a.namedProfile()
	profile, profileOK := shellQuote(name)
	as, asOK := shellQuote(a.as)
	if name != "" && profileOK && asOK && len(profile)+len(as) <= 192 {
		command := "vecnet --net " + profile
		if a.as != "" {
			command += " --as " + as
		}
		guidance = "run `" + command + " codex status`"
	}
	return fmt.Sprintf("vecnet: Codex auto-delivery inactive for %s: %s; %s\n", codexNoticeHubDisplay(s.Hub), string(s.Reason), guidance)
}

// Presentation is escaped and bounded independently of the full hub identity
// used for hashing, ownership and metadata comparisons.
func codexNoticeHubDisplay(hub string) string {
	var out strings.Builder
	for _, r := range hub {
		quoted := strconv.QuoteToASCII(string(r))
		part := quoted[1 : len(quoted)-1]
		if out.Len()+len(part) > 192 {
			out.WriteString("...")
			break
		}
		out.WriteString(part)
	}
	return out.String()
}
