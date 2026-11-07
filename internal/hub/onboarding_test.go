package hub_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// linkCodeRE is the grouped form DotLink shows once (PROTOCOL §20.1).
var linkCodeRE = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}$`)

func TestOnboardLegacyKinds(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	op := operatorCaller(t, env)
	ownerBefore := principalName(t, env, op.PrincipalID)

	t.Run("kinds", func(t *testing.T) {
		got, err := env.Hub.OnboardingKinds(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		want := onboarding.Specs()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("OnboardingKinds = %#v, want registry Specs()", kindsBrief(got))
		}
		if len(got) > 0 {
			got[0].Label = "mutated"
			got[0].Fields = nil
		}
		again, err := env.Hub.OnboardingKinds(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again, want) {
			t.Fatal("OnboardingKinds returned a live registry value")
		}
		for _, spec := range again {
			if spec.Kind == "operator" || spec.Kind == "source" {
				t.Fatalf("listing includes infrastructure kind %s", spec.Kind)
			}
		}
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		metaBefore := onboardKeys(t, env)
		if _, err := env.Hub.OnboardingKinds(ctx, op); err != nil {
			t.Fatal(err)
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore || !reflect.DeepEqual(onboardKeys(t, env), metaBefore) {
			t.Fatal("listing mutated peers or receipts")
		}
	})

	t.Run("authority", func(t *testing.T) {
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		metaBefore := onboardKeys(t, env)
		foreign := auth.Caller{Kind: "operator", PrincipalID: "prn_other", PeerID: "peer_other", PeerKind: "operator"}
		sessionShaped := op
		sessionShaped.PeerKind = "session"
		for _, c := range []auth.Caller{{}, {Kind: "peer", PeerID: op.PeerID, PrincipalID: op.PrincipalID, PeerKind: "operator"}, {Kind: "principal", PrincipalID: op.PrincipalID}, foreign, sessionShaped} {
			if _, err := env.Hub.OnboardingKinds(ctx, c); !onboardStatus(t, err, http.StatusForbidden, "forbidden") {
				t.Fatalf("kinds caller %#v: %v", c.Kind, err)
			}
			req := onboardReq("session", "auth-"+c.Kind+"-"+c.PeerKind, map[string]any{"name": "Blocked"})
			if _, err := env.Hub.OnboardAgent(ctx, c, req); !onboardStatus(t, err, http.StatusForbidden, "forbidden") {
				t.Fatalf("submit caller %#v: %v", c, err)
			}
			if _, err := env.Hub.OnboardingStatus(ctx, c, req.RequestID); !onboardStatus(t, err, http.StatusForbidden, "forbidden") {
				t.Fatalf("status caller %#v: %v", c, err)
			}
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore || !reflect.DeepEqual(onboardKeys(t, env), metaBefore) {
			t.Fatal("a refused caller created a peer or receipt")
		}
	})

	t.Run("revoked operator", func(t *testing.T) {
		revoked := op
		if _, err := env.Hub.DB.Exec(`UPDATE credentials SET revoked_at=? WHERE cred_id=?`, envelope.FormatTime(env.Now.Now()), op.CredentialID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := env.Hub.DB.Exec(`UPDATE credentials SET revoked_at=NULL WHERE cred_id=?`, op.CredentialID); err != nil {
				t.Errorf("restore operator credential: %v", err)
			}
		})
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		if _, err := env.Hub.OnboardAgent(ctx, revoked, onboardReq("session", "revoked-submit", map[string]any{"name": "Nope"})); !onboardStatus(t, err, http.StatusUnauthorized, "unauthenticated") {
			t.Fatal(err)
		}
		if _, err := env.Hub.OnboardingStatus(ctx, revoked, "revoked-submit"); !onboardStatus(t, err, http.StatusUnauthorized, "unauthenticated") {
			t.Fatal(err)
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore {
			t.Fatal("revoked operator created a peer")
		}
	})

	fixed := map[string]string{"session": "Ada Lovelace", "launcher": "Launch Pad", "openclaw": "Claw", "pagchat-bot": "Pag"}
	var session onboarding.SubmitResult
	for kind, name := range fixed {
		t.Run(kind, func(t *testing.T) {
			res := mustOnboard(t, env, op, onboardReq(kind, "fixed-"+kind, map[string]any{"name": name}))
			if res.Status != "created" || res.Kind != kind || res.PeerID == "" || res.HumanOutput == "" {
				t.Fatalf("created result missing fields: status %q kind %q peer %q human %t", res.Status, res.Kind, res.PeerID, res.HumanOutput != "")
			}
			if res.AuthState != onboarding.AuthPending {
				t.Fatalf("auth_state = %q, want pending", res.AuthState)
			}
			assertInvite(t, env, kind, res, false, "")
			assertPeer(t, env, res, kind, name, false, op.PrincipalID)
			assertDefaults(t, env, res.PeerID, 60, 20)
			if kind == "session" {
				session = res
			}
		})
	}

	t.Run("free-form name", func(t *testing.T) {
		res := mustOnboard(t, env, op, onboardReq("session", "free-form", map[string]any{"name": "Hello, world!"}))
		assertPeer(t, env, res, "session", "Hello, world!", false, op.PrincipalID)
		assertInvite(t, env, "session", res, false, "")
		if _, err := env.Hub.OnboardAgent(ctx, op, onboardReq("session", "open-bad", map[string]any{"name": "Hello, world!", "name_open": true})); !onboardStatus(t, err, http.StatusUnprocessableEntity, "invalid_value") {
			t.Fatal(err)
		}
	})

	t.Run("open name", func(t *testing.T) {
		suggested := mustOnboard(t, env, op, onboardReq("launcher", "open-suggested", map[string]any{"name": "Suggested Name", "name_open": true}))
		assertPeer(t, env, suggested, "launcher", "Suggested Name", true, op.PrincipalID)
		assertInvite(t, env, "launcher", suggested, true, "Suggested Name")
		plain := mustOnboard(t, env, op, onboardReq("openclaw", "open-plain", map[string]any{"name_open": true}))
		wantName := "invited-" + strings.ToLower(plain.PeerID[len(plain.PeerID)-6:])
		assertPeer(t, env, plain, "openclaw", wantName, true, op.PrincipalID)
		assertInvite(t, env, "openclaw", plain, true, "")
	})

	t.Run("ttl", func(t *testing.T) {
		short := mustOnboard(t, env, op, onboardReq("session", "ttl-short", map[string]any{"name": "Short", "enrol_ttl_seconds": 60}))
		assertExpiry(t, env, short.PeerID, env.Now.Now().Add(60*time.Second))
		long := mustOnboard(t, env, op, onboardReq("session", "ttl-long", map[string]any{"name": "Long", "enrol_ttl_seconds": 604800}))
		assertExpiry(t, env, long.PeerID, env.Now.Now().Add(604800*time.Second))
		zero := mustOnboard(t, env, op, onboardReq("session", "ttl-zero", map[string]any{"name": "Zero", "enrol_ttl_seconds": 0}))
		assertExpiry(t, env, zero.PeerID, env.Now.Now().Add(24*time.Hour))
		assertExpiry(t, env, session.PeerID, env.Now.Now().Add(24*time.Hour))
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		for _, n := range []int{59, 604801} {
			if _, err := env.Hub.OnboardAgent(ctx, op, onboardReq("session", "ttl-bad", map[string]any{"name": "Bad", "enrol_ttl_seconds": n})); !onboardField(t, err, http.StatusUnprocessableEntity, "invalid_value", "enrol_ttl_seconds") {
				t.Fatal(err)
			}
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore {
			t.Fatal("a refused ttl created a peer")
		}
	})

	t.Run("existing principal", func(t *testing.T) {
		prn, _ := env.NewPrincipal("Owner Kept")
		res := mustOnboard(t, env, op, onboardReq("pagchat-bot", "under-principal", map[string]any{"name": "Agent One", "principal_id": prn}))
		assertPeer(t, env, res, "pagchat-bot", "Agent One", false, op.PrincipalID)
		if res.PrincipalID != prn {
			t.Fatalf("principal_id = %s, want %s", res.PrincipalID, prn)
		}
		if got := principalName(t, env, prn); got != "Owner Kept" {
			t.Fatalf("principal renamed to %q", got)
		}
		if got := principalName(t, env, op.PrincipalID); got != ownerBefore {
			t.Fatalf("operator principal renamed from %q to %q", ownerBefore, got)
		}
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		if _, err := env.Hub.OnboardAgent(ctx, op, onboardReq("session", "missing-principal", map[string]any{"name": "Ghost", "principal_id": "prn_missing"})); err == nil {
			t.Fatal("unknown principal succeeded")
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore || len(onboardKeys(t, env)) == 0 {
			t.Fatal("unknown principal left a peer or dropped receipts")
		}
		metaBefore := onboardKeys(t, env)
		recovered := mustOnboard(t, env, op, onboardReq("session", "missing-principal", map[string]any{"name": "Ghost"}))
		if recovered.PeerID == "" || recovered.Status != "created" {
			t.Fatalf("retry after a refused submit did not create: %+v", recovered)
		}
		if len(onboardKeys(t, env)) != len(metaBefore)+1 {
			t.Fatal("refused submit consumed the request id")
		}
	})

	t.Run("dot link", func(t *testing.T) {
		res := mustOnboard(t, env, op, onboardReq("dot", "dot-one", map[string]any{"name": "Dot One"}))
		if res.Status != "created" || res.AuthState != onboarding.AuthPending || !linkCodeRE.MatchString(res.HumanOutput) {
			t.Fatalf("dot result status %q auth %q code ok %t", res.Status, res.AuthState, linkCodeRE.MatchString(res.HumanOutput))
		}
		prompt, err := onboarding.RenderPrompt("dot", "link", map[string]string{"name": "Dot One", "peer_id": res.PeerID})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(prompt, res.HumanOutput) || strings.Contains(res.HumanOutput, "printf") {
			t.Fatal("dot prompt contains the link code, or the code was replaced by an invite block")
		}
		assertPeer(t, env, res, "dot", "Dot One", false, op.PrincipalID)
		if n := countRows(t, env, `SELECT count(*) FROM enrol_tokens WHERE peer_id=?`, res.PeerID); n != 0 {
			t.Fatalf("dot enrol tokens = %d", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM dot_links WHERE peer_id=?`, res.PeerID); n != 1 {
			t.Fatalf("dot link rows = %d", n)
		}
		assertSecretAbsent(t, env, res.HumanOutput)
		legacy, err := env.Hub.DotLink(ctx, res.PeerID)
		if err != nil || legacy == res.HumanOutput || !linkCodeRE.MatchString(legacy) {
			t.Fatalf("direct DotLink = %q err %v", legacy, err)
		}
	})

	t.Run("not add-agent kinds", func(t *testing.T) {
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		for _, kind := range []string{"operator", "source", "nope"} {
			if _, err := env.Hub.OnboardAgent(ctx, op, onboardReq(kind, "bad-kind-"+kind, map[string]any{"name": "X"})); !onboardStatus(t, err, http.StatusBadRequest, "unknown_kind") {
				t.Fatal(err)
			}
		}
		stale := onboardReq("session", "stale", map[string]any{"name": "X"})
		stale.SpecVersion = "1999-01-01"
		if _, err := env.Hub.OnboardAgent(ctx, op, stale); !onboardStatus(t, err, http.StatusConflict, "stale_spec") {
			t.Fatal(err)
		}
		if _, err := env.Hub.OnboardAgent(ctx, op, onboardReq("session", "unknown-field", map[string]any{"name": "X", "as": op.PeerID})); !onboardField(t, err, http.StatusBadRequest, "unknown_field", "as") {
			t.Fatal(err)
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore {
			t.Fatal("a rejected kind or field created a peer")
		}
	})

	t.Run("grokbot rejects bad input", func(t *testing.T) {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		key := "crsr_" + hex.EncodeToString(raw[:])
		peersBefore := countRows(t, env, `SELECT count(*) FROM peers`)
		routesBefore := countRows(t, env, `SELECT count(*) FROM routes`)
		metaBefore := onboardKeys(t, env)
		badURL := "https://evil.example/hook"
		if _, err := env.Hub.OnboardAgent(ctx, op, onboardReq("grokbot", "grok-bad-url", map[string]any{
			"name": "Gateway", "webhook_url": badURL, "webhook_key": key,
		})); err == nil || strings.Contains(err.Error(), "evil.example") || strings.Contains(err.Error(), key) {
			t.Fatal("invalid grok input leaked or was accepted")
		}
		if countRows(t, env, `SELECT count(*) FROM peers`) != peersBefore || countRows(t, env, `SELECT count(*) FROM routes`) != routesBefore || !reflect.DeepEqual(onboardKeys(t, env), metaBefore) {
			t.Fatal("invalid grok input created a peer, route or receipt")
		}
	})

	t.Run("preference snapshot", func(t *testing.T) {
		kept := mustOnboard(t, env, op, onboardReq("session", "before-rates", map[string]any{"name": "Before Rates"}))
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
		res := mustOnboard(t, env, op, onboardReq("session", "after-rates", map[string]any{"name": "After Rates"}))
		assertDefaults(t, env, kept.PeerID, 60, 20)
		assertDefaults(t, env, res.PeerID, 70, 9)
		direct, _, _, err := env.Hub.AddPeerWith(ctx, hub.AddPeerOpts{Kind: "session", Name: "Direct Rates"})
		if err != nil {
			t.Fatal(err)
		}
		assertDefaults(t, env, direct, 70, 9)
		if n := len(onboardKeys(t, env)); n < 1 {
			t.Fatal("direct AddPeerWith removed onboarding receipts")
		}
		keys := onboardKeys(t, env)
		if _, _, _, err := env.Hub.AddPeerWith(ctx, hub.AddPeerOpts{Kind: "session", Name: "Direct Again"}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(onboardKeys(t, env), keys) {
			t.Fatal("direct AddPeerWith wrote an onboarding receipt")
		}
	})

	if got := principalName(t, env, op.PrincipalID); got != ownerBefore {
		t.Fatalf("operator principal renamed from %q to %q", ownerBefore, got)
	}
}

func operatorCaller(t *testing.T, env *testhub.Env) auth.Caller {
	t.Helper()
	var prn, cred string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, env.OperatorPeer).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.DB.QueryRow(`SELECT cred_id FROM credentials WHERE kind='operator' AND subject_id=? AND revoked_at IS NULL`, prn).Scan(&cred); err != nil {
		t.Fatal(err)
	}
	return auth.Caller{CredentialID: cred, Kind: "operator", PrincipalID: prn, PeerID: env.OperatorPeer, PeerKind: "operator"}
}

func onboardReq(kind, id string, inputs map[string]any) onboarding.SubmitRequest {
	return onboarding.SubmitRequest{Kind: kind, SpecVersion: onboarding.SpecVersion, RequestID: id, Inputs: inputs}
}

func mustOnboard(t *testing.T, env *testhub.Env, c auth.Caller, req onboarding.SubmitRequest) onboarding.SubmitResult {
	t.Helper()
	res, err := env.Hub.OnboardAgent(context.Background(), c, req)
	if err != nil {
		t.Fatalf("OnboardAgent(%s): %v", req.RequestID, err)
	}
	return res
}

func onboardStatus(t *testing.T, err error, status int, code string) bool {
	t.Helper()
	var api *hub.APIError
	if !errors.As(err, &api) || api.Status != status || api.Code != code {
		t.Errorf("error = %v, want %d %s", err, status, code)
		return false
	}
	return true
}

func onboardField(t *testing.T, err error, status int, code, field string) bool {
	t.Helper()
	var api *hub.APIError
	if !onboardStatus(t, err, status, code) {
		return false
	}
	if errors.As(err, &api) && api.Details["field"] != field {
		t.Errorf("details.field = %#v, want %s", api.Details["field"], field)
		return false
	}
	return true
}

func assertInvite(t *testing.T, env *testhub.Env, kind string, res onboarding.SubmitResult, open bool, suggestion string) {
	t.Helper()
	token := enrolToken(res.HumanOutput)
	if !strings.HasPrefix(token, "vn_enrol_") {
		t.Fatal("invite block has no enrolment token")
	}
	info, err := env.Hub.InviteInfo(context.Background(), res.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	var expires string
	if err := env.Hub.DB.QueryRow(`SELECT expires_at FROM enrol_tokens WHERE peer_id=? AND token_hash=?`, res.PeerID, auth.HashSecret(token)).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"owner": info.Owner, "hub_url": strings.TrimRight(env.Hub.Cfg.PublicURL, "/"),
		"profile": onboarding.Slug(info.Name), "token": token, "expires": expires,
	}
	if open {
		values["open"] = "true"
		if suggestion != "" {
			values["name"] = suggestion
		}
	}
	want, err := onboarding.RenderPrompt(kind, onboarding.PhaseInvite, values)
	if err != nil {
		t.Fatal(err)
	}
	if res.HumanOutput != want {
		t.Fatalf("human output differs from the registry invite\n got %s\nwant %s", redact(res.HumanOutput, token), redact(want, token))
	}
	assertSecretAbsent(t, env, token)
	safe, err := res.MarshalSafe()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(safe), token) {
		t.Fatal("MarshalSafe kept the enrolment token")
	}
}

func assertPeer(t *testing.T, env *testhub.Env, res onboarding.SubmitResult, kind, name string, open bool, operatorPrn string) {
	t.Helper()
	var gotName, gotKind, prn string
	var nameOpen, sends, threads int
	if err := env.Hub.DB.QueryRow(`SELECT name, kind, prn_id, name_open, sends_per_min, threads_per_hour FROM peers WHERE peer_id=?`, res.PeerID).Scan(&gotName, &gotKind, &prn, &nameOpen, &sends, &threads); err != nil {
		t.Fatal(err)
	}
	if gotName != name || gotKind != kind || (nameOpen != 0) != open {
		t.Fatalf("peer = %s %s open %d, want %s %s open %t", gotName, gotKind, nameOpen, name, kind, open)
	}
	if prn == operatorPrn {
		t.Fatal("peer was created on the operator principal")
	}
	if res.PrincipalID != prn {
		t.Fatalf("result principal %s, row %s", res.PrincipalID, prn)
	}
	if n := countRows(t, env, `SELECT count(*) FROM scopes WHERE peer_id=?`, res.PeerID); n != 0 {
		t.Fatalf("scopes = %d, want the default allow-all", n)
	}
	var actor, detail string
	if err := env.Hub.DB.QueryRow(`SELECT actor, detail FROM audit WHERE action='peer_add' AND target_ids LIKE ? ORDER BY id DESC LIMIT 1`, "%"+res.PeerID+"%").Scan(&actor, &detail); err != nil {
		t.Fatal(err)
	}
	if actor != env.OperatorPeer {
		t.Fatalf("peer_add actor = %q, want the operator peer", actor)
	}
	if strings.Contains(detail, "vn_") || strings.Contains(detail, "printf") {
		t.Fatal("peer_add detail contains credential material")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(detail), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["kind"] != kind || parsed["request_id"] == "" {
		t.Fatalf("peer_add detail = %s", detail)
	}
}

func assertDefaults(t *testing.T, env *testhub.Env, peer string, sends, threads int) {
	t.Helper()
	var gotSends, gotThreads int
	if err := env.Hub.DB.QueryRow(`SELECT sends_per_min, threads_per_hour FROM peers WHERE peer_id=?`, peer).Scan(&gotSends, &gotThreads); err != nil {
		t.Fatal(err)
	}
	if gotSends != sends || gotThreads != threads {
		t.Fatalf("limits = %d/min %d/hour, want %d/min %d/hour", gotSends, gotThreads, sends, threads)
	}
}

func assertExpiry(t *testing.T, env *testhub.Env, peer string, want time.Time) {
	t.Helper()
	var got string
	if err := env.Hub.DB.QueryRow(`SELECT expires_at FROM enrol_tokens WHERE peer_id=? AND used_at IS NULL`, peer).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != envelope.FormatTime(want) {
		t.Fatalf("expires_at = %s, want %s", got, envelope.FormatTime(want))
	}
}

func assertSecretAbsent(t *testing.T, env *testhub.Env, secret string) {
	t.Helper()
	for _, value := range append(onboardValues(t, env), auditDetails(t, env)...) {
		if strings.Contains(value, secret) {
			t.Fatal("a receipt or audit row contains one-time output")
		}
	}
}

func enrolToken(block string) string {
	const marker = "printf %s '"
	i := strings.Index(block, marker)
	if i < 0 {
		return ""
	}
	rest := block[i+len(marker):]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

func principalName(t *testing.T, env *testhub.Env, prn string) string {
	t.Helper()
	var name string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM principals WHERE prn_id=?`, prn).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func countRows(t *testing.T, env *testhub.Env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func onboardKeys(t *testing.T, env *testhub.Env) []string {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT key FROM meta WHERE key LIKE 'onboarding.v1/%' ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if keys == nil {
		keys = []string{}
	}
	return keys
}

func onboardValues(t *testing.T, env *testhub.Env) []string {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT value FROM meta WHERE key LIKE 'onboarding.v1/%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func auditDetails(t *testing.T, env *testhub.Env) []string {
	t.Helper()
	rows, err := env.Hub.DB.Query(`SELECT detail FROM audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func kindsBrief(specs []onboarding.KindSpec) []string {
	out := make([]string, len(specs))
	for i, spec := range specs {
		out[i] = spec.Kind
	}
	return out
}
