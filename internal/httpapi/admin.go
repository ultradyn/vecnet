package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Admin endpoints (PROTOCOL §2.12). The auth middleware admits only the
// operator credential to /v1/admin/... (403 forbidden otherwise), so these
// handlers do not check the caller again.

// adminRoutes registers the /v1/admin/... endpoints.
func (s *server) adminRoutes(mux *http.ServeMux) {
	s.opRoutes(mux)
	mux.HandleFunc("POST /v1/admin/principals", s.adminAddPrincipal)
	mux.HandleFunc("GET /v1/admin/principals", s.adminListPrincipals)
	mux.HandleFunc("PUT /v1/admin/principals/{id}/limits", s.adminPrincipalLimits)
	mux.HandleFunc("PUT /v1/admin/principals/{id}/default-scopes", s.adminPrincipalDefaultScopes)
	mux.HandleFunc("POST /v1/admin/principals/{id}/rotate", s.adminRotatePrincipal)
	mux.HandleFunc("POST /v1/admin/peers", s.adminAddPeer)
	mux.HandleFunc("POST /v1/admin/peers/{id}/move", s.adminMovePeer)
	mux.HandleFunc("POST /v1/admin/peers/{id}/rotate", s.adminRotatePeer)
	mux.HandleFunc("POST /v1/admin/peers/{id}/enrol-token", s.adminReissueEnrol)
	mux.HandleFunc("PUT /v1/admin/peers/{id}/scopes", s.adminSetScopes)
	mux.HandleFunc("PUT /v1/admin/peers/{id}/limits", s.adminSetLimits)
	mux.HandleFunc("POST /v1/admin/peers/{id}/disable", s.adminDisable(true))
	mux.HandleFunc("POST /v1/admin/peers/{id}/enable", s.adminDisable(false))
	mux.HandleFunc("POST /v1/admin/sources", s.adminAddSource)
	mux.HandleFunc("GET /v1/admin/sources", s.adminListSources)
	mux.HandleFunc("POST /v1/admin/sources/{id}/disable", s.adminDisableSource)
	mux.HandleFunc("POST /v1/admin/routes", s.adminAddRoute)
	mux.HandleFunc("GET /v1/admin/routes", s.adminListRoutes)
	mux.HandleFunc("DELETE /v1/admin/routes/{id}", s.adminRemoveRoute)
	mux.HandleFunc("POST /v1/admin/routes/{id}/rotate", s.adminRotateRoute)
	mux.HandleFunc("POST /v1/admin/routes/{id}/verify", s.adminVerifyRoute)
	mux.HandleFunc("POST /v1/admin/teams", s.adminAddTeam)
	mux.HandleFunc("POST /v1/admin/teams/{id}/members", s.adminTeamMembers)
	mux.HandleFunc("GET /v1/admin/teams", s.adminListTeams)
	mux.HandleFunc("POST /v1/admin/groups/{id}/unarchive", s.adminUnarchiveGroup)
	mux.HandleFunc("GET /v1/admin/dead-letters", s.adminDeadLetters)
	mux.HandleFunc("POST /v1/admin/dead-letters/{dly}/retry", s.adminRetryDeadLetter)
	mux.HandleFunc("POST /v1/admin/dead-letters/{dly}/discard", s.adminDiscardDeadLetter)
	mux.HandleFunc("PATCH /v1/admin/threads/{id}", s.adminPatchThread)
	mux.HandleFunc("POST /v1/admin/blobs/{sha}/purge", s.adminPurgeBlob)
	mux.HandleFunc("PUT /v1/admin/notify", s.adminSetNotify)
	mux.HandleFunc("GET /v1/admin/audit", s.adminAudit)
	mux.HandleFunc("GET /v1/admin/counters", s.adminCounters)
	mux.HandleFunc("GET /v1/admin/subscriptions", s.adminListSubscriptions)
	mux.HandleFunc("POST /v1/admin/subscriptions/{id}/pause", s.adminPauseSubscription)
	mux.HandleFunc("POST /v1/admin/subscriptions/{id}/resume", s.adminResumeSubscription)
}

// readObject reads the body as a JSON object restricted to allowed keys
// with every required key present (400 invalid_body otherwise).
func readObject(r *http.Request, allowed, required []string) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return decodeObject(raw, allowed, required)
}

// readNoBody accepts an empty body or an empty JSON object; anything else is
// 400 invalid_body.
func readNoBody(r *http.Request) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	_, err = decodeObject(raw, nil, nil)
	return err
}

// stringsField decodes top[name] as an array of strings (nil when absent).
func stringsField(top map[string]json.RawMessage, name string) ([]string, error) {
	raw, has := top[name]
	if !has {
		return nil, nil
	}
	var elements []json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &elements) != nil {
		return nil, &envelope.SchemaError{Path: "/" + name, Message: name + " must be an array of strings"}
	}
	out := make([]string, len(elements))
	for i, element := range elements {
		if isNull(element) || json.Unmarshal(element, &out[i]) != nil {
			return nil, &envelope.SchemaError{Path: "/" + name + "/" + strconv.Itoa(i), Message: "must be a string"}
		}
	}
	return out, nil
}

// intField decodes top[name] as a whole number.
func intField(top map[string]json.RawMessage, name string) (int, error) {
	raw := bytes.TrimSpace(top[name])
	if len(raw) == 0 || raw[0] == '"' {
		return 0, &envelope.SchemaError{Path: "/" + name, Message: name + " must be an integer"}
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return 0, &envelope.SchemaError{Path: "/" + name, Message: name + " must be an integer"}
	}
	v, err := strconv.Atoi(n.String())
	if err != nil {
		return 0, &envelope.SchemaError{Path: "/" + name, Message: name + " must be an integer"}
	}
	return v, nil
}

// adminAddPrincipal is POST /v1/admin/principals {"name"}: 201
// {"principal_id", "secret"}.
func (s *server) adminAddPrincipal(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"name"}, []string{"name"})
	if err != nil {
		writeError(w, err)
		return
	}
	name, err := stringField(top, "name")
	if err != nil {
		writeError(w, err)
		return
	}
	prn, secret, err := s.Hub.AddPrincipal(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]string{"principal_id": prn, "secret": secret})
}

// adminRotatePrincipal is POST /v1/admin/principals/{id}/rotate: 200 {"secret"}.
func (s *server) adminRotatePrincipal(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	secret, err := s.Hub.RotatePrincipal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

// adminAddPeer is POST /v1/admin/peers {"kind", "name"?, "principal_id"?,
// "enrol_ttl_seconds"?, "name_open"?}: 201 {"peer_id", "name", "name_open",
// "enrol_token", "enrol_expires_at", "principal_id", "hub_url"}; a dot's
// enrol_token and enrol_expires_at are "", since it links with `vecnet peer
// link` instead. name is required unless name_open, which leaves it to the
// agent at enrol (name is then a suggestion). Without principal_id the peer
// gets an implicit principal named like it. hub_url is the hub's public URL,
// for an invite (PROTOCOL §2.12).
func (s *server) adminAddPeer(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"kind", "name", "principal_id", "enrol_ttl_seconds", "name_open"}, []string{"kind"})
	if err != nil {
		writeError(w, err)
		return
	}
	var f [3]string
	for i, name := range []string{"kind", "name", "principal_id"} {
		if f[i], err = stringField(top, name); err != nil {
			writeError(w, err)
			return
		}
	}
	open, err := optBool(top, "name_open")
	if err != nil {
		writeError(w, err)
		return
	}
	if _, has := top["name"]; !has && !open {
		writeError(w, &envelope.SchemaError{Path: "/name", Message: "name is required"})
		return
	}
	ttl, err := enrolTTL(top)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	peer, tok, exp, err := s.Hub.AddPeerWith(ctx, hub.AddPeerOpts{Kind: f[0], Name: f[1], PrincipalID: f[2], TTL: ttl, NameOpen: open})
	if err != nil {
		writeError(w, err)
		return
	}
	s.writeInvite(w, r, peer, tok, exp)
}

// adminMovePeer is POST /v1/admin/peers/{id}/move {"principal_id"}: 204.
func (s *server) adminMovePeer(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"principal_id"}, []string{"principal_id"})
	if err != nil {
		writeError(w, err)
		return
	}
	prn, err := stringField(top, "principal_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.MovePeer(r.Context(), r.PathValue("id"), prn); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminRotatePeer is POST /v1/admin/peers/{id}/rotate: 200 {"secret"}, the
// peer's new credential; the old one stops working at once.
func (s *server) adminRotatePeer(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	secret, err := s.Hub.RotatePeer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

// adminSetScopes is PUT /v1/admin/peers/{id}/scopes {"allow": [ids],
// "deny": [ids]}: 204; replaces the peer's scopes (absent lists are empty).
func (s *server) adminSetScopes(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"allow", "deny"}, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	allow, err := stringsField(top, "allow")
	if err != nil {
		writeError(w, err)
		return
	}
	deny, err := stringsField(top, "deny")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetScopes(r.Context(), r.PathValue("id"), allow, deny); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminSetLimits is PUT /v1/admin/peers/{id}/limits {"sends_per_min",
// "threads_per_hour"}: 204.
func (s *server) adminSetLimits(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"sends_per_min", "threads_per_hour"}, []string{"sends_per_min", "threads_per_hour"})
	if err != nil {
		writeError(w, err)
		return
	}
	perMin, err := intField(top, "sends_per_min")
	if err != nil {
		writeError(w, err)
		return
	}
	perHour, err := intField(top, "threads_per_hour")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetPeerLimits(r.Context(), r.PathValue("id"), perMin, perHour); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminDisable is POST /v1/admin/peers/{id}/disable (or /enable): 204.
func (s *server) adminDisable(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := readNoBody(r); err != nil {
			writeError(w, err)
			return
		}
		if err := s.Hub.SetPeerDisabled(r.Context(), r.PathValue("id"), disabled); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// adminAddRoute is POST /v1/admin/routes {"owner": "peer_…"|"prn_…", "url",
// "kind"?, "key"?}. Kind http (the default) is 201 {"route_id", "whsec"} and
// pushes nothing until verified. Kind grokbot takes the routine's crsr_ key
// and is 201 {"route_id"}: live at once, and the key is never returned.
func (s *server) adminAddRoute(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"owner", "url", "kind", "key"}, []string{"owner", "url"})
	if err != nil {
		writeError(w, err)
		return
	}
	owner, err := stringField(top, "owner")
	if err != nil {
		writeError(w, err)
		return
	}
	u, err := stringField(top, "url")
	if err != nil {
		writeError(w, err)
		return
	}
	routeKind, err := stringField(top, "kind")
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := stringField(top, "key")
	if err != nil {
		writeError(w, err)
		return
	}
	var kind string
	switch {
	case strings.HasPrefix(owner, "peer_"):
		kind = "peer"
	case strings.HasPrefix(owner, "prn_"):
		kind = "principal"
	default:
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "owner is a peer_ or prn_ id")
		e.Details["field"] = "owner"
		writeError(w, e)
		return
	}
	invalid := func(field, msg string) {
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", msg)
		e.Details["field"] = field
		writeError(w, e)
	}
	switch routeKind {
	case "", hub.RouteKindHTTP:
		if _, has := top["key"]; has {
			invalid("key", "only a grokbot route takes a key")
			return
		}
		id, whsec, err := s.Hub.AddRoute(r.Context(), kind, owner, u)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, map[string]string{"route_id": id, "whsec": whsec})
	case hub.RouteKindGrokbot:
		id, err := s.Hub.AddGrokbotRoute(r.Context(), kind, owner, u, key)
		if err != nil {
			writeError(w, err)
			return
		}
		s.kick() // verified at once: queued deliveries may be due now
		writeJSON(w, http.StatusCreated, map[string]string{"route_id": id})
	default:
		invalid("kind", "kind is http or grokbot")
	}
}

// adminListRoutes is GET /v1/admin/routes: 200 {"routes": [RouteInfo]}. No
// key of any kind is returned.
func (s *server) adminListRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.Hub.ListRoutes(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

// adminRemoveRoute is DELETE /v1/admin/routes/{id}: 204, or 404 for an
// unknown route. Pending deliveries remain available through pull.
func (s *server) adminRemoveRoute(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.RemoveRoute(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminRotateRoute is POST /v1/admin/routes/{id}/rotate. An http route takes
// no body and is 200 {"whsec"}; the old key stays valid for 24 h (PROTOCOL
// §3.5). A grokbot route takes {"key": "crsr_…"} and is 204: the new key
// applies at once and is never returned.
func (s *server) adminRotateRoute(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	var top map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 {
		if top, err = decodeObject(raw, []string{"key"}, nil); err != nil {
			writeError(w, err)
			return
		}
	}
	if _, has := top["key"]; has {
		key, err := stringField(top, "key")
		if err != nil {
			writeError(w, err)
			return
		}
		if err := s.Hub.RotateGrokbotKey(r.Context(), r.PathValue("id"), key); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	whsec, err := s.Hub.RotateRoute(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"whsec": whsec})
}

// adminVerifyRoute is POST /v1/admin/routes/{id}/verify: sends the route its
// verification challenge (PROTOCOL §3.2) through Deps.VerifyRoute; 204 once
// the receiver echoed it, 422 invalid_value when it did not.
func (s *server) adminVerifyRoute(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if s.VerifyRoute == nil {
		writeError(w, errors.New("httpapi: no route verifier wired"))
		return
	}
	id := r.PathValue("id")
	if err := s.VerifyRoute(r.Context(), id); err != nil {
		var apiErr *hub.APIError
		if errors.As(err, &apiErr) {
			writeError(w, err)
			return
		}
		e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "the route did not answer its verification challenge")
		e.Details["field"] = "url"
		e.Details["route_id"] = id
		e.Details["reason"] = hub.EgressErr(err)
		writeError(w, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminAddTeam is POST /v1/admin/teams {"name", "members"}: 201 {"team_id"}.
func (s *server) adminAddTeam(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"name", "members"}, []string{"name", "members"})
	if err != nil {
		writeError(w, err)
		return
	}
	name, err := stringField(top, "name")
	if err != nil {
		writeError(w, err)
		return
	}
	members, err := stringsField(top, "members")
	if err != nil {
		writeError(w, err)
		return
	}
	id, err := s.Hub.CreateTeam(r.Context(), callerFrom(r.Context()), name, members)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"team_id": id})
}

// adminTeamMembers is POST /v1/admin/teams/{id}/members {"members"}: 204.
func (s *server) adminTeamMembers(w http.ResponseWriter, r *http.Request) {
	members, err := decodeMembers(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.AddTeamMembers(r.Context(), callerFrom(r.Context()), r.PathValue("id"), members); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminListTeams is GET /v1/admin/teams: {"teams": [{team_id, name, members}]}.
func (s *server) adminListTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := s.Hub.ListTeams(r.Context(), callerFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Teams []hub.TeamInfo `json:"teams"`
	}{teams})
}

// adminUnarchiveGroup is POST /v1/admin/groups/{id}/unarchive: 204.
func (s *server) adminUnarchiveGroup(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.UnarchiveGroup(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminDeadLetters is GET /v1/admin/dead-letters: {"dead_letters": […]}.
func (s *server) adminDeadLetters(w http.ResponseWriter, r *http.Request) {
	dls, err := s.Hub.DeadLetters(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DeadLetters []hub.DeadLetter `json:"dead_letters"`
	}{dls})
}

// adminRetryDeadLetter is POST /v1/admin/dead-letters/{dly}/retry: 204; the
// delivery is queued for push now and the push worker is kicked.
func (s *server) adminRetryDeadLetter(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.RetryDeadLetter(r.Context(), r.PathValue("dly")); err != nil {
		writeError(w, err)
		return
	}
	s.kick()
	w.WriteHeader(http.StatusNoContent)
}

// adminDiscardDeadLetter is POST /v1/admin/dead-letters/{dly}/discard: 204.
func (s *server) adminDiscardDeadLetter(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.DiscardDeadLetter(r.Context(), r.PathValue("dly")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminPatchThread is PATCH /v1/admin/threads/{id} {"budget_remaining"?,
// "title"?, "history_access"?, "locked_participants"?}: 204. At least one
// field is required; budget_remaining and title may be null (unlimited, no
// title).
func (s *server) adminPatchThread(w http.ResponseWriter, r *http.Request) {
	fields := []string{"budget_remaining", "title", "history_access", "locked_participants"}
	top, err := readObject(r, fields, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(top) == 0 {
		writeError(w, &envelope.SchemaError{Path: "", Message: "at least one of " + strings.Join(fields, ", ") + " is required"})
		return
	}
	var p hub.ThreadPatch
	if raw, has := top["budget_remaining"]; has {
		p.BudgetSet = true
		if !isNull(raw) {
			n, err := intField(top, "budget_remaining")
			if err != nil {
				writeError(w, err)
				return
			}
			p.Budget = &n
		}
	}
	if raw, has := top["title"]; has {
		p.TitleSet = true
		if !isNull(raw) {
			t, err := stringField(top, "title")
			if err != nil {
				writeError(w, err)
				return
			}
			p.Title = &t
		}
	}
	if _, has := top["history_access"]; has {
		v, err := stringField(top, "history_access")
		if err != nil {
			writeError(w, err)
			return
		}
		p.HistoryAccess = &v
	}
	if raw, has := top["locked_participants"]; has {
		var b bool
		if isNull(raw) || json.Unmarshal(raw, &b) != nil {
			writeError(w, &envelope.SchemaError{Path: "/locked_participants", Message: "locked_participants must be a boolean"})
			return
		}
		p.Locked = &b
	}
	if err := s.Hub.PatchThread(r.Context(), r.PathValue("id"), p); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminPurgeBlob is POST /v1/admin/blobs/{sha}/purge: 204.
func (s *server) adminPurgeBlob(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.PurgeBlob(r.Context(), r.PathValue("sha")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminSetNotify is PUT /v1/admin/notify {"routes": [{"url", "filter"}]}: 200
// {"routes": [{id, url, filter, whsec}]}. Replaces every route; an unchanged
// URL keeps its whsec_.
func (s *server) adminSetNotify(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"routes"}, []string{"routes"})
	if err != nil {
		writeError(w, err)
		return
	}
	var items []json.RawMessage
	if isNull(top["routes"]) || json.Unmarshal(top["routes"], &items) != nil {
		writeError(w, &envelope.SchemaError{Path: "/routes", Message: "routes must be an array"})
		return
	}
	specs := make([]hub.NotifyRouteSpec, 0, len(items))
	for i, item := range items {
		obj, err := decodeObject(item, []string{"url", "filter"}, []string{"url", "filter"})
		if err == nil {
			var spec hub.NotifyRouteSpec
			if spec.URL, err = stringField(obj, "url"); err == nil {
				spec.Filter, err = stringField(obj, "filter")
			}
			specs = append(specs, spec)
		}
		var se *envelope.SchemaError
		if errors.As(err, &se) {
			se.Path = "/routes/" + strconv.Itoa(i) + se.Path
		}
		if err != nil {
			writeError(w, err)
			return
		}
	}
	routes, err := s.Hub.SetNotifyRoutes(r.Context(), specs)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Routes []hub.NotifyRoute `json:"routes"`
	}{routes})
}

// adminAudit is GET /v1/admin/audit?limit=&before=: {"entries": […], "next"},
// newest first; next (null on the last page) is the before of the next page.
func (s *server) adminAudit(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var before int64
	if q := r.URL.Query(); q.Has("before") {
		before, err = strconv.ParseInt(q.Get("before"), 10, 64)
		if err != nil || before < 1 {
			e := hub.E(http.StatusUnprocessableEntity, "invalid_value", "before must be a positive whole number")
			e.Details["field"] = "before"
			writeError(w, e)
			return
		}
	}
	entries, next, err := s.Hub.AuditPage(r.Context(), limit, before)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Entries []hub.AuditEntry `json:"entries"`
		Next    *string          `json:"next"`
	}{entries, next})
}

// adminCounters is GET /v1/admin/counters: {"loop_cut", "rate_limited",
// "dead_letters", "push_conflicts", "push_auth_failed", "events_push_failed", "events_paused"}.
func (s *server) adminCounters(w http.ResponseWriter, r *http.Request) {
	c, err := s.Hub.Counters(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// adminPrincipalLimits replaces the operator's principal provisioning limits.
func (s *server) adminPrincipalLimits(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"max_peers", "creates_per_hour", "kinds"}, []string{"max_peers", "creates_per_hour", "kinds"})
	if err != nil {
		writeError(w, err)
		return
	}
	max, err := intField(top, "max_peers")
	if err != nil {
		writeError(w, err)
		return
	}
	rate, err := intField(top, "creates_per_hour")
	if err != nil {
		writeError(w, err)
		return
	}
	kinds, err := stringsField(top, "kinds")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetPrincipalLimits(r.Context(), r.PathValue("id"), hub.PrincipalLimits{MaxPeers: max, CreatesPerHour: rate, Kinds: kinds}); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminPrincipalDefaultScopes replaces defaults for subsequently created peers.
func (s *server) adminPrincipalDefaultScopes(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"allow", "deny"}, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	allow, err := stringsField(top, "allow")
	if err != nil {
		writeError(w, err)
		return
	}
	deny, err := stringsField(top, "deny")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.SetPrincipalDefaultScopes(r.Context(), r.PathValue("id"), allow, deny); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminListPrincipals exposes limits and counts without credentials.
func (s *server) adminListPrincipals(w http.ResponseWriter, r *http.Request) {
	principals, err := s.Hub.Principals(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	rows := make([]opapi.PrincipalRow, 0, len(principals))
	for _, p := range principals {
		rows = append(rows, opapi.PrincipalRow{PrincipalID: p.PrincipalID, Name: p.Name,
			MaxPeers: p.Limits.MaxPeers, CreatesPerHour: p.Limits.CreatesPerHour, Kinds: p.Limits.Kinds,
			Active: p.Active, Disabled: p.Disabled, CreatesLastHour: p.CreatesLastHour, ScopedPeers: p.ScopedPeers,
			DefaultAllow: p.DefaultAllow, DefaultDeny: p.DefaultDeny})
	}
	writeJSON(w, http.StatusOK, map[string]any{"principals": rows})
}
