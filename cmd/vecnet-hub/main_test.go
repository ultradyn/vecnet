package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/XertroV/vecnet/internal/store"
	"github.com/XertroV/vecnet/internal/testhub"
)

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

func TestSIGTERMSendsHubRestarting(t *testing.T) { testRestartShutdown(t, noEnv, nil, 5000) }

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
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}, tc.flags...)
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
				resp.Body.Close()
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
