package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/localruntime"
	"github.com/XertroV/vecnet/internal/platform"
)

type localEnrollment struct {
	IntentID  string    `json:"intent_id,omitempty"`
	Hub       string    `json:"hub,omitempty"`
	Caller    string    `json:"caller,omitempty"`
	Config    string    `json:"config"`
	Profile   string    `json:"profile"`
	As        string    `json:"as,omitempty"`
	Directory string    `json:"directory"`
	ThreadID  string    `json:"thread_id"`
	CodexHome string    `json:"codex_home"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
type localSetupResult struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

func (a *app) localEnrollments() ([]localEnrollment, error) {
	raw, err := a.readLocalState("enrollments.json")
	if errors.Is(err, os.ErrNotExist) {
		return []localEnrollment{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Version     int               `json:"version"`
		Enrollments []localEnrollment `json:"enrollments"`
	}
	if _, err := parseJSONNode(raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		return nil, usageErr("invalid local enrollment state")
	}
	for _, r := range doc.Enrollments {
		if !filepath.IsAbs(r.Config) || !filepath.IsAbs(r.Directory) || r.Profile == "" || !codexThreadID.MatchString(r.ThreadID) || !filepath.IsAbs(r.CodexHome) {
			return nil, usageErr("invalid local enrollment target")
		}
	}
	return doc.Enrollments, nil
}
func (a *app) saveLocalEnrollment(r localEnrollment) error {
	unlock, err := a.lockLocalState(a.ctx, "enrollments")
	if err != nil {
		return err
	}
	defer unlock()
	records, err := a.localEnrollments()
	if err != nil {
		return err
	}
	found := false
	for i, old := range records {
		if old.IntentID == r.IntentID && old.Config == r.Config && old.Profile == r.Profile && old.As == r.As && old.ThreadID == r.ThreadID && old.CodexHome == r.CodexHome {
			records[i] = r
			found = true
			break
		}
	}
	if !found {
		records = append(records, r)
	}
	raw, err := json.Marshal(struct {
		Version     int               `json:"version"`
		Enrollments []localEnrollment `json:"enrollments"`
	}{1, records})
	if err != nil {
		return err
	}
	return a.writeLocalState("enrollments.json", raw)
}

// prepareLocalEnrollment records the exact intent before the one-use token is
// redeemed. A crash after saving credentials can therefore resume via repair.
func (a *app) prepareLocalEnrollment(profile string) (*localEnrollment, error) {
	if !isCodexEnvironment(a.getenv) {
		return nil, nil
	}
	thread := a.getenv("CODEX_THREAD_ID")
	if !codexThreadID.MatchString(thread) {
		return nil, errLocalEnrollmentHost
	}
	config, err := configPath(a.getenv)
	if err != nil {
		return nil, err
	}
	config, err = canonicalLocalSetupPath(config)
	if err != nil {
		return nil, err
	}
	project, err := a.observeLocalProject(localSoftwareVersion(), time.Now())
	if err != nil {
		return nil, err
	}
	home := a.getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(platform.Home(a.getenv), ".codex")
	}
	home, err = canonicalCodexPath(home)
	if err != nil {
		return nil, usageErr("Codex home is unavailable")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	r := localEnrollment{IntentID: hex.EncodeToString(id[:]), Config: config, Profile: profile, As: a.as, Directory: project.Root, ThreadID: thread, CodexHome: home, State: "prepared", UpdatedAt: time.Now().UTC()}
	if err := a.saveLocalEnrollment(r); err != nil {
		return nil, err
	}
	return &r, nil
}

// This is a host capability result, not a retryable filesystem failure. No
// target is persisted without an exact identity supplied by the host.
var errLocalEnrollmentHost = errors.New("unsupported automatic setup: host must supply the exact root thread ID in CODEX_THREAD_ID; credentials can be enrolled, but setup requires running enrollment in the intended supported root or explicit vecnet --net PROFILE codex setup --thread UUID --codex-home DIR")

// automaticEnrollment only replays committed intent. It cannot manufacture
// authority for credentials already present under a reused profile name.
func (a *app) automaticEnrollment(profile string) localSetupResult {
	if isCodexEnvironment(a.getenv) && !codexThreadID.MatchString(a.getenv("CODEX_THREAD_ID")) {
		return localSetupResult{State: "unsupported", Reason: errLocalEnrollmentHost.Error()}
	}
	config, err := configPath(a.getenv)
	if err != nil {
		return localSetupResult{State: "pending", Reason: err.Error()}
	}
	config, err = canonicalLocalSetupPath(config)
	if err != nil {
		return localSetupResult{State: "pending", Reason: err.Error()}
	}
	cfg, err := loadConfig(config)
	if err != nil {
		return localSetupResult{State: "pending", Reason: err.Error()}
	}
	records, err := a.localEnrollments()
	if err != nil {
		return localSetupResult{State: "pending", Reason: err.Error()}
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Config == config && records[i].Profile == profile && records[i].As == a.as && records[i].IntentID != "" && records[i].IntentID == cfg.Profiles[profile].EnrollmentIntent {
			return a.reconcileLocalEnrollment(records[i])
		}
	}
	return localSetupResult{State: "pending", Reason: "no saved enrollment intent; use explicit codex setup with the intended exact thread"}
}

func (a *app) ensureLocalRuntime(ctx context.Context) error {
	if a.localRuntime != nil {
		return a.localRuntime(ctx)
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.HasSuffix(bin, ".test") || strings.HasSuffix(bin, ".test.exe") {
		return usageErr("delivery runtime unavailable in test executable")
	}
	_, err = localruntime.Ensure(ctx, localruntime.Options{Home: platform.Home(a.getenv), Binary: bin, Node: a.getenv("VECNET_NODE"), Version: localSoftwareVersion(), Getenv: a.getenv})
	return err
}
func (a *app) reconcileLocalEnrollment(r localEnrollment) localSetupResult {
	result := localSetupResult{State: "pending"}
	fail := func(err error) localSetupResult {
		result.Reason = err.Error()
		r.State = result.State
		r.Reason = result.Reason
		r.UpdatedAt = time.Now().UTC()
		if e := a.saveLocalEnrollment(r); e != nil {
			result.State = "pending"
			result.Reason += "; could not save setup progress: " + e.Error()
		}
		return result
	}
	stop := func(state string, err error) localSetupResult { result.State = state; return fail(err) }
	// Runtime activation has its own 30s bound. Leave room afterward for two
	// 10s connector publication cycles and the addressed readiness probes.
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	// Serialize credential replacement with the entire addressed setup. The
	// existing setup path only writes the independent pin and authority stores.
	configUnlock, err := lockPath(ctx, r.Config+".lock")
	if err != nil {
		return fail(err)
	}
	defer configUnlock()
	cfg, err := loadConfig(r.Config)
	if err != nil {
		return fail(err)
	}
	p, ok := cfg.Profiles[r.Profile]
	if !ok || r.IntentID == "" || r.Hub == "" || p.EnrollmentCaller == "" ||
		p.EnrollmentIntent != r.IntentID || p.EnrollmentCredential != hookDigest([]byte(p.Secret)) ||
		normalizeHub(p.Hub) != r.Hub || p.EnrollmentHub != r.Hub || p.PeerID != p.EnrollmentCaller ||
		(r.Caller != "" && r.Caller != p.EnrollmentCaller) || p.Secret == "" {
		return stop("superseded", usageErr("saved enrollment was not committed or its credential/profile identity was replaced; use explicit addressed setup for the current credentials"))
	}
	// The config commit carries the redeemed caller even if interruption preceded
	// the first post-commit progress write. The registry never carries a secret.
	r.Caller = p.EnrollmentCaller
	if err := a.checkCodexEnrollmentOptOut(r.Config, r.Profile, r.As); err != nil {
		if errors.Is(err, errCodexOptOut) {
			return stop("disabled", err)
		}
		return fail(err)
	}
	if st, err := os.Stat(r.Directory); err != nil || !st.IsDir() {
		return fail(usageErr("enrollment project is unavailable"))
	}
	worker := *a
	worker.ctx = ctx
	worker.localEnrollmentRetry = true
	worker.localProjectRoot = r.Directory
	worker.net = r.Profile
	worker.as = r.As
	worker.json = true
	worker.getenv = func(k string) string {
		switch k {
		case "VECNET_CONFIG":
			return r.Config
		case "CODEX_HOME":
			return r.CodexHome
		case "CODEX_THREAD_ID":
			return r.ThreadID
		case "VECNET_PROFILE":
			return r.Profile
		}
		return a.getenv(k)
	}
	if err := worker.useStoredHub(r.Profile, p); err != nil {
		return fail(err)
	}
	caller, err := worker.codexBindCaller(newClient(p.Hub, p.Secret))
	if err != nil {
		return fail(err)
	}
	if caller != r.Caller {
		return stop("superseded", usageErr("saved enrollment caller changed; use explicit addressed setup for the current credentials"))
	}
	var captured bytes.Buffer
	worker.stdout = &captured
	worker.stderr = io.Discard
	// Saved setup is addressed to this original root. It never follows the
	// conversation that happens to run repair, and never passes --renew.
	if err := worker.ensureLocalRuntime(ctx); err != nil {
		return fail(err)
	}
	if err := worker.retireEmptyLegacySources(); err != nil {
		return fail(err)
	}
	settings := filepath.Join(r.Directory, ".codex/hooks.json")
	// Automatic enrollment uses the default personal project layer's guards.
	// Existing tracked files still need a deliberate explicit installer action.
	if err := defaultSettingsRedirect(settings, "the project .codex/hooks.json"); err != nil {
		return fail(err)
	}
	if _, err := worker.hookGit(r.Directory, "--show-toplevel"); err == nil {
		if err := worker.checkCodexUntracked(r.Directory, settings); err != nil {
			return fail(err)
		}
	} else if !hookNotGitRepo(err) {
		return fail(fmt.Errorf("cannot verify project hook target: %w", err))
	}
	if err := worker.cmdCodexSetup([]string{"--thread", r.ThreadID, "--codex-home", r.CodexHome, "--settings", settings}, time.Now); err != nil {
		return fail(err)
	}
	// A reload first publishes the newly launched engine as unverified; its
	// ready status may not be published until the following 10s heartbeat.
	deadline := time.Now().Add(25 * time.Second)
	for {
		captured.Reset()
		if err := worker.reportCodexSetup(r.Profile, r.ThreadID, r.CodexHome, r.Config, time.Now()); err != nil {
			return fail(err)
		}
		var report codexSetupReport
		if err := json.Unmarshal(captured.Bytes(), &report); err != nil {
			return fail(err)
		}
		if report.Delivery == "ready" {
			result.State = "ready"
			break
		}
		result.Reason = report.Reason
		if report.Delivery == "unsupported" {
			return fail(usageErr(report.Reason))
		}
		if time.Now().After(deadline) || !sleepCtx(ctx, 250*time.Millisecond) {
			return fail(usageErr(result.Reason))
		}
	}
	r.State = result.State
	r.Reason = ""
	r.UpdatedAt = time.Now().UTC()
	if err := a.saveLocalEnrollment(r); err != nil {
		return fail(err)
	}
	return result
}
func (a *app) retryLocalEnrollments() error {
	records, err := a.localEnrollments()
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range records {
		if !localEnrollmentRetryable(r.State) {
			continue
		}
		if result := a.reconcileLocalEnrollment(r); localEnrollmentRetryable(result.State) {
			failures = append(failures, fmt.Errorf("%s: %s", r.Profile, result.Reason))
		}
	}
	return errors.Join(failures...)
}

// Resolve the existing prefix for a config that is about to be created.
func canonicalLocalSetupPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	tail := []string{}
	for {
		resolved, err := platform.Canonical(path)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
			return "", err
		}
		if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", usageErr("dangling setup path symlink")
		}
		tail = append(tail, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

func localEnrollmentRetryable(state string) bool { return state == "prepared" || state == "pending" }
