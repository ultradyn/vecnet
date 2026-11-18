package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

// bootHub prepares disposable operation-test schemas before the readiness
// budget. Startup/migration contract tests use bootHubFromDisk or run directly.
// Existing stores (including their identity, state and WAL) are never replaced.
func bootHub(t *testing.T, args []string, getenv func(string) string) *bootedHub {
	t.Helper()
	prepareBootHubStore(t, args, getenv)
	return bootHubFromDisk(t, args, getenv)
}

func prepareBootHubStore(t *testing.T, args []string, getenv func(string) string) {
	t.Helper()
	// Operation callers must explicitly select their private data directory.
	// Accept both flag spellings and = forms, with flags winning over the env.
	dir := getenv("VECNET_DATA_DIR")
	for i := 0; i < len(args); i++ {
		arg := strings.TrimPrefix(args[i], "-")
		arg = strings.TrimPrefix(arg, "-")
		if arg == "data-dir" && i+1 < len(args) {
			i++
			dir = args[i]
		} else if value, ok := strings.CutPrefix(arg, "data-dir="); ok {
			dir = value
		}
	}
	if dir == "" {
		t.Fatal("bootHub requires an explicit private data directory")
	}
	prepareHubSchema(t, dir)
}

func prepareHubSchema(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "hub.db")
	if _, err := os.Lstat(path); err == nil {
		return
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := testhub.FreshStore(t, path).Close(); err != nil {
		t.Fatal(err)
	}
}

// Exercise fixture preparation directly; real serving/restart coverage lives in
// the OAuth, identity and raw-startup tests. Existing database/WAL bytes and
// identity material must survive repeated preparation, including while open.
func TestBootHubOperationStoreIsolation(t *testing.T) {
	seen := map[string]bool{}
	for _, form := range []string{"env", "--data-dir", "-data-dir="} {
		t.Run(form, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "hub")
			unused := filepath.Join(t.TempDir(), "unopened")
			envDir := unused
			args := []string{"--listen", "127.0.0.1:0"}
			switch form {
			case "env":
				envDir = dir
			case "--data-dir":
				args = append(args, form, dir)
			default:
				args = append(args, form+dir)
			}
			getenv := func(key string) string {
				if key == "VECNET_DATA_DIR" {
					return envDir
				}
				return ""
			}
			prepareBootHubStore(t, args, getenv)
			path := filepath.Join(dir, "hub.db")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("fixture did not create the selected database: %v", err)
			}
			db := testhub.OpenFixtureStore(t, path)
			readSeed := func(name string) string {
				t.Helper()
				value, err := db.Meta(context.Background(), name)
				if err != nil || value == "" {
					t.Fatalf("read %s: %v", name, err)
				}
				return value
			}
			seed, seal := readSeed("hub_identity_seed"), readSeed("hub_key")
			key, err := hubid.KeyFromSeedHex(seed)
			if err != nil {
				t.Fatal(err)
			}
			if seen[key.Pubkey()] || seen[seal] {
				t.Fatal("independent stores shared identity material")
			}
			seen[key.Pubkey()], seen[seal] = true, true
			if err := db.SetMeta(context.Background(), "isolation_marker", form); err != nil {
				t.Fatal(err)
			}
			readFile := func(name string) []byte {
				t.Helper()
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			before, wal := readFile(path), readFile(path+"-wal")
			if len(wal) == 0 {
				t.Fatal("preservation check requires a real nonempty WAL")
			}
			prepareBootHubStore(t, args, getenv)
			if !bytes.Equal(readFile(path), before) || !bytes.Equal(readFile(path+"-wal"), wal) {
				t.Fatal("preparation changed existing database or WAL bytes")
			}
			if readSeed("hub_identity_seed") != seed || readSeed("hub_key") != seal || readSeed("isolation_marker") != form {
				t.Fatal("preparation changed existing identity or state")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before = readFile(path)
			prepareBootHubStore(t, args, getenv)
			if !bytes.Equal(readFile(path), before) {
				t.Fatal("preparation replaced the closed database on restart")
			}
			if _, err := os.Stat(unused); !os.IsNotExist(err) {
				t.Fatalf("unused environment directory was touched: %v", err)
			}
		})
	}
}

func TestBackfillLimitsRejectBeforeOpeningStore(t *testing.T) {
	for _, flags := range [][]string{{"--storage-backfill-rows=-1"}, {"--storage-backfill-bytes=-1"}, {"--storage-backfill-target=-1ms"}, {"--storage-backfill-bytes=9223372036854775807"}} {
		dir := filepath.Join(t.TempDir(), "unopened")
		args := append([]string{"--data-dir", dir, "--listen", "127.0.0.1:0"}, flags...)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := run(ctx, args, io.Discard, noEnv); err == nil || !strings.Contains(err.Error(), "invalid_limits") {
			t.Fatalf("invalid backfill limits accepted: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid limits opened store")
		}
	}
}

func TestInitPrintsSecretOnce(t *testing.T) {
	dir := t.TempDir()
	var first, second bytes.Buffer
	if err := runInit(dir, "Max", &first); err != nil {
		t.Fatalf("first runInit: %v", err)
	}
	if !strings.Contains(first.String(), "vn_operator_") {
		t.Fatalf("first output lacks the operator secret:\n%s", first.String())
	}
	peer := regexp.MustCompile(`peer_[0-9A-HJKMNP-TV-Z]{26}`).FindString(first.String())
	if peer == "" {
		t.Fatalf("first output lacks the operator peer id:\n%s", first.String())
	}
	if err := runInit(dir, "Max", &second); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	if !strings.Contains(second.String(), "already initialised") {
		t.Fatalf("second output lacks \"already initialised\":\n%s", second.String())
	}
	if strings.Contains(second.String(), "vn_operator_") {
		t.Fatalf("second output repeats a secret:\n%s", second.String())
	}
	if !strings.Contains(second.String(), peer) {
		t.Fatalf("second output does not name the existing peer %s:\n%s", peer, second.String())
	}
}

// Cancel the same lifecycle context that SIGTERM cancels on Unix and a console
// interrupt cancels on Windows. Sending SIGTERM to a Windows process is not a
// graceful shutdown, and bootHub deliberately runs the hub in-process.
func TestShutdownSendsHubRestarting(t *testing.T) { testRestartShutdown(t, noEnv, nil, 5000) }

func testRestartShutdown(t *testing.T, getenv func(string) string, extra []string, wantMS int) {
	t.Helper()
	dir := t.TempDir()
	h, db, err := openHub(dir, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = h.EnsureOperator(context.Background(), "operator")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, token, err := h.AddPeer(context.Background(), "session", "reader", "")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, secret, err := h.Auth.Enrol(context.Background(), token)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir}, extra...)
	boot := bootHub(t, args, getenv)
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownGrace+2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", boot.base+"/v1/events?after=head", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	read := func() string {
		var frame strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			frame.WriteString(line)
			if line == "\n" {
				return frame.String()
			}
		}
	}
	if frame := read(); frame != "retry: 3000\n\n" {
		t.Fatalf("retry = %q", frame)
	}
	if frame := read(); !strings.HasPrefix(frame, "id: ") || !strings.HasSuffix(frame, "\ndata:\n\n") {
		t.Fatalf("prime = %q", frame)
	}
	started := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- boot.stop() }()
	want := fmt.Sprintf("event: hub_restarting\nretry: %d\ndata: {\"retry_after_ms\":%d}\n\n", wantMS, wantMS)
	if frame := read(); frame != want {
		t.Fatalf("restart = %q, want %q", frame, want)
	}
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("after restart = %q %v", rest, err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ShutdownGrace):
		t.Fatal("run did not finish within ShutdownGrace")
	}
	if elapsed := time.Since(started); elapsed >= ShutdownGrace {
		t.Fatalf("shutdown took %v", elapsed)
	}
}

func TestRestartRetryFlag(t *testing.T) {
	for _, value := range []string{"70000", "-1", "bad"} {
		err := run(context.Background(), []string{"--restart-retry-ms", value}, io.Discard, noEnv)
		if err == nil || !strings.Contains(err.Error(), "restart-retry-ms") {
			t.Fatalf("invalid retry %q = %v", value, err)
		}
	}
	env := func(k string) string {
		if k == "VECNET_RESTART_RETRY_MS" {
			return "2000"
		}
		return ""
	}
	testRestartShutdown(t, env, nil, 2000)
	t.Run("flag overrides env including zero", func(t *testing.T) { testRestartShutdown(t, env, []string{"--restart-retry-ms", "0"}, 0) })
	for _, value := range []string{"-1", "70000", "nope"} {
		err := run(context.Background(), nil, io.Discard, func(k string) string {
			if k == "VECNET_RESTART_RETRY_MS" {
				return value
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "restart-retry-ms") {
			t.Fatalf("invalid env retry %q = %v", value, err)
		}
	}
}

func TestRelocatedRunsNoWorkers(t *testing.T) {
	e := testhub.Start(t)
	callback := testhub.NewCallback(t)
	recipient, _ := e.NewPeer("session", "push recipient")
	_, sender := e.NewPeer("grokbot", "sender")
	route, _, err := e.Hub.AddRoute(context.Background(), "peer", recipient, callback.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Hub.MarkRouteVerified(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	resp, raw := e.Do(sender, "POST", "/v1/messages", map[string]any{
		"idempotency_key": "relocated-push", "priority": "wake", "recipients": []string{recipient}, "content": map[string]string{"text": "due push"},
	})
	if resp.StatusCode != 202 {
		t.Fatalf("seed delivery: %d %s", resp.StatusCode, raw)
	}
	var due int
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM deliveries WHERE route_id=? AND state='queued' AND held=0`, route).Scan(&due); err != nil || due != 1 {
		t.Fatalf("due deliveries: %d %v", due, err)
	}
	boot := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", e.Hub.Cfg.DataDir, "--public-url", e.URL, "--relocated-to", "HTTPS://NEXT.EXAMPLE:443/", "--allow-private-egress"}, noEnv)
	if !strings.Contains(boot.out.String(), "vecnet-hub relocated: answering hub_moved → https://next.example\n") {
		t.Fatalf("announcement: %s", boot.out.String())
	}
	// The push worker claims due work at once (push.Worker starts with a zero
	// timer), so a relocated hub that started workers would call back within
	// milliseconds; two seconds leaves ample margin on a loaded box.
	time.Sleep(2 * time.Second)
	if got := callback.Requests(); got != 0 {
		t.Fatalf("relocated workers delivered %d callbacks", got)
	}
	if err := boot.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRelocatedRefusesOwnURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for _, target := range []string{"HTTPS://OLD.EXAMPLE:443/", "javascript:alert(1)", "https://next.example?secret=1"} {
		dir := t.TempDir()
		err := run(context.Background(), []string{"--listen", ln.Addr().String(), "--data-dir", dir, "--public-url", "https://old.example", "--relocated-to", target}, io.Discard, noEnv)
		if err == nil || !strings.Contains(err.Error(), "relocated-to") {
			t.Fatalf("target %q must fail before binding: %v", target, err)
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("invalid relocation wrote data: %v %v", entries, readErr)
		}
	}
	t.Run("environment", func(t *testing.T) {
		dir := t.TempDir()
		env := func(k string) string {
			if k == "VECNET_RELOCATED_TO" {
				return "https://next.example"
			}
			return ""
		}
		boot := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dir}, env)
		resp, err := http.Get(boot.base + "/mcp")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 410 {
			t.Fatalf("env relocation: %d", resp.StatusCode)
		}
	})
}

func TestServeClearsBootNotices(t *testing.T) {
	e := testhub.Start(t)
	n, err := e.Hub.PostNotice(context.Background(), auth.Caller{Kind: "operator", PrincipalID: "operator"}, hub.NoticeRequest{Kind: "restart", Message: "restart now"})
	if err != nil {
		t.Fatal(err)
	}
	boot := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", e.Hub.Cfg.DataDir}, noEnv)
	req, err := http.NewRequest("GET", boot.base+"/v1/hub/notices", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.OperatorSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(raw) != "{\"notices\":[]}\n" {
		t.Fatalf("boot notices: %d %s %v", resp.StatusCode, raw, err)
	}
	var how string
	if err := e.Hub.DB.QueryRow(`SELECT cleared_how FROM hub_notices WHERE notice_id=?`, n.NoticeID).Scan(&how); err != nil || how != "boot" {
		t.Fatalf("clear reason: %q %v", how, err)
	}
}

func servedIdentity(t *testing.T, base string) envelope.HubIdentity {
	t.Helper()
	resp, err := http.Get(base + "/v1/hub/identity")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc envelope.HubIdentity
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("identity: %d", resp.StatusCode)
	}
	return doc
}

func TestIdentityShowAndRotate(t *testing.T) {
	dir := t.TempDir()
	args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
	boot := bootHub(t, args, noEnv)
	old := servedIdentity(t, boot.base)
	if err := boot.stop(); err != nil {
		t.Fatal(err)
	}
	readSeed := func() string {
		t.Helper()
		db, err := store.Open(filepath.Join(dir, "hub.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var seed string
		if err := db.QueryRow(`SELECT value FROM meta WHERE key='hub_identity_seed'`).Scan(&seed); err != nil {
			t.Fatal(err)
		}
		return seed
	}
	originalSeed := readSeed()
	var out bytes.Buffer
	command := func(extra ...string) error {
		out.Reset()
		return run(context.Background(), append([]string{"identity", extra[0], "--data-dir", dir}, extra[1:]...), &out, noEnv)
	}
	if err := command("show"); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("pubkey %s\nfingerprint %s\ncreated_at %s\n", old.Pubkey, old.Fingerprint, old.KeyCreatedAt)
	if out.String() != want {
		t.Fatalf("show: %q want %q", out.String(), want)
	}
	if err := command("rotate"); err == nil || !strings.Contains(err.Error(), "--yes") || out.Len() != 0 {
		t.Fatalf("unconfirmed rotation: %v %q", err, out.String())
	}
	if readSeed() != originalSeed {
		t.Fatal("unconfirmed rotation changed the seed")
	}
	if err := command("show"); err != nil || out.String() != want {
		t.Fatalf("unconfirmed rotation changed identity: %v %q", err, out.String())
	}
	if err := command("rotate", "--yes"); err != nil {
		t.Fatal(err)
	}
	rotation := out.String()
	nextBoot := bootHub(t, args, noEnv)
	next := servedIdentity(t, nextBoot.base)
	if next.Pubkey == old.Pubkey || next.Previous == nil || next.Previous.Pubkey != old.Pubkey {
		t.Fatalf("rotation: old %+v new %+v", old, next)
	}
	if err := hubid.VerifyEndorsement(*next.Previous, next.Pubkey); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("old %s\nnew %s\n", old.Fingerprint, next.Fingerprint); rotation != want {
		t.Fatalf("rotate output: %q want %q", rotation, want)
	}
	if err := nextBoot.stop(); err != nil {
		t.Fatal(err)
	}
	if err := command("show"); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("pubkey %s\nfingerprint %s\ncreated_at %s\nprevious %s\n", next.Pubkey, next.Fingerprint, next.KeyCreatedAt, old.Pubkey); out.String() != want {
		t.Fatalf("rotated show: %q want %q", out.String(), want)
	}
	// Refusal must not even initialize a missing data directory.
	absent := dir + "/uncreated"
	if err := run(context.Background(), []string{"identity", "rotate", "--data-dir", absent}, io.Discard, noEnv); err == nil {
		t.Fatal("unconfirmed fresh rotation succeeded")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed rotation wrote data: %v", err)
	}
}

func TestIdentityShowPreservesLiveState(t *testing.T) {
	e := testhub.Start(t)
	ctx := context.Background()
	a, as := e.NewPeer("session", "sender")
	b, bs := e.NewPeer("session", "reactor")
	resp, raw := e.Do(as, "POST", "/v1/messages", map[string]any{
		"idempotency_key": "identity-show-live", "priority": "hold", "recipients": []string{b}, "content": map[string]string{"text": "target"},
	})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, raw)
	}
	var target hub.SendResponse
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	resp, raw = e.Do(bs, "POST", "/v1/reactions", map[string]string{
		"message_id": target.MessageID, "emoji": "✅", "op": "add", "priority": "wake",
	})
	var reaction hub.ReactResponse
	if err := json.Unmarshal(raw, &reaction); err != nil || resp.StatusCode != 200 || reaction.Wake == nil {
		t.Fatalf("reaction: %d %s %v", resp.StatusCode, raw, err)
	}
	id := reaction.Wake.DeliveryID
	if ok, err := e.Hub.BeginDispatch(ctx, id); err != nil || !ok {
		t.Fatalf("begin dispatch: %v %v", ok, err)
	}
	// Use the same lifecycle seam as an open SSE request; no background
	// worker can complete the reaction claim before show reads the database.
	e.Hub.StreamOpened(ctx, a)
	defer e.Hub.StreamClosed(ctx, a)
	check := func(wantState string, wantClaims int) {
		t.Helper()
		var reach, state string
		var claims int
		if err := e.Hub.DB.QueryRow(`SELECT reachability FROM peers WHERE peer_id=?`, a).Scan(&reach); err != nil {
			t.Fatal(err)
		}
		if err := e.Hub.DB.QueryRow(`SELECT state, dispatching FROM deliveries WHERE dly_id=?`, id).Scan(&state, &claims); err != nil {
			t.Fatal(err)
		}
		if reach != "connected" || state != wantState || claims != wantClaims {
			t.Errorf("live state: reach=%s state=%s claims=%d; want connected %s %d", reach, state, claims, wantState, wantClaims)
		}
	}
	check("queued", 1)
	var out bytes.Buffer
	if err := run(ctx, []string{"identity", "show", "--data-dir", e.Hub.Cfg.DataDir}, &out, noEnv); err != nil {
		t.Fatal(err)
	}
	doc := servedIdentity(t, e.URL)
	if want := fmt.Sprintf("pubkey %s\nfingerprint %s\ncreated_at %s\n", doc.Pubkey, doc.Fingerprint, doc.KeyCreatedAt); out.String() != want {
		t.Fatalf("show: %q want %q", out.String(), want)
	}
	check("queued", 1)
	// A remove while dispatch is in flight must not withdraw the wake.
	resp, raw = e.Do(bs, "POST", "/v1/reactions", map[string]string{
		"message_id": target.MessageID, "emoji": "✅", "op": "remove",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("remove: %d %s", resp.StatusCode, raw)
	}
	check("queued", 1)
	if err := e.Hub.EndDispatch(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	check("pushed", 0)
}

func TestRelocatedAfterIdentityRotation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const own, target = "https://old.example", "https://next.example"
	h, db, err := openHub(dir, own, false)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, _, err := h.EnsureOperator(ctx, "operator")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	oldKey := h.IdentityKey().Pubkey()
	n, err := h.PostNotice(ctx, auth.Caller{Kind: "operator"}, hub.NoticeRequest{
		Kind: "moving", Message: "Operator move announcement", NewURL: target,
		At: envelope.FormatTime(time.Now().Add(time.Hour)),
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyNotice(oldKey, n); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"identity", "rotate", "--data-dir", dir, "--yes"}, io.Discard, noEnv); err != nil {
		t.Fatal(err)
	}
	args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir, "--public-url", own, "--relocated-to", target}
	boot := bootHub(t, args, noEnv)
	doc := servedIdentity(t, boot.base)
	if doc.Pubkey == oldKey {
		t.Fatal("rotation did not change the served key")
	}
	resp, err := http.Get(boot.base + "/v1/threads")
	if err != nil {
		t.Fatal(err)
	}
	var moved struct {
		Error struct {
			Code    string
			Details struct{ Notice envelope.HubNotice }
		}
	}
	err = json.NewDecoder(resp.Body).Decode(&moved)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 410 || moved.Error.Code != "hub_moved" {
		t.Fatalf("relocated response: %d %+v %v", resp.StatusCode, moved, err)
	}
	got := moved.Error.Details.Notice
	if err := hubid.VerifyNotice(doc.Pubkey, got); err != nil {
		t.Errorf("moving notice must verify against the served identity: %v", err)
	}
	if got.NewURL != target || got.Message != n.Message || got.At != n.At || got.ExpiresAt != n.ExpiresAt {
		t.Errorf("replacement lost operator move fields: %+v", got)
	}
	req, err := http.NewRequest("GET", boot.base+"/v1/hub/notices", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var page struct{ Notices []envelope.HubNotice }
	err = json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || len(page.Notices) != 1 || page.Notices[0] != got {
		t.Fatalf("active notices differ from the 410 notice: %d %+v %v", resp.StatusCode, page, err)
	}
	if err := boot.stop(); err != nil {
		t.Fatal(err)
	}
	// A subsequent relocated restart reuses the replacement unchanged.
	next := bootHub(t, args, noEnv)
	db, err = store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var activeID string
	if err := db.QueryRow(`SELECT notice_id FROM hub_notices WHERE cleared_at IS NULL AND kind='moving'`).Scan(&activeID); err != nil || activeID != got.NoticeID {
		t.Fatalf("restart replaced valid notice: %s %v", activeID, err)
	}
	var postActor, clearActor string
	if err := db.QueryRow(`SELECT actor FROM audit WHERE action='notice_post' ORDER BY id DESC LIMIT 1`).Scan(&postActor); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT actor FROM audit WHERE action='notice_clear' ORDER BY id DESC LIMIT 1`).Scan(&clearActor); err != nil {
		t.Fatal(err)
	}
	if postActor != "hub" || clearActor != "hub" {
		t.Fatalf("relocated re-sign audit actors: post=%s clear=%s", postActor, clearActor)
	}
	if servedIdentity(t, next.base).Pubkey != doc.Pubkey {
		t.Fatal("restart changed identity")
	}
}

func TestMCPAllowedOriginsStartup(t *testing.T) {
	for _, useFlag := range []bool{false, true} {
		t.Run(map[bool]string{false: "env", true: "flag"}[useFlag], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "unopened")
			args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
			getenv := noEnv
			if useFlag {
				args = append(args, "--mcp-allowed-origins", "https://chatgpt.com/path")
			} else {
				getenv = func(key string) string {
					if key == "VECNET_MCP_ALLOWED_ORIGINS" {
						return "https://chatgpt.com/path"
					}
					return ""
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := run(ctx, args, io.Discard, getenv)
			if err == nil || !strings.Contains(err.Error(), "MCP allowed origins") {
				t.Fatalf("startup error = %v, want invalid MCP allowed origins", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid config opened data directory: %v", err)
			}
		})
	}
}

func TestMCPAllowedOriginsFlagAndEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		flags     []string
		origin    string
		want      int
	}{
		{"default", "", nil, "https://chatgpt.com", 401},
		{"none", "none", nil, "https://chatgpt.com", 403},
		{"custom env", "https://client.example", nil, "https://client.example", 401},
		{"flag wins", "https://client.example", []string{"--mcp-allowed-origins", "none"}, "https://client.example", 403},
		{"empty flag resets default", "none", []string{"--mcp-allowed-origins", ""}, "https://chatgpt.com", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir}, tc.flags...)
			h := bootHub(t, args, func(key string) string {
				if key == "VECNET_MCP_ALLOWED_ORIGINS" {
					return tc.env
				}
				return ""
			})
			req, err := http.NewRequest(http.MethodPost, h.base+"/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", tc.origin)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == 401 && !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
				t.Fatal("missing discovery challenge")
			}
		})
	}
}

func TestMCPLegacySessionsStartup(t *testing.T) {
	for _, useFlag := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "unopened")
		args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
		getenv := noEnv
		if useFlag {
			args = append(args, "--mcp-legacy-sessions", "invalid")
		} else {
			getenv = func(k string) string {
				if k == "VECNET_MCP_LEGACY_SESSIONS" {
					return "invalid"
				}
				return ""
			}
		}
		err := run(context.Background(), args, io.Discard, getenv)
		if err == nil || !strings.Contains(err.Error(), "MCP legacy sessions") {
			t.Fatalf("invalid config error: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("invalid config opened data directory: %v", err)
		}
	}
}

func TestOAuthRedirectsStartup(t *testing.T) {
	for _, flagValue := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "unopened")
		args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
		getenv := func(key string) string {
			if key == "VECNET_OAUTH_REDIRECTS" {
				return "http://public.example/callback?PRIVATE_VALUE=secret"
			}
			return ""
		}
		if flagValue {
			args = append(args, "--oauth-redirects", "https://*.example/callback")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := run(ctx, args, io.Discard, getenv)
		if err == nil || !strings.Contains(err.Error(), "OAuth redirects") || strings.Contains(err.Error(), "PRIVATE_VALUE") {
			t.Fatalf("startup error = %v, want redacted invalid OAuth redirects", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("invalid config opened data directory: %v", err)
		}
	}
}

func TestOAuthRedirectsFlagAndEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		flags     []string
		want      int
	}{
		{"env", "https://connector.example/callback", nil, 201},
		{"flag wins", "http://invalid.example/cb", []string{"--oauth-redirects", "https://connector.example/callback"}, 201},
		{"empty flag keeps defaults", "https://connector.example/callback", []string{"--oauth-redirects", ""}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}, tc.flags...)
			h := bootHub(t, args, func(key string) string {
				if key == "VECNET_OAUTH_REDIRECTS" {
					return tc.env
				}
				return ""
			})
			for _, uri := range []string{"https://connector.example/callback", "https://chatgpt.com/connector_platform_oauth_redirect", "https://www.cursor.com/agents/mcp/oauth/callback"} {
				body, _ := json.Marshal(map[string]any{"redirect_uris": []string{uri}})
				resp, err := http.Post(h.base+"/oauth/register", "application/json", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				// Drain before Close so the transport reuses the connection rather
				// than racing replacement dials against the next request/shutdown.
				// A new connection with no headers can hold Shutdown past 2 s.
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := http.StatusCreated
				if uri == "https://connector.example/callback" {
					want = tc.want
				}
				if resp.StatusCode != want {
					t.Fatalf("register %s: %d, want %d", uri, resp.StatusCode, want)
				}
			}
		})
	}
}

func TestEventsRefreshIntervalValidation(t *testing.T) {
	for _, source := range []string{"env", "flag"} {
		for _, value := range []string{"invalid", "0", "-1m", "59s", "24h1s"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "unopened")
				args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
				getenv := noEnv
				if source == "flag" {
					args = append(args, "--events-refresh-interval", value)
				} else {
					getenv = func(key string) string {
						if key == "VECNET_EVENTS_REFRESH_INTERVAL" {
							return value
						}
						return ""
					}
				}
				err := run(context.Background(), args, io.Discard, getenv)
				if err == nil || !strings.Contains(err.Error(), "events refresh interval") {
					t.Fatalf("startup error = %v", err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("invalid interval opened data directory: %v", err)
				}
			})
		}
	}
}

func TestEventsRefreshIntervalFlagAndEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		flags     []string
		want      time.Duration
	}{
		{"default", "", nil, 30 * time.Minute},
		{"env minimum", "1m", nil, time.Minute},
		{"env maximum", "24h", nil, 24*time.Hour - events.RefreshMargin},
		{"flag", "", []string{"--events-refresh-interval", "7m"}, 7 * time.Minute},
		{"flag wins over invalid env", "invalid", []string{"--events-refresh-interval", "8m"}, 8 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// runInit still creates the operator through production startup.
			prepareHubSchema(t, dir)
			var out bytes.Buffer
			if err := runInit(dir, "Max", &out); err != nil {
				t.Fatal(err)
			}
			bearer := regexp.MustCompile(`vn_operator_\S+`).FindString(out.String())
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir, "--allow-private-egress"}, tc.flags...)
			h := bootHub(t, args, func(key string) string {
				if key == "VECNET_EVENTS_REFRESH_INTERVAL" {
					return tc.env
				}
				return ""
			})
			cb := testhub.NewCallback(t)
			subscribeSmoke(t, h.base, bearer, cb.URL)
			resp, raw := hubDo(t, http.MethodGet, h.base+"/v1/admin/subscriptions", bearer, nil)
			var result []struct {
				RefreshBefore string `json:"refresh_before"`
			}
			if resp.StatusCode != 200 || json.Unmarshal(raw, &result) != nil || len(result) != 1 {
				t.Fatalf("subscriptions: %d %s", resp.StatusCode, raw)
			}
			deadline, err := time.Parse(time.RFC3339Nano, result[0].RefreshBefore)
			if err != nil {
				t.Fatal(err)
			}
			if delta := time.Until(deadline) - tc.want; delta < -5*time.Second || delta > 5*time.Second {
				t.Fatalf("refresh interval differs from %s by %s", tc.want, delta)
			}
		})
	}
}

func TestPreferenceBootstrapFlagsAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
		want      int64
		origin    string
	}{
		{"default", "", nil, 500, ""}, {"env", "700", nil, 700, "VECNET_THREAD_BUDGET"}, {"flag", "700", []string{"--thread-budget", "800"}, 800, "--thread-budget"}, {"equal env", "500", nil, 500, "VECNET_THREAD_BUDGET"}, {"equal flag", "700", []string{"--thread-budget", "500"}, 500, "--thread-budget"}, {"flag overrides invalid env", "synthetic-invalid-environment", []string{"--thread-budget", "500"}, 500, "--thread-budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
			resolve := registerPreferenceFlags(fs, func(key string) string {
				if key == "VECNET_THREAD_BUDGET" {
					return tc.env
				}
				return ""
			})
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg, err := resolve()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			h, db, err := openHub(dir, "", false, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s, err := h.Preferences.LoadSnapshot(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			e, err := s.Get("agents.thread_budget")
			if err != nil {
				t.Fatal(err)
			}
			n, _ := e.Value.Int64()
			source := prefs.SourceFlag
			if tc.origin == "" {
				source = prefs.SourceDefault
			}
			if n != tc.want || e.Source != source || e.Origin != tc.origin {
				t.Fatalf("bootstrap = %+v", e)
			}
		})
	}
}

func TestPreferenceFlagDiagnosticsExcludeEnvironment(t *testing.T) {
	for _, name := range []string{"events-refresh-interval", "presence-ttl", "session-presence-ttl", "launcher-presence-ttl", "default-max-depth", "default-ttl", "thread-budget", "sends-per-minute", "threads-per-hour", "creates-per-hour"} {
		for _, arg := range []string{"--help", "--unknown-option"} {
			t.Run(name+"/"+arg, func(t *testing.T) {
				const rejected = "synthetic-invalid-environment-<>&\""
				env := "VECNET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
				getenv := func(key string) string {
					if key == env {
						return rejected
					}
					return ""
				}
				// Capture run's real flag diagnostics, including its default usage.
				file, err := os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				func() {
					previous := os.Stderr
					os.Stderr = file
					defer func() { os.Stderr = previous }()
					err = run(context.Background(), []string{arg}, io.Discard, getenv)
				}()
				if err == nil {
					t.Fatal("missing flag diagnostic")
				}
				output, readErr := os.ReadFile(file.Name())
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Contains(output, []byte("Usage of vecnet-hub:")) || !bytes.Contains(output, []byte("-"+name)) {
					t.Fatal("missing preference flag usage")
				}
				if bytes.Contains(output, []byte("synthetic-invalid-environment")) || strings.Contains(err.Error(), "synthetic-invalid-environment") {
					t.Fatal("rejected environment value entered flag diagnostics")
				}
			})
		}
	}
}
func TestPreferenceBootstrapRejectsInvalidBeforeOpeningStore(t *testing.T) {
	for _, arg := range []string{"--thread-budget=synthetic-secret", "--presence-ttl=59s", "--send-warning-after=59s", "--send-warning-after=-1s", "--send-warning-after=1.5s", "--default-ttl=1.5s", "--default-max-depth=65", "--creates-per-hour=0", "--session-presence-ttl=-1m", "--launcher-presence-ttl=25h"} {
		dir := filepath.Join(t.TempDir(), "unopened")
		err := run(context.Background(), []string{"--listen", "127.0.0.1:0", "--data-dir", dir, arg}, io.Discard, noEnv)
		if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("unsafe or missing bootstrap rejection: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("opened store on invalid bootstrap: %v", err)
		}
	}
}

func TestPreferenceBootstrapAllEnabledFlags(t *testing.T) {
	for _, tc := range []struct {
		flag, key, raw string
		want           int64
	}{
		{"events-refresh-interval", "events.refresh_interval", "7m", 420},
		{"presence-ttl", "presence.default_ttl", "2h", 7200},
		{"send-warning-after", "presence.send_warning_after", "30m", 1800},
		{"send-warning-after", "presence.send_warning_after", "0s", 0},
		{"session-presence-ttl", "presence.session_ttl", "30m", 1800},
		{"launcher-presence-ttl", "presence.launcher_ttl", "45m", 2700},
		{"default-max-depth", "agents.default_max_depth", "40", 40},
		{"default-ttl", "agents.default_ttl", "48h", 172800},
		{"thread-budget", "agents.thread_budget", "700", 700},
		{"sends-per-minute", "rates.sends_per_minute", "80", 80},
		{"threads-per-hour", "rates.threads_per_hour", "30", 30},
		{"creates-per-hour", "provisioning.creates_per_hour", "90", 90},
	} {
		for _, fromEnv := range []bool{false, true} {
			t.Run(tc.flag+map[bool]string{false: "/flag", true: "/env"}[fromEnv], func(t *testing.T) {
				env := "VECNET_" + strings.ToUpper(strings.ReplaceAll(tc.flag, "-", "_"))
				fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
				resolve := registerPreferenceFlags(fs, func(key string) string {
					if fromEnv && key == env {
						return tc.raw
					}
					return ""
				})
				args := []string{}
				origin := env
				if !fromEnv {
					args = []string{"--" + tc.flag, tc.raw}
					origin = "--" + tc.flag
				}
				if err := fs.Parse(args); err != nil {
					t.Fatal(err)
				}
				cfg, err := resolve()
				if err != nil {
					t.Fatal(err)
				}
				e := cfg.PreferenceBootstrap[tc.key]
				n, _ := e.Value.Int64()
				if n != tc.want || e.Source != prefs.SourceFlag || e.Origin != origin {
					t.Fatalf("bootstrap = %+v", e)
				}
			})
		}
	}
}

func TestHubURLRefusedAtStartup(t *testing.T) {
	const (
		user  = "USERSECRET"
		query = "QUERYSECRET"
		frag  = "FRAG"
	)
	cases := []struct {
		name, flag, value string
		env               func(string) string
	}{
		{name: "public userinfo", flag: "--public-url", value: "https://alice:" + user + "@hub.example/vecnet"},
		{name: "public query", flag: "--public-url", value: "https://hub.example/vecnet?token=" + query},
		{name: "public fragment", flag: "--public-url", value: "https://hub.example/vecnet#" + frag},
		{name: "env public query", env: func(k string) string {
			if k == "VECNET_PUBLIC_URL" {
				return "https://hub.example/vecnet?token=" + query
			}
			return ""
		}},
		{name: "relocated userinfo", flag: "--relocated-to", value: "https://alice:" + user + "@next.example/vecnet"},
		{name: "env relocated fragment", env: func(k string) string {
			if k == "VECNET_RELOCATED_TO" {
				return "https://next.example/vecnet#" + frag
			}
			return ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "unopened")
			args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
			if tc.flag != "" {
				args = append(args, tc.flag, tc.value)
			}
			getenv := noEnv
			if tc.env != nil {
				getenv = tc.env
			}
			err := run(context.Background(), args, io.Discard, getenv)
			if err == nil {
				t.Fatal("hub started")
			}
			msg := err.Error()
			for _, secret := range []string{user, query, frag, "alice:"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error contains %q: %s", secret, msg)
				}
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatalf("refused hub opened %s: %v", dir, statErr)
			}
		})
	}
}

func TestHubLogsCheckedPublicURL(t *testing.T) {
	ipv6 := "https://[2001:db8::7]/vecnet"
	// This checks the announced URL. Complete real init/migrations before
	// bootHub's listening deadline, just as the other initialized smoke hubs do.
	dataDir, _ := initHub(t)
	h := bootHub(t, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir, "--public-url", ipv6}, noEnv)
	if !strings.Contains(h.out.String(), "public url "+ipv6) {
		t.Fatalf("log dropped the public URL: %s", h.out.String())
	}
}
