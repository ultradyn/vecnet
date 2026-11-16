package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
)

var codexGeneration = regexp.MustCompile(`^[0-9a-f]{32}$`)

func canonicalCodexPath(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return platform.Canonical(p)
}

// Caller holds the authority's lock until all effects have completed. Every row
// is validated: an invalid or conflicting authority cannot grant partial access.
func (a *app) codexOwner(o opts, now time.Time, cleanup ...bool) (codexBinding, error) {
	var none codexBinding
	if !codexThreadID.MatchString(o.str("thread")) || !codexGeneration.MatchString(o.str("expect-generation")) || !filepath.IsAbs(o.str("codex-home")) {
		return none, usageErr("unverifiable Codex ownership; run codex setup")
	}
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return none, err
	}
	file, err := readCodexAuthority(path)
	if err != nil {
		return none, err
	}
	config, err := configPath(a.getenv)
	if err != nil {
		return none, err
	}
	config, err = canonicalCodexPath(config)
	if err != nil {
		return none, err
	}
	home, err := canonicalCodexPath(o.str("codex-home"))
	if err != nil {
		return none, err
	}
	recipients, threads := map[string]bool{}, map[string]bool{}
	var found *codexBinding
	for _, b := range file.Bindings {
		if !codexGeneration.MatchString(b.Generation) || !codexThreadID.MatchString(b.ThreadID) || b.Profile == "" || b.Caller == "" || b.Hub == "" || !filepath.IsAbs(b.ConfigPath) || !filepath.IsAbs(b.CodexHome) || b.ExpiresAt.IsZero() {
			return none, usageErr("legacy or invalid Codex binding; explicitly bind again")
		}
		recipient := normalizeHub(b.Hub) + "\n" + codexRecipient(b)
		thread := b.CodexHome + "\n" + b.ThreadID
		if recipients[recipient] || threads[thread] {
			return none, usageErr("conflicting Codex ownership")
		}
		recipients[recipient], threads[thread] = true, true
		if b.ConfigPath == config && b.Profile == a.namedProfile() && b.As == a.as {
			if found != nil {
				return none, usageErr("conflicting Codex profile")
			}
			copy := b
			found = &copy
		}
	}
	if found == nil {
		return none, usageErr("Codex owner not registered")
	}
	b := *found
	if b.ConfigPath != config || b.CodexHome != home || b.ThreadID != o.str("thread") || b.Generation != o.str("expect-generation") || b.Hub != normalizeHub(o.str("expect-hub")) || b.Caller != o.str("expect-caller") || (!(len(cleanup) > 0 && cleanup[0]) && !now.Before(b.ExpiresAt)) {
		return none, usageErr("Codex ownership changed or expired")
	}
	return b, nil
}

func (a *app) lockCodexOwner() (string, func(), error) {
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return "", nil, err
	}
	// Missing authority must not create one through a delivery operation.
	if _, err := readCodexBindings(path); err != nil {
		return "", nil, err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	return path, unlock, err
}

type codexPendingSend struct {
	Owner   codexBinding `json:"owner"`
	Claim   string       `json:"claim"`
	Attempt string       `json:"attempt"`
}

func codexUpdateState(ctx context.Context, path string, allowMissing bool, fn func(*hookState) error) error {
	return (stateFile{path}).updateMode(ctx, fn, true, allowMissing)
}

func sameCodexOwner(a, b codexBinding) bool {
	return a.ConfigPath == b.ConfigPath && a.Generation == b.Generation && a.Profile == b.Profile && a.As == b.As && a.Hub == b.Hub && a.Caller == b.Caller && a.CodexHome == b.CodexHome && a.ThreadID == b.ThreadID
}

func codexPendingPath(path string, b codexBinding) string {
	return path + "." + b.Generation + ".pending"
}
func codexHasPending(path string, b codexBinding) bool {
	if b.Generation == "" {
		return false
	}
	_, err := os.Lstat(codexPendingPath(path, b))
	return !errors.Is(err, os.ErrNotExist)
}

// Under the authority lock, return definitely unsent work to the shared replay
// queue before transferring ownership. Accepted and ambiguous sends stay shown.
func (a *app) codexTransferClaims(b codexBinding) error {
	path, err := a.codexOwnerStatePath(b)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return codexUpdateState(a.ctx, path, false, func(s *hookState) error {
		if s.Hub != "" && (s.Hub != b.Hub || s.Caller != b.Caller || s.As != b.As) {
			return usageErr("delivery state identity changed; explicit recovery required")
		}
		kept := make([]claimRef, 0, len(s.Claims))
		for _, r := range s.Claims {
			if r.Generation != b.Generation || r.Accepted {
				kept = append(kept, r)
				continue
			}
			s.Shown = slices.DeleteFunc(s.Shown, func(id string) bool { return id == r.ID })
			s.Replay = append(s.Replay, replayRef{ID: r.ID, After: r.After, Priority: r.Priority, Order: r.Order})
		}
		s.Claims = kept
		return nil
	})
}

func (a *app) codexAcceptClaim(b codexBinding, nonce string) error {
	path, err := a.codexOwnerStatePath(b)
	if err != nil {
		return err
	}
	return codexUpdateState(a.ctx, path, false, func(s *hookState) error {
		if s.Hub != b.Hub || s.Caller != b.Caller || s.As != b.As {
			return usageErr("delivery state identity changed")
		}
		found := false
		for i := range s.Claims {
			if s.Claims[i].Generation == b.Generation && s.Claims[i].Claim == nonce {
				s.Claims[i].Accepted = true
				found = true
			}
		}
		if !found {
			return usageErr("accepted claim missing; explicit recovery required")
		}
		return nil
	})
}

func (a *app) codexOwnerStatePath(b codexBinding) (string, error) {
	return statePath(func(key string) string {
		if key == "VECNET_CONFIG" {
			return b.ConfigPath
		}
		return a.getenv(key)
	}, b.Profile, b.As)
}

func (a *app) codexClaimEligible(b codexBinding, nonce string) error {
	path, err := a.codexOwnerStatePath(b)
	if err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s hookState
	if validateCodexStateJSON(raw) != nil {
		return usageErr("corrupt delivery state; explicit recovery required")
	}
	if json.Unmarshal(raw, &s) != nil {
		return usageErr("corrupt delivery state; explicit recovery required")
	}
	if s.Hub != b.Hub || s.Caller != b.Caller || s.As != b.As {
		return usageErr("delivery state belongs to another identity")
	}
	found := false
	for _, r := range s.Claims {
		if r.Claim == nonce && r.Generation == b.Generation {
			if r.Accepted {
				return usageErr("claim already accepted")
			}
			found = true
		}
	}
	if !found {
		return usageErr("no eligible claim for this owner")
	}
	return nil
}

// guard fences the actual RPC, not just its preceding poll. Its private marker
// survives a lost response or connector crash; an ambiguous send blocks handoff
// until exact-thread receipt recovery settles it. EOF is deliberately ambiguous.
func (a *app) cmdCodexGuard(args []string, nowTime func() time.Time, settle bool) error {
	o, err := parseFlags("codex guard", args, map[string]flagKind{"thread": strFlag, "codex-home": strFlag, "expect-generation": strFlag, "expect-hub": strFlag, "expect-caller": strFlag, "claim": strFlag, "attempt": strFlag, "outcome": strFlag, "receipt-key": strFlag, "delivery-ids": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 || !hookID.MatchString(o.str("claim")) || !hookID.MatchString(o.str("attempt")) {
		return usageErr("invalid Codex guard claim")
	}
	if settle && o.str("outcome") != "accepted" && o.str("outcome") != "rejected" {
		return usageErr("settle requires --outcome accepted or rejected from exact-thread recovery")
	}
	receipt := o.has("receipt-key") || o.has("delivery-ids")
	if receipt && (!settle || o.str("outcome") != "accepted" || !o.has("receipt-key") || !o.has("delivery-ids")) {
		return usageErr("receipt settlement requires accepted outcome, exact key and delivery set")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	worker := *a
	worker.ctx = ctx
	worker.inHook = true
	path, unlock, err := worker.lockCodexOwner()
	if err != nil {
		return err
	}
	defer unlock()
	b, err := worker.codexOwner(o, nowTime(), settle)
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
	if _, err := worker.codexOwner(o, nowTime(), settle); err != nil {
		return err
	}
	pendingPath := codexPendingPath(path, b)
	if settle {
		raw, err := readPrivateCodexStatus(pendingPath)
		missing := errors.Is(err, os.ErrNotExist)
		if err != nil && !missing {
			return err
		}
		if missing && !receipt {
			return nil
		}
		var pending codexPendingSend
		if !missing {
			if err := validateCodexStateJSON(raw); err != nil {
				return err
			}
			if json.Unmarshal(raw, &pending) != nil || !sameCodexOwner(pending.Owner, b) || pending.Claim != o.str("claim") || pending.Attempt != o.str("attempt") {
				return usageErr("unresolved Codex send belongs to another claim")
			}
		}
		if receipt {
			if err := worker.codexRecordReceipt(b, o.str("claim"), o.str("receipt-key"), o.str("delivery-ids"), missing); err != nil {
				return err
			}
		} else if o.str("outcome") == "accepted" {
			if err := worker.codexAcceptClaim(b, o.str("claim")); err != nil {
				return err
			}
		}
		if missing {
			return nil
		}
		return removeCodexPending(pendingPath)
	}
	if codexHasPending(path, b) {
		return usageErr("unresolved Codex send requires receipt recovery")
	}
	if err := worker.codexClaimEligible(b, o.str("claim")); err != nil {
		return err
	}
	raw, _ := json.Marshal(codexPendingSend{Owner: b, Claim: o.str("claim"), Attempt: o.str("attempt")})
	if err := writeCodexDurable(pendingPath, raw); err != nil {
		return err
	}
	if err := hookWrite(ctx, a.stdout, []byte("{\"ready\":true}\n")); err != nil {
		return err
	}
	done := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(a.stdin)
		scanner.Buffer(make([]byte, 128), 1024)
		if scanner.Scan() {
			done <- scanner.Text()
		} else {
			done <- ""
		}
	}()
	select {
	case outcome := <-done:
		if outcome == "accepted" {
			if err := worker.codexAcceptClaim(b, o.str("claim")); err != nil {
				return err
			}
		}
		if outcome == "accepted" || outcome == "rejected" {
			return removeCodexPending(pendingPath)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("Codex guard timed out; send needs receipt recovery")
	}
}
