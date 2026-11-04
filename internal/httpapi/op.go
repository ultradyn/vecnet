package httpapi

import (
	"net/http"
	"strconv"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/opapi"
)

// opRoutes lives under the existing operator-only authentication boundary.
func (s *server) opRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/inbox", s.opInbox)
	mux.HandleFunc("POST /v1/admin/threads/{id}/read", s.opMarkThreadRead)
	mux.HandleFunc("GET /v1/admin/threads", s.opThreads)
	mux.HandleFunc("GET /v1/admin/threads/{id}", s.opThread)
	mux.HandleFunc("GET /v1/admin/threads/{id}/deliveries", s.opMatrix)
	mux.HandleFunc("GET /v1/admin/peers", s.opPeers)
	mux.HandleFunc("GET /v1/admin/loop-cuts", s.opLoopCuts)
	mux.HandleFunc("GET /v1/admin/delivery-health", s.opDeliveryHealth)
	mux.HandleFunc("GET /v1/admin/activity", s.opActivity)
}

func opResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func opInvalid(field, message string) *hub.APIError {
	e := hub.E(http.StatusUnprocessableEntity, "invalid_value", message)
	e.Details["field"] = field
	return e
}

func (s *server) opInbox(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	before := q.Get("before")
	if q.Has("before") && !ids.Is(ids.Dly, before) {
		writeError(w, opInvalid("before", "before must be a dly_ id"))
		return
	}
	value, err := s.op.Inbox(r.Context(), callerFrom(r.Context()), opapi.InboxQuery{Limit: limit, Before: before, UnreadOnly: q.Get("unread") == "1"})
	opResult(w, value, err)
}

func (s *server) opMarkThreadRead(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.op.MarkThreadRead(r.Context(), callerFrom(r.Context()), r.PathValue("id"))
	opResult(w, value, err)
}

func (s *server) opThreads(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	participant := q.Get("participant")
	if q.Has("participant") && !ids.Is(ids.Peer, participant) {
		writeError(w, opInvalid("participant", "participant must be a peer_ id"))
		return
	}
	exclude := q.Get("exclude_participant")
	if q.Has("exclude_participant") && !ids.Is(ids.Peer, exclude) {
		writeError(w, opInvalid("exclude_participant", "exclude_participant must be a peer_ id"))
		return
	}
	value, err := s.op.Threads(r.Context(), opapi.ThreadQuery{Participant: participant, ExcludeParticipant: exclude, Cursor: q.Get("cursor"), Limit: limit})
	opResult(w, value, err)
}

func (s *server) opThread(w http.ResponseWriter, r *http.Request) {
	value, err := s.op.Thread(r.Context(), callerFrom(r.Context()), r.PathValue("id"))
	opResult(w, value, err)
}

func (s *server) opMatrix(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := s.op.Matrix(r.Context(), r.PathValue("id"), r.URL.Query().Get("after"), limit)
	opResult(w, value, err)
}

func (s *server) opPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := s.op.Peers(r.Context())
	if peers == nil {
		peers = []opapi.PeerRow{}
	}
	opResult(w, struct {
		Peers []opapi.PeerRow `json:"peers"`
	}{peers}, err)
}

func (s *server) opLoopCuts(w http.ResponseWriter, r *http.Request) {
	hours := 24
	q := r.URL.Query()
	if q.Has("hours") {
		n, err := strconv.Atoi(q.Get("hours"))
		if err != nil || n < 1 || n > 168 {
			writeError(w, opInvalid("hours", "hours must be between 1 and 168"))
			return
		}
		hours = n
	}
	value, err := s.op.LoopCuts(r.Context(), hours)
	opResult(w, value, err)
}

func (s *server) opDeliveryHealth(w http.ResponseWriter, r *http.Request) {
	value, err := s.op.DeliveryHealth(r.Context())
	opResult(w, value, err)
}
