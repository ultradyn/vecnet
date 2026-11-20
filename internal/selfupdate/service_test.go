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
		f.host.files[pkgRoot+"/package.json"] = `{"name":"vecnet","version":"` + latest + `"}`
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
	want := prefix + "/bin/npm install --global --prefix " + prefix + " --registry=https://registry.npmjs.org/ --ignore-scripts --no-audit --no-fund --loglevel=error vecnet@0.2.7"
	if got := strings.Join(f.installs[0], " "); got != want {
		t.Fatalf("install = %s", got)
	}
	if strings.Contains(strings.Join(f.envs[0], "\n"), "VECNET_") {
		t.Fatal("hub environment reached npm")
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

// The job installs what the registry says at click time; if that is no
// longer newer (latest was pulled or the tag moved back), nothing installs.
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
