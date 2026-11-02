package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// threadsBody is the GET /v1/threads response; Next null on the last page.
type threadsBody struct {
	Threads []envelope.ThreadSummary `json:"threads"`
	Next    *string                  `json:"next"`
}

// historyBody is the GET /v1/threads/{id}/messages response.
type historyBody struct {
	Messages  []envelope.Message `json:"messages"`
	NextAfter *string            `json:"next_after"`
	// HostedBlobs is set for the operator only, as {} when nothing on the
	// page is hosted: message id -> the sha256s of its attachments the hub
	// hosts (Hub.HostedAttachments).
	HostedBlobs *map[string][]string `json:"hosted_blobs,omitempty"`
}

// queryLimit parses ?limit= (PROTOCOL §2.8): absent → 0 (the hub's
// default); a positive integer, clamped by the hub at 200; anything else is
// 422 invalid_value.
func queryLimit(r *http.Request) (int, error) {
	q := r.URL.Query()
	if !q.Has("limit") {
		return 0, nil
	}
	n, err := strconv.Atoi(q.Get("limit"))
	if err != nil || n < 1 {
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "limit must be a positive whole number")
		e.Details["field"] = "limit"
		return 0, e
	}
	return n, nil
}

// getThreads is GET /v1/threads?participant=&group=&since=&limit=&cursor=
// (PROTOCOL §2.8): the threads the caller participates in, newest activity
// first, with an opaque next paging token.
func (s *server) getThreads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var since time.Time
	if v := q.Get("since"); v != "" {
		if since, err = envelope.ParseTime(v); err != nil {
			e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "since must be YYYY-MM-DDTHH:MM:SS.sssZ")
			e.Details["field"] = "since"
			writeError(w, e)
			return
		}
	}
	threads, next, err := s.Hub.ListThreads(r.Context(), callerFrom(r.Context()),
		q.Get("participant"), q.Get("group"), since, limit, q.Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	body := threadsBody{Threads: threads}
	if next != "" {
		body.Next = &next
	}
	writeJSON(w, http.StatusOK, body)
}

// getThreadMessages is GET /v1/threads/{id}/messages?after=&limit=
// (PROTOCOL §2.8, §16.3): the history visible to the caller.
func (s *server) getThreadMessages(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	msgs, next, err := s.Hub.ThreadMessages(r.Context(), callerFrom(r.Context()),
		r.PathValue("id"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	body := historyBody{Messages: msgs, NextAfter: next}
	if c := callerFrom(r.Context()); c.Kind == "operator" {
		// The operator UI shows hosted images inline: an attachment that
		// also names an origin_url may still have hosted bytes, which a
		// stored Message cannot say (it never holds a blob_url).
		hosted, err := s.Hub.HostedAttachments(r.Context(), msgs)
		if err != nil {
			writeError(w, err)
			return
		}
		body.HostedBlobs = &hosted
	}
	writeJSON(w, http.StatusOK, body)
}
