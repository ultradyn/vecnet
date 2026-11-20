package selfupdate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type serviceFixture struct {
	reg      *fakeRegistry
	host     *fakeHost
	svc      *Service
	now      time.Time
	mu       sync.Mutex
	installs [][]string
	envs     [][]string
	restarts int
	// onInstall runs inside the fake npm install; it may rewrite host files.
	onInstall func() error
}

func newServiceFixture(t *testing.T, latest string, withRestart bool) *serviceFixture {
	t.Helper()
	f := &serviceFixture{reg: newFakeRegistry(t, `{"latest":"`+latest+`"}`), host: newFakeHost("0.2.6"), now: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	c := f.reg.checker("0.2.6", time.Hour)
	c.Now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	f.svc = NewService(c, f.host.system())
	f.svc.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	f.svc.Install = func(_ context.Context, name string, args, env []string) ([]byte, error) {
		f.mu.Lock()
		f.installs = append(f.installs, append([]string{name}, args...))
		f.envs = append(f.envs, env)
		hook := f.onInstall
		f.mu.Unlock()
		if hook != nil {
			return []byte("npm output"), hook()
		}
		// A good install: the package now carries the new version.
		f.host.setFile(pkgRoot+"/package.json", `{"name":"vecnet","version":"`+latest+`"}`)
		return nil, nil
	}
	if withRestart {
		f.svc.Restart = func() { f.mu.Lock(); f.restarts++; f.mu.Unlock() }
	}
	f.svc.Detect(context.Background())
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *serviceFixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func waitJob(t *testing.T, svc *Service, states ...string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := svc.Status(); st.Job != nil {
			for _, s := range states {
				if st.Job.State == s {
					return *st.Job
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job never reached %v: %+v", states, svc.Status().Job)
	return Job{}
}

func TestStatusReportsUpdateAndOneClick(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	st := f.svc.Status()
	if !st.Enabled || st.Current != "0.2.6" || st.Latest != "0.2.7" || !st.UpdateAvailable || !st.OneClick || !st.Restart {
		t.Fatalf("Status = %+v", st)
	}
	if st.ReleaseURL != "https://www.npmjs.com/package/vecnet/v/0.2.7" || st.ManualCommand != "" || st.CheckedAt == "" {
		t.Fatalf("Status = %+v", st)
	}
}

func TestStatusUpToDate(t *testing.T) {
	f := newServiceFixture(t, "0.2.6", true)
	st := f.svc.Status()
	if !st.Enabled || st.UpdateAvailable || st.OneClick || st.ReleaseURL != "" {
		t.Fatalf("Status = %+v", st)
	}
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("Start = %v", err)
	}
}

func TestStatusManualWhenNotDetected(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.host.env["FLY_APP_NAME"] = "vecnet"
	f.svc = NewService(f.svc.Checker, f.host.system())
	f.svc.Detect(context.Background())
	st := f.svc.Status()
	if !st.UpdateAvailable || st.OneClick || st.ManualReason != ReasonContainer || st.ManualCommand != "npm install -g vecnet@0.2.7" {
		t.Fatalf("Status = %+v", st)
	}
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start = %v", err)
	}
	if len(f.installs) != 0 {
		t.Fatal("installed without one-click")
	}
}

func TestStartInstallsVerifiedVersionAndRestarts(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	job, _, err := f.svc.Start()
	if err != nil || job.Version != "0.2.7" || job.State != StateChecking {
		t.Fatalf("Start = %+v, %v", job, err)
	}
	done := waitJob(t, f.svc, StateRestarting, StateFailed)
	if done.State != StateRestarting || done.Error != "" {
		t.Fatalf("job = %+v", done)
	}
	// Restart runs just after the state change.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		f.mu.Lock()
		n := f.restarts
		f.mu.Unlock()
		if n > 0 {
			break
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restarts != 1 || len(f.installs) != 1 {
		t.Fatalf("restarts %d installs %v", f.restarts, f.installs)
	}
	want := prefix + "/bin/npm install --global --prefix " + prefix + " --registry=https://registry.npmjs.org/ --strict-ssl=true --ignore-scripts --no-audit --no-fund --loglevel=error vecnet@0.2.7"
	if got := strings.Join(f.installs[0], " "); got != want {
		t.Fatalf("install = %s", got)
	}
	if strings.Contains(strings.Join(f.envs[0], "\n"), "VECNET_") {
		t.Fatal("hub environment reached npm")
	}
	// The new binary ran its --version self-check before the restart.
	f.host.mu.Lock()
	last := strings.Join(f.host.runs[len(f.host.runs)-1], " ")
	f.host.mu.Unlock()
	if last != nativeExe+" --version" {
		t.Fatalf("last command = %q", last)
	}
	// Registry: the initial check plus the job's re-check.
	if n := f.reg.hits.Load(); n != 2 {
		t.Fatalf("registry hits = %d", n)
	}
}

func TestStartWithoutRestartEndsInstalled(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", false)
	if st := f.svc.Status(); !st.OneClick || st.Restart {
		t.Fatalf("Status = %+v", st)
	}
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, f.svc, StateInstalled, StateFailed); j.State != StateInstalled {
		t.Fatalf("job = %+v", j)
	}
}

// The job installs exactly the version Start accepted, and only while npm
// still names it latest: a pulled tag, or one that moved, installs nothing.
func TestStartRechecksRegistry(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.reg.set(200, `{"latest":"0.2.6"}`)
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, f.svc, StateFailed, StateRestarting)
	if j.State != StateFailed || !strings.Contains(j.Error, "nothing was installed") {
		t.Fatalf("job = %+v", j)
	}
	if len(f.installs) != 0 || f.restarts != 0 {
		t.Fatal("installed or restarted")
	}
}

func TestStartRegistryDownInstallsNothing(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.reg.set(503, ``)
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, f.svc, StateFailed, StateRestarting); j.State != StateFailed {
		t.Fatalf("job = %+v", j)
	}
	if len(f.installs) != 0 || f.restarts != 0 {
		t.Fatal("installed or restarted")
	}
}

func TestFailedInstallLeavesHubRunning(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.onInstall = func() error { return errors.New("exit status 1") }
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, f.svc, StateFailed, StateRestarting)
	if j.State != StateFailed || !strings.Contains(j.Error, "keeps running 0.2.6") || strings.Contains(j.Error, "npm output") {
		t.Fatalf("job = %+v", j)
	}
	if f.restarts != 0 {
		t.Fatal("restarted after a failed install")
	}
}

func TestUnverifiedInstallDoesNotRestart(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	// npm "succeeds" but leaves the old version in place.
	f.onInstall = func() error { return nil }
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, f.svc, StateFailed, StateRestarting)
	if j.State != StateFailed || !strings.Contains(j.Error, "not the expected version") {
		t.Fatalf("job = %+v", j)
	}
	if f.restarts != 0 {
		t.Fatal("restarted an unverified install")
	}
}

func TestStartSingleFlightAndCooldown(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", false)
	release := make(chan struct{})
	f.onInstall = func() error { <-release; return errors.New("fail") }
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.svc, StateInstalling)
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start = %v", err)
	}
	close(release)
	waitJob(t, f.svc, StateFailed)
	_, wait, err := f.svc.Start()
	if !errors.Is(err, ErrCooldown) || wait <= 0 || wait > Cooldown {
		t.Fatalf("Start in cooldown = %v, %v", err, wait)
	}
	f.advance(Cooldown)
	f.onInstall = nil
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatalf("Start after cooldown = %v", err)
	}
	waitJob(t, f.svc, StateInstalled)
	if len(f.installs) != 2 {
		t.Fatalf("installs = %d", len(f.installs))
	}
}

func TestRestartingJobBlocksAnotherStart(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.svc, StateRestarting)
	f.advance(time.Hour)
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Start while restarting = %v", err)
	}
}

func TestStartRefusesWhenTagMovedOn(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.reg.set(200, `{"latest":"0.2.8"}`)
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, f.svc, StateFailed, StateRestarting)
	if j.State != StateFailed || !strings.Contains(j.Error, "could not confirm 0.2.7") {
		t.Fatalf("job = %+v", j)
	}
	if len(f.installs) != 0 {
		t.Fatal("installed a version Start did not audit")
	}
}

// M4: detection is redone at job start. A package installed by hand since
// boot (any version but the running one) is never replaced or downgraded.
func TestStartRedetectsAndNeverReplacesManualInstall(t *testing.T) {
	for name, pkg := range map[string]string{
		"pre-release": `{"name":"vecnet","version":"0.3.0-rc.1"}`,
		"newer":       `{"name":"vecnet","version":"0.2.9"}`,
		"older":       `{"name":"vecnet","version":"0.2.5"}`,
		"other name":  `{"name":"evil","version":"0.2.6"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newServiceFixture(t, "0.2.7", true)
			f.host.setFile(pkgRoot+"/package.json", pkg)
			if _, _, err := f.svc.Start(); err != nil {
				t.Fatal(err)
			}
			j := waitJob(t, f.svc, StateFailed, StateRestarting)
			if j.State != StateFailed || !strings.Contains(j.Error, "no longer qualifies") {
				t.Fatalf("job = %+v", j)
			}
			if len(f.installs) != 0 || f.restarts != 0 {
				t.Fatal("installed or restarted")
			}
		})
	}
}

func TestStartRedetectsContainerAndPrefix(t *testing.T) {
	for name, mutate := range map[string]func(h *fakeHost){
		"npm gone":       func(h *fakeHost) { h.mu.Lock(); delete(h.files, prefix+"/bin/npm"); h.mu.Unlock() },
		"prefix moved":   func(h *fakeHost) { h.mu.Lock(); h.prefix = "/usr/local"; h.mu.Unlock() },
		"now read-only":  func(h *fakeHost) { h.mu.Lock(); h.readonly[prefix+"/bin"] = true; h.mu.Unlock() },
		"systemd-nspawn": func(h *fakeHost) { h.setFile("/run/systemd/container", "systemd-nspawn") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newServiceFixture(t, "0.2.7", true)
			mutate(f.host)
			if _, _, err := f.svc.Start(); err != nil {
				t.Fatal(err)
			}
			if j := waitJob(t, f.svc, StateFailed, StateRestarting); j.State != StateFailed {
				t.Fatalf("job = %+v", j)
			}
			if len(f.installs) != 0 {
				t.Fatal("installed")
			}
		})
	}
}

// A missing package (an earlier install stopped half way) may be retried.
func TestStartAllowsMissingPackage(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.host.mu.Lock()
	delete(f.host.files, pkgRoot+"/package.json")
	f.host.mu.Unlock()
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, f.svc, StateRestarting, StateFailed); j.State != StateRestarting {
		t.Fatalf("job = %+v", j)
	}
}

// M2: a binary that fails its self-check never drains or restarts.
func TestSelfCheckFailureDoesNotRestart(t *testing.T) {
	for name, setup := range map[string]func(h *fakeHost){
		"does not run":  func(h *fakeHost) { h.mu.Lock(); h.selfErr = errors.New("exec format error"); h.mu.Unlock() },
		"wrong version": func(h *fakeHost) { h.mu.Lock(); h.selfOut = "vecnet-hub 0.2.6\n"; h.mu.Unlock() },
		"garbage":       func(h *fakeHost) { h.mu.Lock(); h.selfOut = "Segmentation fault"; h.mu.Unlock() },
	} {
		t.Run(name, func(t *testing.T) {
			f := newServiceFixture(t, "0.2.7", true)
			setup(f.host)
			if _, _, err := f.svc.Start(); err != nil {
				t.Fatal(err)
			}
			j := waitJob(t, f.svc, StateFailed, StateRestarting)
			if j.State != StateFailed || !strings.Contains(j.Error, "not restarting") || !strings.Contains(j.Error, "npm install -g vecnet@0.2.7") {
				t.Fatalf("job = %+v", j)
			}
			if f.restarts != 0 {
				t.Fatal("restarted after a failed self-check")
			}
		})
	}
}

func TestWrongArchitectureDoesNotRestart(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.onInstall = func() error {
		f.host.setFile(pkgRoot+"/package.json", `{"name":"vecnet","version":"0.2.7"}`)
		f.host.setFile(nativeExe, elfHeader(183)) // aarch64 on an amd64 hub
		return nil
	}
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, f.svc, StateFailed, StateRestarting)
	if j.State != StateFailed || !strings.Contains(j.Error, "another CPU architecture") {
		t.Fatalf("job = %+v", j)
	}
	if f.restarts != 0 {
		t.Fatal("restarted a wrong-architecture binary")
	}
}

// L3: the audit is written at Start; a failed write starts nothing.
func TestStartAuditsFirstAndRefusesOnAuditFailure(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	var audited []string
	f.svc.Audit = func(from, to string) error { audited = append(audited, from+">"+to); return errors.New("disk full") }
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrAudit) {
		t.Fatalf("Start = %v", err)
	}
	if st := f.svc.Status(); st.Job != nil {
		t.Fatalf("job started: %+v", st.Job)
	}
	f.svc.Audit = func(from, to string) error { audited = append(audited, from+">"+to); return nil }
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatalf("Start after audit recovers = %v", err)
	}
	waitJob(t, f.svc, StateRestarting)
	if strings.Join(audited, ",") != "0.2.6>0.2.7,0.2.6>0.2.7" {
		t.Fatalf("audited = %v", audited)
	}
}

// L2: the job runs under the hub's context; shutdown stops npm.
func TestShutdownCancelsInstall(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	f.svc.Context, f.svc.Go = ctx, wg.Go
	f.svc.Install = func(ctx context.Context, _ string, _, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.svc, StateInstalling)
	cancel()
	wg.Wait() // the job ended with the hub
	j := waitJob(t, f.svc, StateFailed)
	if !strings.Contains(j.Error, "shutting down") || f.restarts != 0 {
		t.Fatalf("job = %+v restarts %d", j, f.restarts)
	}
}

// L8: a hub on an ephemeral port is never offered one-click.
func TestEphemeralListenRefusesOneClick(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	f.svc.EphemeralListen = true
	st := f.svc.Status()
	if st.OneClick || st.ManualReason != ReasonEphemeralPort || st.ManualCommand == "" {
		t.Fatalf("Status = %+v", st)
	}
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start = %v", err)
	}
}

// N3: a refusal found when a job starts is remembered, so the card stops
// offering one-click and further clicks neither audit nor run.
func TestRedetectRefusalIsRemembered(t *testing.T) {
	f := newServiceFixture(t, "0.2.7", true)
	audits := 0
	f.svc.Audit = func(string, string) error { audits++; return nil }
	f.host.setFile(pkgRoot+"/package.json", `{"name":"vecnet","version":"0.3.0-rc.1"}`)
	if _, _, err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.svc, StateFailed)
	st := f.svc.Status()
	if st.OneClick || st.ManualReason != ReasonVersion || st.ManualCommand == "" {
		t.Fatalf("Status = %+v", st)
	}
	f.advance(time.Hour)
	if _, _, err := f.svc.Start(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second Start = %v", err)
	}
	if audits != 1 || len(f.installs) != 0 {
		t.Fatalf("audits %d installs %d", audits, len(f.installs))
	}
}

// N2: the self-check reads standard output only; standard error noise
// (a Node or loader warning) neither fails nor passes it.
func TestSelfCheckReadsStdoutOnly(t *testing.T) {
	inst := &Install{Prefix: prefix, Root: pkgRoot, Exe: nativeExe}
	sys := System{RunStdout: func(context.Context, string, []string, []string) ([]byte, []byte, error) {
		return []byte("vecnet-hub 0.2.7\n"), []byte("warning: something noisy\n"), nil
	}}
	if err := SelfCheck(context.Background(), sys, inst, "0.2.7"); err != nil {
		t.Fatalf("stderr noise failed the check: %v", err)
	}
	sys.RunStdout = func(context.Context, string, []string, []string) ([]byte, []byte, error) {
		return nil, []byte("vecnet-hub 0.2.7\n"), nil
	}
	if err := SelfCheck(context.Background(), sys, inst, "0.2.7"); err == nil {
		t.Fatal("a version on stderr passed the check")
	}
	sys.RunStdout = func(context.Context, string, []string, []string) ([]byte, []byte, error) {
		return nil, []byte("error while loading shared libraries\n"), errors.New("exit status 127")
	}
	err := SelfCheck(context.Background(), sys, inst, "0.2.7")
	if err == nil || !strings.Contains(err.Error(), "did not run (stderr: error while loading shared libraries)") {
		t.Fatalf("err = %v", err)
	}
}
