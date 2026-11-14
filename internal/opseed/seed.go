// Package opseed builds the shared, deliberately hostile operator UI fixture.
// It is development-only and is not linked into either production command.
package opseed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
)

type Options struct{ Scale, Many bool }
type Scenario struct {
	OperatorPeer                          string
	Peers                                 map[string]string
	Secrets                               map[string]string
	Threads                               map[string]string
	Approvals, DeadLetters, Subscriptions []string
}

const hostileText = "<img src=x onerror=alert(1)>\x1b]52;c;aGk=\x07\x1b[2Jpwn\u202e"

type builder struct {
	ctx     context.Context
	h       *hub.Hub
	s       Scenario
	callers map[string]auth.Caller
	serial  int
}

// Seed populates a fresh hub, using its public write methods for all ordinary
// traffic. SQL is reserved for fixture states that cannot be reached without
// external transports, elapsed time, or exhausting a real queue.
func Seed(ctx context.Context, h *hub.Hub, opCaller auth.Caller, o Options) (Scenario, error) {
	b := &builder{ctx: ctx, h: h, s: Scenario{OperatorPeer: opCaller.PeerID, Peers: map[string]string{"operator": opCaller.PeerID}, Secrets: map[string]string{}, Threads: map[string]string{}}, callers: map[string]auth.Caller{"operator": opCaller}}
	if opCaller.Kind != "operator" || opCaller.PeerID == "" {
		return Scenario{}, fmt.Errorf("opseed: operator caller required")
	}
	for _, p := range [][3]string{
		{"codex", "session", "codex-hark"}, {"grok", "grokbot", "grok-reviewer"},
		{"sleuth", "pagchat-bot", "pag-hark-sleuth"}, {"digest", "pagchat-bot", "pag-digest"},
		{"claw", "openclaw", "claw-ops"}, {"dot", "dot", "dot-research"},
		{"triage", "grokbot", "grok-triage"}, {"hostile", "session", hostileText}, {"gh", "source", "gh-ultradyn"},
	} {
		if err := b.peer(p[0], p[1], p[2]); err != nil {
			return Scenario{}, err
		}
	}
	for _, p := range []struct{ key, state string }{{"operator", "working"}, {"codex", "working"}, {"sleuth", "working"}, {"digest", "idle"}, {"claw", "idle"}, {"hostile", "blocked"}} {
		if err := h.SetPresence(ctx, b.callers[p.key], "", hub.PresenceReport{State: p.state, At: h.Now(), Location: "demo", Capabilities: []string{"demo"}}); err != nil {
			return Scenario{}, err
		}
	}
	h.ObservePush(ctx, b.s.Peers["dot"], true)
	h.ObservePush(ctx, b.s.Peers["grok"], true)
	h.ObservePush(ctx, b.s.Peers["triage"], false)
	if err := b.messages(); err != nil {
		return Scenario{}, err
	}
	b.s.Approvals = []string{b.s.Approvals[2], b.s.Approvals[3], b.s.Approvals[0], b.s.Approvals[1]}
	if err := b.deliveryStates(); err != nil {
		return Scenario{}, err
	}
	if err := b.subscriptions(); err != nil {
		return Scenario{}, err
	}
	if o.Scale {
		if err := b.scale(); err != nil {
			return Scenario{}, err
		}
	}
	if o.Many {
		if err := b.many(); err != nil {
			return Scenario{}, err
		}
	}
	return b.s, nil
}

func (b *builder) peer(key, kind, name string) error {
	id, tok, err := b.h.AddPeer(b.ctx, kind, name, "")
	if err != nil {
		return err
	}
	b.s.Peers[key] = id
	if kind == "dot" {
		return nil
	}
	_, sec, err := b.h.Auth.Enrol(b.ctx, tok)
	if err != nil {
		return err
	}
	b.s.Secrets[key] = sec
	c, err := b.h.Auth.Resolve(b.ctx, sec)
	b.callers[key] = c
	return err
}

func (b *builder) send(from, key, title, text string, tweak func(*envelope.SendRequest)) (*hub.SendResponse, error) {
	b.serial++
	r := envelope.SendRequest{IdempotencyKey: fmt.Sprintf("demo-%d", b.serial), Priority: "hold", Content: envelope.SendContent{Text: text}, ThreadTitle: title}
	if id := b.s.Threads[key]; id != "" {
		r.ThreadID = &id
	} else {
		r.Recipients = []string{b.s.OperatorPeer}
	}
	if from == "operator" && r.ThreadID == nil {
		r.Recipients = []string{b.s.Peers["codex"]}
	}
	if tweak != nil {
		tweak(&r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	resp, _, err := b.h.Send(b.ctx, b.callers[from], raw, "operator_ui")
	if err != nil {
		return nil, fmt.Errorf("opseed %s/%s: %w", key, from, err)
	}
	b.s.Threads[key] = resp.ThreadID
	return resp, nil
}

func (b *builder) approval(key, question string, options []string, ttl time.Duration) (string, error) {
	id := ids.New(ids.Apr)
	_, err := b.send("codex", key, "", question, func(r *envelope.SendRequest) {
		r.Kind = "approval_request"
		r.Priority = "wake"
		r.Recipients = []string{b.s.OperatorPeer}
		r.ApprovalRequest = &envelope.ApprovalRequest{ApprovalID: id, Question: question, Options: options, ExpiresAt: envelope.FormatTime(b.h.Now().Add(ttl))}
	})
	if err == nil {
		b.s.Approvals = append(b.s.Approvals, id)
	}
	return id, err
}

func (b *builder) messages() error {
	_, err := b.send("codex", "hark", "hark B196: radio end-phrase bug", "Reproduced the radio end-phrase bug. Requesting a review.", func(r *envelope.SendRequest) {
		r.Recipients = []string{b.s.OperatorPeer, b.s.Peers["sleuth"], b.s.Peers["grok"], b.s.Peers["claw"]}
	})
	if err != nil {
		return err
	}
	if _, err = b.send("sleuth", "hark", "", strings.Repeat("Trace: listen loop waiting for the end phrase.\n", 140), nil); err != nil {
		return err
	}
	blob, err := b.h.PutBlob(b.ctx, b.callers["codex"], strings.NewReader("radio: received over\nradio: finalize missing\n"), "text/plain", 0)
	if err != nil {
		return err
	}
	if _, err = b.send("codex", "hark", "", "Attached the reproduction log.", func(r *envelope.SendRequest) {
		r.Attachments = []envelope.Attachment{{SHA256: blob.SHA256, Size: blob.Size, MediaType: blob.MediaType, Name: "hark-listen.log"}}
	}); err != nil {
		return err
	}
	job, err := b.send("operator", "hark", "", "Run the classifier checks.", func(r *envelope.SendRequest) {
		r.Recipients = []string{b.s.Peers["codex"]}
		r.JobRequest = &envelope.JobRequest{Version: 1, JobKey: "classifier-check", Operation: "build.run", Instructions: "Run classifier checks", Input: json.RawMessage(`{}`)}
	})
	if err != nil {
		return err
	}
	if _, err = b.send("codex", "hark", "", "Classifier checks passed.", func(r *envelope.SendRequest) {
		r.Kind = "job_status"
		r.JobStatus = &envelope.JobStatus{JobID: job.JobID, Sequence: 1, State: "succeeded"}
	}); err != nil {
		return err
	}
	closed, err := b.approval("hark", "Run the reproduction?", []string{"yes", "no"}, time.Hour)
	if err != nil {
		return err
	}
	if _, err = b.send("operator", "hark", "", "Approved reproduction.", func(r *envelope.SendRequest) {
		r.Kind = "approval_response"
		r.ApprovalResponse = &envelope.ApprovalResponse{ApprovalID: closed, Decision: "yes"}
	}); err != nil {
		return err
	}
	if _, err = b.approval("hark", "An earlier review window", []string{"yes", "no"}, time.Millisecond); err != nil {
		return err
	}
	if _, err = b.h.ExpireApprovals(b.ctx, b.h.Now().Add(time.Second)); err != nil {
		return err
	}
	if _, err = b.approval("hark", "Push classifier fix to hark master and release?", []string{"yes", "no", "yes but no release"}, 30*time.Minute); err != nil {
		return err
	}
	if _, err = b.send("codex", "hosting", "Vecnet hosting", "Scope the hub hosting cost and nightly off-host backup.", nil); err != nil {
		return err
	}
	if _, err = b.approval("hosting", "Proceed with the hosting plan?", []string{"yes", "no"}, 45*time.Minute); err != nil {
		return err
	}
	for _, p := range [][4]string{{"codex", "ticket18", "Ticket 18: operator UI", "The dashboard performs no writes."}, {"digest", "digest", "Daily digest", "Held until you return: all tests are green."}, {"triage", "quota", "Quota exhausted", "This thread has exhausted its message budget."}} {
		if _, err = b.send(p[0], p[1], p[2], p[3], nil); err != nil {
			return err
		}
	}
	if _, err = b.h.DB.ExecContext(b.ctx, `UPDATE threads SET budget_remaining=0 WHERE thr_id=?`, b.s.Threads["quota"]); err != nil {
		return err
	}
	if _, err = b.h.PutBlob(b.ctx, b.callers["hostile"], strings.NewReader("radio: received over\nradio: finalize missing\n"), "text/plain", 0); err != nil {
		return err
	}
	if _, err = b.send("hostile", "hostile", hostileText, hostileText, func(r *envelope.SendRequest) {
		r.Attachments = []envelope.Attachment{{SHA256: blob.SHA256, Size: blob.Size, MediaType: blob.MediaType, Name: "..\x1b[31m.txt"}}
	}); err != nil {
		return err
	}
	return nil
}

func (b *builder) deliveryStates() error {
	// This local receiver is supplied by the demo binary. The fixture records
	// verification directly, just as it records the historical push outcomes.
	route, _, err := b.h.AddRoute(b.ctx, "peer", b.s.Peers["triage"], b.h.Cfg.PublicURL+"/__demo/sink/push")
	if err != nil {
		return err
	}
	if err := b.h.MarkRouteVerified(b.ctx, route); err != nil {
		return err
	}
	for i, p := range []struct {
		state           string
		status, attempt int
		message         string
	}{{"dead_letter", 503, 8, "HTTP 503 after 8/8 attempts"}, {"dead_letter", 410, 1, "HTTP 410 Gone"}, {"dead_letter", 0, 8, "network connection refused"}, {"failed", 409, 1, "HTTP 409 Conflict"}, {"queued", 503, 2, "retry pending"}} {
		resp, err := b.send("operator", fmt.Sprintf("delivery-%d", i), fmt.Sprintf("Delivery health %d", i+1), p.message, func(r *envelope.SendRequest) { r.Priority = "wake"; r.Recipients = []string{b.s.Peers["triage"]} })
		if err != nil {
			return err
		}
		id := resp.Deliveries[0].DeliveryID
		var status any
		if p.status != 0 {
			status = p.status
		}
		var next any
		left := 0
		if p.state == "queued" {
			next = envelope.FormatTime(b.h.Now().Add(time.Hour))
			left = 6
		}
		if _, err = b.h.DB.ExecContext(b.ctx, `UPDATE deliveries SET state=?,last_status=?,last_error=?,attempt=?,attempts_left=?,next_attempt_at=? WHERE dly_id=?`, p.state, status, p.message, p.attempt, left, next, id); err != nil {
			return err
		}
		if p.state == "dead_letter" {
			b.s.DeadLetters = append(b.s.DeadLetters, id)
		}
		if p.state == "queued" {
			if _, err = b.send("operator", fmt.Sprintf("delivery-%d", i), "", "Queued behind the retrying delivery.", func(r *envelope.SendRequest) { r.Priority = "wake"; r.Recipients = []string{b.s.Peers["triage"]} }); err != nil {
				return err
			}
		}
	}
	for _, reason := range []string{"max_depth_exceeded", "expired", "thread_budget_exhausted"} {
		targets, _ := json.Marshal([]string{b.s.Threads["quota"]})
		detail, _ := json.Marshal(map[string]string{"reason": reason})
		if _, err := b.h.DB.ExecContext(b.ctx, `INSERT INTO audit(at,actor,action,target_ids,detail) VALUES(?,?,'loop_cut',?,?)`, envelope.FormatTime(b.h.Now()), b.s.Peers["triage"], string(targets), string(detail)); err != nil {
			return err
		}
	}
	_, err = b.h.DB.ExecContext(b.ctx, `INSERT INTO counters(name,value) VALUES('loop_cut',3)`)
	return err
}

func (b *builder) scale() error {
	if _, err := b.h.DB.ExecContext(b.ctx, `UPDATE peers SET sends_per_min=2000,threads_per_hour=200 WHERE peer_id=?`, b.s.OperatorPeer); err != nil {
		return err
	}
	recipients := []string{}
	for i := 0; i < 63; i++ {
		key := fmt.Sprintf("scale-%02d", i)
		if err := b.peer(key, "session", key); err != nil {
			return err
		}
		recipients = append(recipients, b.s.Peers[key])
	}
	for start := 0; start < len(recipients); start += 21 {
		batch := recipients[start : start+21]
		if _, err := b.send("operator", "participants", "64 participants", "A wide participant matrix.", func(r *envelope.SendRequest) { r.Recipients = batch }); err != nil {
			return err
		}
	}
	for i := 0; i < 500; i++ {
		if _, err := b.send("operator", "long", "500-message thread", fmt.Sprintf("History message %03d", i+1), nil); err != nil {
			return err
		}
	}
	return nil
}

// many exercises bounded operator lists without coupling this fixture to archive APIs.
func (b *builder) many() error {
	if _, err := b.h.DB.ExecContext(b.ctx, `UPDATE peers SET sends_per_min=2000,threads_per_hour=2000 WHERE peer_id=?`, b.s.OperatorPeer); err != nil {
		return err
	}
	for i := 0; i < 63; i++ {
		key := fmt.Sprintf("browse-%02d", i)
		if err := b.peer(key, "session", key); err != nil {
			return err
		}
	}
	for i := 0; i < 310; i++ {
		key := fmt.Sprintf("browse-thread-%03d", i)
		if _, err := b.send("operator", key, fmt.Sprintf("Browse project %03d", i), "Fixture conversation for bounded browsing.", func(r *envelope.SendRequest) {
			r.Recipients = []string{b.s.Peers[fmt.Sprintf("browse-%02d", i%63)]}
		}); err != nil {
			return err
		}
	}
	for i := 0; i < 10; i++ {
		id := b.s.Peers[fmt.Sprintf("browse-%02d", i)]
		if _, err := b.h.DB.ExecContext(b.ctx, `UPDATE peers SET disabled=1 WHERE peer_id=?`, id); err != nil {
			return err
		}
	}
	for i := 10; i < 20; i++ {
		id := b.s.Peers[fmt.Sprintf("browse-%02d", i)]
		if err := b.h.SetPeerArchived(b.ctx, id, true); err != nil {
			return err
		}
	}
	return nil
}
