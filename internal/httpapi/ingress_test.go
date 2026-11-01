package httpapi_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

type addSourceResp struct {
	SourceID   string `json:"source_id"`
	IngressURL string `json:"ingress_url"`
	Secret     string `json:"secret"`
}

// addSource is POST /v1/admin/sources expecting 201, routed to target.
func addSource(t *testing.T, env *testhub.Env, target string, extra map[string]any) addSourceResp {
	t.Helper()
	body := map[string]any{"kind": "github", "name": "o/r hook", "target": target}
	for k, v := range extra {
		body[k] = v
	}
	raw := adminDo(t, env, "POST", "/v1/admin/sources", body, http.StatusCreated)
	var r addSourceResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v; %s", err, raw)
	}
	if !strings.HasPrefix(r.SourceID, "src_") || len(r.Secret) != 64 {
		t.Fatalf("POST /v1/admin/sources body %s, want source_id and a 64-hex secret", raw)
	}
	if r.IngressURL != env.URL+"/v1/ingress/"+r.SourceID {
		t.Fatalf("ingress_url = %q", r.IngressURL)
	}
	return r
}

func addTeam(t *testing.T, env *testhub.Env, members ...string) string {
	t.Helper()
	raw := adminDo(t, env, "POST", "/v1/admin/teams", map[string]any{"name": "T", "members": members}, http.StatusCreated)
	var r struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r.TeamID
}

func ghFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../ingress/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// commentOn returns the issue_comment fixture re-numbered to issue n.
func commentOn(t *testing.T, n int) []byte {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(ghFixture(t, "issue_comment.created.json"), &v); err != nil {
		t.Fatal(err)
	}
	v["issue"].(map[string]any)["number"] = n
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ghSign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// ghPost POSTs body to the source with the given headers; a header whose
// value is "" is not sent. r overrides body as the request reader when set.
func ghPost(t *testing.T, env *testhub.Env, src string, hdr map[string]string, body []byte, r io.Reader) (*http.Response, []byte) {
	t.Helper()
	if r == nil {
		r = strings.NewReader(string(body))
	}
	req, err := http.NewRequest("POST", env.URL+"/v1/ingress/"+src, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GitHub-Hookshot/abc123")
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, out
}

func ghHeaders(secret, event, delivery string, body []byte) map[string]string {
	return map[string]string{
		"X-Hub-Signature-256": ghSign(secret, body),
		"X-GitHub-Event":      event,
		"X-GitHub-Delivery":   delivery,
	}
}

// ghDeliver is a signed POST; it requires 202 and returns the message_id.
func ghDeliver(t *testing.T, env *testhub.Env, s addSourceResp, event, delivery string, body []byte) string {
	t.Helper()
	resp, raw := ghPost(t, env, s.SourceID, ghHeaders(s.Secret, event, delivery, body), body, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /v1/ingress/%s = %d; %s", s.SourceID, resp.StatusCode, raw)
	}
	var r struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || !strings.HasPrefix(r.MessageID, "msg_") {
		t.Fatalf("ingress body %s, want message_id (%v)", raw, err)
	}
	return r.MessageID
}

// lastDelivery returns the newest delivery Event in peer's log.
func lastDelivery(t *testing.T, env *testhub.Env, secret string) (d struct {
	Via     *string                `json:"via"`
	Ingress string                 `json:"ingress"`
	Thread  envelope.ThreadSummary `json:"thread"`
	Message envelope.Message       `json:"message"`
}) {
	t.Helper()
	pr := mustPoll(t, env, secret, "?after="+zeroCursor(env)+"&wait=0")
	for i := len(pr.Events) - 1; i >= 0; i-- {
		if pr.Events[i].Type != "delivery" {
			continue
		}
		raw := pr.Events[i].Data
		if err := envelope.Validate("delivery", raw); err != nil {
			t.Fatalf("delivery is not schema-valid: %v; %s", err, raw)
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("decode delivery: %v; %s", err, raw)
		}
		return d
	}
	t.Fatalf("no delivery in the log: %+v", pr.Events)
	return d
}

func TestIngressHappyPath(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	team := addTeam(t, env, a, b)
	s := addSource(t, env, team, nil)

	body := ghFixture(t, "issue_comment.created.json")
	const delivery = "d3b07384-6f1c-4a9e-9b2a-8c1f0e7a5b21"
	msgID := ghDeliver(t, env, s, "issue_comment", delivery, body)

	for _, sec := range []string{aSec, bSec} {
		d := lastDelivery(t, env, sec)
		m := d.Message
		if m.MessageID != msgID {
			t.Fatalf("delivery message_id = %s, want %s", m.MessageID, msgID)
		}
		if m.Sender != s.SourceID {
			t.Fatalf("sender = %q, want %q", m.Sender, s.SourceID)
		}
		if d.Ingress != "inbound_webhook" {
			t.Fatalf("ingress = %q", d.Ingress)
		}
		if d.Via == nil || *d.Via != team {
			t.Fatalf("via = %v, want %s", d.Via, team)
		}
		if m.Origin == nil || m.Origin.System != "github" || m.Origin.ThreadID != "o/r#196" || m.Origin.EventID != delivery {
			t.Fatalf("origin = %+v", m.Origin)
		}
		if m.Metadata["vecnet.source_kind"] != "github" || m.Metadata["vecnet.event"] != "issue_comment.created" {
			t.Fatalf("metadata = %v", m.Metadata)
		}
		if m.Priority != "wake_idle" {
			t.Fatalf("priority = %q", m.Priority)
		}
		wantText := `[o/r] issue_comment.created on #196 by alice: "radio listen does not finalize on 'over'" https://github.com/o/r/issues/196#issuecomment-2450117799`
		if m.Content.Text != wantText {
			t.Fatalf("text = %q", m.Content.Text)
		}
		if d.Thread.Title == nil || *d.Thread.Title != "o/r#196: radio listen does not finalize on 'over'" {
			t.Fatalf("thread title = %v", d.Thread.Title)
		}
		if m.Loop.Depth != 0 || !slices.Equal(m.Loop.SenderChain, []string{s.SourceID}) {
			t.Fatalf("loop = %+v", m.Loop)
		}
		if !slices.Equal(sorted(d.Thread.Participants...), sorted(a, b)) {
			t.Fatalf("participants = %v, want the team's members and never the source", d.Thread.Participants)
		}

		if len(m.Attachments) != 1 {
			t.Fatalf("attachments = %+v", m.Attachments)
		}
		att := m.Attachments[0]
		if att.Name != "github-event.json" || att.MediaType != "application/json" || att.Size != int64(len(body)) {
			t.Fatalf("attachment = %+v", att)
		}
		resp, err := http.Get(att.BlobURL)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || string(got) != string(body) {
			t.Fatalf("GET blob_url = %d, %v; body equal %v", resp.StatusCode, err, string(got) == string(body))
		}
	}
	if n := rowCount(t, env, `SELECT count(*) FROM deliveries WHERE recipient = ?`, s.SourceID); n != 0 {
		t.Fatalf("the source received %d deliveries", n)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM events WHERE owner = ?`, s.SourceID); n != 0 {
		t.Fatalf("the source has %d log events", n)
	}
}

// A second event on an issue whose latest message has expired is still a
// fresh message: a source never inherits a thread's loop (PROTOCOL §10).
func TestIngressEventAfterThreadTTL(t *testing.T) {
	env := testhub.Start(t)
	p, pSec := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)
	body := ghFixture(t, "issue_comment.created.json")
	ghDeliver(t, env, s, "issue_comment", "d-1", body)

	first := lastDelivery(t, env, pSec)
	env.Now.Advance(25 * time.Hour) // past the thread's 24 h TTL

	ghDeliver(t, env, s, "issue_comment", "d-2", body)
	d := lastDelivery(t, env, pSec)
	if d.Message.ThreadID != first.Message.ThreadID {
		t.Fatalf("second event landed in %s, want %s", d.Message.ThreadID, first.Message.ThreadID)
	}
	if d.Message.Loop.Depth != 0 || !slices.Equal(d.Message.Loop.SenderChain, []string{s.SourceID}) {
		t.Fatalf("loop = %+v, want depth 0 and chain [source]", d.Message.Loop)
	}
}

func TestIngressDedupeAndOversize(t *testing.T) {
	env := testhub.Start(t)
	p, _ := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)

	body := ghFixture(t, "issue_comment.created.json")
	first := ghDeliver(t, env, s, "issue_comment", "same-delivery", body)
	second := ghDeliver(t, env, s, "issue_comment", "same-delivery", body)
	if first != second {
		t.Fatalf("redelivery message_id = %s, want %s", second, first)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM messages WHERE sender = ?`, s.SourceID); n != 1 {
		t.Fatalf("%d message rows, want 1", n)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM deliveries WHERE msg_id = ?`, first); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}

	// Not JSON at all, and correctly signed: a 413 proves the cap ran first.
	huge := make([]byte, 300<<10)
	for i := range huge {
		huge[i] = 'x'
	}
	hdr := ghHeaders(s.Secret, "issue_comment", "huge-1", huge)
	resp, raw := ghPost(t, env, s.SourceID, hdr, huge, nil)
	mustErr(t, resp, raw, http.StatusRequestEntityTooLarge, "payload_too_large")

	// The same, chunked, so the cap cannot lean on Content-Length.
	hdr = ghHeaders(s.Secret, "issue_comment", "huge-2", huge)
	resp, raw = ghPost(t, env, s.SourceID, hdr, nil, io.MultiReader(strings.NewReader(string(huge))))
	if resp.Request.ContentLength > 0 {
		t.Fatalf("expected a chunked request, Content-Length %d", resp.Request.ContentLength)
	}
	mustErr(t, resp, raw, http.StatusRequestEntityTooLarge, "payload_too_large")

	if n := rowCount(t, env, `SELECT count(*) FROM messages WHERE sender = ?`, s.SourceID); n != 1 {
		t.Fatalf("oversize bodies wrote messages: %d rows", n)
	}
}

// Parallel redeliveries race past the pre-check; the send transaction still
// admits exactly one (Review Focus 4).
func TestIngressConcurrentRedelivery(t *testing.T) {
	env := testhub.Start(t)
	p, _ := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)
	body := ghFixture(t, "issue_comment.created.json")

	const n = 8
	got := make([]string, n)
	hdr := ghHeaders(s.Secret, "issue_comment", "race-1", body)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			req, err := http.NewRequest("POST", s.IngressURL, strings.NewReader(string(body)))
			if err != nil {
				t.Error(err)
				return
			}
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var r struct {
				MessageID string `json:"message_id"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || resp.StatusCode != http.StatusAccepted {
				t.Errorf("delivery = %d, %v", resp.StatusCode, err)
				return
			}
			got[i] = r.MessageID
		})
	}
	wg.Wait()
	for _, id := range got {
		if id == "" || id != got[0] {
			t.Fatalf("message ids = %v, want one id", got)
		}
	}
	if c := rowCount(t, env, `SELECT count(*) FROM messages WHERE sender = ?`, s.SourceID); c != 1 {
		t.Fatalf("%d message rows, want 1", c)
	}
}

func TestIngressSameIssueSameThread(t *testing.T) {
	env := testhub.Start(t)
	p, pSec := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)

	ghDeliver(t, env, s, "issue_comment", "c-1", commentOn(t, 196))
	first := lastDelivery(t, env, pSec)
	ghDeliver(t, env, s, "issue_comment", "c-2", commentOn(t, 196))
	second := lastDelivery(t, env, pSec)
	if second.Message.ThreadID != first.Message.ThreadID {
		t.Fatalf("second comment went to %s, want %s", second.Message.ThreadID, first.Message.ThreadID)
	}
	if second.Thread.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", second.Thread.MessageCount)
	}

	ghDeliver(t, env, s, "issue_comment", "c-3", commentOn(t, 197))
	third := lastDelivery(t, env, pSec)
	if third.Message.ThreadID == first.Message.ThreadID {
		t.Fatal("a comment on #197 landed in #196's thread")
	}
	if third.Message.Origin.ThreadID != "o/r#197" {
		t.Fatalf("origin = %+v", third.Message.Origin)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM source_threads WHERE src_id = ?`, s.SourceID); n != 2 {
		t.Fatalf("%d source_threads rows, want 2", n)
	}
}

func TestIngressFilterAndSignature(t *testing.T) {
	env := testhub.Start(t)
	p, _ := env.NewPeer("session", "P")
	s := addSource(t, env, p, map[string]any{"event_allow": []string{"pull_request"}})
	comment := ghFixture(t, "issue_comment.created.json")
	pr := ghFixture(t, "pull_request.opened.json")

	// Allowlist: dropped events are a 202, so GitHub does not redeliver them.
	resp, raw := ghPost(t, env, s.SourceID, ghHeaders(s.Secret, "issue_comment", "f-1", comment), comment, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("filtered event = %d; %s", resp.StatusCode, raw)
	}
	var dropped map[string]any
	if err := json.Unmarshal(raw, &dropped); err != nil {
		t.Fatal(err)
	}
	if dropped["dropped"] != true || dropped["reason"] != "event_filtered" || len(dropped) != 2 {
		t.Fatalf("filtered body = %s", raw)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM messages`); n != 0 {
		t.Fatalf("a filtered event wrote %d messages", n)
	}

	// An allowed event of the same source goes through.
	ghDeliver(t, env, s, "pull_request", "f-2", pr)

	// Bad signature.
	hdr := ghHeaders(s.Secret, "pull_request", "f-3", pr)
	hdr["X-Hub-Signature-256"] = ghSign("not-the-secret", pr)
	resp, raw = ghPost(t, env, s.SourceID, hdr, pr, nil)
	mustErr(t, resp, raw, http.StatusUnauthorized, "bad_signature")

	// A signature does not vouch for other bytes.
	hdr = ghHeaders(s.Secret, "pull_request", "f-4", pr)
	resp, raw = ghPost(t, env, s.SourceID, hdr, append(append([]byte{}, pr...), ' '), nil)
	mustErr(t, resp, raw, http.StatusUnauthorized, "bad_signature")

	// Each required header is required; the check runs before verification.
	for _, h := range []string{"X-Hub-Signature-256", "X-GitHub-Event", "X-GitHub-Delivery"} {
		hdr = ghHeaders(s.Secret, "pull_request", "f-5", pr)
		hdr[h] = ""
		resp, raw = ghPost(t, env, s.SourceID, hdr, pr, nil)
		mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")
	}

	// ping: 200, nothing created, whatever the allowlist says.
	before := rowCount(t, env, `SELECT count(*) FROM messages`)
	ping := []byte(`{"zen":"Design for failure.","hook_id":1}`)
	resp, raw = ghPost(t, env, s.SourceID, ghHeaders(s.Secret, "ping", "f-6", ping), ping, nil)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != `{"ok":true}` {
		t.Fatalf("ping = %d; %s", resp.StatusCode, raw)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM messages`); n != before {
		t.Fatalf("ping wrote a message: %d rows, was %d", n, before)
	}
	// A ping with a bad signature is refused like anything else.
	hdr = ghHeaders("wrong", "ping", "f-7", ping)
	resp, raw = ghPost(t, env, s.SourceID, hdr, ping, nil)
	mustErr(t, resp, raw, http.StatusUnauthorized, "bad_signature")

	// A signed body that is not JSON is the sender's fault, not a 500.
	junk := []byte("not json")
	resp, raw = ghPost(t, env, s.SourceID, ghHeaders(s.Secret, "pull_request", "f-8", junk), junk, nil)
	mustErr(t, resp, raw, http.StatusBadRequest, "invalid_body")

	// An unknown source is a 404, before any secret is consulted.
	resp, raw = ghPost(t, env, "src_01J0000000000000000000000A", ghHeaders(s.Secret, "pull_request", "f-9", pr), pr, nil)
	mustErr(t, resp, raw, http.StatusNotFound, "not_found")
}

func TestSourceInDirectoryNotAddressable(t *testing.T) {
	env := testhub.Start(t)
	p, pSec := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)

	dir, _ := getPeers(t, env, pSec)
	e, ok := dir[s.SourceID]
	if !ok {
		t.Fatalf("GET /v1/peers has no entry for %s", s.SourceID)
	}
	if str(t, e["kind"]) != "source" || str(t, e["name"]) != "o/r hook" {
		t.Fatalf("entry = %v", e)
	}
	if str(t, e["reachability"]) != "unknown" || str(t, e["presence"]) != "unknown" {
		t.Fatalf("entry = %v, want reachability and presence unknown", e)
	}
	for k := range e {
		if !slices.Contains(directoryKeys, k) {
			t.Fatalf("source entry has non-directory key %q", k)
		}
	}

	resp, raw := env.Do(pSec, "POST", "/v1/messages", sendBody("to-source", s.SourceID))
	mustErr(t, resp, raw, http.StatusNotFound, "unknown_recipient")

	// Every peer hears about the source, as about any directory entry (§8).
	evs := directoryEvents(t, env, p)
	if len(evs) == 0 || str(t, evs[len(evs)-1]["peer_id"]) != s.SourceID || str(t, evs[len(evs)-1]["change"]) != "enrolled" {
		t.Fatalf("directory_updated events = %v", evs)
	}

	// A disabled source leaves the directory and stops accepting events.
	adminDo(t, env, "POST", "/v1/admin/sources/"+s.SourceID+"/disable", nil, http.StatusNoContent)
	dir, _ = getPeers(t, env, pSec)
	if _, ok := dir[s.SourceID]; ok {
		t.Fatal("a disabled source is still listed")
	}
	body := ghFixture(t, "issue_comment.created.json")
	resp, raw = ghPost(t, env, s.SourceID, ghHeaders(s.Secret, "issue_comment", "off-1", body), body, nil)
	mustErr(t, resp, raw, http.StatusForbidden, "forbidden")
}

func TestSourceRateLimit(t *testing.T) {
	env := testhub.Start(t)
	p, _ := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)
	if _, err := env.Hub.DB.Exec(`UPDATE sources SET sends_per_min = 2 WHERE src_id = ?`, s.SourceID); err != nil {
		t.Fatal(err)
	}
	body := ghFixture(t, "issue_comment.created.json")
	first := ghDeliver(t, env, s, "issue_comment", "r-1", body)
	ghDeliver(t, env, s, "issue_comment", "r-2", body)

	resp, raw := ghPost(t, env, s.SourceID, ghHeaders(s.Secret, "issue_comment", "r-3", body), body, nil)
	mustErr(t, resp, raw, http.StatusTooManyRequests, "rate_limited")
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}

	// A redelivery is not a send: it still answers while the source is limited.
	if got := ghDeliver(t, env, s, "issue_comment", "r-1", body); got != first {
		t.Fatalf("redelivery while limited = %s, want %s", got, first)
	}

	if c := rowCount(t, env, `SELECT count(*) FROM audit WHERE action = 'rate_limited' AND actor = ?`, s.SourceID); c != 1 {
		t.Fatalf("%d rate_limited audit rows, want 1", c)
	}

	env.Now.Advance(61 * time.Second)
	ghDeliver(t, env, s, "issue_comment", "r-3", body)
}

// Sources have the peers' second limit too (PROTOCOL §16.2): new threads per
// hour, while further events on a known issue pass.
func TestSourceThreadRateLimit(t *testing.T) {
	env := testhub.Start(t)
	p, _ := env.NewPeer("session", "P")
	s := addSource(t, env, p, nil)
	if _, err := env.Hub.DB.Exec(`UPDATE sources SET threads_per_hour = 1 WHERE src_id = ?`, s.SourceID); err != nil {
		t.Fatal(err)
	}
	ghDeliver(t, env, s, "issue_comment", "h-1", commentOn(t, 196))
	ghDeliver(t, env, s, "issue_comment", "h-2", commentOn(t, 196))

	body := commentOn(t, 197)
	resp, raw := ghPost(t, env, s.SourceID, ghHeaders(s.Secret, "issue_comment", "h-3", body), body, nil)
	e := mustErr(t, resp, raw, http.StatusTooManyRequests, "rate_limited")
	if e.Error.Details["limit"] != "threads_per_hour" {
		t.Fatalf("details = %v", e.Error.Details)
	}
	if n := rowCount(t, env, `SELECT count(*) FROM blob_quota WHERE peer_id = ?`, s.SourceID); n != 1 {
		t.Fatalf("%d quota rows", n)
	}
	var charged int
	if err := env.Hub.DB.QueryRow(`SELECT bytes FROM blob_quota WHERE peer_id = ?`, s.SourceID).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if want := 2 * len(commentOn(t, 196)); charged != want {
		t.Fatalf("source charged %d blob bytes, want %d: a rate-limited event stored its payload", charged, want)
	}

	env.Now.Advance(61 * time.Minute)
	ghDeliver(t, env, s, "issue_comment", "h-3", body)
}

func TestAdminSources(t *testing.T) {
	env := testhub.Start(t)
	p, _ := env.NewPeer("session", "P")

	// Bad targets and values.
	for name, c := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"unknown peer":   {map[string]any{"kind": "github", "name": "n", "target": "peer_01J0000000000000000000000A"}, 404, "unknown_recipient"},
		"unknown team":   {map[string]any{"kind": "github", "name": "n", "target": "team_01J0000000000000000000000A"}, 404, "unknown_recipient"},
		"unknown thread": {map[string]any{"kind": "github", "name": "n", "target": "thr_01J0000000000000000000000A"}, 404, "unknown_recipient"},
		"not a target":   {map[string]any{"kind": "github", "name": "n", "target": "src_01J0000000000000000000000A"}, 404, "unknown_recipient"},
		"bad priority":   {map[string]any{"kind": "github", "name": "n", "target": p, "priority": "urgent"}, 422, "invalid_value"},
		"bad kind":       {map[string]any{"kind": "svn", "name": "n", "target": p}, 422, "invalid_value"},
		"no name":        {map[string]any{"kind": "github", "name": "", "target": p}, 422, "invalid_value"},
	} {
		resp, raw := env.Do(env.OperatorSecret, "POST", "/v1/admin/sources", c.body)
		t.Run(name, func(t *testing.T) { mustErr(t, resp, raw, c.status, c.code) })
	}

	s := addSource(t, env, p, map[string]any{"event_allow": []string{"issues"}, "priority": "hold"})
	if h := rowCount(t, env, `SELECT count(*) FROM sources WHERE secret_enc = ? OR secret_hash = ?`, s.Secret, s.Secret); h != 0 {
		t.Fatal("the source secret is stored in the clear")
	}

	raw := adminDo(t, env, "GET", "/v1/admin/sources", nil, http.StatusOK)
	if strings.Contains(string(raw), s.Secret) {
		t.Fatalf("GET /v1/admin/sources leaks the secret: %s", raw)
	}
	var list struct {
		Sources []struct {
			SourceID       string   `json:"source_id"`
			Kind           string   `json:"kind"`
			Name           string   `json:"name"`
			Target         string   `json:"target"`
			EventAllow     []string `json:"event_allow"`
			Priority       string   `json:"priority"`
			SendsPerMin    int      `json:"sends_per_min"`
			ThreadsPerHour int      `json:"threads_per_hour"`
			Disabled       bool     `json:"disabled"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sources) != 1 {
		t.Fatalf("sources = %s", raw)
	}
	got := list.Sources[0]
	if got.SourceID != s.SourceID || got.Kind != "github" || got.Target != p || got.Priority != "hold" ||
		got.SendsPerMin != 60 || got.ThreadsPerHour != 20 || got.Disabled || !slices.Equal(got.EventAllow, []string{"issues"}) {
		t.Fatalf("source = %+v", got)
	}

	if n := rowCount(t, env, `SELECT count(*) FROM audit WHERE action = 'source_add' AND detail LIKE ?`, "%"+s.Secret+"%"); n != 0 {
		t.Fatal("the audit row carries the secret")
	}

	adminDo(t, env, "POST", "/v1/admin/sources/"+s.SourceID+"/disable", nil, http.StatusNoContent)
	adminDo(t, env, "POST", "/v1/admin/sources/src_01J0000000000000000000000A/disable", nil, http.StatusNotFound)
}

func TestSourceTargetThread(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	sr := postSend(t, env, aSec, sendBody("start", b), http.StatusAccepted)

	s := addSource(t, env, sr.ThreadID, map[string]any{"priority": "hold"})
	msgID := ghDeliver(t, env, s, "issue_comment", "t-1", ghFixture(t, "issue_comment.created.json"))

	for _, sec := range []string{aSec, bSec} {
		d := lastDelivery(t, env, sec)
		if d.Message.MessageID != msgID {
			t.Fatalf("delivery = %s, want %s", d.Message.MessageID, msgID)
		}
		if d.Message.ThreadID == sr.ThreadID {
			t.Fatal("the event landed in the target thread, not its own issue thread")
		}
		if !slices.Equal(sorted(d.Thread.Participants...), sorted(a, b)) {
			t.Fatalf("participants = %v, want the target thread's", d.Thread.Participants)
		}
		if d.Message.Priority != "hold" {
			t.Fatalf("priority = %q, want the source's configured hold", d.Message.Priority)
		}
	}
}
