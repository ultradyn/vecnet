package hub_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestOnboardingReceiptIsolation(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	op := operatorCaller(t, env)
	other := testhub.Start(t)
	otherOp := operatorCaller(t, other)

	req := onboardReq("session", "retry-1", map[string]any{"name": "Retry Me", "enrol_ttl_seconds": 604800})
	first := mustOnboard(t, env, op, req)
	token := enrolToken(first.HumanOutput)
	if !strings.HasPrefix(token, "vn_enrol_") {
		t.Fatal("first submit has no enrolment token")
	}
	keys := onboardKeys(t, env)
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "onboarding.v1/"+op.PrincipalID+"/") || !strings.HasSuffix(keys[0], "/"+req.RequestID) {
		t.Fatalf("receipt key = %v, want onboarding.v1/%s/%s", keys, op.PrincipalID, req.RequestID)
	}
	raw := onboardValues(t, env)
	if len(raw) != 1 {
		t.Fatalf("receipt rows = %d", len(raw))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(raw[0]), &rec); err != nil {
		t.Fatal(err)
	}
	digest, _ := rec["digest"].(string)
	if len(digest) != 64 {
		t.Fatalf("digest length = %d", len(digest))
	}
	for _, forbidden := range []string{token, first.HumanOutput, "printf", "vn_enrol_", "webhook", "crsr_"} {
		if strings.Contains(raw[0], forbidden) {
			t.Fatalf("receipt contains %q", forbidden)
		}
	}
	if rec["subject"] != op.PrincipalID || rec["peer_id"] != first.PeerID || rec["request_id"] != req.RequestID || rec["kind"] != "session" {
		t.Fatalf("receipt identity = %#v", rec)
	}

	sameOnOther := mustOnboard(t, other, otherOp, req)
	otherRaw := onboardValues(t, other)
	var otherRec map[string]any
	if err := json.Unmarshal([]byte(otherRaw[0]), &otherRec); err != nil {
		t.Fatal(err)
	}
	if otherRec["digest"] == digest {
		t.Fatal("two hubs stored the same digest for one request")
	}
	if sameOnOther.PeerID == first.PeerID {
		t.Fatal("two hubs minted the same peer id")
	}

	replay := mustOnboard(t, env, op, req)
	if replay.Status != "replayed" || replay.PeerID != first.PeerID || replay.PrincipalID != first.PrincipalID || replay.HumanOutput != "" {
		t.Fatalf("replay = status %q peer %q human %t", replay.Status, replay.PeerID, replay.HumanOutput != "")
	}
	safe, err := replay.MarshalSafe()
	if err != nil || strings.Contains(string(safe), token) {
		t.Fatal("replay encoding contains the enrolment token")
	}
	if countRows(t, env, `SELECT count(*) FROM peers WHERE name=?`, "Retry Me") != 1 {
		t.Fatal("replay created another peer")
	}
	if got := onboardKeys(t, env); len(got) != 1 || got[0] != keys[0] {
		t.Fatalf("replay keys = %v", got)
	}

	changed := onboardReq("session", req.RequestID, map[string]any{"name": "Changed Name", "enrol_ttl_seconds": 604800})
	if _, err := env.Hub.OnboardAgent(ctx, op, changed); !onboardStatus(t, err, 409, "idempotency_conflict") || strings.Contains(err.Error(), "Changed Name") {
		t.Fatalf("conflict = %v", err)
	}
	if countRows(t, env, `SELECT count(*) FROM peers WHERE name=?`, "Changed Name") != 0 {
		t.Fatal("conflicting retry created a peer")
	}
	still := onboardValues(t, env)
	if still[0] != raw[0] {
		t.Fatal("conflicting retry rewrote the receipt")
	}

	status, err := env.Hub.OnboardingStatus(ctx, op, req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if status.PeerID != first.PeerID || status.HumanOutput != "" || status.Kind != "session" || status.AuthState != onboarding.AuthPending {
		t.Fatalf("status = %+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil || strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "human_output") {
		t.Fatal("status encoding contains human output")
	}
	if _, err := env.Hub.OnboardingStatus(ctx, op, "does-not-exist"); !onboardStatus(t, err, 404, "not_found") {
		t.Fatal(err)
	}

	prn, _ := env.NewPrincipal("reader")
	reader := op
	reader.PrincipalID = prn
	reader.CredentialID = ""
	reader.PeerID = ""
	if _, err := env.Hub.OnboardingStatus(ctx, reader, req.RequestID); !onboardStatus(t, err, 403, "forbidden") {
		t.Fatal(err)
	}

	enrolledPeer, secret, err := env.Hub.Auth.Enrol(ctx, token)
	if err != nil || enrolledPeer != first.PeerID || !strings.HasPrefix(secret, "vn_peer_") {
		t.Fatal("enrolment token from the first submit did not enrol")
	}
	assertSecretAbsent(t, env, secret)
	status, err = env.Hub.OnboardingStatus(ctx, op, req.RequestID)
	if err != nil || status.AuthState != onboarding.AuthVerified || status.HumanOutput != "" {
		t.Fatalf("status after enrol = %+v %v", status, err)
	}

	dot := mustOnboard(t, env, op, onboardReq("dot", "dot-retry", map[string]any{"name": "Dot Retry"}))
	if !linkCodeRE.MatchString(dot.HumanOutput) {
		t.Fatal("dot submit did not return a link code")
	}
	dotReplay := mustOnboard(t, env, op, onboardReq("dot", "dot-retry", map[string]any{"name": "Dot Retry"}))
	if dotReplay.HumanOutput != "" || dotReplay.PeerID != dot.PeerID || strings.Contains(dotReplay.HumanOutput, dot.HumanOutput) {
		t.Fatal("dot replay returned the link code or another peer")
	}
	dotStatus, err := env.Hub.OnboardingStatus(ctx, op, "dot-retry")
	if err != nil || dotStatus.HumanOutput != "" || dotStatus.AuthState != onboarding.AuthPending {
		t.Fatalf("dot status = %+v %v", dotStatus, err)
	}
	assertSecretAbsent(t, env, dot.HumanOutput)
	assertSecretAbsent(t, env, strings.ReplaceAll(dot.HumanOutput, "-", ""))
	directDot, _, _, err := env.Hub.AddPeerWith(ctx, hub.AddPeerOpts{Kind: "dot", Name: "Direct Dot"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := env.Hub.DotLink(ctx, directDot)
	if err != nil || !linkCodeRE.MatchString(code) {
		t.Fatalf("direct DotLink = %q %v", code, err)
	}
	if strings.Contains(strings.Join(onboardValues(t, env), "\n"), code) {
		t.Fatal("direct DotLink wrote the code into a receipt")
	}

	var wg sync.WaitGroup
	out := make([]onboarding.SubmitResult, 2)
	errs := make([]error, 2)
	race := onboardReq("launcher", "race-1", map[string]any{"name": "Race"})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i], errs[i] = env.Hub.OnboardAgent(ctx, op, race)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("concurrent submit: %v %v", errs[0], errs[1])
	}
	if out[0].PeerID == "" || out[0].PeerID != out[1].PeerID {
		t.Fatalf("concurrent peers = %s %s", out[0].PeerID, out[1].PeerID)
	}
	humans := 0
	if out[0].HumanOutput != "" {
		humans++
	}
	if out[1].HumanOutput != "" {
		humans++
	}
	if humans != 1 {
		t.Fatalf("concurrent human outputs = %d", humans)
	}
	if countRows(t, env, `SELECT count(*) FROM peers WHERE name=?`, "Race") != 1 {
		t.Fatal("concurrent submit created two peers")
	}

	env.Now.Advance(25 * time.Hour)
	staleAt := envelope.FormatTime(env.Now.Now().Add(-25 * time.Hour))
	if _, err := env.Hub.DB.Exec(`INSERT INTO idempotency(sender, key, body_sha256, response, created_at) VALUES (?, 'stale-send', 'abc', '{}', ?)`, op.PeerID, staleAt); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"idempotency_key": "stale-send", "priority": "hold", "recipients": []string{first.PeerID},
		"content": map[string]any{"text": "gc probe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.Hub.Send(ctx, op, body, "http_send"); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := env.Hub.DB.QueryRow(`SELECT created_at FROM idempotency WHERE sender=? AND key='stale-send'`, op.PeerID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created == staleAt {
		t.Fatal("send idempotency did not replace the expired row")
	}
	if !strings.Contains(strings.Join(onboardKeys(t, env), "\n"), req.RequestID) {
		t.Fatal("send idempotency GC removed the onboarding receipt")
	}
	aged, err := env.Hub.OnboardingStatus(ctx, op, req.RequestID)
	if err != nil || aged.PeerID != first.PeerID || aged.HumanOutput != "" {
		t.Fatalf("status after send GC = %+v %v", aged, err)
	}

	clone := env.CloneDB(t)
	cloned, err := clone.Hub.OnboardingStatus(ctx, op, req.RequestID)
	if err != nil || cloned.PeerID != first.PeerID || cloned.HumanOutput != "" || cloned.AuthState != onboarding.AuthVerified {
		t.Fatalf("status after reopen = %+v %v", cloned, err)
	}
	cloneRaw := onboardValues(t, clone)
	found := false
	for _, value := range cloneRaw {
		if strings.Contains(value, digest) && !strings.Contains(value, token) {
			found = true
		}
	}
	if !found {
		t.Fatal("reopened store lost the keyed digest")
	}

	short := mustOnboard(t, env, op, onboardReq("session", "expires", map[string]any{"name": "Expires", "enrol_ttl_seconds": 60}))
	env.Now.Advance(2 * time.Minute)
	expired, err := env.Hub.OnboardingStatus(ctx, op, "expires")
	if err != nil || expired.PeerID != short.PeerID || expired.AuthState != onboarding.AuthUnlinked {
		t.Fatalf("status after token expiry = %+v %v", expired, err)
	}

	sent, _, err := env.Hub.Send(ctx, op, mustJSON(t, map[string]any{
		"idempotency_key": "setup-probe", "priority": "hold", "recipients": []string{first.PeerID},
		"content": map[string]any{"text": "setup probe"},
	}), "http_send")
	if err != nil || len(sent.Deliveries) != 1 {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(onboardValue(t, env, keys[0])), &record); err != nil {
		t.Fatal(err)
	}
	record["thread_id"] = sent.ThreadID
	record["message_id"] = sent.MessageID
	record["delivery_id"] = sent.Deliveries[0].DeliveryID
	encodedRec, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE meta SET value=? WHERE key=?`, string(encodedRec), keys[0]); err != nil {
		t.Fatal(err)
	}
	withSetup, err := env.Hub.OnboardingStatus(ctx, op, req.RequestID)
	if err != nil || withSetup.Setup == nil || withSetup.Setup.DeliveryID != sent.Deliveries[0].DeliveryID || withSetup.Setup.State != onboarding.SetupHeld {
		t.Fatalf("derived setup = %+v %v", withSetup.Setup, err)
	}
	if _, err := env.Hub.DB.Exec(`UPDATE deliveries SET state='processed', held=0 WHERE dly_id=?`, sent.Deliveries[0].DeliveryID); err != nil {
		t.Fatal(err)
	}
	processed, err := env.Hub.OnboardingStatus(ctx, op, req.RequestID)
	if err != nil || processed.Setup == nil || processed.Setup.State != onboarding.SetupProcessed {
		t.Fatalf("processed setup = %+v %v", processed.Setup, err)
	}

	sum := sha256.Sum256([]byte(raw[0]))
	if strings.Contains(raw[0], hex.EncodeToString(sum[:])) {
		t.Fatal("receipt stores an unkeyed fingerprint of itself")
	}
	mac := hmac.New(sha256.New, []byte("not-the-hub-key"))
	mac.Write([]byte(req.RequestID))
	if strings.Contains(raw[0], hex.EncodeToString(mac.Sum(nil))) {
		t.Fatal("receipt stores a digest under an unrelated key")
	}
}

func onboardValue(t *testing.T, env *testhub.Env, key string) string {
	t.Helper()
	var value string
	if err := env.Hub.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
