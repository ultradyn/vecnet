package cli

// Per-profile native canary opt-in (delivery Phase 2, task 3).
//
// With build-version gating parked, this durable record is the only
// production gate on native pointer delivery into a live Claude session
// (VECNET_CLAUDE_DELIVERY_CANARY=0 is the host's kill switch, never an
// enable). It fails closed everywhere:
//
//   - at most one record per user, for one exact peer scope: no --as
//     delegation and no principal credential may hold it;
//   - another recipient, or another profile or hub alias of the same
//     recipient, is refused while a record exists;
//   - enable never clears an opt-out (global or addressed), and an opt-out
//     still withdraws policy whatever the record says;
//   - disable is idempotent and works before a pin exists.
//
// The record lives in the v2 claude-bindings.json document, mutated only
// under the claude-bindings lock through the validating writer. Neither verb
// contacts the hub: the pinned identity comes from config or the local TOFU
// pin.

import (
	"context"
	"errors"
	"slices"

	"github.com/XertroV/vecnet/internal/ids"
)

var (
	errClaudeCanaryScope     = errors.New("the first canary accepts peer credentials and a single exact recipient scope only")
	errClaudeCanaryHeld      = errors.New("another recipient holds the canary; disable it there first")
	errClaudeCanaryAlias     = errors.New("this recipient's canary is held through another profile or hub alias; disable it there first")
	errClaudeCanaryRepinned  = errors.New("the canary record predates a hub identity change; run `claude canary disable` then `claude canary enable`")
	errClaudeCanaryNotOptIn  = errors.New("the recipient has not opted into the native canary")
	claudeCanaryUsageMessage = "claude canary: expected enable or disable"
)

// claudeCanaryEligible is the exact scope shape the canary accepts: the
// caller's own inbox (no delegation) under a peer credential.
func claudeCanaryEligible(s claudeScope) bool {
	return s.As == "" && ids.Is(ids.Peer, s.Caller) && s.Recipient == s.Caller
}

// claudeCanaryAdmits reports whether the stored canary record opts this
// scope in. Anything but exactly one eligible record matching through
// claudeSameRecipient is false.
func claudeCanaryAdmits(canary []claudeScope, s claudeScope) bool {
	if len(canary) != 1 || !claudeCanaryEligible(s) {
		return false
	}
	c := canary[0]
	// A hub identity change withdraws consent: the record admits only under
	// the identity it was granted for, even through the same endpoint (owner
	// ruling 2026-11-21). Disable still matches by endpoint, so the stale
	// record can be cleared and re-granted.
	if c.HubIdentity != s.HubIdentity {
		return false
	}
	return claudeCanaryEligible(c) && claudeSameRecipient(c, s)
}

// setClaudeCanary records (enabled) or withdraws (disabled) the native
// canary opt-in for one exact scope under the claude-bindings lock.
func (a *app) setClaudeCanary(ctx context.Context, scope claudeScope, enabled bool) error {
	if enabled {
		// No delegation, a peer credential, its own inbox: checked first so
		// every other shape gets the one fixed refusal.
		if !claudeCanaryEligible(scope) {
			return errClaudeCanaryScope
		}
		if !claudeHubPinned(scope.HubIdentity) {
			return errClaudeHubUnpinned
		}
		if err := claudeScopeValid(scope); err != nil {
			return err
		}
	} else if err := claudeOptOutScopeValid(scope); err != nil {
		return err
	}
	unlock, err := a.lockLocalState(ctx, claudeLockName)
	if err != nil {
		return err
	}
	defer unlock()
	if enabled {
		// The scope must still address the present config and its pin.
		if err := a.claudeScopeWitness(scope); err != nil {
			return err
		}
	}
	doc, err := a.readClaudeBindingsDocument()
	if err != nil {
		return err
	}
	if enabled {
		// At most one record exists (the validating reader enforces it).
		if len(doc.Canary) > 0 {
			switch held := doc.Canary[0]; {
			case held == scope:
				return nil // Idempotent: the exact record already exists.
			case claudeSameRecipient(held, scope):
				// The same recipient through the same endpoint but under a
				// different pinned identity: the hub re-pinned, and consent
				// never silently survives an identity change. The copy names
				// the recovery (disable clears the stale record).
				if held.HubEndpoint == scope.HubEndpoint && held.HubIdentity != scope.HubIdentity {
					return errClaudeCanaryRepinned
				}
				return errClaudeCanaryAlias
			default:
				return errClaudeCanaryHeld
			}
		}
		doc.Canary = append(doc.Canary, scope)
	} else {
		kept := slices.DeleteFunc(slices.Clone(doc.Canary), func(c claudeScope) bool { return claudeSameRecipient(c, scope) })
		if len(kept) == len(doc.Canary) {
			return nil // Idempotent: nothing of this recipient's is stored.
		}
		doc.Canary = kept
	}
	if doc.Canary == nil {
		doc.Canary = []claudeScope{}
	}
	return a.writeClaudeBindingsDocument(doc)
}

// cmdClaudeCanary is `vecnet --net P claude canary enable|disable`. It is a
// local durable record only: no hub request, no registration, no change to
// any opt-out and no change to the legacy hook path.
func (a *app) cmdClaudeCanary(args []string) error {
	if len(args) == 0 {
		return usageErr(claudeCanaryUsageMessage)
	}
	verb := args[0]
	if verb != "enable" && verb != "disable" {
		return usageErr(claudeCanaryUsageMessage)
	}
	o, err := parseFlags("claude canary "+verb, args[1:], nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("claude canary " + verb + " takes no positional arguments")
	}
	profile := a.namedProfile()
	if profile == "" {
		return usageErr("claude canary " + verb + " requires the addressed profile: --net PROFILE")
	}
	// Any --as, even an empty one, is a delegated scope: refused outright.
	if verb == "enable" && (a.asSet || a.as != "") {
		return errClaudeCanaryScope
	}
	// Withdrawal never waits for a pin: the legacy endpoint form still
	// matches a stored record through claudeSameRecipient. Enable refuses an
	// unpinned profile in setClaudeCanary.
	scope, err := a.claudeScopeForProfileAllowLegacy(profile)
	if err != nil {
		return err
	}
	if err := a.setClaudeCanary(a.ctx, scope, verb == "enable"); err != nil {
		return err
	}
	if verb == "disable" {
		return a.ok("claude native canary disabled for this recipient (the legacy hook path is unchanged)")
	}
	human := "claude native canary enabled for this recipient (the legacy hook path is unchanged)"
	if blocked, err := a.claudeCanaryOptOutApplies(scope); err == nil && blocked {
		human += "; a durable opt-out still applies and claude enable is required to clear it"
	}
	human += "\n" + claudeCanaryNextSteps(profile)
	return a.ok(human)
}

// claudeCanaryNextSteps is the fixed guidance after a successful enable. The
// running delivery daemon rereads the binding snapshot every 10 seconds, so a
// daemon already on this version needs no restart; one started before the
// upgrade must be restarted once to load the native-delivery code.
func claudeCanaryNextSteps(profile string) string {
	return "next steps:\n" +
		"  the running delivery daemon picks this up within about 10 seconds; check with\n" +
		"    vecnet --net " + profile + " claude status   (expect mode: canary once a live Claude session is registered)\n" +
		"  if the daemon was started before you upgraded vecnet, restart it once:\n" +
		"    systemctl --user restart vecnet-codex-delivery.service   (managed delivery daemon)\n" +
		"    systemctl --user restart vecnet-launcher                 (launcher-embedded daemon)"
}

// claudeCanaryOptOutApplies reports whether a durable opt-out (global or
// addressed) still withdraws policy for this scope.
func (a *app) claudeCanaryOptOutApplies(scope claudeScope) (bool, error) {
	settings, err := a.claudeSettings()
	if err != nil {
		return false, err
	}
	if settings.Disabled {
		return true, nil
	}
	unlock, err := a.lockLocalState(a.ctx, claudeLockName)
	if err != nil {
		return false, err
	}
	defer unlock()
	doc, err := a.readClaudeBindingsDocument()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(doc.OptOuts, func(s claudeScope) bool { return claudeSameRecipient(s, scope) }), nil
}
