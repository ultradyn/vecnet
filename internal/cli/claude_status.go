package cli

// Passive Claude status (claude delivery foundation, task 5). The shared
// delivery host publishes one private sidecar (bindings.json.claude-status.json
// next to the existing Codex status) while it holds the singleton lease; the
// CLI accepts a live owner only from a fresh file naming an exact live
// PID/start identity whose addressed binding carries the current generation.
// Everything here is passive: no hub request, no socket write, no Claude-side
// output, and no claim that native delivery is anything but unavailable.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	claudeSettingsName = "claude-settings.json"
	claudeSettingsLock = "claude-settings"
	// A heartbeat older than three 10-second host reload intervals is stale.
	claudeStatusFreshFor = 30 * time.Second
	claudeStatusMax      = 1 << 20
)

var (
	errClaudeDisabledGlobal = errors.New("claude registration disabled globally; claude enable without --net clears it")

	claudeStatusStates = map[string]bool{"registered": true, "unsupported": true, "opted_out": true, "blocked": true}
	claudeStatusKey    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// claudeStatusFile is the host's heartbeat sidecar: strict v1, owner-only,
// with the writer's exact Linux PID/start/boot identity.
type claudeStatusFile struct {
	Version   int                   `json:"version"`
	UpdatedAt time.Time             `json:"updated_at"`
	HostPID   int                   `json:"host_pid"`
	HostStart string                `json:"host_start"`
	BootID    string                `json:"boot_id"`
	Bindings  []claudeStatusBinding `json:"bindings"`
}

type claudeStatusBinding struct {
	BindingKey string `json:"binding_key"`
	Generation string `json:"generation"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
}

// claudeStatusResult is the passive CLI output. owner is "registered" only
// for a fresh exact live writer; mode stays "legacy" (or "opted_out" after a
// durable disable), pending_count and blocked_pending_limit report the
// durable claim ledger's unresolved set and cap state for this recipient
// (zero and false with no recorded delivery activity), and nothing here ever
// claims a native-ready state.
type claudeStatusResult struct {
	Version             int    `json:"version"`
	Owner               string `json:"owner"`
	Mode                string `json:"mode"`
	Reason              string `json:"reason,omitempty"`
	Generation          string `json:"generation,omitempty"`
	PendingCount        int    `json:"pending_count"`
	BlockedPendingLimit bool   `json:"blocked_pending_limit"`
}

// claudeSettingsFile is the host's versioned Claude settings document; the
// global disable flag lives here, never in the binding document.
type claudeSettingsFile struct {
	Version  int  `json:"version"`
	Disabled bool `json:"disabled"`
}

func (a *app) claudeSettings() (claudeSettingsFile, error) {
	raw, err := a.readLocalState(claudeSettingsName)
	if errors.Is(err, os.ErrNotExist) {
		return claudeSettingsFile{Version: 1}, nil
	}
	if err != nil {
		return claudeSettingsFile{}, err
	}
	var doc claudeSettingsFile
	if _, err := parseJSONNode(raw); err != nil {
		return claudeSettingsFile{}, usageErr("invalid claude settings state")
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Version != 1 {
		return claudeSettingsFile{}, usageErr("invalid claude settings state")
	}
	return doc, nil
}

// setClaudeGlobalDisable latches or clears the global disable flag under the
// settings lock. It grants no ownership and removes no legacy path.
func (a *app) setClaudeGlobalDisable(ctx context.Context, disabled bool) error {
	unlock, err := a.lockLocalState(ctx, claudeSettingsLock)
	if err != nil {
		return err
	}
	defer unlock()
	doc, err := a.claudeSettings()
	if err != nil {
		return err
	}
	doc.Version, doc.Disabled = 1, disabled
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return a.writeLocalState(claudeSettingsName, raw)
}

// claudeStatusHostPaths derives the sidecar and singleton lock paths from the
// shared Codex authority location, exactly as the host computes them.
func claudeStatusHostPaths(getenv func(string) string) (sidecar, lock string, err error) {
	bindings, err := codexBindingsPath(getenv)
	if err != nil {
		return "", "", err
	}
	return bindings + ".claude-status.json", bindings + ".status.json" + ".lock", nil
}

func claudeStatusFileValid(doc claudeStatusFile) error {
	if doc.Version != 1 || doc.HostPID <= 1 || !claudeStartID.MatchString(doc.HostStart) ||
		!codexThreadID.MatchString(doc.BootID) || doc.UpdatedAt.IsZero() || len(doc.Bindings) > claudeBindingsMax {
		return usageErr("invalid claude host status document")
	}
	keys := map[string]bool{}
	for _, b := range doc.Bindings {
		if !claudeToken.MatchString(b.Generation) || !claudeStatusStates[b.State] || len(b.Reason) > 256 ||
			!claudeStatusKey.MatchString(b.BindingKey) {
			return usageErr("invalid claude host status binding row")
		}
		if keys[b.BindingKey] {
			return usageErr("duplicate claude host status binding key")
		}
		keys[b.BindingKey] = true
	}
	return nil
}

// readClaudeStatusSidecar reads and strictly validates the private sidecar.
// A missing, unsafe or malformed document is never a live owner.
func (a *app) readClaudeStatusSidecar() (claudeStatusFile, error) {
	sidecar, _, err := claudeStatusHostPaths(a.getenv)
	if err != nil {
		return claudeStatusFile{}, err
	}
	raw, err := claudeReadPrivate(sidecar)
	if err != nil {
		return claudeStatusFile{}, err
	}
	var doc claudeStatusFile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || d.Decode(new(struct{})) != io.EOF {
		return claudeStatusFile{}, usageErr("invalid claude host status document")
	}
	if err := claudeStatusFileValid(doc); err != nil {
		return claudeStatusFile{}, err
	}
	return doc, nil
}

// claudeReadPrivate reads one owner-only regular file without following
// symlinks, refusing anything group/world accessible or replaced mid-read.
func claudeReadPrivate(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || before.Size() > claudeStatusMax {
		return nil, usageErr("unsafe or oversized claude host status file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, claudeStatusMax+1))
	if err != nil || len(raw) > claudeStatusMax {
		return nil, usageErr("claude host status file is unreadable")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm()&0o077 != 0 {
		return nil, usageErr("claude host status changed while reading")
	}
	return raw, nil
}

// readClaudeStatus reports the passive status for one exact addressed scope.
// A live owner requires: liveness, matching boot and start identity, fresh
// status, evidence the singleton lease is held (an additional check, never
// proof of the writer, and never obtained by unlinking the lock), and the
// addressed binding present as registered at the current generation.
func (a *app) readClaudeStatus(ctx context.Context, scope claudeScope) (claudeStatusResult, error) {
	if err := claudeScopeValid(scope); err != nil {
		return claudeStatusResult{}, err
	}
	result := claudeStatusResult{Version: 1, Owner: "none", Mode: "legacy", PendingCount: 0}
	// The durable claim ledger is factual whatever the passive modes say:
	// unresolved native reservations and the staging cap state are reported
	// even when no host is live.
	summary, err := a.claudeClaimsSummary(claudeBindingKey(scope))
	if err != nil {
		return claudeStatusResult{}, err
	}
	result.PendingCount, result.BlockedPendingLimit = summary.Pending, summary.BlockedPendingLimit
	settings, err := a.claudeSettings()
	if err != nil {
		return claudeStatusResult{}, err
	}
	opted := false
	if settings.Disabled {
		opted = true
		result.Reason = errClaudeDisabledGlobal.Error()
	} else {
		unlock, err := a.lockLocalState(ctx, claudeLockName)
		if err != nil {
			return claudeStatusResult{}, err
		}
		doc, err := a.readClaudeBindingsDocument()
		unlock()
		if err != nil {
			return claudeStatusResult{}, err
		}
		if contains := func() bool {
			for _, s := range doc.OptOuts {
				if claudeSameRecipient(s, scope) {
					return true
				}
			}
			return false
		}(); contains {
			opted = true
			result.Reason = errClaudeOptOut.Error()
		}
		for _, b := range doc.Bindings {
			if claudeSameRecipient(b.claudeScope, scope) {
				result.Generation = b.Generation
			}
		}
	}
	if opted {
		// A durable disable states its mode and never implies a live owner.
		result.Mode, result.Generation = "opted_out", ""
		return result, nil
	}

	doc, err := a.readClaudeStatusSidecar()
	if err != nil {
		result.Reason = "no live claude host status is available"
		return result, nil
	}
	_, lock, err := claudeStatusHostPaths(a.getenv)
	if err != nil {
		return claudeStatusResult{}, err
	}
	key := claudeBindingKey(scope)
	reason := ""
	stale := time.Now().UTC().Sub(doc.UpdatedAt)
	switch {
	case claudeBootID() == "" || doc.BootID != claudeBootID():
		reason = "claude host status reports another boot"
	case !claudeHostLive(doc.HostPID, doc.HostStart):
		reason = "claude host process is not live"
	case stale > claudeStatusFreshFor || stale < -claudeStatusFreshFor:
		reason = "claude host status is stale"
	case !claudeSingletonHeld(lock):
		reason = "no active delivery host lease"
	default:
		found := false
		for _, b := range doc.Bindings {
			if b.BindingKey != key {
				continue
			}
			found = true
			if b.State != "registered" {
				reason = "the registered claude root is not live: " + b.Reason
			} else if result.Generation == "" {
				reason = "no claude registration is stored for this recipient"
			} else if b.Generation != result.Generation {
				reason = "claude host status predates the current binding generation"
			}
		}
		if !found {
			reason = "no claude registration is stored for this recipient"
		}
	}
	if reason != "" {
		result.Reason, result.Generation = reason, ""
		return result, nil
	}
	result.Owner, result.Reason = "registered", ""
	return result, nil
}

// cmdClaude hosts the claude connector surface: the private runtime-snapshot
// child, the passive status reader, and the addressed disable/enable flags.
// No subcommand performs a hub request, starts native promotion or runs
// generic maintenance.
func (a *app) cmdClaude(args []string) error {
	if len(args) == 0 {
		return usageErr("claude: expected runtime-snapshot, status, deliveries, resolve, disable or enable")
	}
	switch args[0] {
	case "runtime-snapshot":
		if len(args) > 1 {
			return usageErr("claude runtime-snapshot: unexpected arguments")
		}
		return a.cmdClaudeRuntimeSnapshot()
	case "status":
		return a.cmdClaudeStatus(args[1:])
	case "deliveries":
		return a.cmdClaudeDeliveries(args[1:])
	case "resolve":
		return a.cmdClaudeResolve(args[1:])
	case "disable", "enable":
		return a.cmdClaudeDisableEnable(args[0], args[1:])
	case "deliver":
		// Private single-shot transport child of the shared delivery host;
		// deliberately absent from the usage line above.
		return a.cmdClaudeDeliver(args[1:])
	}
	return usageErr("claude: expected runtime-snapshot, status, deliveries, resolve, disable or enable")
}

// cmdClaudeRuntimeSnapshot is the read-only host child: a versioned,
// redacted view of the private registry. It contains only opaque keys,
// generations, passive states, the native-delivery admission booleans and
// the hub/profile coordinates the host's own scoped children need — never
// socket paths, session ids, config contents or credentials — and never
// parses the registry host-side beyond validation.
func (a *app) cmdClaudeRuntimeSnapshot() error {
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 5*time.Second)
	defer cancel()
	unlock, err := a.lockLocalState(ctx, claudeLockName)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := a.readClaudeBindingsDocument()
	if err != nil {
		return err
	}
	settings, err := a.claudeSettings()
	if err != nil {
		return err
	}
	snapshot := claudeRuntimeSnapshot{Version: 1, Bindings: make([]claudeSnapshotBinding, 0, len(rows.Bindings))}
	for _, b := range rows.Bindings {
		state, reason := claudeBindingPassiveState(b, rows.OptOuts, settings.Disabled)
		snapshot.Bindings = append(snapshot.Bindings, claudeSnapshotBinding{
			BindingKey: claudeBindingKey(b.claudeScope), Generation: b.Generation, State: state, Reason: reason,
			Admission: claudeAdmissionEvidence(b, rows.OptOuts, settings.Disabled),
			Scope: claudeSnapshotScope{
				Profile: b.Profile, As: b.As, HubEndpoint: b.HubEndpoint, Caller: b.Caller, Recipient: b.Recipient,
			},
		})
	}
	return a.emitValue(snapshot)
}

// claudeSnapshotAdmission is the native-delivery gate for one stored row
// (task 7): process liveness with start/boot identity, durable policy
// consent, and the qualified compatibility range. Booleans only; no paths.
type claudeSnapshotAdmission struct {
	Live             bool `json:"live"`
	Policy           bool `json:"policy"`
	VersionQualified bool `json:"version_qualified"`
}

// claudeSnapshotScope carries only the coordinates the shared host needs to
// open its own scoped CLI children (watch, inbox) for this recipient: the
// profile name and hub/caller/recipient identities. No config paths.
type claudeSnapshotScope struct {
	Profile     string `json:"profile"`
	As          string `json:"as,omitempty"`
	HubEndpoint string `json:"hub_endpoint"`
	Caller      string `json:"caller"`
	Recipient   string `json:"recipient"`
}

// claudeVersionQualifiedBuilds is the qualified compatibility baseline: Linux
// x86-64 Claude Code 2.1.322 is the initial lab baseline, and unknown or
// untested builds stay legacy until qualified — no version-number guessing.
var claudeVersionQualifiedBuilds = []string{"2.1.322"}

func claudeVersionQualified(version string) bool {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return false
	}
	return slices.Contains(claudeVersionQualifiedBuilds, version)
}

// claudeAdmissionEvidence computes the native-delivery admission booleans:
// live is exact process evidence (this boot, live pid, start identity, OS
// user), policy is the absence of both durable opt-outs, and version
// qualification is the compatibility range. The deliver helper recomputes
// the same evidence under the registry lock before resolving an endpoint.
func claudeAdmissionEvidence(b claudeBinding, optOuts []claudeScope, globalDisable bool) claudeSnapshotAdmission {
	policy := !globalDisable && !slices.ContainsFunc(optOuts, func(s claudeScope) bool {
		return claudeSameRecipient(s, b.claudeScope)
	})
	live := false
	if boot := claudeBootID(); boot != "" && b.BootID == boot && claudeHostLive(b.PID, b.ProcessStart) {
		live = claudeProcUser(b.PID, b.UID) == nil
	}
	return claudeSnapshotAdmission{Live: live, Policy: policy, VersionQualified: claudeVersionQualified(b.Version)}
}

// claudeBindingPassiveState classifies one stored row for the snapshot:
// registered only on platform-verified live evidence, opted_out behind a
// durable disable, unsupported on an unqualified platform, blocked otherwise.
func claudeBindingPassiveState(b claudeBinding, optOuts []claudeScope, globalDisable bool) (string, string) {
	if globalDisable {
		return "opted_out", errClaudeDisabledGlobal.Error()
	}
	for _, s := range optOuts {
		if claudeSameRecipient(s, b.claudeScope) {
			return "opted_out", errClaudeOptOut.Error()
		}
	}
	if err := validateClaudeRoot(b.claudeRoot); err != nil {
		if errors.Is(err, errClaudeUnsupported) {
			return "unsupported", err.Error()
		}
		return "blocked", err.Error()
	}
	return "registered", ""
}

func (a *app) cmdClaudeStatus(args []string) error {
	o, err := parseFlags("claude status", args, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("claude status takes no positional arguments")
	}
	profile := a.namedProfile()
	if profile == "" {
		return usageErr("claude status requires the addressed profile: --net PROFILE")
	}
	scope, err := a.claudeScopeForProfile(profile)
	if err != nil {
		return err
	}
	result, err := a.readClaudeStatus(a.ctx, scope)
	if err != nil {
		return err
	}
	if a.json {
		return a.emitValue(result)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "owner: %s\n", result.Owner)
	fmt.Fprintf(&out, "mode: %s\n", result.Mode)
	if result.Generation != "" {
		fmt.Fprintf(&out, "generation: %s\n", result.Generation)
	}
	if result.Reason != "" {
		fmt.Fprintf(&out, "reason: %s\n", result.Reason)
	}
	fmt.Fprintf(&out, "pending_count: %d\n", result.PendingCount)
	if result.BlockedPendingLimit {
		fmt.Fprintln(&out, "blocked_pending_limit: true")
	}
	fmt.Fprint(a.stdout, out.String())
	return nil
}

// cmdClaudeDisableEnable latches or clears the passive registration flags.
// Without a profile it addresses the global flag in the versioned Claude
// settings; with one it addresses exactly that recipient's opt-out in the
// binding document. Global disable wins over profile enable, and neither
// action removes the legacy hook path or starts native promotion.
func (a *app) cmdClaudeDisableEnable(verb string, args []string) error {
	o, err := parseFlags("claude "+verb, args, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("claude " + verb + " takes no positional arguments")
	}
	profile := a.namedProfile()
	if profile == "" {
		if err := a.setClaudeGlobalDisable(a.ctx, verb == "disable"); err != nil {
			return err
		}
		return a.ok("claude passive registration " + verb + "d globally (the legacy hook path is unchanged)")
	}
	scope, err := a.claudeScopeForProfile(profile)
	if err != nil {
		return err
	}
	if err := a.setClaudeOptOut(a.ctx, scope, verb == "disable"); err != nil {
		return err
	}
	return a.ok("claude registration " + verb + "d for this recipient (the legacy hook path is unchanged)")
}
