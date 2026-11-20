package cli

// Claude enrollment and registration wiring (claude delivery foundation,
// task 4). The durable intent flow mirrors the Codex one (localsetup.go)
// without touching its records: Claude's exact target is a canonical config
// root plus the committed profile witness, never a fabricated Codex
// thread/home pair. Everything here is local: enrollment reconciliation
// performs no hub request, and the registration hook performs no hub lookup
// and emits no Claude context.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/platform"
)

const (
	claudeEnrollmentsName = "claude-enrollments.json"
	claudeEnrollmentsLock = "claude-enrollments"
	claudeEnrollmentsMax  = 32
)

// Setup phases recorded in the enrollment record and the setup result. A
// committed credential is never conflated with native delivery readiness:
// "armed" and "registered" both leave Claude on the legacy hook path.
const (
	claudeEnrollmentPrepared   = "prepared"   // intent saved before redemption
	claudeEnrollmentPending    = "pending"    // reconciliation incomplete; retryable
	claudeEnrollmentArmed      = "armed"      // witness committed; the session's own hook registers
	claudeEnrollmentRegistered = "registered" // this process registered the exact root
	claudeEnrollmentSuperseded = "superseded" // the saved intent no longer matches the config
	claudeEnrollmentCanceled   = "canceled"   // the invitation was never redeemed
	claudeEnrollmentDisabled   = "disabled"   // a durable opt-out blocks registration
)

func claudeEnrollmentRetryable(state string) bool {
	return state == claudeEnrollmentPrepared || state == claudeEnrollmentPending
}

// errClaudeEnrollmentHost is a host capability result, not a filesystem
// failure: without the exact live session evidence no target is persisted.
// The build is never part of it: it is observed from the kernel-attested
// executable at registration, never read from the environment.
var errClaudeEnrollmentHost = errors.New("unsupported automatic claude setup: the host must supply its live process identity (CLAUDE_PID) and session id (CLAUDE_CODE_SESSION_ID); credentials can be enrolled, but session registration stays on the legacy hook path")

// isClaudeEnvironment reports whether this run is inside a Claude Code
// host tree (its Bash tool, hooks and MCP servers carry these markers).
func isClaudeEnvironment(getenv func(string) string) bool {
	return getenv("CLAUDECODE") != "" || getenv("CLAUDE_CODE_ENTRYPOINT") != ""
}

// claudeEnrollment is one durable pre-redemption intent, stored in a
// separately versioned private document. Caller is the verified profile
// witness committed with the credentials; the record never carries a
// secret.
type claudeEnrollment struct {
	IntentID   string    `json:"intent_id"`
	Hub        string    `json:"hub,omitempty"`
	Caller     string    `json:"caller,omitempty"`
	Config     string    `json:"config"`
	Profile    string    `json:"profile"`
	As         string    `json:"as,omitempty"`
	ConfigRoot string    `json:"config_root"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type claudeEnrollmentsFile struct {
	Version     int                `json:"version"`
	Enrollments []claudeEnrollment `json:"enrollments"`
}

func claudeEnrollmentValid(r claudeEnrollment) error {
	if !claudeToken.MatchString(r.IntentID) || r.UpdatedAt.IsZero() {
		return usageErr("invalid claude enrollment record")
	}
	if !localRegistryPath(r.Config) || !localRegistryPath(r.ConfigRoot) {
		return usageErr("invalid claude enrollment target")
	}
	if r.Profile == "" || !localRegistryText(r.Profile) {
		return usageErr("invalid claude enrollment profile")
	}
	if r.As != "" && !hookID.MatchString(r.As) {
		return usageErr("invalid claude enrollment delegation")
	}
	if r.Hub != "" && (!claudeHubKey.MatchString(r.Hub) || normalizeHub(r.Hub) != r.Hub) {
		return usageErr("invalid claude enrollment hub")
	}
	if r.Caller != "" && !hookID.MatchString(r.Caller) {
		return usageErr("invalid claude enrollment caller")
	}
	switch r.State {
	case claudeEnrollmentPrepared, claudeEnrollmentPending, claudeEnrollmentArmed,
		claudeEnrollmentRegistered, claudeEnrollmentSuperseded, claudeEnrollmentCanceled, claudeEnrollmentDisabled:
	default:
		return usageErr("invalid claude enrollment phase")
	}
	return nil
}

func (a *app) claudeEnrollments() ([]claudeEnrollment, error) {
	raw, err := a.readLocalState(claudeEnrollmentsName)
	if errors.Is(err, os.ErrNotExist) {
		return []claudeEnrollment{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc claudeEnrollmentsFile
	if _, err := parseJSONNode(raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		return nil, usageErr("invalid claude enrollment state")
	}
	if len(doc.Enrollments) > claudeEnrollmentsMax {
		return nil, usageErr("invalid claude enrollment state")
	}
	for _, r := range doc.Enrollments {
		if err := claudeEnrollmentValid(r); err != nil {
			return nil, err
		}
	}
	return doc.Enrollments, nil
}

func (a *app) saveClaudeEnrollment(r claudeEnrollment) error {
	if err := claudeEnrollmentValid(r); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 2*time.Second)
	defer cancel()
	unlock, err := a.lockLocalState(ctx, claudeEnrollmentsLock)
	if err != nil {
		return err
	}
	defer unlock()
	raw, err := a.readLocalState(claudeEnrollmentsName)
	var doc claudeEnrollmentsFile
	switch {
	case errors.Is(err, os.ErrNotExist):
		doc = claudeEnrollmentsFile{Version: 1, Enrollments: []claudeEnrollment{}}
	case err != nil:
		return err
	default:
		if _, e := parseJSONNode(raw); e != nil {
			return e
		}
		if e := json.Unmarshal(raw, &doc); e != nil || doc.Version != 1 {
			return usageErr("invalid claude enrollment state")
		}
		if len(doc.Enrollments) > claudeEnrollmentsMax {
			return usageErr("invalid claude enrollment state")
		}
		for _, old := range doc.Enrollments {
			if err := claudeEnrollmentValid(old); err != nil {
				return err
			}
		}
	}
	found := false
	for i, old := range doc.Enrollments {
		if old.IntentID == r.IntentID && old.Config == r.Config && old.Profile == r.Profile && old.As == r.As && old.ConfigRoot == r.ConfigRoot {
			doc.Enrollments[i] = r
			found = true
			break
		}
	}
	if !found {
		if len(doc.Enrollments) >= claudeEnrollmentsMax {
			return usageErr("too many claude enrollment records")
		}
		doc.Enrollments = append(doc.Enrollments, r)
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	return a.writeLocalState(claudeEnrollmentsName, raw)
}

// prepareClaudeEnrollment records the exact Claude target before the
// one-use invitation is redeemed, so a crash after saving credentials can
// resume via repair. It returns (nil, nil) outside a Claude host tree; the
// Codex durable flow owns a Codex environment.
func (a *app) prepareClaudeEnrollment(profile string) (*claudeEnrollment, error) {
	if !isClaudeEnvironment(a.getenv) || isCodexEnvironment(a.getenv) {
		return nil, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(a.getenv("CLAUDE_PID")))
	if err != nil || pid <= 1 || procStart(pid) == "" {
		return nil, errClaudeEnrollmentHost
	}
	if !hookSessionID.MatchString(strings.TrimSpace(a.getenv("CLAUDE_CODE_SESSION_ID"))) {
		return nil, errClaudeEnrollmentHost
	}
	config, err := configPath(a.getenv)
	if err != nil {
		return nil, err
	}
	config, err = canonicalLocalSetupPath(config)
	if err != nil {
		return nil, err
	}
	root, err := a.claudeConfigRoot()
	if err != nil {
		return nil, errClaudeEnrollmentHost
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	r := claudeEnrollment{
		IntentID:   hex.EncodeToString(id[:]),
		Config:     config,
		Profile:    profile,
		As:         a.as,
		ConfigRoot: root,
		State:      claudeEnrollmentPrepared,
		UpdatedAt:  time.Now().UTC(),
	}
	if err := a.saveClaudeEnrollment(r); err != nil {
		return nil, err
	}
	return &r, nil
}

// claudeConfigRoot is the canonical Claude Code config root of this host
// tree: CLAUDE_CONFIG_DIR or the conventional ~/.claude.
func (a *app) claudeConfigRoot() (string, error) {
	root := a.getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		home := platform.Home(a.getenv)
		if home == "" {
			return "", usageErr("claude config root is unavailable")
		}
		root = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(root) {
		return "", usageErr("claude config root must be absolute; set CLAUDE_CONFIG_DIR")
	}
	return canonicalCodexPath(root)
}

// claudeRootEvidence builds the claimed root from live host evidence: the
// Claude process (CLAUDE_PID), its canonical config root, the session's own
// id and the kernel-attested build of its executable. CLAUDE_PID only names
// the claim; the proof is the kernel ancestry, /proc start identity and
// Claude's own session registry row, checked inside registerClaudeRoot.
// The build is not observed: build-version gating is parked (see
// claude_build.go), so no hook or registration path ever hashes the
// executable and the root's build fields stay empty. CLAUDE_CODE_VERSION is
// never read.
func (a *app) claudeRootEvidence(session string) (claudeRoot, error) {
	var none claudeRoot
	pid, err := strconv.Atoi(strings.TrimSpace(a.getenv("CLAUDE_PID")))
	if err != nil || pid <= 1 {
		return none, usageErr("claude registration needs the Claude process identity (CLAUDE_PID)")
	}
	session = strings.TrimSpace(session)
	if !hookSessionID.MatchString(session) {
		return none, usageErr("claude registration needs the session's own id")
	}
	start := procStart(pid)
	if start == "" {
		return none, usageErr("claude registration cannot observe the claimed process")
	}
	boot := claudeBootID()
	if boot == "" {
		return none, errClaudeUnsupported
	}
	root, err := a.claudeConfigRoot()
	if err != nil {
		return none, err
	}
	socket := a.getenv("CLAUDE_CODE_MESSAGING_SOCKET")
	if socket != "" && (!filepath.IsAbs(socket) || filepath.Clean(socket) != socket) {
		return none, usageErr("claude messaging endpoint must be a clean absolute path")
	}
	return claudeRoot{
		ConfigRoot:   root,
		SessionID:    session,
		SocketPath:   socket,
		ProcessStart: start,
		BootID:       boot,
		PID:          pid,
		UID:          os.Geteuid(),
	}, nil
}

// claudeHubIdentity is the profile's pinned hub identity: the canonical
// ed25519 key from its config or TOFU pin. A profile with no pin has no hub
// identity yet, and registration stays pending with a fixed reason; the
// endpoint string is never substituted for it.
func (a *app) claudeHubIdentity(profile string, p Profile) (string, error) {
	pin, _ := a.effectivePin(profile, p)
	if _, err := hubid.DecodePubkey(pin); err != nil {
		return "", errClaudeHubUnpinned
	}
	return pin, nil
}

// claudeScopeForProfile derives the delivery scope from the present config,
// its committed witness and its pinned hub identity. The caller witness (the
// enrollment caller or the stored peer id) is mandatory: a profile name
// alone, or credentials no verified enrollment committed, never authorizes
// registration. An unpinned profile refuses with errClaudeHubUnpinned.
func (a *app) claudeScopeForProfile(profile string) (claudeScope, error) {
	scope, err := a.claudeScopeForProfileAllowLegacy(profile)
	if err != nil {
		return claudeScope{}, err
	}
	if scope.HubIdentity == scope.HubEndpoint {
		return claudeScope{}, errClaudeHubUnpinned
	}
	return scope, nil
}

// claudeScopeForProfileAllowLegacy is claudeScopeForProfile for the
// surfaces that must still work before a pin exists — addressed opt-outs,
// status and the legacy-key claim listing: an unpinned profile yields the
// legacy endpoint form (hub identity equal to the endpoint), which no
// binding or canary record ever accepts.
func (a *app) claudeScopeForProfileAllowLegacy(profile string) (claudeScope, error) {
	var none claudeScope
	config, err := configPath(a.getenv)
	if err != nil {
		return none, err
	}
	config, err = canonicalLocalSetupPath(config)
	if err != nil {
		return none, err
	}
	cfg, err := loadConfig(config)
	if err != nil {
		return none, err
	}
	p, ok := cfg.Profiles[profile]
	if !ok {
		return none, usageErr("claude registration profile is absent from the present config")
	}
	hub := normalizeHub(p.Hub)
	caller := p.EnrollmentCaller
	if caller == "" {
		caller = p.PeerID
	}
	if caller == "" {
		return none, usageErr("claude registration requires a stored profile witness (enrol or verified setup committed one); a profile name alone is never authority")
	}
	identity, err := a.claudeHubIdentity(profile, p)
	if errors.Is(err, errClaudeHubUnpinned) {
		identity = hub
	} else if err != nil {
		return none, err
	}
	scope := claudeScope{
		ConfigPath:  config,
		Profile:     profile,
		As:          a.as,
		HubIdentity: identity,
		HubEndpoint: hub,
		Caller:      caller,
	}
	scope.Recipient = claudeScopeRecipient(scope)
	return scope, nil
}

// claudeRegisterHook registers the exact Claude root a hook payload
// speaks for. It consumes the already-read hook data (never reopening
// stdin), maps the payload's agent_id to the CLAUDE_AGENT_ID evidence key
// so child agents are refused, and performs no hub lookup. It writes
// nothing to the hook's output: a refusal leaves the legacy path
// byte-identical.
func (a *app) claudeRegisterHook(ctx context.Context, profile string, raw []byte) localSetupResult {
	var input struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
	}
	if len(raw) == 0 || len(raw) > 1<<20 || json.Unmarshal(raw, &input) != nil {
		return localSetupResult{State: claudeEnrollmentPending, Reason: "claude registration needs the hook payload's session identity"}
	}
	session := strings.TrimSpace(input.SessionID)
	if session == "" {
		session = strings.TrimSpace(a.getenv("CLAUDE_CODE_SESSION_ID"))
	}
	worker := *a
	agent := strings.TrimSpace(input.AgentID)
	if agent != "" || strings.TrimSpace(worker.getenv("CLAUDE_AGENT_ID")) != "" {
		if agent == "" {
			agent = strings.TrimSpace(worker.getenv("CLAUDE_AGENT_ID"))
		}
		worker.getenv = func(k string) string {
			if k == "CLAUDE_AGENT_ID" {
				return agent
			}
			return a.getenv(k)
		}
	}
	scope, err := worker.claudeScopeForProfile(profile)
	if err != nil {
		return claudeSetupResultFor(err)
	}
	root, err := worker.claudeRootEvidence(session)
	if err != nil {
		return claudeSetupResultFor(err)
	}
	_, err = worker.registerClaudeRoot(ctx, scope, root)
	return claudeSetupResultFor(err)
}

// claudeRegistrationEvidence reports whether this process carries Claude
// host-tree evidence at all, so reconciliation outside a session (a
// background repair) does not manufacture a claim.
func (a *app) claudeRegistrationEvidence() bool {
	if !isClaudeEnvironment(a.getenv) {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(a.getenv("CLAUDE_PID")))
	return err == nil && pid > 1
}

func claudeSetupResultFor(err error) localSetupResult {
	switch {
	case err == nil:
		return localSetupResult{State: claudeEnrollmentRegistered}
	case errors.Is(err, errClaudeOptOut), errors.Is(err, errClaudeDisabledGlobal):
		return localSetupResult{State: claudeEnrollmentDisabled, Reason: err.Error()}
	case errors.Is(err, errClaudeUnsupported):
		return localSetupResult{State: "unsupported", Reason: err.Error()}
	default:
		return localSetupResult{State: claudeEnrollmentPending, Reason: err.Error()}
	}
}

// reconcileClaudeEnrollment replays a committed intent: it re-verifies the
// saved intent ID, identity, canonical config and credential against the
// present config commit, registers the exact root when this process
// carries session evidence, and ensures the local delivery runtime. It
// performs no hub request and never consumes another invitation.
func (a *app) reconcileClaudeEnrollment(ctx context.Context, r claudeEnrollment) localSetupResult {
	result := localSetupResult{State: claudeEnrollmentPending}
	fail := func(err error) localSetupResult {
		result.Reason = err.Error()
		r.State = result.State
		r.Reason = result.Reason
		r.UpdatedAt = time.Now().UTC()
		if e := a.saveClaudeEnrollment(r); e != nil {
			result.State = claudeEnrollmentPending
			result.Reason += "; could not save claude setup progress: " + e.Error()
		}
		return result
	}
	stop := func(state string, err error) localSetupResult { result.State = state; return fail(err) }
	cfg, err := loadConfig(r.Config)
	if err != nil {
		return fail(err)
	}
	p, ok := cfg.Profiles[r.Profile]
	if !ok || r.IntentID == "" || r.Hub == "" || p.EnrollmentCaller == "" ||
		p.EnrollmentIntent != r.IntentID || p.EnrollmentCredential != hookDigest([]byte(p.Secret)) ||
		normalizeHub(p.Hub) != r.Hub || p.EnrollmentHub != r.Hub || p.PeerID != p.EnrollmentCaller ||
		(r.Caller != "" && r.Caller != p.EnrollmentCaller) || p.Secret == "" {
		return stop(claudeEnrollmentSuperseded, usageErr("saved claude enrollment was not committed or its credential/profile identity was replaced; use explicit addressed setup for the current credentials"))
	}
	// The config commit carries the redeemed caller even if interruption
	// preceded the first post-commit progress write.
	r.Caller = p.EnrollmentCaller
	phase, reason := claudeEnrollmentArmed, "witness committed; registration completes in the Claude session's own hook (native delivery is not enabled; the legacy hook path is unchanged)"
	if a.claudeRegistrationEvidence() {
		scope, err := a.claudeScopeForProfile(r.Profile)
		if err != nil {
			return fail(err)
		}
		root, err := a.claudeRootEvidence(strings.TrimSpace(a.getenv("CLAUDE_CODE_SESSION_ID")))
		if err != nil {
			return fail(err)
		}
		if _, err := a.registerClaudeRoot(ctx, scope, root); err != nil {
			// Both durable opt-outs settle the record in the stable
			// disabled phase (as the hook path's claudeSetupResultFor
			// maps them); anything else stays retryable pending.
			if errors.Is(err, errClaudeOptOut) || errors.Is(err, errClaudeDisabledGlobal) {
				return stop(claudeEnrollmentDisabled, err)
			}
			return fail(err)
		}
		phase, reason = claudeEnrollmentRegistered, "exact Claude root registered passively (native delivery is not enabled; the legacy hook path is unchanged)"
	}
	if err := a.ensureLocalRuntime(ctx); err != nil {
		return fail(fmt.Errorf("claude setup recorded; local delivery runtime is not available: %w", err))
	}
	result.State, result.Reason = phase, reason
	r.State, r.Reason, r.UpdatedAt = phase, "", time.Now().UTC()
	if err := a.saveClaudeEnrollment(r); err != nil {
		result.State = claudeEnrollmentPending
		result.Reason += "; could not save claude setup progress: " + err.Error()
		return result
	}
	return result
}

func (a *app) retryClaudeEnrollments() error {
	records, err := a.claudeEnrollments()
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range records {
		if !claudeEnrollmentRetryable(r.State) {
			continue
		}
		if result := a.reconcileClaudeEnrollment(a.ctx, r); claudeEnrollmentRetryable(result.State) {
			failures = append(failures, fmt.Errorf("%s: %s", r.Profile, result.Reason))
		}
	}
	return errors.Join(failures...)
}
