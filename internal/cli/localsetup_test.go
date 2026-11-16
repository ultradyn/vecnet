package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutomaticEnrollmentRetainsCredentialsAndRetriesWithoutToken(t *testing.T) {
	home := localSetupHome(t)
	project := t.TempDir()
	t.Chdir(project)
	codexHome := filepath.Join(home, "codex")
	if err := cliFixtureMkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	var redeemed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/enrol":
			redeemed.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"peer_id": "peer_A", "secret": "vn_peer_saved"})
		case "/v1/peers/me":
			json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": "peer_A"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	env := map[string]string{"HOME": home, "VECNET_CONFIG": filepath.Join(home, "config.toml"), "CODEX_HOME": codexHome, "CODEX_THREAD_ID": bindT1}
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdin: strings.NewReader("invitation"), stdout: &out, stderr: &errs, getenv: func(k string) string { return env[k] }}
	a.localRuntime = func(context.Context) error { return usageErr("runtime unavailable") }
	if err := a.cmdEnrol([]string{"--hub", server.URL, "--token", "-", "--profile", "new-peer"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(env["VECNET_CONFIG"])
	if err != nil || cfg.Profiles["new-peer"].Secret != "vn_peer_saved" {
		t.Fatalf("credentials: %v %#v", err, cfg)
	}
	if !strings.Contains(out.String(), "setup pending") {
		t.Fatalf("false enrollment readiness: %s / %s", out.String(), errs.String())
	}
	records, err := a.localEnrollments()
	if err != nil || len(records) != 1 || records[0].ThreadID != bindT1 {
		t.Fatalf("pending setup: %#v %v", records, err)
	}
	if err := a.retryLocalEnrollments(); err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("pending retry must retain the runtime failure: %v", err)
	}
	if redeemed.Load() != 1 {
		t.Fatal("retry redeemed the invitation again")
	}
	if _, err := os.Stat(filepath.Join(home, ".local/state/vecnet/codex/bindings.json")); !os.IsNotExist(err) {
		t.Fatalf("unavailable runtime acquired ownership: %v", err)
	}
}

func TestAutomaticEnrollmentDoesNotInferCodexThread(t *testing.T) {
	home := localSetupHome(t)
	t.Chdir(t.TempDir())
	var out, errs bytes.Buffer
	env := map[string]string{"HOME": home, "VECNET_CONFIG": filepath.Join(home, "config.toml"), "CODEX_CI": "1"}
	a := &app{ctx: context.Background(), stdout: &out, stderr: &errs, getenv: func(k string) string { return env[k] }}
	if err := saveConfig(env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{"p": {Hub: "http://localhost", Secret: "vn_peer_local"}}}); err != nil {
		t.Fatal(err)
	}
	r := a.automaticEnrollment("p")
	if r.State != "unsupported" || !strings.Contains(r.Reason, "thread") {
		t.Fatalf("%#v", r)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/state/vecnet/codex/bindings.json")); !os.IsNotExist(err) {
		t.Fatal("inferred ownership")
	}
}

func TestAllProjectRepairMigratesOtherCheckoutAndDefersMissing(t *testing.T) {
	home := localSetupHome(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	env := map[string]string{"HOME": home}
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdout: &out, stderr: &errs, getenv: func(k string) string { return env[k] }}
	cliFixtureChdir(t, dirB)
	if _, err := a.observeLocalProject("old", time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dirB, ".codex/hooks.json")
	if err := cliFixtureMkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"custom":true,"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile old --event user-prompt","timeout":5}]}]}}`
	if err := cliFixtureWriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.registerLocalArtifact(path, "codex-hooks", "old", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}
	if _, err := a.observeLocalProject("new", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.repairLocalProjects(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if bytes.Contains(got, []byte("--profile old")) || !bytes.Contains(got, []byte("--client codex")) || !bytes.Contains(got, []byte("true")) {
		t.Fatalf("other checkout not migrated: %s", got)
	}
	if err := os.RemoveAll(dirB); err != nil {
		t.Fatal(err)
	}
	_ = a.repairLocalProjects()
	if _, err := os.Stat(dirB); !os.IsNotExist(err) {
		t.Fatal("recreated deleted project")
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Root == dirB && p.Status != "deferred" {
			t.Fatalf("missing project marked complete: %#v", p)
		}
	}
}

func TestLocalMaintenanceDoesNotRunForDeliveryChildrenOrStatus(t *testing.T) {
	for _, args := range [][]string{{"hook", "--event", "app-server"}, {"codex", "status"}, {"codex", "bindings"}, {"projects", "list"}, {"watch"}, {"mcp"}, {"help"}} {
		if localMaintenanceCommand(args) {
			t.Fatalf("recursive/read-only maintenance allowed: %v", args)
		}
	}
	if !localMaintenanceCommand([]string{"peers"}) {
		t.Fatal("ordinary use not registered")
	}
}

func TestAutomaticEnrollmentReadyUsesSavedRootAndPreservesGeneration(t *testing.T) {
	f := newBindFixture(t)
	if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	f.now = time.Now().UTC()
	project, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	f.env["CODEX_THREAD_ID"] = bindT1
	f.bind("p", bindT1)
	writeSetupStatus(t, f)
	before := append([]byte(nil), f.raw()...)
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }, localRuntime: func(context.Context) error { return nil }}
	r, err := a.prepareLocalEnrollment("p")
	if err != nil {
		t.Fatal(err)
	}
	commitEnrollmentFixture(t, a, r, "peer_A")
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	if got := a.reconcileLocalEnrollment(*r); got.State != "ready" {
		t.Fatalf("%#v", got)
	}
	if !bytes.Equal(before, f.raw()) {
		t.Fatal("repair renewed or changed existing owner")
	}
	if _, err := os.Stat(filepath.Join(project, ".codex/hooks.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".codex/hooks.json")); !os.IsNotExist(err) {
		t.Fatal("repair followed invoking cwd")
	}
	projects, err := a.localProjects()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range projects {
		if p.Root == project && len(p.Artifacts) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved-root hook artifact not registered: %#v", projects)
	}
}

func TestAutomaticEnrollmentCannotTransferOrRenewExpiredOwner(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "different-root", true: "expired"}[expired], func(t *testing.T) {
			f := newBindFixture(t)
			if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
				t.Fatal(err)
			}
			f.now = time.Now().UTC()
			if expired {
				f.now = f.now.Add(-48 * time.Hour)
			}
			f.bind("p", bindT1)
			before := append([]byte(nil), f.raw()...)
			f.env["CODEX_THREAD_ID"] = bindT1
			if !expired {
				f.env["CODEX_THREAD_ID"] = bindT2
			}
			t.Chdir(t.TempDir())
			a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }, localRuntime: func(context.Context) error { return nil }}
			intent, err := a.prepareLocalEnrollment("p")
			if err != nil {
				t.Fatal(err)
			}
			commitEnrollmentFixture(t, a, intent, "peer_A")
			if r := a.reconcileLocalEnrollment(*intent); r.State != "pending" {
				t.Fatalf("%#v", r)
			}
			if !bytes.Equal(before, f.raw()) {
				t.Fatal("automatic setup changed protected ownership")
			}
		})
	}
}

func TestAutomaticEnrollmentPersistsIntentBeforeConfigExists(t *testing.T) {
	home := localSetupHome(t)
	t.Chdir(t.TempDir())
	codexHome := filepath.Join(home, "codex")
	if err := cliFixtureMkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "VECNET_CONFIG": filepath.Join(home, "new/config.toml"), "CODEX_HOME": codexHome, "CODEX_THREAD_ID": bindT1}
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return env[k] }}
	if _, err := a.prepareLocalEnrollment("p"); err != nil {
		t.Fatal(err)
	}
	records, err := a.localEnrollments()
	if err != nil || len(records) != 1 {
		t.Fatalf("%#v %v", records, err)
	}
	if _, err := os.Stat(env["VECNET_CONFIG"]); !os.IsNotExist(err) {
		t.Fatal("intent created credentials")
	}
}

// A rejected invitation must never authorize setup of a pre-existing profile.
func TestEnrollmentRejectedInvitationCannotReplayOldCredential(t *testing.T) {
	f := newBindFixture(t)
	if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	f.env["CODEX_THREAD_ID"] = bindT1
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "rejected", http.StatusUnauthorized) }))
	defer rejected.Close()
	var calls atomic.Int32
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }, localRuntime: func(context.Context) error { calls.Add(1); return usageErr("runtime reached") }}
	if err := a.cmdEnrol([]string{"--hub", rejected.URL, "--token", "invitation", "--profile", "p"}); err == nil {
		t.Fatal("expected rejected invitation")
	}
	if err := a.retryLocalEnrollments(); err != nil {
		t.Fatalf("canceled enrollment remains actionable: %v", err)
	}
	records, err := a.localEnrollments()
	if err != nil || len(records) != 1 || records[0].State != "canceled" {
		t.Fatalf("%#v %v", records, err)
	}
	if calls.Load() != 0 {
		t.Fatal("failed redemption authorized setup of old credential")
	}
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatalf("failed enrollment created authority: %v", err)
	}
}

func TestEnrollmentRequiresIntentPersistenceBeforeRedemption(t *testing.T) {
	home := localSetupHome(t)
	t.Chdir(t.TempDir())
	codexHome := filepath.Join(home, "codex")
	if err := cliFixtureMkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	// HOME is unusable for the private intent, while config remains writable.
	badHome := filepath.Join(home, "not-a-directory")
	if err := cliFixtureWriteFile(badHome, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	var redeemed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redeemed.Add(1)
		json.NewEncoder(w).Encode(map[string]string{"peer_id": "peer_A", "secret": "vn_peer_saved"})
	}))
	defer server.Close()
	env := map[string]string{"HOME": badHome, "VECNET_CONFIG": filepath.Join(home, "config.toml"), "CODEX_HOME": codexHome, "CODEX_THREAD_ID": bindT1}
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return env[k] }}
	if err := a.cmdEnrol([]string{"--hub", server.URL, "--token", "invitation", "--profile", "p"}); err == nil {
		t.Fatal("accepted enrollment without durable intent")
	}
	if redeemed.Load() != 0 {
		t.Fatal("consumed invitation despite failed intent persistence")
	}
}

// Simulate the single config commit independently of progress writes, as after
// interruption immediately following credential persistence.
func commitEnrollmentFixture(t *testing.T, a *app, r *localEnrollment, caller string) {
	t.Helper()
	cfg, err := loadConfig(r.Config)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles[r.Profile]
	r.Hub = normalizeHub(p.Hub)
	if err := a.saveLocalEnrollment(*r); err != nil {
		t.Fatal(err)
	}
	p.PeerID = caller
	p.EnrollmentIntent = r.IntentID
	p.EnrollmentCredential = hookDigest([]byte(p.Secret))
	p.EnrollmentHub = r.Hub
	p.EnrollmentCaller = caller
	cfg.Profiles[r.Profile] = p
	if err := saveConfigDurable(r.Config, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentCommittedIntentRejectsCredentialReplacement(t *testing.T) {
	for _, field := range []string{"secret", "hub", "peer", "marker", "live-caller"} {
		t.Run(field, func(t *testing.T) {
			f := newBindFixture(t)
			if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			f.env["CODEX_THREAD_ID"] = bindT1
			var calls atomic.Int32
			a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }, localRuntime: func(context.Context) error { calls.Add(1); return usageErr("runtime reached") }}
			intent, err := a.prepareLocalEnrollment("p")
			if err != nil {
				t.Fatal(err)
			}
			commitEnrollmentFixture(t, a, intent, "peer_A")
			cfg, err := loadConfig(intent.Config)
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.Profiles["p"]
			switch field {
			case "secret":
				p.Secret = "vn_peer_replacement"
			case "hub":
				p.Hub = "http://localhost:1"
			case "peer":
				p.PeerID = "peer_B"
			case "marker":
				p.EnrollmentIntent = "different"
			case "live-caller":
				f.peer.Store("peer_B")
			}
			cfg.Profiles["p"] = p
			if err := saveConfigDurable(intent.Config, cfg); err != nil {
				t.Fatal(err)
			}
			if got := a.reconcileLocalEnrollment(*intent); got.State != "superseded" {
				t.Fatalf("%#v", got)
			}
			if calls.Load() != 0 {
				t.Fatal("changed credential/identity reached runtime activation")
			}
			if _, err := os.Stat(f.path); !os.IsNotExist(err) {
				t.Fatalf("replacement created authority: %v", err)
			}
		})
	}
}

func TestEnrollmentMissingThreadReportsUnsupportedWithoutRepairPromise(t *testing.T) {
	home := localSetupHome(t)
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"peer_id": "peer_A", "secret": "vn_peer_saved"})
	}))
	defer server.Close()
	env := map[string]string{"HOME": home, "VECNET_CONFIG": filepath.Join(home, "config.toml"), "CODEX_CI": "1"}
	a := &app{ctx: context.Background(), stdout: &out, stderr: &bytes.Buffer{}, getenv: func(k string) string { return env[k] }}
	if err := a.cmdEnrol([]string{"--hub", server.URL, "--token", "invitation", "--profile", "p"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unsupported") || !strings.Contains(out.String(), "CODEX_THREAD_ID") || strings.Contains(out.String(), "retry: vecnet repair") {
		t.Fatalf("%s", out.String())
	}
	records, err := a.localEnrollments()
	if err != nil || len(records) != 0 {
		t.Fatalf("invented target: %#v %v", records, err)
	}
	cfg, err := loadConfig(env["VECNET_CONFIG"])
	if err != nil || cfg.Profiles["p"].Secret != "vn_peer_saved" {
		t.Fatal("unsupported host lost credentials")
	}
}

func localSetupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := cliFixtureChmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

// The runtime port simulates only the connector's external status file. Binding,
// authentication, hook installation and matching readiness checks stay real.
func enrollmentStatusRuntime(path string, done chan<- error, delay ...time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		go func() {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				raw, err := os.ReadFile(path)
				var file codexBindingsFile
				if err == nil && json.Unmarshal(raw, &file) == nil && len(file.Bindings) == 1 {
					if len(delay) > 0 && !sleepCtx(ctx, delay[0]) {
						done <- ctx.Err()
						return
					}
					b := file.Bindings[0]
					status := codexConnectorStatus{Version: 1, UpdatedAt: time.Now().UTC()}
					statusRaw, err := json.Marshal(map[string]any{"version": status.Version, "updated_at": status.UpdatedAt, "bindings": []any{map[string]any{
						"config_path": b.ConfigPath, "profile": b.Profile, "as": b.As, "thread_id": b.ThreadID, "codex_home": b.CodexHome, "generation": b.Generation, "state": "ready", "liveness": map[string]bool{"live": true},
					}}})
					if err == nil {
						err = writeFileAtomic(path+".status.json", statusRaw)
					}
					done <- err
					return
				}
				select {
				case <-ctx.Done():
					done <- ctx.Err()
					return
				case <-ticker.C:
				}
			}
		}()
		return nil
	}
}

func TestAutomaticEnrollmentWaitsForNextConnectorStatus(t *testing.T) {
	f := newBindFixture(t)
	if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	f.now = time.Now().UTC()
	t.Chdir(t.TempDir())
	f.env["CODEX_THREAD_ID"] = bindT1
	done := make(chan error, 1)
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }, localRuntime: enrollmentStatusRuntime(f.path, done, 18*time.Second)}
	intent, err := a.prepareLocalEnrollment("p")
	if err != nil {
		t.Fatal(err)
	}
	commitEnrollmentFixture(t, a, intent, "peer_A")
	result := a.reconcileLocalEnrollment(*intent)
	if result.State != "ready" {
		t.Fatalf("next connector heartbeat was not awaited: %#v", result)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentFreshRecipientConvergesWithConnectorStatus(t *testing.T) {
	home := localSetupHome(t)
	project := t.TempDir()
	t.Chdir(project)
	codexHome := filepath.Join(home, "codex")
	if err := cliFixtureMkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	var redeemed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/enrol":
			redeemed.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"peer_id": "peer_A", "secret": "vn_peer_saved"})
		case "/v1/peers/me":
			json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": "peer_A"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	env := map[string]string{"HOME": home, "VECNET_CONFIG": filepath.Join(home, "new/config.toml"), "CODEX_HOME": codexHome, "CODEX_THREAD_ID": bindT1}
	path := filepath.Join(home, ".local/state/vecnet/codex/bindings.json")
	done := make(chan error, 1)
	var out bytes.Buffer
	a := &app{ctx: context.Background(), stdout: &out, stderr: &bytes.Buffer{}, getenv: func(k string) string { return env[k] }, localRuntime: enrollmentStatusRuntime(path, done)}
	if err := a.cmdEnrol([]string{"--hub", server.URL, "--token", "invitation", "--profile", "fresh"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "session delivery ready") {
		t.Fatalf("%s", out.String())
	}
	records, err := a.localEnrollments()
	if err != nil || len(records) != 1 || records[0].State != "ready" || records[0].Caller != "peer_A" {
		t.Fatalf("%#v %v", records, err)
	}
	registry, err := a.readLocalState("enrollments.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(registry, []byte("vn_peer_saved")) || bytes.Contains(registry, []byte(hookDigest([]byte("vn_peer_saved")))) {
		t.Fatal("registry contains credential material")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.retryLocalEnrollments(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if redeemed.Load() != 1 || !bytes.Equal(before, after) {
		t.Fatal("repeat renewed ownership or redeemed token")
	}
}

func TestEnrollmentInterruptedAfterConfigCommitResumesPreparedRecord(t *testing.T) {
	f := newBindFixture(t)
	if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	original := t.TempDir()
	t.Chdir(original)
	f.env["CODEX_THREAD_ID"] = bindT1
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
	intent, err := a.prepareLocalEnrollment("p")
	if err != nil {
		t.Fatal(err)
	}
	commitEnrollmentFixture(t, a, intent, "peer_A")
	records, err := a.localEnrollments()
	if err != nil || len(records) != 1 || records[0].State != "prepared" || records[0].Caller != "" {
		t.Fatalf("fixture must stop before progress write: %#v %v", records, err)
	}
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	f.env["CODEX_THREAD_ID"] = bindT2
	done := make(chan error, 1)
	// A fresh app has no in-memory intent or invitation, as after process death.
	retry := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }, localRuntime: enrollmentStatusRuntime(f.path, done)}
	if err := retry.retryLocalEnrollments(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var authority codexBindingsFile
	if err := json.Unmarshal(f.raw(), &authority); err != nil {
		t.Fatal(err)
	}
	if len(authority.Bindings) != 1 || authority.Bindings[0].ThreadID != bindT1 {
		t.Fatalf("followed new root: %#v", authority)
	}
	if _, err := os.Stat(filepath.Join(original, ".codex/hooks.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".codex/hooks.json")); !os.IsNotExist(err) {
		t.Fatal("followed retry cwd")
	}
}

func TestEnrollmentDurableSavePreservesOtherProfileMetadata(t *testing.T) {
	path := filepath.Join(localSetupHome(t), "new/parent/config.toml")
	cfg := Config{OperatorProfile: "op", Profiles: map[string]Profile{"p": {Hub: "http://localhost", Secret: "vn_peer_test", PeerID: "peer_A", EnrollmentIntent: "id", EnrollmentCredential: "digest", EnrollmentHub: "http://localhost", EnrollmentCaller: "peer_A"}, "op": {Hub: "http://localhost", Secret: "vn_operator_test"}}}
	if err := saveConfigDurable(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.OperatorProfile != cfg.OperatorProfile || got.Profiles["p"] != cfg.Profiles["p"] || got.Profiles["op"] != cfg.Profiles["op"] {
		t.Fatal("durable enrollment changed unrelated config semantics")
	}
	cliFixtureRequireMode(t, path, 0600)
}

func TestEnrollmentSupersededIntentDoesNotPreventLaterRepair(t *testing.T) {
	f := newBindFixture(t)
	if err := cliFixtureChmod(f.env["HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	f.env["CODEX_THREAD_ID"] = bindT1
	a := &app{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
	older, err := a.prepareLocalEnrollment("p")
	if err != nil {
		t.Fatal(err)
	}
	commitEnrollmentFixture(t, a, older, "peer_A")
	latest, err := a.prepareLocalEnrollment("p")
	if err != nil {
		t.Fatal(err)
	}
	commitEnrollmentFixture(t, a, latest, "peer_A")
	if got := a.reconcileLocalEnrollment(*older); got.State != "superseded" {
		t.Fatalf("%#v", got)
	}
	// The latest intent is already complete; only the superseded row remains.
	latest.State = "ready"
	if err := a.saveLocalEnrollment(*latest); err != nil {
		t.Fatal(err)
	}
	if err := a.retryLocalEnrollments(); err != nil {
		t.Fatalf("stale intent prevented convergence: %v", err)
	}
}
