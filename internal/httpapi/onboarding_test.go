package httpapi_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Task 4 pins. Decisions §2 and Review Focus of
// docs/plans/2026-11-06-agent-onboarding-kinds.md. PROTOCOL §§2.12–2.13,
// 4.5, 12, 14 and 20.1: operator admin only, no-store, and status/replay
// never carry a routine URL, key, ciphertext or legacy human output.

var (
	enrolTokenRE = regexp.MustCompile(`vn_enrol_[A-Za-z0-9_-]+`)
	linkCodeRE   = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}$`)
)

func TestAgentOnboardAuthorization(t *testing.T) {
	env := testhub.Start(t)
	_, peer := env.NewPeer("session", "ordinary peer")
	_, principal := env.NewPrincipal("ordinary principal")
	before := takeOnboardCensus(t, env)

	submit := agentBody("session", "blocked-submit", map[string]any{"name": "Blocked"})
	for _, c := range []struct {
		name, secret, method, path string
		body                       any
		status                     int
		code                       string
	}{
		{"no credential kinds", "", http.MethodGet, "/v1/admin/agent-kinds", nil, http.StatusUnauthorized, "unauthenticated"},
		{"no credential submit", "", http.MethodPost, "/v1/admin/agents", submit, http.StatusUnauthorized, "unauthenticated"},
		{"no credential status", "", http.MethodGet, onboardingStatusPath("blocked-submit"), nil, http.StatusUnauthorized, "unauthenticated"},
		{"unknown credential kinds", "vn_peer_not-a-real-secret", http.MethodGet, "/v1/admin/agent-kinds", nil, http.StatusUnauthorized, "unauthenticated"},
		{"unknown credential submit", "vn_peer_not-a-real-secret", http.MethodPost, "/v1/admin/agents", submit, http.StatusUnauthorized, "unauthenticated"},
		{"unknown credential status", "vn_peer_not-a-real-secret", http.MethodGet, onboardingStatusPath("blocked-submit"), nil, http.StatusUnauthorized, "unauthenticated"},
		{"peer kinds", peer, http.MethodGet, "/v1/admin/agent-kinds", nil, http.StatusForbidden, "forbidden"},
		{"peer submit", peer, http.MethodPost, "/v1/admin/agents", submit, http.StatusForbidden, "forbidden"},
		{"peer status", peer, http.MethodGet, onboardingStatusPath("blocked-submit"), nil, http.StatusForbidden, "forbidden"},
		{"principal kinds", principal, http.MethodGet, "/v1/admin/agent-kinds", nil, http.StatusForbidden, "forbidden"},
		{"principal submit", principal, http.MethodPost, "/v1/admin/agents", submit, http.StatusForbidden, "forbidden"},
		{"principal status", principal, http.MethodGet, onboardingStatusPath("blocked-submit"), nil, http.StatusForbidden, "forbidden"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, raw := env.Do(c.secret, c.method, c.path, c.body)
			mustErr(t, resp, raw, c.status, c.code)
		})
	}
	if takeOnboardCensus(t, env) != before {
		t.Fatal("an unauthorized call wrote onboarding state")
	}

	t.Run("operator list", func(t *testing.T) {
		resp, raw := env.Do(env.OperatorSecret, http.MethodGet, "/v1/admin/agent-kinds", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		assertKindList(t, raw)
		if takeOnboardCensus(t, env) != before {
			t.Fatal("listing wrote onboarding state")
		}
	})

	t.Run("listing never grants write", func(t *testing.T) {
		resp, raw := env.Do(peer, http.MethodPost, "/v1/admin/agents", agentBody("session", "after-list", map[string]any{"name": "After List"}))
		mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
		resp, raw = env.Do(principal, http.MethodPost, "/v1/admin/agents", agentBody("grokbot", "after-list-prn", map[string]any{"name": "Nope"}))
		mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
		if takeOnboardCensus(t, env) != before {
			t.Fatal("a peer or principal wrote onboarding state after the operator listed kinds")
		}
	})

	t.Run("reject before side effects", func(t *testing.T) {
		for _, c := range []struct {
			name, field, path string
			body              any
			status            int
			code              string
		}{
			{name: "stale contract", body: map[string]any{"kind": "session", "spec_version": "1999-01-01", "request_id": "stale-1", "inputs": map[string]any{"name": "Nope"}}, status: http.StatusConflict, code: "stale_spec"},
			{name: "unsupported kind", body: agentBody("operator", "kind-op", map[string]any{"name": "Nope"}), status: http.StatusBadRequest, code: "unknown_kind", field: "kind"},
			{name: "infrastructure kind", body: agentBody("source", "kind-src", map[string]any{"name": "Nope"}), status: http.StatusBadRequest, code: "unknown_kind", field: "kind"},
			{name: "unknown kind", body: agentBody("not-a-kind", "kind-nope", map[string]any{"name": "Nope"}), status: http.StatusBadRequest, code: "unknown_kind", field: "kind"},
			{name: "client selected action", body: map[string]any{"kind": "session", "spec_version": onboarding.SpecVersion, "request_id": "act-1", "submit_action": "grokbot_setup", "inputs": map[string]any{"name": "Nope"}}, status: http.StatusBadRequest, code: "invalid_body", path: "/submit_action"},
			{name: "unknown input", body: agentBody("session", "unk-1", map[string]any{"name": "Nope", "submit_action": "drop"}), status: http.StatusBadRequest, code: "unknown_field", field: "submit_action"},
			{name: "bad shape", body: `{"kind":`, status: http.StatusBadRequest, code: "invalid_body"},
			{name: "kind type", body: `{"kind":1,"spec_version":"` + onboarding.SpecVersion + `","request_id":"bad-type","inputs":{}}`, status: http.StatusBadRequest, code: "invalid_body", path: "/kind"},
			{name: "inputs type", body: `{"kind":"session","spec_version":"` + onboarding.SpecVersion + `","request_id":"bad-inputs","inputs":[]}`, status: http.StatusBadRequest, code: "invalid_body", path: "/inputs"},
		} {
			t.Run(c.name, func(t *testing.T) {
				resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", c.body)
				e := mustErr(t, resp, raw, c.status, c.code)
				requireNoStore(t, resp)
				if c.field != "" && e.Error.Details["field"] != c.field {
					t.Fatalf("details.field = %v, want %s", e.Error.Details["field"], c.field)
				}
				if c.path != "" && e.Error.Details["path"] != c.path {
					t.Fatalf("details.path = %v, want %s", e.Error.Details["path"], c.path)
				}
				if strings.Contains(string(raw), "Nope") || strings.Contains(string(raw), "drop") || strings.Contains(string(raw), "grokbot_setup") {
					t.Fatal("rejection echoed the submitted value or a client-selected action")
				}
				if takeOnboardCensus(t, env) != before {
					t.Fatal("a rejected submit wrote onboarding state")
				}
			})
		}
	})

	t.Run("only operator reaches submit and status", func(t *testing.T) {
		resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", agentBody("session", "op-only", map[string]any{"name": "Operator Only"}))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}
		requireNoStore(t, resp)
		created := decodeOnboardResult(t, raw)
		if created.Status != "created" || created.PeerID == "" || created.RequestID != "op-only" || created.HumanOutput == "" {
			t.Fatal("operator submit did not return a created peer and human output")
		}
		token := enrolTokenRE.FindString(created.HumanOutput)
		if token == "" {
			t.Fatal("operator submit human output has no enrolment token")
		}
		after := takeOnboardCensus(t, env)
		if after.peers != before.peers+1 || after.receipts != before.receipts+1 || after.routes != before.routes {
			t.Fatal("operator submit did not record exactly one peer and one receipt")
		}
		for _, c := range []struct {
			name, secret string
			status       int
			code         string
		}{
			{"peer", peer, http.StatusForbidden, "forbidden"},
			{"principal", principal, http.StatusForbidden, "forbidden"},
			{"no credential", "", http.StatusUnauthorized, "unauthenticated"},
		} {
			t.Run(c.name, func(t *testing.T) {
				resp, raw := env.Do(c.secret, http.MethodGet, onboardingStatusPath("op-only"), nil)
				mustErr(t, resp, raw, c.status, c.code)
				if strings.Contains(string(raw), created.PeerID) || strings.Contains(string(raw), token) || strings.Contains(string(raw), "human_output") {
					t.Fatal("status reached a non-operator")
				}
			})
		}
		resp, raw = env.Do(env.OperatorSecret, http.MethodGet, onboardingStatusPath("op-only"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("operator status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		status := decodeOnboardResult(t, raw)
		if status.PeerID != created.PeerID || status.RequestID != "op-only" || strings.Contains(string(raw), "human_output") || strings.Contains(string(raw), token) {
			t.Fatal("operator status leaked human output or returned another peer")
		}
		resp, raw = env.Do(env.OperatorSecret, http.MethodGet, onboardingStatusPath("missing-request"), nil)
		e := mustErr(t, resp, raw, http.StatusNotFound, "not_found")
		requireNoStore(t, resp)
		if e.Error.Details["request_id"] != "missing-request" {
			t.Fatal("operator status of an unknown id did not reach the onboarding service")
		}
		resp, raw = env.Do(peer, http.MethodPost, "/v1/admin/agents", agentBody("session", "peer-write", map[string]any{"name": "Peer Write"}))
		mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
		resp, raw = env.Do(env.OperatorSecret, http.MethodGet, onboardingStatusPath("bad id"), nil)
		e = mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
		if e.Error.Details["field"] != "request_id" {
			t.Fatalf("details.field = %v, want request_id", e.Error.Details["field"])
		}
		requireNoStore(t, resp)
		if takeOnboardCensus(t, env) != after {
			t.Fatal("a refused status or peer submit wrote onboarding state")
		}
	})
}

func TestAgentOnboardHTTPRedaction(t *testing.T) {
	env := testhub.Start(t)
	var kicks atomic.Int32
	env.Configure(func(d *httpapi.Deps) { d.Kick = func() { kicks.Add(1) } })

	t.Run("legacy invite", func(t *testing.T) {
		body := agentBody("session", "legacy-1", map[string]any{"name": "Legacy One"})
		resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("fresh status = %d, want 201", resp.StatusCode)
		}
		requireNoStore(t, resp)
		created := decodeOnboardResult(t, raw)
		if created.Status != "created" || created.Kind != "session" || created.PeerID == "" || created.HumanOutput == "" {
			t.Fatal("fresh legacy response is missing the created peer or human output")
		}
		token := enrolTokenRE.FindString(created.HumanOutput)
		if token == "" {
			t.Fatal("fresh legacy human output has no enrolment token")
		}
		if kicks.Load() != 0 {
			t.Fatal("a legacy invite kicked the push worker")
		}

		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replay status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		replay := decodeOnboardResult(t, raw)
		if replay.Status != "replayed" || replay.PeerID != created.PeerID || replay.PrincipalID != created.PrincipalID {
			t.Fatal("replay did not return the original peer")
		}
		assertNoHuman(t, raw, created.HumanOutput, token)
		if kicks.Load() != 0 {
			t.Fatal("a replay kicked the push worker")
		}

		resp, raw = env.Do(env.OperatorSecret, http.MethodGet, onboardingStatusPath("legacy-1"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		got := decodeOnboardResult(t, raw)
		if got.PeerID != created.PeerID || got.Kind != "session" || got.AuthState != onboarding.AuthPending {
			t.Fatal("status did not return the safe legacy receipt")
		}
		assertNoHuman(t, raw, created.HumanOutput, token)

		changed := agentBody("session", "legacy-1", map[string]any{"name": "Changed Legacy Name"})
		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", changed)
		e := mustErr(t, resp, raw, http.StatusConflict, "idempotency_conflict")
		requireNoStore(t, resp)
		if e.Error.Details["request_id"] != "legacy-1" {
			t.Fatalf("details.request_id = %v", e.Error.Details["request_id"])
		}
		assertNoHuman(t, raw, created.HumanOutput, token)
		if strings.Contains(string(raw), "Changed Legacy Name") {
			t.Fatal("conflict echoed the changed name")
		}
		if kicks.Load() != 0 {
			t.Fatal("a conflict kicked the push worker")
		}
	})

	t.Run("dot link", func(t *testing.T) {
		body := agentBody("dot", "dot-1", map[string]any{"name": "Dot One"})
		resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("fresh status = %d, want 201", resp.StatusCode)
		}
		requireNoStore(t, resp)
		created := decodeOnboardResult(t, raw)
		if created.Status != "created" || !linkCodeRE.MatchString(created.HumanOutput) {
			t.Fatal("fresh dot response did not return the one-time link code")
		}
		code := created.HumanOutput
		if kicks.Load() != 0 {
			t.Fatal("a dot link kicked the push worker")
		}

		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replay status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		replay := decodeOnboardResult(t, raw)
		if replay.Status != "replayed" || replay.PeerID != created.PeerID {
			t.Fatal("dot replay did not return the original peer")
		}
		assertNoHuman(t, raw, code, strings.ReplaceAll(code, "-", ""))

		resp, raw = env.Do(env.OperatorSecret, http.MethodGet, onboardingStatusPath("dot-1"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		got := decodeOnboardResult(t, raw)
		if got.PeerID != created.PeerID || got.AuthState != onboarding.AuthPending {
			t.Fatal("dot status did not return the safe receipt")
		}
		assertNoHuman(t, raw, code, strings.ReplaceAll(code, "-", ""))
		if kicks.Load() != 0 {
			t.Fatal("dot status kicked the push worker")
		}
	})

	t.Run("grok setup", func(t *testing.T) {
		key := fakeRoutineKey(t)
		routineURL := fakeRoutineURL(t)
		pasted := "Bearer " + key
		body := agentBody("grokbot", "grok-1", map[string]any{"name": "Helper Bot", "webhook_url": routineURL, "webhook_key": pasted})
		beforeKicks := kicks.Load()
		resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("fresh status = %d, want 201", resp.StatusCode)
		}
		requireNoStore(t, resp)
		created := decodeOnboardResult(t, raw)
		if created.Status != "created" || created.PeerID == "" || created.RouteID == "" || created.Setup == nil || created.ConnectorName == "" || created.AuthState != onboarding.AuthUnlinked {
			t.Fatal("fresh grok response is missing the peer, route or setup")
		}
		if created.HumanOutput != "" || strings.Contains(string(raw), "human_output") {
			t.Fatal("fresh grok response carried human output")
		}
		sealed := routeCiphertext(t, env, created.RouteID)
		assertAbsent(t, raw, "fresh grok response", routineURL, key, pasted, sealed)
		if kicks.Load() != beforeKicks+1 {
			t.Fatalf("kicks = %d, want %d after a new setup", kicks.Load(), beforeKicks+1)
		}

		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replay status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		replay := decodeOnboardResult(t, raw)
		if replay.Status != "replayed" || replay.PeerID != created.PeerID || replay.RouteID != created.RouteID || replay.Setup == nil || replay.Setup.DeliveryID != created.Setup.DeliveryID {
			t.Fatal("grok replay did not return the original setup")
		}
		assertAbsent(t, raw, "grok replay", routineURL, key, pasted, sealed)
		assertNoHuman(t, raw, pasted)
		if kicks.Load() != beforeKicks+1 {
			t.Fatal("a grok replay kicked the push worker again")
		}

		resp, raw = env.Do(env.OperatorSecret, http.MethodGet, onboardingStatusPath("grok-1"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		requireNoStore(t, resp)
		got := decodeOnboardResult(t, raw)
		if got.PeerID != created.PeerID || got.RouteID != created.RouteID || got.Setup == nil || got.Setup.MessageID != created.Setup.MessageID || got.AuthState != onboarding.AuthUnlinked {
			t.Fatal("grok status did not return the safe setup receipt")
		}
		assertAbsent(t, raw, "grok status", routineURL, key, pasted, sealed)
		assertNoHuman(t, raw, pasted)
		if kicks.Load() != beforeKicks+1 {
			t.Fatal("grok status kicked the push worker")
		}

		otherKey := fakeRoutineKey(t)
		otherURL := fakeRoutineURL(t)
		changed := agentBody("grokbot", "grok-1", map[string]any{"name": "Helper Bot", "webhook_url": otherURL, "webhook_key": "Bearer " + otherKey})
		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", changed)
		e := mustErr(t, resp, raw, http.StatusConflict, "idempotency_conflict")
		requireNoStore(t, resp)
		if e.Error.Details["request_id"] != "grok-1" {
			t.Fatalf("details.request_id = %v", e.Error.Details["request_id"])
		}
		assertAbsent(t, raw, "grok conflict", routineURL, key, pasted, sealed, otherURL, otherKey, "Bearer "+otherKey)
		if kicks.Load() != beforeKicks+1 {
			t.Fatal("a grok conflict kicked the push worker")
		}
	})

	t.Run("field errors", func(t *testing.T) {
		before := takeOnboardCensus(t, env)
		beforeKicks := kicks.Load()
		key := fakeRoutineKey(t)
		routineURL := fakeRoutineURL(t)
		leakedURL := routineURL + "?leak=1"
		resp, raw := env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", agentBody("grokbot", "grok-bad-url", map[string]any{
			"name": "Bad URL", "webhook_url": leakedURL, "webhook_key": "Bearer " + key,
		}))
		e := mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
		requireNoStore(t, resp)
		if e.Error.Details["field"] != "webhook_url" {
			t.Fatalf("details.field = %v, want webhook_url", e.Error.Details["field"])
		}
		assertAbsent(t, raw, "url field error", leakedURL, routineURL[strings.LastIndex(routineURL, "/")+1:], key, "Bearer "+key)
		if strings.Contains(e.Error.Message, key) || strings.Contains(e.Error.Message, "leak=1") {
			t.Fatal("field error message included submitted bytes")
		}

		badKey := "Bearer Bearer " + key
		validURL := fakeRoutineURL(t)
		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", agentBody("grokbot", "grok-bad-key", map[string]any{
			"name": "Bad Key", "webhook_url": validURL, "webhook_key": badKey,
		}))
		e = mustErr(t, resp, raw, http.StatusUnprocessableEntity, "invalid_value")
		requireNoStore(t, resp)
		if e.Error.Details["field"] != "webhook_key" {
			t.Fatalf("details.field = %v, want webhook_key", e.Error.Details["field"])
		}
		if e.Error.Message != "a routine key is printable ASCII starting crsr_" {
			t.Fatal("webhook_key error text is not the fixed message")
		}
		assertAbsent(t, raw, "key field error", badKey, key, validURL)

		resp, raw = env.Do(env.OperatorSecret, http.MethodPost, "/v1/admin/agents", agentBody("session", "session-secret-field", map[string]any{
			"name": "Session", "webhook_key": key,
		}))
		e = mustErr(t, resp, raw, http.StatusBadRequest, "unknown_field")
		requireNoStore(t, resp)
		if e.Error.Details["field"] != "webhook_key" {
			t.Fatalf("details.field = %v, want webhook_key", e.Error.Details["field"])
		}
		assertAbsent(t, raw, "unknown field error", key)
		if takeOnboardCensus(t, env) != before {
			t.Fatal("a field error wrote onboarding state")
		}
		if kicks.Load() != beforeKicks {
			t.Fatal("a field error kicked the push worker")
		}
	})
}

type onboardCensus struct {
	peers, routes, receipts, messages int
}

func takeOnboardCensus(t *testing.T, env *testhub.Env) onboardCensus {
	t.Helper()
	q := func(sql string) int {
		t.Helper()
		var n int
		if err := env.Hub.DB.QueryRow(sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return onboardCensus{
		peers:    q(`SELECT count(*) FROM peers`),
		routes:   q(`SELECT count(*) FROM routes`),
		receipts: q(`SELECT count(*) FROM meta WHERE key LIKE 'onboarding.v1/%'`),
		messages: q(`SELECT count(*) FROM messages`),
	}
}

func agentBody(kind, id string, inputs map[string]any) map[string]any {
	return map[string]any{
		"kind":         kind,
		"spec_version": onboarding.SpecVersion,
		"request_id":   id,
		"inputs":       inputs,
	}
}

func onboardingStatusPath(id string) string {
	return "/v1/admin/agents/onboarding/" + url.PathEscape(id)
}

func requireNoStore(t *testing.T, resp *http.Response) {
	t.Helper()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func assertKindList(t *testing.T, raw []byte) {
	t.Helper()
	var got struct {
		Kinds []onboarding.KindSpec `json:"kinds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal("kind list is not JSON")
	}
	want, err := json.Marshal(onboarding.Specs())
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.Marshal(got.Kinds)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(gotRaw) {
		t.Fatal("kind list is not the hub registry")
	}
	allowed := map[string]bool{onboarding.ActionInvite: true, onboarding.ActionOAuthLink: true, onboarding.ActionGrokbotSetup: true}
	for _, spec := range got.Kinds {
		if spec.Kind == "operator" || spec.Kind == "source" {
			t.Fatalf("listing includes infrastructure kind %s", spec.Kind)
		}
		if spec.SpecVersion != onboarding.SpecVersion || spec.PromptTemplate == "" || !allowed[spec.SubmitAction] {
			t.Fatalf("kind %s is missing its contract", spec.Kind)
		}
	}
}

func decodeOnboardResult(t *testing.T, raw []byte) onboarding.SubmitResult {
	t.Helper()
	var res onboarding.SubmitResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal("response is not an onboarding result")
	}
	return res
}

func assertNoHuman(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	if strings.Contains(string(raw), "human_output") {
		t.Fatal("response contains human_output")
	}
	assertAbsent(t, raw, "response", secrets...)
}

func assertAbsent(t *testing.T, raw []byte, where string, secrets ...string) {
	t.Helper()
	text := string(raw)
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		if strings.Contains(text, secret) {
			t.Fatalf("%s contains a secret", where)
		}
	}
}

func routeCiphertext(t *testing.T, env *testhub.Env, routeID string) string {
	t.Helper()
	var sealed string
	if err := env.Hub.DB.QueryRow(`SELECT coalesce(key_enc,'') FROM routes WHERE route_id=?`, routeID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatal("route key is not sealed")
	}
	return sealed
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
