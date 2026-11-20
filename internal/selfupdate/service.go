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
	// Audit, when non-nil, records the confirmed install target once the
	// job's registry re-check has chosen it, before npm runs.
	Audit func(from, to string)
	Log   *slog.Logger

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
// registry. It takes no version argument by design. It returns the new job,
// or one of ErrUnavailable, ErrNoUpdate, ErrBusy and ErrCooldown (with how
// long until a start is allowed).
func (s *Service) Start() (Job, time.Duration, error) {
	if s == nil || s.Checker == nil || !s.Checker.Enabled() {
		return Job{}, 0, ErrUnavailable
	}
	latest, _ := s.Checker.Latest()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inst == nil {
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
	s.lastStart = now
	s.job = &Job{State: StateChecking, Version: latest, StartedAt: now.UTC().Format(time.RFC3339)}
	j := *s.job
	inst := *s.inst
	go s.run(inst)
	return j, 0, nil
}

// run is the update job. Any failure before Restart leaves the running hub
// as it was: npm replaces files on disk, never this process's image.
func (s *Service) run(inst Install) {
	ctx, cancel := context.WithTimeout(context.Background(), InstallTimeout)
	defer cancel()

	// Re-read the registry now: the cached answer may be hours old. Install
	// only a version it confirms is newer, never one from the request.
	version, err := s.Checker.Check(ctx)
	if err != nil || !Newer(version, s.Checker.Current) {
		s.fail("could not confirm the latest version on npm; nothing was installed")
		return
	}
	s.set(StateInstalling, version)
	if s.Audit != nil {
		s.Audit(s.Checker.Current, version)
	}
	install := s.Install
	if install == nil {
		install = s.Sys.Run
	}
	args := []string{"install", "--global", "--prefix", inst.Prefix, "--registry=" + Registry, "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error", PackageName + "@" + version}
	out, err := install(ctx, inst.NPM, args, NPMEnv(s.Sys.Getenv, &inst))
	if err != nil {
		s.Log.Warn("vecnet-hub: npm install failed", "version", version, "err", err.Error(), "output", tail(out, 2048))
		msg := "npm install failed; the hub keeps running " + s.Checker.Current
		if ctx.Err() != nil {
			msg = "npm install timed out; the hub keeps running " + s.Checker.Current
		}
		s.fail(msg)
		return
	}
	s.set(StateVerifying, version)
	if err := VerifyInstalled(s.Sys, &inst, version); err != nil {
		s.Log.Warn("vecnet-hub: installed update did not verify", "version", version, "err", err.Error())
		s.fail(err.Error() + "; the hub keeps running " + s.Checker.Current + ". Reinstall with: npm install -g " + PackageName + "@" + version)
		return
	}
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
