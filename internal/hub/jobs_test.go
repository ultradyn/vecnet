package hub_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/testhub"
)

// jobReq is a kind message carrying a job_request for jobKey.
func jobReq(key, jobKey, instructions string, recipients ...string) map[string]any {
	b := req(key, recipients...)
	b["job_request"] = map[string]any{
		"version": 1, "job_key": jobKey, "operation": "build.run",
		"instructions": instructions, "input": map[string]any{"target": "all", "n": 2},
	}
	return b
}

// jobStatus is a kind job_status report into thr.
func jobStatus(key, thr, job string, seq int, state string) map[string]any {
	b := req(key)
	b["thread_id"] = thr
	b["kind"] = "job_status"
	b["job_status"] = map[string]any{"job_id": job, "sequence": seq, "state": state}
	return b
}

func jobRow(t *testing.T, env *testhub.Env, job string) (state string, lastSeq int, requester, recipient string) {
	t.Helper()
	if err := env.Hub.DB.QueryRow(`SELECT state, last_sequence, requester, recipient FROM jobs WHERE job_id = ?`, job).
		Scan(&state, &lastSeq, &requester, &recipient); err != nil {
		t.Fatalf("load job %s: %v", job, err)
	}
	return
}

func TestJobCreateAndDedupe(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")

	sr := mustSend(t, env, aSec, jobReq("j1", "build-1", "build everything", b))
	if !ids.Is(ids.Job, sr.JobID) {
		t.Fatalf("job_id = %q, want a job_ id", sr.JobID)
	}
	if state, seq, rq, rc := jobRow(t, env, sr.JobID); state != "requested" || seq != 0 || rq != a || rc != b {
		t.Fatalf("job row = %s seq %d requester %s recipient %s; want requested 0 A B", state, seq, rq, rc)
	}
	evs := deliveryEvents(t, env, b)
	if len(evs) != 1 {
		t.Fatalf("B has %d deliveries, want 1", len(evs))
	}
	_, _, m := decodeDelivery(t, evs[0])
	if m.Metadata["vecnet.job_id"] != sr.JobID {
		t.Fatalf("delivery metadata[vecnet.job_id] = %v, want %s", m.Metadata["vecnet.job_id"], sr.JobID)
	}
	if m.JobRequest == nil || m.JobRequest.JobKey != "build-1" || m.JobRequest.Instructions != "build everything" {
		t.Fatalf("delivery job_request = %+v, want the request echoed", m.JobRequest)
	}
	var in map[string]any
	if err := json.Unmarshal(m.JobRequest.Input, &in); err != nil || in["target"] != "all" {
		t.Fatalf("job_request.input = %s, want echoed", m.JobRequest.Input)
	}

	// Same (thread, job_key), same body: the same job, a new message.
	again := jobReq("j2", "build-1", "build everything")
	again["thread_id"] = sr.ThreadID
	sr2 := mustSend(t, env, aSec, again)
	if sr2.JobID != sr.JobID || sr2.MessageID == sr.MessageID {
		t.Fatalf("dedupe: job %s msg %s; want job %s and a new message", sr2.JobID, sr2.MessageID, sr.JobID)
	}
	if evs := deliveryEvents(t, env, b); len(evs) != 2 {
		t.Fatalf("B has %d deliveries after the repeat, want 2", len(evs))
	} else if _, _, m2 := decodeDelivery(t, evs[1]); m2.Metadata["vecnet.job_id"] != sr.JobID {
		t.Fatalf("repeat delivery metadata[vecnet.job_id] = %v, want %s", m2.Metadata["vecnet.job_id"], sr.JobID)
	}
	if n := count(t, env, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("jobs rows = %d, want 1", n)
	}

	// Same (thread, job_key), different body: 409 job_conflict.
	diff := jobReq("j3", "build-1", "build only the docs")
	diff["thread_id"] = sr.ThreadID
	mustReject(t, env, aSec, diff, http.StatusConflict, "job_conflict")

	// Not exactly one peer recipient: 422 job_single_recipient.
	g := addGroup(t, env, a, a, b)
	mustReject(t, env, aSec, jobReq("j4", "build-2", "x", g), http.StatusUnprocessableEntity, "job_single_recipient")
	mustReject(t, env, aSec, jobReq("j5", "build-3", "x", b, c), http.StatusUnprocessableEntity, "job_single_recipient")
	if n := count(t, env, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("jobs rows = %d after rejections, want 1", n)
	}

	// The same job_key in another thread is another job.
	other := mustSend(t, env, aSec, jobReq("j6", "build-1", "build everything", b))
	if other.JobID == "" || other.JobID == sr.JobID {
		t.Fatalf("job in a new thread = %q, want a fresh job id", other.JobID)
	}
}

func TestJobStatusSequenceAndAuthz(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	c, cSec := env.NewPeer("session", "C")

	sr := mustSend(t, env, aSec, jobReq("j1", "deploy", "deploy it", b))
	job, thr := sr.JobID, sr.ThreadID
	join := req("join", c) // C joins the thread but is not the job's recipient
	join["thread_id"] = thr
	mustSend(t, env, aSec, join)

	mustReject(t, env, bSec, jobStatus("s0", thr, ids.New(ids.Job), 1, "accepted"), http.StatusNotFound, "unknown_job")

	r1 := mustSend(t, env, bSec, jobStatus("s1", thr, job, 1, "accepted"))
	if state, seq, _, _ := jobRow(t, env, job); state != "accepted" || seq != 1 {
		t.Fatalf("after accepted seq1: %s %d", state, seq)
	}
	var stored struct {
		JobID *string
	}
	if err := env.Hub.DB.QueryRow(`SELECT job_id FROM messages WHERE msg_id = ?`, r1.MessageID).Scan(&stored.JobID); err != nil ||
		stored.JobID == nil || *stored.JobID != job {
		t.Fatalf("messages.job_id of the report = %v, %v; want %s", stored.JobID, err, job)
	}
	aDeliveries := len(deliveryEvents(t, env, a))

	mustReject(t, env, bSec, jobStatus("s2", thr, job, 1, "running"), http.StatusConflict, "stale_sequence")
	if got := len(deliveryEvents(t, env, a)); got != aDeliveries {
		t.Fatalf("a stale report was delivered: A has %d deliveries, want %d", got, aDeliveries)
	}
	mustReject(t, env, cSec, jobStatus("s3", thr, job, 2, "running"), http.StatusForbidden, "not_recipient")
	mustReject(t, env, aSec, jobStatus("s3a", thr, job, 2, "running"), http.StatusForbidden, "not_recipient")

	mustSend(t, env, bSec, jobStatus("s4", thr, job, 2, "running"))
	if state, seq, _, _ := jobRow(t, env, job); state != "running" || seq != 2 {
		t.Fatalf("after running seq2: %s %d", state, seq)
	}
	mustReject(t, env, bSec, jobStatus("s5", thr, job, 3, "accepted"), http.StatusUnprocessableEntity, "invalid_transition")
	if state, seq, _, _ := jobRow(t, env, job); state != "running" || seq != 2 {
		t.Fatalf("after a rejected transition: %s %d, want running 2", state, seq)
	}

	mustSend(t, env, aSec, jobStatus("s6", thr, job, 4, "cancelled"))
	if state, seq, _, _ := jobRow(t, env, job); state != "cancellation_requested" || seq != 2 {
		t.Fatalf("after A cancelled seq4: %s %d, want cancellation_requested with B's sequence 2", state, seq)
	}
	mustReject(t, env, bSec, jobStatus("s7", thr, job, 5, "running"), http.StatusUnprocessableEntity, "invalid_transition")
	mustSend(t, env, bSec, jobStatus("s8", thr, job, 5, "cancelled"))
	if state, seq, _, _ := jobRow(t, env, job); state != "cancelled" || seq != 5 {
		t.Fatalf("after B cancelled seq5: %s %d, want cancelled 5", state, seq)
	}

	// Terminal: nothing moves it, not even the requester's cancel.
	mustReject(t, env, bSec, jobStatus("s9", thr, job, 6, "succeeded"), http.StatusUnprocessableEntity, "invalid_transition")
	mustReject(t, env, aSec, jobStatus("s10", thr, job, 7, "cancelled"), http.StatusUnprocessableEntity, "invalid_transition")
}

// TestJobTransitionTable walks every (from, reported) pair of PROTOCOL §5.6
// for the recipient's reports.
func TestJobTransitionTable(t *testing.T) {
	allowed := map[string]map[string]bool{
		"requested":              {"accepted": true, "running": true, "succeeded": true, "failed": true, "cancelled": true},
		"accepted":               {"running": true, "succeeded": true, "failed": true, "cancelled": true},
		"running":                {"succeeded": true, "failed": true, "cancelled": true},
		"cancellation_requested": {"succeeded": true, "failed": true, "cancelled": true},
		"succeeded":              {}, "failed": {}, "cancelled": {},
	}
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	env.SetLimits(a, 1000, 1000)
	env.SetLimits(b, 1000, 1000)
	i := 0
	for from, to := range allowed {
		for _, state := range []string{"accepted", "running", "succeeded", "failed", "cancelled"} {
			i++
			sr := mustSend(t, env, aSec, jobReq(fmt.Sprintf("r%d", i), "t", "x", b))
			if _, err := env.Hub.DB.Exec(`UPDATE jobs SET state = ? WHERE job_id = ?`, from, sr.JobID); err != nil {
				t.Fatal(err)
			}
			body := jobStatus(fmt.Sprintf("s%d", i), sr.ThreadID, sr.JobID, 1, state)
			resp, raw := env.Do(bSec, "POST", "/v1/messages", body)
			if to[state] {
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("%s -> %s = %d %s, want 202", from, state, resp.StatusCode, raw)
				}
				if got, _, _, _ := jobRow(t, env, sr.JobID); got != state {
					t.Fatalf("%s -> %s left state %s", from, state, got)
				}
			} else {
				if code, _ := apiError(t, raw); resp.StatusCode != http.StatusUnprocessableEntity || code != "invalid_transition" {
					t.Fatalf("%s -> %s = %d %s, want 422 invalid_transition", from, state, resp.StatusCode, raw)
				}
			}
		}
	}
}

// A repeat of a job_request addressed to another peer is a different
// request for the same (thread, job_key): 409 job_conflict, never the
// existing job_id (whose recipient could not report on it).
func TestJobDedupeRequiresSameRecipient(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")

	sr := mustSend(t, env, aSec, jobReq("j1", "build-1", "build everything", b))
	join := req("join", c)
	join["thread_id"] = sr.ThreadID
	mustSend(t, env, aSec, join)

	toC := jobReq("j2", "build-1", "build everything", c)
	toC["thread_id"] = sr.ThreadID
	_, det := mustReject(t, env, aSec, toC, http.StatusConflict, "job_conflict")
	if det["job_id"] != sr.JobID {
		t.Fatalf("job_conflict details.job_id = %v, want %s", det["job_id"], sr.JobID)
	}
	if state, _, rq, rc := jobRow(t, env, sr.JobID); state != "requested" || rq != a || rc != b {
		t.Fatalf("job row = %s %s %s; want requested A B", state, rq, rc)
	}
	if n := count(t, env, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("jobs rows = %d, want 1", n)
	}
}

// The recipient's principal may report status as any of its peers; another
// principal's peer may not (PROTOCOL §5.6, §15 item 1).
func TestJobStatusFromRecipientPrincipal(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	prn, prnSec := env.NewPrincipal("bob")
	b, _ := peerUnder(t, env, "session", "b", prn)
	b2, _ := peerUnder(t, env, "session", "b2", prn)
	eve, eveSec := env.NewPrincipal("eve")
	e, _ := peerUnder(t, env, "session", "e", eve)

	sr := mustSend(t, env, aSec, jobReq("j1", "deploy", "deploy it", b))
	job, thr := sr.JobID, sr.ThreadID
	join := req("join", b2, e)
	join["thread_id"] = thr
	mustSend(t, env, aSec, join)

	as := func(body map[string]any, peer string) map[string]any {
		body["as"] = peer
		return body
	}
	// Bob's principal credential, sending as B's sibling b2, reports for B.
	mustSend(t, env, prnSec, as(jobStatus("s1", thr, job, 1, "accepted"), b2))
	if state, seq, _, _ := jobRow(t, env, job); state != "accepted" || seq != 1 {
		t.Fatalf("after the principal's accepted seq1: %s %d, want accepted 1", state, seq)
	}
	// Eve's principal credential is not B's: 403, and the job is unchanged.
	mustReject(t, env, eveSec, as(jobStatus("s2", thr, job, 2, "running"), e), http.StatusForbidden, "not_recipient")
	if state, seq, _, _ := jobRow(t, env, job); state != "accepted" || seq != 1 {
		t.Fatalf("after eve's rejected report: %s %d, want accepted 1", state, seq)
	}
}

// jobWithRecipient starts a job from A to B and returns its ids.
func jobWithRecipient(t *testing.T, env *testhub.Env) (job, thr, aSec, bSec string) {
	t.Helper()
	_, aSec = env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	sr := mustSend(t, env, aSec, jobReq("j1", "deploy", "deploy it", b))
	return sr.JobID, sr.ThreadID, aSec, bSec
}

// The requester's cancel sequence is not the job's (PROTOCOL §5.6): a high
// one must not make the recipient's next honest report stale.
func TestRequesterCancelKeepsRecipientSequence(t *testing.T) {
	env := testhub.Start(t)
	job, thr, aSec, bSec := jobWithRecipient(t, env)
	mustSend(t, env, bSec, jobStatus("s1", thr, job, 1, "accepted"))
	mustSend(t, env, bSec, jobStatus("s2", thr, job, 2, "running"))
	mustSend(t, env, aSec, jobStatus("c1", thr, job, 100, "cancelled"))
	if state, seq, _, _ := jobRow(t, env, job); state != "cancellation_requested" || seq != 2 {
		t.Fatalf("after requester cancelled seq100: %s %d, want cancellation_requested 2", state, seq)
	}
	// A second requester cancel is accepted and changes nothing.
	mustSend(t, env, aSec, jobStatus("c2", thr, job, 1, "cancelled"))
	if state, seq, _, _ := jobRow(t, env, job); state != "cancellation_requested" || seq != 2 {
		t.Fatalf("after a second requester cancel: %s %d, want cancellation_requested 2", state, seq)
	}
	mustSend(t, env, bSec, jobStatus("s3", thr, job, 3, "cancelled"))
	if state, seq, _, _ := jobRow(t, env, job); state != "cancelled" || seq != 3 {
		t.Fatalf("after recipient cancelled seq3: %s %d, want cancelled 3", state, seq)
	}
}

func TestRequesterCancelAnySequence(t *testing.T) {
	env := testhub.Start(t)
	job, thr, aSec, bSec := jobWithRecipient(t, env)
	mustSend(t, env, bSec, jobStatus("s1", thr, job, 5, "running"))
	mustSend(t, env, aSec, jobStatus("c1", thr, job, 1, "cancelled"))
	if state, seq, _, _ := jobRow(t, env, job); state != "cancellation_requested" || seq != 5 {
		t.Fatalf("after requester cancelled seq1: %s %d, want cancellation_requested 5", state, seq)
	}
}

// (guard)
func TestRequesterCancelTerminalStillRefused(t *testing.T) {
	env := testhub.Start(t)
	job, thr, aSec, bSec := jobWithRecipient(t, env)
	mustSend(t, env, bSec, jobStatus("s1", thr, job, 1, "succeeded"))
	mustReject(t, env, aSec, jobStatus("c1", thr, job, 9, "cancelled"), http.StatusUnprocessableEntity, "invalid_transition")
	if state, seq, _, _ := jobRow(t, env, job); state != "succeeded" || seq != 1 {
		t.Fatalf("after a refused cancel: %s %d, want succeeded 1", state, seq)
	}
}

// (guard)
func TestRecipientStaleSequenceStillRefused(t *testing.T) {
	env := testhub.Start(t)
	job, thr, _, bSec := jobWithRecipient(t, env)
	mustSend(t, env, bSec, jobStatus("s1", thr, job, 2, "accepted"))
	_, det := mustReject(t, env, bSec, jobStatus("s2", thr, job, 2, "running"), http.StatusConflict, "stale_sequence")
	if det["last_sequence"] != float64(2) {
		t.Fatalf("stale_sequence details.last_sequence = %v, want 2", det["last_sequence"])
	}
}
