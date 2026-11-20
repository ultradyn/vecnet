package selfupdate

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Job states, in order. A job ends in installed (no restart wired),
// restarting (the hub is about to re-execute itself) or failed.
const (
	StateChecking   = "checking"
	StateInstalling = "installing"
	StateVerifying  = "verifying"
	StateRestarting = "restarting"
	StateInstalled  = "installed"
	StateFailed     = "failed"
)

// Limits on the update job.
const (
	InstallTimeout = 10 * time.Minute
	// Cooldown separates the starts of two jobs, so repeated clicks or a
	// script cannot run npm in a loop.
	Cooldown = time.Minute
)

// Errors Start returns; the HTTP layer maps each to a fixed status and code.
var (
	ErrUnavailable = errors.New("one-click update is not available on this hub")
	ErrNoUpdate    = errors.New("no newer version is known")
	ErrBusy        = errors.New("an update is already running")
	ErrCooldown    = errors.New("an update started recently")
	ErrAudit       = errors.New("could not record the update in the audit log; nothing was started")
)

// Job is the state of the last update job.
type Job struct {
	State      string `json:"state"`
	Version    string `json:"version,omitempty"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	// Error is a fixed explanation on failure; it never carries npm output.
	Error string `json:"error,omitempty"`
}

// Status is GET /v1/admin/update.
type Status struct {
	// Enabled: the hub checks npm (a release build with a non-zero
	// interval). When false every other field but Current is empty.
	Enabled         bool   `json:"enabled"`
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	CheckedAt       string `json:"checked_at,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url,omitempty"`
	// OneClick: POST /v1/admin/update can install Latest on this host.
	OneClick bool `json:"one_click"`
	// Restart: a one-click update restarts the hub by itself.
	Restart bool `json:"restart"`
	// ManualReason is why one-click update is not offered (Reason* codes).
	ManualReason  string `json:"manual_reason,omitempty"`
	ManualCommand string `json:"manual_command,omitempty"`
	Job           *Job   `json:"job,omitempty"`
}

// Service is the hub's update state: the checker, what Detect found, and the
// current job. The zero value is not usable; build one with NewService.
type Service struct {
	Checker *Checker
	Sys     System
	// Install runs npm install; nil means Sys.Run. Tests inject a fake so no
	// test ever installs anything.
	Install Runner
	// Restart asks the hub to drain and re-execute itself; nil means the
	// operator restarts it (the job ends installed).
	Restart func()
	// Audit, when non-nil, records an update as Start accepts it; an error
	// refuses the start (ErrAudit).
	Audit func(from, to string) error
	// Context, when non-nil, bounds every job: the hub passes its
	// background context, so shutdown cancels a running npm install.
	Context context.Context
	// Go, when non-nil, runs a job (the hub's background WaitGroup), so
	// shutdown waits for it; nil means a plain goroutine.
	Go func(func())
	// EphemeralListen: the hub listens on port 0, so a restart would come up
	// on another port. One-click update is refused (ReasonEphemeralPort).
	EphemeralListen bool
	Log             *slog.Logger

	mu        sync.Mutex
	detected  bool
	inst      *Install
	reason    string
	job       *Job
	lastStart time.Time
}

// NewService returns a Service. Detection runs later, in Detect.
func NewService(c *Checker, sys System) *Service {
	return &Service{Checker: c, Sys: sys, Log: slog.Default()}
}

// Detect runs Detect once and remembers the answer; a later call, or one
// after SetDetected, keeps the first answer. A disabled checker skips it:
// with the check off, one-click update is never offered.
func (s *Service) Detect(ctx context.Context) {
	if s == nil || !s.Checker.Enabled() {
		return
	}
	s.mu.Lock()
	done := s.detected
	s.mu.Unlock()
	if done {
		return
	}
	inst, reason := Detect(ctx, s.Sys, s.Checker.Current)
	s.mu.Lock()
	s.detected, s.inst, s.reason = true, inst, reason
	s.mu.Unlock()
	if inst != nil {
		s.Log.Info("vecnet-hub: one-click update available (global npm install)", "prefix", inst.Prefix)
	} else {
		s.Log.Info("vecnet-hub: one-click update not offered", "reason", reason)
	}
}

// SetDetected records a detection result in place of Detect, for callers
// (tests) that build an Install themselves.
func (s *Service) SetDetected(inst *Install, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detected, s.inst, s.reason = true, inst, reason
}

// Status reports the check, detection and job state. It makes no network
// request.
func (s *Service) Status() Status {
	if s == nil || s.Checker == nil {
		return Status{}
	}
	st := Status{Current: s.Checker.Current}
	if !s.Checker.Enabled() {
		return st
	}
	st.Enabled = true
	latest, at := s.Checker.Latest()
	s.mu.Lock()
	inst, reason, detected := s.inst, s.reason, s.detected
	if s.job != nil {
		j := *s.job
		st.Job = &j
	}
	s.mu.Unlock()
	if latest == "" {
		return st
	}
	st.Latest, st.CheckedAt = latest, at.UTC().Format(time.RFC3339)
	st.UpdateAvailable = Newer(latest, st.Current)
	if !st.UpdateAvailable {
		return st
	}
	st.ReleaseURL = "https://www.npmjs.com/package/" + PackageName + "/v/" + latest
	switch {
	case inst != nil && s.EphemeralListen:
		st.ManualReason = ReasonEphemeralPort
	case inst != nil:
		st.OneClick, st.Restart = true, s.Restart != nil
	case !detected:
		st.ManualReason = ReasonDetectFailed
	default:
		st.ManualReason = reason
	}
	if !st.OneClick {
		st.ManualCommand = "npm install -g " + PackageName + "@" + latest
	}
	return st
}

// Start begins an update to the version the checker last read from the
// registry. It takes no version argument by design. It audits the update
// before anything runs and returns the new job, or one of ErrUnavailable,
// ErrNoUpdate, ErrBusy, ErrCooldown (with how long until a start is
// allowed) and ErrAudit.
func (s *Service) Start() (Job, time.Duration, error) {
	if s == nil || s.Checker == nil || !s.Checker.Enabled() {
		return Job{}, 0, ErrUnavailable
	}
	latest, _ := s.Checker.Latest()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inst == nil || s.EphemeralListen {
		return Job{}, 0, ErrUnavailable
	}
	// A running job, or one that has handed over to the restart, is busy.
	if s.job != nil && s.job.State != StateInstalled && s.job.State != StateFailed {
		return Job{}, 0, ErrBusy
	}
	if !Newer(latest, s.Checker.Current) {
		return Job{}, 0, ErrNoUpdate
	}
	now := s.Checker.now()
	if !s.lastStart.IsZero() {
		if wait := s.lastStart.Add(Cooldown).Sub(now); wait > 0 {
			return Job{}, wait, ErrCooldown
		}
	}
	if s.Audit != nil {
		if err := s.Audit(s.Checker.Current, latest); err != nil {
			s.Log.Warn("vecnet-hub: audit hub_update failed; update not started", "err", err.Error())
			return Job{}, 0, ErrAudit
		}
	}
	s.lastStart = now
	s.job = &Job{State: StateChecking, Version: latest, StartedAt: now.UTC().Format(time.RFC3339)}
	j := *s.job
	ctx := s.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if s.Go != nil {
		s.Go(func() { s.run(ctx, latest) })
	} else {
		go s.run(ctx, latest)
	}
	return j, 0, nil
}

// reinstall is the manual recovery command for version.
func reinstall(version string) string {
	return "npm install -g " + PackageName + "@" + version
}

// run is the update job for exactly the version Start accepted and audited.
// Any failure before Restart leaves the running hub as it was: npm replaces
// files on disk, never this process's image.
func (s *Service) run(parent context.Context, version string) {
	ctx, cancel := context.WithTimeout(parent, InstallTimeout)
	defer cancel()
	current := s.Checker.Current
	keeps := "; the hub keeps running " + current

	// Re-read the registry: npm must still name this version as latest. A
	// tag that moved since the click (or was pulled) installs nothing.
	latest, err := s.Checker.Check(ctx)
	if err != nil || latest != version {
		s.fail("could not confirm " + version + " as the latest version on npm; nothing was installed" + keeps)
		return
	}
	// Re-detect from disk now, not at boot: npm, the prefix and the package
	// may have changed. A package.json naming any version but the running
	// one (say a pre-release installed by hand) is never replaced.
	inst, reason := Redetect(ctx, s.Sys, current)
	if inst == nil {
		s.Log.Warn("vecnet-hub: update refused; installation changed since start", "reason", reason)
		s.fail("this installation no longer qualifies for one-click update (" + reason + "); nothing was installed" + keeps)
		return
	}
	s.set(StateInstalling, version)
	install := s.Install
	if install == nil {
		install = s.Sys.Run
	}
	args := []string{"install", "--global", "--prefix", inst.Prefix, "--registry=" + Registry, "--strict-ssl=true",
		"--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error", PackageName + "@" + version}
	out, err := install(ctx, inst.NPM, args, NPMEnv(s.Sys.Getenv, inst))
	if err != nil {
		s.Log.Warn("vecnet-hub: npm install failed", "version", version, "err", err.Error(), "output", tail(out, 4096))
		what := "npm install failed"
		switch {
		case parent.Err() != nil:
			what = "npm install was stopped by the hub shutting down"
		case ctx.Err() != nil:
			what = "npm install timed out"
		}
		s.fail(what + keeps + ". If vecnet-hub no longer starts, reinstall with: " + reinstall(version))
		return
	}
	s.set(StateVerifying, version)
	if err := VerifyInstalled(s.Sys, inst, version); err != nil {
		s.Log.Warn("vecnet-hub: installed update did not verify", "version", version, "err", err.Error())
		s.fail(err.Error() + keeps + ", but the next start may fail. Reinstall with: " + reinstall(version))
		return
	}
	if err := SelfCheck(ctx, s.Sys, inst, version); err != nil {
		s.Log.Warn("vecnet-hub: installed update failed its self-check", "version", version, "err", err.Error())
		s.fail(err.Error() + "; not restarting" + keeps + ", but the next start may fail. Reinstall with: " + reinstall(version))
		return
	}
	s.mu.Lock()
	s.inst = inst
	s.mu.Unlock()
	if s.Restart == nil {
		s.finish(StateInstalled, "")
		s.Log.Info("vecnet-hub: update installed; restart the hub to run it", "version", version)
		return
	}
	s.finish(StateRestarting, "")
	s.Log.Info("vecnet-hub: update installed; restarting", "version", version)
	s.Restart()
}

func (s *Service) set(state, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job.State, s.job.Version = state, version
}

func (s *Service) finish(state, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job.State, s.job.Error = state, msg
	s.job.FinishedAt = s.Checker.now().UTC().Format(time.RFC3339)
}

func (s *Service) fail(msg string) { s.finish(StateFailed, msg) }

// Executable is the binary a restart re-executes; "" when no install was
// detected.
func (s *Service) Executable() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inst == nil {
		return ""
	}
	return s.inst.Exe
}

// tail keeps the last n bytes of npm's output for the log.
func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}
