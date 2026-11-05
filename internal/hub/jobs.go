package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// JobIDMetadataKey is the hub-set metadata key naming the job a job_request
// Message created or reused (PROTOCOL §2.2 Response, R04).
const JobIDMetadataKey = "vecnet.job_id"

// Job states (PROTOCOL §5.6, R19). requested is recorded by the hub when it
// creates the job; cancellation_requested when the requester cancels a job
// that is not terminal.
const (
	JobRequested             = "requested"
	JobCancellationRequested = "cancellation_requested"
	JobCancelled             = "cancelled"
)

// jobTransitions is the transition table of PROTOCOL §5.6: the reported
// states a job may move to from each state. succeeded, failed and cancelled
// are terminal (no entry).
var jobTransitions = map[string]map[string]bool{
	"requested":              {"accepted": true, "running": true, "succeeded": true, "failed": true, "cancelled": true},
	"accepted":               {"running": true, "succeeded": true, "failed": true, "cancelled": true},
	"running":                {"succeeded": true, "failed": true, "cancelled": true},
	"cancellation_requested": {"succeeded": true, "failed": true, "cancelled": true},
}

func jobTerminal(state string) bool { return jobTransitions[state] == nil }

// kindEffect is what a send's kind-specific payload (job_request,
// job_status, approval_request, approval_response) does: checks run before
// the Message is written, apply runs after it is inserted, in the same
// transaction.
type kindEffect struct {
	jobID      string // messages.job_id, SendResponse.job_id for a job_request
	approvalID string // messages.approval_id
	jobRequest bool   // a job_request: SendResponse.job_id is set
	apply      func(ctx context.Context, tx *sql.Tx, msgID, now string) error
}

// checkKindPayload enforces the per-kind requirements of $defs/message on a
// SendRequest: approval_request, approval_response and job_status kinds
// carry their payload, a plain message carries none of them (400
// invalid_body with the JSON pointer). A job_request travels only on kind
// message (PROTOCOL §5.6): elsewhere it is 422 invalid_value.
func checkKindPayload(req *envelope.SendRequest) error {
	kind := req.Kind
	if kind == "" {
		kind = "message"
	}
	missing := func(field string) error {
		return &envelope.SchemaError{Path: "/" + field, Message: fmt.Sprintf("kind %s requires %s", kind, field)}
	}
	switch kind {
	case "approval_request":
		if req.ApprovalRequest == nil {
			return missing("approval_request")
		}
	case "approval_response":
		if req.ApprovalResponse == nil {
			return missing("approval_response")
		}
	case "job_status":
		if req.JobStatus == nil {
			return missing("job_status")
		}
	case "message":
		for field, present := range map[string]bool{
			"job_status": req.JobStatus != nil, "approval_request": req.ApprovalRequest != nil,
			"approval_response": req.ApprovalResponse != nil,
		} {
			if present {
				return &envelope.SchemaError{Path: "/" + field, Message: "kind message must not carry " + field}
			}
		}
	}
	if req.JobRequest != nil && kind != "message" {
		e := E(http.StatusUnprocessableEntity, "invalid_value", "job_request travels only on kind message")
		e.Details["field"] = "job_request"
		return e
	}
	return nil
}

// prepareKind runs the kind-specific checks of a send (PROTOCOL §2.2 Jobs,
// §5.6, §9) and returns the effect to apply once the Message exists, or nil
// when the send carries no job or approval payload.
func (h *Hub) prepareKind(ctx context.Context, tx *sql.Tx, c auth.Caller, snd sender, thr *threadRow,
	req *envelope.SendRequest, addressed []string, now time.Time) (*kindEffect, error) {
	// checkKindPayload has made each kind carry its payload.
	switch req.Kind {
	case "", "message":
		if req.JobRequest != nil {
			return prepareJobRequest(ctx, tx, snd, thr, req.JobRequest, addressed)
		}
	case "job_status":
		return prepareJobStatus(ctx, tx, c, snd, req.JobStatus)
	case "approval_request":
		return prepareApprovalRequest(ctx, tx, snd, thr, req.ApprovalRequest)
	case "approval_response":
		return prepareApprovalResponse(ctx, tx, snd, thr, req.ApprovalResponse, now)
	}
	return nil, nil
}

// prepareJobRequest checks a job_request: it names exactly one peer
// recipient (422 job_single_recipient); its (thread, job_key) either is
// new (a job is created in state requested), or exists with the same
// request body and recipient (the job_id is reused and the Message is
// still sent), or exists with a different body or recipient (409
// job_conflict).
func prepareJobRequest(ctx context.Context, tx *sql.Tx, snd sender, thr *threadRow, jr *envelope.JobRequest, addressed []string) (*kindEffect, error) {
	if len(addressed) != 1 || !ids.Is(ids.Peer, addressed[0]) {
		e := E(http.StatusUnprocessableEntity, "job_single_recipient", "a job_request must name exactly one peer recipient")
		e.Details["recipients"] = append([]string{}, addressed...)
		return nil, e
	}
	recipient := addressed[0]
	raw, err := json.Marshal(jr)
	if err != nil {
		return nil, fmt.Errorf("hub: encode job_request: %w", err)
	}
	sha, err := canonicalSHA256(raw)
	if err != nil {
		return nil, err
	}
	if !thr.isNew {
		var jobID, prevSHA, prevRecipient string
		err := tx.QueryRowContext(ctx, `SELECT job_id, request_sha256, recipient FROM jobs WHERE thr_id = ? AND job_key = ?`,
			thr.ID, jr.JobKey).Scan(&jobID, &prevSHA, &prevRecipient)
		switch {
		case err == nil && prevSHA == sha && prevRecipient == recipient:
			return &kindEffect{jobID: jobID, jobRequest: true}, nil
		case err == nil:
			e := E(http.StatusConflict, "job_conflict", "job_key was already used in this thread with a different job_request or recipient")
			e.Details["job_key"] = jr.JobKey
			e.Details["job_id"] = jobID
			return nil, e
		case !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("hub: find job: %w", err)
		}
	}
	jobID := ids.New(ids.Job)
	return &kindEffect{jobID: jobID, jobRequest: true, apply: func(ctx context.Context, tx *sql.Tx, _ string, now string) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO jobs(job_id, thr_id, job_key, request_sha256, requester, recipient, state, last_sequence, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			jobID, thr.ID, jr.JobKey, sha, snd.peer, recipient, JobRequested, now, now); err != nil {
			return fmt.Errorf("hub: insert job: %w", err)
		}
		return nil
	}}, nil
}

// prepareJobStatus checks a job_status report (PROTOCOL §5.6, R19): the job
// exists (404 unknown_job); the reporter is the job's recipient or a
// credential of its principal (403 not_recipient), except that the
// requester may report cancelled, which moves a job that is not terminal to
// cancellation_requested; a recipient's sequence is above the job's last (409
// stale_sequence); and the state is allowed from the job's state by the
// transition table (422 invalid_transition).
//
// sequence belongs to the recipient: a requester's cancel is neither checked
// against nor stored as the job's last sequence, so a high one cannot make
// the recipient's next report stale. A requester cancel of a terminal job is
// still 422 invalid_transition; another while cancellation_requested leaves
// the job unchanged.
func prepareJobStatus(ctx context.Context, tx *sql.Tx, c auth.Caller, snd sender, js *envelope.JobStatus) (*kindEffect, error) {
	var requester, recipient, recipientPrn, state string
	var lastSeq int64
	err := tx.QueryRowContext(ctx,
		`SELECT j.requester, j.recipient, coalesce(p.prn_id, ''), j.state, j.last_sequence
		 FROM jobs j LEFT JOIN peers p ON p.peer_id = j.recipient WHERE j.job_id = ?`, js.JobID).
		Scan(&requester, &recipient, &recipientPrn, &state, &lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		e := E(http.StatusNotFound, "unknown_job", "no such job")
		e.Details["job_id"] = js.JobID
		return nil, e
	}
	if err != nil {
		return nil, fmt.Errorf("hub: load job: %w", err)
	}
	notRecipient := func() error {
		e := E(http.StatusForbidden, "not_recipient", "only the job's recipient or its principal may report status")
		e.Details["job_id"] = js.JobID
		return e
	}
	fromRequester := false
	switch {
	case snd.peer == recipient:
	case snd.peer == requester:
		if js.State != JobCancelled {
			return nil, notRecipient()
		}
		fromRequester = true
	case recipientPrn != "" && mayActFor(c, recipient, recipientPrn):
	default:
		return nil, notRecipient()
	}
	if !fromRequester && js.Sequence <= lastSeq {
		e := E(http.StatusConflict, "stale_sequence", "sequence must be greater than the job's last reported sequence")
		e.Details["job_id"] = js.JobID
		e.Details["last_sequence"] = lastSeq
		return nil, e
	}
	if fromRequester {
		if jobTerminal(state) {
			return nil, invalidTransition(js.JobID, state, js.State)
		}
		if state == JobCancellationRequested {
			return &kindEffect{jobID: js.JobID}, nil
		}
		return &kindEffect{jobID: js.JobID, apply: func(ctx context.Context, tx *sql.Tx, _ string, now string) error {
			if _, err := tx.ExecContext(ctx,
				`UPDATE jobs SET state = ?, updated_at = ? WHERE job_id = ?`,
				JobCancellationRequested, now, js.JobID); err != nil {
				return fmt.Errorf("hub: update job: %w", err)
			}
			return nil
		}}, nil
	}
	if !jobTransitions[state][js.State] {
		return nil, invalidTransition(js.JobID, state, js.State)
	}
	return &kindEffect{jobID: js.JobID, apply: func(ctx context.Context, tx *sql.Tx, _ string, now string) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE jobs SET state = ?, last_sequence = ?, updated_at = ? WHERE job_id = ?`,
			js.State, js.Sequence, now, js.JobID); err != nil {
			return fmt.Errorf("hub: update job: %w", err)
		}
		return nil
	}}, nil
}

func invalidTransition(job, from, to string) *APIError {
	e := E(http.StatusUnprocessableEntity, "invalid_transition", "the job cannot move to this state from its current state")
	e.Details["job_id"] = job
	e.Details["from"] = from
	e.Details["state"] = to
	return e
}
