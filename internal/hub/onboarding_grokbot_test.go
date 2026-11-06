package hub_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// TestGrokbotOnboardingAtomicReplay is Task 3's replay pin: a failed write
// leaves nothing, and a concurrent submit, a lost response or a reopened
// database returns the same peer, route, connector and setup ids. A changed
// request under that id conflicts, and the keyed digest tells the two apart.
func TestGrokbotOnboardingAtomicReplay(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		env := testhub.Start(t)
		ctx := context.Background()
		op := operatorCaller(t, env)
		key := fakeRoutineKey(t)
		url := fakeRoutineURL(t)
		secrets := []string{key, url, "Bearer " + key}
		before := grokCensus(t, env)
		got := mustOnboard(t, env, op, grokReq("boundary-ok", "Boundary Ok", url, "Bearer "+key, "", false))
		if got.Status != "created" || got.PeerID == "" || got.RouteID == "" || got.Setup == nil {
			t.Fatal("control submit did not create the grok peer, route and setup")
		}
		assertNoRoutineSecret(t, env, "control", got, nil, secrets)
		if reflectCensus(t, env, before) {
			t.Fatal("control submit wrote nothing")
		}

		boundaries := []struct {
			name, finding, sql string
		}{
			{"connector", "", `CREATE TRIGGER grok_fail_connector BEFORE INSERT ON meta WHEN NEW.key LIKE 'onboarding.v1/connector/%' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"principal", "", `CREATE TRIGGER grok_fail_principal BEFORE INSERT ON principals BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"peer", "", `CREATE TRIGGER grok_fail_peer BEFORE INSERT ON peers BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"directory", "", `CREATE TRIGGER grok_fail_directory BEFORE INSERT ON events WHEN NEW.type='directory_updated' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"peer_add", "", `CREATE TRIGGER grok_fail_peer_add BEFORE INSERT ON audit WHEN NEW.action='peer_add' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"route", "", `CREATE TRIGGER grok_fail_route BEFORE INSERT ON routes BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"assign-route", "t3 I1", `CREATE TRIGGER grok_fail_assign_route BEFORE UPDATE OF route_id ON peers BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"route_add", "", `CREATE TRIGGER grok_fail_route_add BEFORE INSERT ON audit WHEN NEW.action='route_add' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"thread", "", `CREATE TRIGGER grok_fail_thread BEFORE INSERT ON threads BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"participant", "", `CREATE TRIGGER grok_fail_participant BEFORE INSERT ON thread_participants BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"message", "", `CREATE TRIGGER grok_fail_message BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"thread-counters", "t3 I1", `CREATE TRIGGER grok_fail_thread_counters BEFORE UPDATE OF message_count ON threads BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"delivery_event", "", `CREATE TRIGGER grok_fail_delivery_event BEFORE INSERT ON events WHEN NEW.type='delivery' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"delivery", "", `CREATE TRIGGER grok_fail_delivery BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"operator_view", "", `CREATE TRIGGER grok_fail_operator_view BEFORE INSERT ON events WHEN NEW.type='operator_view' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"setup_audit", "", `CREATE TRIGGER grok_fail_setup_audit BEFORE INSERT ON audit WHEN NEW.action='grokbot_setup' BEGIN SELECT RAISE(ABORT,'injected'); END`},
			{"receipt", "", `CREATE TRIGGER grok_fail_receipt BEFORE INSERT ON meta WHEN NEW.key LIKE 'onboarding.v1/%' AND NEW.key NOT LIKE 'onboarding.v1/connector/%' BEGIN SELECT RAISE(ABORT,'injected'); END`},
		}
		for _, b := range boundaries {
			title := b.name
			if b.finding != "" {
				title = b.name + " (" + b.finding + ")"
			}
			t.Run(title, func(t *testing.T) {
				installTrigger(t, env, b.sql)
				snap := grokCensus(t, env)
				bKey := fakeRoutineKey(t)
				bURL := fakeRoutineURL(t)
				req := grokReq("boundary-"+b.name, "Boundary "+b.name, bURL, "Bearer "+bKey, "", false)
				res, err := env.Hub.OnboardAgent(ctx, op, req)
				if err == nil || !strings.Contains(err.Error(), "injected") {
					t.Fatalf("boundary %s did not fail on the injected write", b.name)
				}
				assertEmptyOnboardResult(t, res)
				assertNoRoutineSecret(t, env, b.name, res, err, []string{bKey, bURL, "Bearer " + bKey})
				if !reflectCensus(t, env, snap) {
					t.Fatalf("boundary %s left a remnant", b.name)
				}
			})
		}
	})

	t.Run("concurrent retry reopen", func(t *testing.T) {
		env := testhub.Start(t)
		ctx := context.Background()
		op := operatorCaller(t, env)
		key := fakeRoutineKey(t)
		url := fakeRoutineURL(t)
		secrets := []string{key, url, "Bearer " + key}
		req := grokReq("grok-replay", "Replay Bot", url, "Bearer "+key, "", false)

		start := make(chan struct{})
		var wg sync.WaitGroup
		out := make([]onboarding.SubmitResult, 6)
		errs := make([]error, 6)
		for i := 0; i < len(out); i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				out[i], errs[i] = env.Hub.OnboardAgent(ctx, op, req)
			}(i)
		}
		close(start)
		wg.Wait()
		var first onboarding.SubmitResult
		created := 0
		for i := range out {
			if errs[i] != nil {
				assertNoRoutineSecret(t, env, "concurrent", onboarding.SubmitResult{}, errs[i], secrets)
				t.Fatalf("concurrent submit %d failed", i)
			}
			assertNoRoutineSecret(t, env, "concurrent", out[i], nil, secrets)
			if out[i].HumanOutput != "" {
				t.Fatal("grok replay returned human output")
			}
			if first.PeerID == "" {
				first = out[i]
			}
			if out[i].Status == "created" {
				created++
			}
			if out[i].PeerID != first.PeerID || out[i].RouteID != first.RouteID || out[i].PrincipalID != first.PrincipalID || out[i].ConnectorName != first.ConnectorName {
				t.Fatal("concurrent submit did not return the same ids")
			}
			if out[i].Setup == nil || first.Setup == nil || out[i].Setup.ThreadID != first.Setup.ThreadID || out[i].Setup.MessageID != first.Setup.MessageID || out[i].Setup.DeliveryID != first.Setup.DeliveryID {
				t.Fatal("concurrent submit did not return the same setup ids")
			}
		}
		if created != 1 {
			t.Fatalf("created results = %d, want 1", created)
		}
		if countRows(t, env, `SELECT count(*) FROM messages WHERE thr_id=?`, first.Setup.ThreadID) != 1 {
			t.Fatal("concurrent submit created more than one setup message")
		}
		if countRows(t, env, `SELECT count(*) FROM deliveries WHERE msg_id=?`, first.Setup.MessageID) != 1 {
			t.Fatal("concurrent submit created more than one setup delivery")
		}

		again := mustOnboard(t, env, op, req)
		if again.Status != "replayed" || again.PeerID != first.PeerID || again.RouteID != first.RouteID || again.Setup == nil || again.Setup.DeliveryID != first.Setup.DeliveryID || again.HumanOutput != "" {
			t.Fatal("lost-response retry did not return the original ids")
		}
		if countRows(t, env, `SELECT count(*) FROM messages WHERE thr_id=?`, first.Setup.ThreadID) != 1 || countRows(t, env, `SELECT count(*) FROM deliveries WHERE msg_id=?`, first.Setup.MessageID) != 1 {
			t.Fatal("lost-response retry created another setup message or delivery")
		}
		assertNoRoutineSecret(t, env, "retry", again, nil, secrets)

		clone := env.CloneDB(t)
		cloneOp := operatorCaller(t, clone)
		reopened := mustOnboard(t, clone, cloneOp, req)
		if reopened.Status != "replayed" || reopened.PeerID != first.PeerID || reopened.RouteID != first.RouteID || reopened.ConnectorName != first.ConnectorName || reopened.Setup == nil || reopened.Setup.ThreadID != first.Setup.ThreadID || reopened.Setup.MessageID != first.Setup.MessageID || reopened.Setup.DeliveryID != first.Setup.DeliveryID {
			t.Fatal("reopened hub did not return the original ids")
		}
		if countRows(t, clone, `SELECT count(*) FROM messages WHERE thr_id=?`, first.Setup.ThreadID) != 1 || countRows(t, clone, `SELECT count(*) FROM deliveries WHERE msg_id=?`, first.Setup.MessageID) != 1 {
			t.Fatal("reopened hub has a different setup message or delivery count")
		}
		digest := receiptDigest(t, env, op.PrincipalID, req.RequestID)
		if receiptDigest(t, clone, cloneOp.PrincipalID, req.RequestID) != digest {
			t.Fatal("reopened hub lost the keyed digest")
		}
	})

	t.Run("conflict", func(t *testing.T) {
		env := testhub.Start(t)
		ctx := context.Background()
		op := operatorCaller(t, env)
		key := fakeRoutineKey(t)
		url := fakeRoutineURL(t)
		req := grokReq("grok-conflict", "Conflict Bot", url, "Bearer "+key, "", false)
		first := mustOnboard(t, env, op, req)
		digest := receiptDigest(t, env, op.PrincipalID, req.RequestID)
		snap := grokCensus(t, env)
		otherKey := fakeRoutineKey(t)
		otherURL := fakeRoutineURL(t)
		changes := []onboarding.SubmitRequest{
			grokReq(req.RequestID, "Conflict Bot", url, "Bearer "+otherKey, "", false),
			grokReq(req.RequestID, "Conflict Bot", otherURL, "Bearer "+key, "", false),
			grokReq(req.RequestID, "Renamed Bot", url, "Bearer "+key, "", false),
			grokReq(req.RequestID, "Conflict Bot", url, "Bearer "+key, "", true),
			grokReq(req.RequestID, "Conflict Bot", url, "Bearer "+key, "vecnet-other-name", false),
		}
		for _, changed := range changes {
			secrets := []string{key, otherKey, url, otherURL, "Bearer " + key, "Bearer " + otherKey}
			res, err := env.Hub.OnboardAgent(ctx, op, changed)
			if !onboardStatus(t, err, 409, "idempotency_conflict") {
				t.Fatal("changed request was not an idempotency conflict")
			}
			if res.PeerID != "" || res.RouteID != "" || res.HumanOutput != "" {
				t.Fatal("conflict returned resource ids")
			}
			assertNoRoutineSecret(t, env, "conflict", res, err, secrets)
			if !reflectCensus(t, env, snap) {
				t.Fatal("conflict wrote a peer, route, message or receipt")
			}
			if receiptDigest(t, env, op.PrincipalID, req.RequestID) != digest {
				t.Fatal("conflict replaced the keyed digest")
			}
			var n int
			if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM routes WHERE url=?`, otherURL).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatal("conflict stored the changed routine URL")
			}
		}
		replay := mustOnboard(t, env, op, req)
		if replay.PeerID != first.PeerID || replay.Status != "replayed" {
			t.Fatal("the original request no longer replays after a conflict")
		}
		secondKey := fakeRoutineKey(t)
		secondURL := fakeRoutineURL(t)
		second := mustOnboard(t, env, op, grokReq("grok-conflict-other", "Other Conflict", secondURL, secondKey, "", false))
		otherDigest := receiptDigest(t, env, op.PrincipalID, "grok-conflict-other")
		if otherDigest == digest || otherDigest == "" || strings.Contains(otherDigest, secondKey) || strings.Contains(digest, key) {
			t.Fatal("keyed digest does not distinguish requests")
		}
		if second.PeerID == first.PeerID {
			t.Fatal("a different request reused the first peer")
		}
		assertNoRoutineSecret(t, env, "second", second, nil, []string{key, secondKey, url, secondURL})
	})
}

// TestGrokbotOnboardingSecretBoundary is Task 3's secret pin: the route holds
// a sealed raw key, the peer keeps creation defaults, the descriptor names
// one mode for that peer only, and no result, message, audit or receipt
// carries the routine URL or key.
func TestGrokbotOnboardingSecretBoundary(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	op := operatorCaller(t, env)
	ownerBefore := principalName(t, env, op.PrincipalID)
	snap, err := env.Hub.Preferences.ReadPreferences(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.Preferences.PatchPreferences(ctx, op, snap.ETag, prefs.Patch{Set: map[string]prefs.Value{
		"rates.sends_per_minute": prefs.Int(70),
		"rates.threads_per_hour": prefs.Int(9),
	}}); err != nil {
		t.Fatal(err)
	}

	key := fakeRoutineKey(t)
	url := fakeRoutineURL(t)
	header := "bEaReR " + key
	secrets := []string{key, url, header, "Bearer " + key}
	res := mustOnboard(t, env, op, grokReq("grok-secret", "Helper Bot", url, header, "", false))
	if res.AuthState != onboarding.AuthUnlinked || res.HumanOutput != "" || res.ConnectorName != "vecnet-helper-bot" || res.SendDefaults["priority"] != "wake_idle" {
		t.Fatal("hosted result is missing unlinked state, the connector name or wake_idle")
	}
	assertNoRoutineSecret(t, env, "hosted", res, nil, secrets)
	assertSealedRawKey(t, env, res.RouteID, key)
	assertDefaults(t, env, res.PeerID, 70, 9)
	if n := countRows(t, env, `SELECT count(*) FROM scopes WHERE peer_id=?`, res.PeerID); n != 0 {
		t.Fatalf("scopes = %d, want the default allow-all", n)
	}
	var runs int
	if err := env.Hub.DB.QueryRow(`SELECT runs_per_hour FROM routes WHERE route_id=?`, res.RouteID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 200 {
		t.Fatalf("runs_per_hour = %d, want 200", runs)
	}
	if n := countRows(t, env, `SELECT count(*) FROM enrol_tokens WHERE peer_id=?`, res.PeerID); n != 0 {
		t.Fatal("grok onboarding minted an enrolment token")
	}
	if n := countRows(t, env, `SELECT count(*) FROM credentials WHERE subject_id=?`, res.PeerID); n != 0 {
		t.Fatal("grok onboarding minted a peer credential")
	}
	if n := countRows(t, env, `SELECT count(*) FROM dot_links WHERE peer_id=?`, res.PeerID); n != 0 {
		t.Fatal("grok onboarding minted a link code")
	}
	var peerName, principalNameGot string
	if err := env.Hub.DB.QueryRow(`SELECT p.name, pr.name FROM peers p JOIN principals pr ON pr.prn_id=p.prn_id WHERE p.peer_id=?`, res.PeerID).Scan(&peerName, &principalNameGot); err != nil {
		t.Fatal(err)
	}
	if peerName != "Helper Bot" || principalNameGot != "Helper Bot" || res.PrincipalID == op.PrincipalID {
		t.Fatal("peer or principal name did not follow creation defaults")
	}
	if principalName(t, env, op.PrincipalID) != ownerBefore {
		t.Fatal("onboarding renamed the operator principal")
	}
	desc := grokDescriptor(t, env, res.ConnectorName)
	if desc.PeerID != res.PeerID || desc.Name != res.ConnectorName || desc.CredentialMode != onboarding.CredentialHostedOAuth {
		t.Fatal("descriptor does not belong to this peer in hosted OAuth mode")
	}
	if countRows(t, env, `SELECT count(*) FROM meta WHERE key LIKE 'onboarding.v1/connector/%' AND value LIKE ?`, "%"+res.PeerID+"%") != 1 {
		t.Fatal("descriptor is not unique to the peer")
	}

	cliKey := fakeRoutineKey(t)
	cliURL := fakeRoutineURL(t)
	cli := mustOnboard(t, env, op, grokReq("grok-cli", "CLI Bot", cliURL, cliKey, "vecnet-cli-bot", true))
	cliSecrets := []string{cliKey, cliURL, key, url}
	assertNoRoutineSecret(t, env, "cli", cli, nil, cliSecrets)
	cliDesc := grokDescriptor(t, env, "vecnet-cli-bot")
	if cliDesc.PeerID != cli.PeerID || cliDesc.CredentialMode != onboarding.CredentialCLIBearer || cliDesc.PeerID == res.PeerID {
		t.Fatal("CLI descriptor is not that peer's cli_bearer mode")
	}
	if strings.Contains(cliDesc.raw, onboarding.CredentialHostedOAuth) || strings.Contains(desc.raw, onboarding.CredentialCLIBearer) {
		t.Fatal("a descriptor names both credential modes")
	}
	if n := countRows(t, env, `SELECT count(*) FROM enrol_tokens WHERE peer_id=?`, cli.PeerID) + countRows(t, env, `SELECT count(*) FROM dot_links WHERE peer_id=?`, cli.PeerID); n != 0 {
		t.Fatal("CLI bearer mode minted a token or a link code")
	}

	peers := countRows(t, env, `SELECT count(*) FROM peers`)
	messages := countRows(t, env, `SELECT count(*) FROM messages`)
	meta := countRows(t, env, `SELECT count(*) FROM meta WHERE key LIKE 'onboarding.v1/%'`)
	clashKey := fakeRoutineKey(t)
	clashURL := fakeRoutineURL(t)
	clash, err := env.Hub.OnboardAgent(ctx, op, grokReq("grok-clash", "Second Helper", clashURL, clashKey, res.ConnectorName, false))
	if !onboardField(t, err, 409, onboarding.CodeConnectorNameConflict, "connector_name") {
		t.Fatal("connector collision was not connector_name_conflict")
	}
	assertNoRoutineSecret(t, env, "collision", clash, err, []string{clashKey, clashURL, key, url})
	if countRows(t, env, `SELECT count(*) FROM peers`) != peers || countRows(t, env, `SELECT count(*) FROM messages`) != messages || countRows(t, env, `SELECT count(*) FROM meta WHERE key LIKE 'onboarding.v1/%'`) != meta {
		t.Fatal("connector collision was not atomic")
	}

	badKey := fakeRoutineKey(t)
	badURL := "https://api2.cursor.sh/automations/webhook/" + strings.Repeat("ab", 18)
	peers = countRows(t, env, `SELECT count(*) FROM peers`)
	_, err = env.Hub.OnboardAgent(ctx, op, grokReq("grok-ungrouped", "Ungrouped", badURL, badKey, "", false))
	if err == nil {
		t.Fatal("onboarding accepted a routine URL without grouped UUID fields")
	}
	assertNoRoutineSecret(t, env, "ungrouped", onboarding.SubmitResult{}, err, []string{badKey, badURL})
	if countRows(t, env, `SELECT count(*) FROM peers`) != peers {
		t.Fatal("rejected routine URL created a peer")
	}
	legacyPeer, _, _, err := env.Hub.AddPeerWith(ctx, hub.AddPeerOpts{Kind: "session", Name: "Legacy Route"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.AddGrokbotRoute(ctx, "peer", legacyPeer, badURL, badKey); err != nil {
		t.Fatal("legacy route add rejected a 36-character routine URL")
	}
	nested := "Bearer Bearer " + fakeRoutineKey(t)
	if _, err := env.Hub.OnboardAgent(ctx, op, grokReq("grok-nested", "Nested", url, nested, "vecnet-nested", false)); err == nil || strings.Contains(err.Error(), nested) {
		t.Fatal("nested Authorization scheme was accepted or echoed")
	}
}

// TestGrokbotSetupHubDelivery is Task 3's setup-message pin: the hub sends
// one wake_idle message to the bot, the operator shares the thread, and the
// body names one credential mode.
func TestGrokbotSetupHubDelivery(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	op := operatorCaller(t, env)
	sub := env.Hub.Activity.Subscribe(ctx)
	key := fakeRoutineKey(t)
	url := fakeRoutineURL(t)
	secrets := []string{key, url, "Bearer " + key}
	res := mustOnboard(t, env, op, grokReq("grok-setup", "Helper Bot", url, "Bearer "+key, "", false))
	assertNoRoutineSecret(t, env, "setup", res, nil, secrets)
	if res.Setup == nil || res.Setup.State != onboarding.SetupQueued {
		t.Fatal("setup delivery is not queued")
	}
	assertHubSetup(t, env, op.PeerID, res, onboarding.CredentialHostedOAuth, "vecnet-helper-bot", "")
	select {
	case ev := <-sub.C:
		if ev.Type != "message" || ev.Sender != hub.HubSender || ev.Kind != "message" || ev.ThreadID != res.Setup.ThreadID || ev.MessageID != res.Setup.MessageID {
			t.Fatal("activity event is not the committed hub setup message")
		}
	default:
		t.Fatal("setup commit did not publish activity")
	}
	replay := mustOnboard(t, env, op, grokReq("grok-setup", "Helper Bot", url, "Bearer "+key, "", false))
	if replay.Status != "replayed" {
		t.Fatal("setup retry did not replay")
	}
	select {
	case <-sub.C:
		t.Fatal("replay published another activity event")
	default:
	}

	cliKey := fakeRoutineKey(t)
	cliURL := fakeRoutineURL(t)
	cli := mustOnboard(t, env, op, grokReq("grok-setup-cli", "CLI Bot", cliURL, cliKey, "", true))
	assertNoRoutineSecret(t, env, "setup-cli", cli, nil, []string{cliKey, cliURL, key, url})
	assertHubSetup(t, env, op.PeerID, cli, onboarding.CredentialCLIBearer, "", onboarding.Slug("CLI Bot"))

	t.Run("missing operator", func(t *testing.T) {
		env := testhub.Start(t)
		op := operatorCaller(t, env)
		if _, err := env.Hub.DB.Exec(`DELETE FROM peers WHERE peer_id=?`, op.PeerID); err != nil {
			t.Fatal(err)
		}
		snap := grokCensus(t, env)
		missKey := fakeRoutineKey(t)
		missURL := fakeRoutineURL(t)
		_, err := env.Hub.OnboardAgent(context.Background(), op, grokReq("grok-no-operator", "Missing Op", missURL, missKey, "", false))
		if err == nil {
			t.Fatal("missing operator peer was accepted")
		}
		assertNoRoutineSecret(t, env, "missing-operator", onboarding.SubmitResult{}, err, []string{missKey, missURL})
		if !reflectCensus(t, env, snap) {
			t.Fatal("missing operator peer wrote onboarding rows")
		}
	})
}

// Activity at is the setup message created_at (PROTOCOL §2.13), including when
// the hub clock moves between the insert and the after-commit publish.
func TestGrokbotSetupActivityAt(t *testing.T) {
	t.Run("(t3 I2)", func(t *testing.T) {
		env := testhub.Start(t)
		ctx := context.Background()
		op := operatorCaller(t, env)
		start := env.Now.Now()
		var ticks int
		env.Hub.Now = func() time.Time {
			ticks++
			return start.Add(time.Duration(ticks) * time.Millisecond)
		}
		sub := env.Hub.Activity.Subscribe(ctx)
		key := fakeRoutineKey(t)
		url := fakeRoutineURL(t)
		res := mustOnboard(t, env, op, grokReq("grok-activity-at", "Clock Bot", url, key, "", false))
		if res.Setup == nil {
			t.Fatal("missing setup")
		}
		var created, body string
		if err := env.Hub.DB.QueryRow(`SELECT created_at, body FROM messages WHERE msg_id=?`, res.Setup.MessageID).Scan(&created, &body); err != nil {
			t.Fatal(err)
		}
		var msg envelope.Message
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.CreatedAt != created {
			t.Fatalf("message body created_at %s, row %s", msg.CreatedAt, created)
		}
		if ticks < 2 {
			t.Fatal("clock did not advance during submit")
		}
		select {
		case ev := <-sub.C:
			if ev.Type != "message" || ev.MessageID != res.Setup.MessageID || ev.At != created {
				t.Fatalf("activity at %q, want message created_at %q", ev.At, created)
			}
		default:
			t.Fatal("setup commit did not publish activity")
		}
		assertNoRoutineSecret(t, env, "activity-at", res, nil, []string{key, url})
	})
}

func grokReq(id, name, url, key, connector string, skip bool) onboarding.SubmitRequest {
	in := map[string]any{"name": name, "webhook_url": url, "webhook_key": key}
	if connector != "" {
		in["connector_name"] = connector
	}
	if skip {
		in["skip_mcp"] = true
	}
	return onboardReq("grokbot", id, in)
}

func fakeRoutineKey(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return "crsr_" + hex.EncodeToString(raw[:])
}

func fakeRoutineURL(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	h := hex.EncodeToString(raw[:])
	return "https://api2.cursor.sh/automations/webhook/" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func grokCensus(t *testing.T, env *testhub.Env) map[string]int {
	t.Helper()
	tables := []string{
		"principals", "peers", "routes", "threads", "thread_participants",
		"messages", "deliveries", "audit", "events", "enrol_tokens",
		"credentials", "dot_links", "route_runs",
	}
	out := make(map[string]int, len(tables)+1)
	for _, table := range tables {
		out[table] = countRows(t, env, "SELECT count(*) FROM "+table)
	}
	out["onboarding_meta"] = countRows(t, env, `SELECT count(*) FROM meta WHERE key LIKE 'onboarding.v1/%'`)
	return out
}

func reflectCensus(t *testing.T, env *testhub.Env, before map[string]int) bool {
	t.Helper()
	after := grokCensus(t, env)
	for k, v := range before {
		if after[k] != v {
			return false
		}
	}
	return true
}

func installTrigger(t *testing.T, env *testhub.Env, sql string) {
	t.Helper()
	name := sql[strings.Index(sql, "TRIGGER ")+len("TRIGGER ") : strings.Index(sql, " BEFORE")]
	if _, err := env.Hub.DB.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := env.Hub.DB.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			t.Errorf("drop trigger %s: %v", name, err)
		}
	})
}

func assertEmptyOnboardResult(t *testing.T, res onboarding.SubmitResult) {
	t.Helper()
	if res.RequestID != "" || res.Kind != "" || res.PeerID != "" || res.PrincipalID != "" || res.RouteID != "" || res.ConnectorName != "" || res.AuthState != "" || res.Status != "" || res.WebPath != "" || res.HumanOutput != "" || res.Setup != nil || len(res.SendDefaults) != 0 {
		t.Fatal("failed submit returned resource ids, setup ids, or human output")
	}
}

func assertNoRoutineSecret(t *testing.T, env *testhub.Env, label string, res onboarding.SubmitResult, err error, secrets []string) {
	t.Helper()
	var blob strings.Builder
	if err != nil {
		blob.WriteString(err.Error())
		var api *hub.APIError
		if errors.As(err, &api) {
			blob.WriteString(api.Message)
			raw, mErr := json.Marshal(api.Details)
			if mErr != nil {
				t.Fatal(mErr)
			}
			blob.Write(raw)
		}
	}
	raw, mErr := json.Marshal(res)
	if mErr != nil {
		t.Fatal(mErr)
	}
	blob.Write(raw)
	for _, q := range []string{
		`SELECT value FROM meta`,
		`SELECT detail FROM audit`,
		`SELECT body FROM messages`,
		`SELECT data FROM events`,
		`SELECT coalesce(key_enc,'') FROM routes`,
	} {
		rows, qErr := env.Hub.DB.Query(q)
		if qErr != nil {
			t.Fatal(qErr)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			blob.WriteString(value)
			blob.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	text := blob.String()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%s contains a routine secret", label)
		}
	}
}

func assertSealedRawKey(t *testing.T, env *testhub.Env, routeID, key string) {
	t.Helper()
	var sealed, stored string
	if err := env.Hub.DB.QueryRow(`SELECT coalesce(key_enc,''), url FROM routes WHERE route_id=?`, routeID).Scan(&sealed, &stored); err != nil {
		t.Fatal(err)
	}
	if sealed == "" || sealed == key || strings.Contains(sealed, key) || strings.Contains(strings.ToLower(sealed), "bearer") || !strings.HasPrefix(sealed, "v1:") {
		t.Fatal("route key is not sealed")
	}
	got, err := env.Hub.GrokbotKey(context.Background(), routeID)
	if err != nil || got != key || strings.Contains(got, " ") || strings.Contains(strings.ToLower(got), "bearer") {
		t.Fatal("sealed route key does not open to the normalized raw key")
	}
	if stored == "" || stored == key {
		t.Fatal("route is missing its routine URL")
	}
}

type grokDescriptorRow struct {
	V              int    `json:"v"`
	PeerID         string `json:"peer_id"`
	Name           string `json:"name"`
	CredentialMode string `json:"credential_mode"`
	raw            string
}

func grokDescriptor(t *testing.T, env *testhub.Env, name string) grokDescriptorRow {
	t.Helper()
	var raw string
	if err := env.Hub.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, "onboarding.v1/connector/"+name).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var row grokDescriptorRow
	if err := dec.Decode(&row); err != nil {
		t.Fatal("connector descriptor has unexpected fields")
	}
	row.raw = raw
	if row.V != 1 || (row.CredentialMode != onboarding.CredentialHostedOAuth && row.CredentialMode != onboarding.CredentialCLIBearer) {
		t.Fatal("connector descriptor has no single credential mode")
	}
	if strings.Contains(raw, onboarding.CredentialHostedOAuth) && strings.Contains(raw, onboarding.CredentialCLIBearer) {
		t.Fatal("connector descriptor names both credential modes")
	}
	return row
}

func receiptDigest(t *testing.T, env *testhub.Env, subject, requestID string) string {
	t.Helper()
	var raw string
	if err := env.Hub.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, "onboarding.v1/"+subject+"/"+requestID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Digest) != 64 {
		t.Fatal("receipt digest is not a keyed hash")
	}
	return rec.Digest
}

func assertHubSetup(t *testing.T, env *testhub.Env, operator string, res onboarding.SubmitResult, mode, connector, profile string) {
	t.Helper()
	if res.Setup == nil {
		t.Fatal("missing setup")
	}
	var sender, kind, body string
	if err := env.Hub.DB.QueryRow(`SELECT sender, kind, body FROM messages WHERE msg_id=?`, res.Setup.MessageID).Scan(&sender, &kind, &body); err != nil {
		t.Fatal(err)
	}
	if sender != hub.HubSender || kind != "message" {
		t.Fatal("setup message is not a hub message")
	}
	var msg envelope.Message
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Kind != "message" || msg.Sender != hub.HubSender || msg.Priority != "wake_idle" || msg.SenderInfo == nil || msg.SenderInfo.Name != hub.HubSender || msg.SenderInfo.Kind != hub.HubSender {
		t.Fatal("setup message sender, kind or priority is wrong")
	}
	if len(msg.Recipients) != 1 || msg.Recipients[0] != res.PeerID {
		t.Fatal("setup message is not addressed only to the bot")
	}
	text := msg.Content.Text
	hubURL := strings.TrimRight(env.Hub.Cfg.PublicURL, "/")
	for _, need := range []string{res.PeerID, "wake_idle", res.Setup.ThreadID, hubURL} {
		if !strings.Contains(text, need) {
			t.Fatalf("setup text is missing %q", need)
		}
	}
	switch mode {
	case onboarding.CredentialHostedOAuth:
		for _, need := range []string{"Hosted OAuth", connector, hubURL + "/mcp"} {
			if !strings.Contains(text, need) {
				t.Fatalf("hosted setup text is missing %q", need)
			}
		}
		if strings.Contains(text, "CLI bearer") || strings.Contains(text, "stdio") || strings.Contains(text, "mcp-remote") || strings.Contains(text, "Bearer ") || linkCodeRE.MatchString(text) {
			t.Fatal("hosted setup text contains a bearer, link code, CLI mode or stdio command")
		}
	case onboarding.CredentialCLIBearer:
		for _, need := range []string{"CLI bearer", profile} {
			if !strings.Contains(text, need) {
				t.Fatalf("CLI setup text is missing %q", need)
			}
		}
		for _, banned := range []string{"OAuth", "peer link", "/mcp", "vecnet-", "stdio", "mcp-remote"} {
			if strings.Contains(text, banned) {
				t.Fatalf("CLI setup text contains %q", banned)
			}
		}
	default:
		t.Fatalf("unknown mode %s", mode)
	}

	rows, err := env.Hub.DB.Query(`SELECT peer_id FROM thread_participants WHERE thr_id=?`, res.Setup.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			t.Fatal(err)
		}
		got[peer] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[operator] || !got[res.PeerID] || got[hub.HubSender] {
		t.Fatal("setup thread participants are not the operator and the bot")
	}

	var recipient, priority, state string
	var held, attempts int
	var route string
	if err := env.Hub.DB.QueryRow(`SELECT recipient, priority, state, held, attempts_left, coalesce(route_id,'') FROM deliveries WHERE dly_id=?`, res.Setup.DeliveryID).Scan(&recipient, &priority, &state, &held, &attempts, &route); err != nil {
		t.Fatal(err)
	}
	if recipient != res.PeerID || priority != "wake_idle" || state != "queued" || held != 0 || attempts != hub.PushAttempts || route != res.RouteID {
		t.Fatal("setup delivery is not queued wake_idle for the bot on its route")
	}
	if countRows(t, env, `SELECT count(*) FROM deliveries WHERE msg_id=?`, res.Setup.MessageID) != 1 {
		t.Fatal("setup message has more than one delivery")
	}
	if countRows(t, env, `SELECT count(*) FROM deliveries WHERE msg_id=? AND recipient=?`, res.Setup.MessageID, operator) != 0 {
		t.Fatal("operator received the setup delivery")
	}
	var verified, kindRoute string
	if err := env.Hub.DB.QueryRow(`SELECT coalesce(verified_at,''), kind FROM routes WHERE route_id=?`, route).Scan(&verified, &kindRoute); err != nil {
		t.Fatal(err)
	}
	if verified == "" || kindRoute != hub.RouteKindGrokbot {
		t.Fatal("setup route is not a live grokbot route")
	}
	var ingress string
	if err := env.Hub.DB.QueryRow(`SELECT json_extract(data,'$.ingress') FROM events WHERE owner=? AND type='delivery'`, res.PeerID).Scan(&ingress); err != nil {
		t.Fatal(err)
	}
	if ingress != "hub" {
		t.Fatal("setup delivery ingress is not hub")
	}
	now := envelope.FormatTime(env.Now.Now())
	var due int
	if err := env.Hub.DB.QueryRow(`
		SELECT count(*) FROM deliveries d
		JOIN routes r ON r.route_id = d.route_id
		WHERE d.dly_id=? AND d.state IN ('queued','pushed') AND d.priority != 'hold' AND d.held=0
		  AND r.verified_at IS NOT NULL AND r.kind=?
		  AND (d.next_attempt_at IS NULL OR d.next_attempt_at <= ?)
		  AND d.expires_at > ?`, res.Setup.DeliveryID, hub.RouteKindGrokbot, now, now).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due != 1 {
		t.Fatal("setup delivery is not claimable by the push worker")
	}
	if state == "accepted" || state == "processed" {
		t.Fatal("queued setup was recorded as processed")
	}
}
