package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/ingress"
)

// maxProviderID is $defs/origin's maxLength for event_id, which carries the
// provider's delivery id.
const maxProviderID = 192

// postIngress is POST /v1/ingress/{src_id} (PROTOCOL §2.11, §10). It takes
// no bearer: the source's own verification authenticates it. limitBody has
// capped the body, and nothing parses it before the signature has passed.
func (s *server) postIngress(w http.ResponseWriter, r *http.Request) {
	srcID := r.PathValue("src_id")
	kind, secret, err := s.Hub.SourceSecret(r.Context(), srcID)
	if err != nil {
		writeError(w, err)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	if kind != "github" {
		writeError(w, errors.New("httpapi: source of an unverifiable kind")) // AddSource creates none
		return
	}
	s.ingestGitHub(w, r, srcID, secret, raw)
}

// ingestGitHub verifies a GitHub delivery and hands it to the hub: 202
// {"message_id"} when accepted or redelivered, 202 {"dropped", "reason"}
// when the allowlist drops it, 200 {"ok"} for ping.
func (s *server) ingestGitHub(w http.ResponseWriter, r *http.Request, srcID, secret string, raw []byte) {
	hdr := map[string]string{}
	for _, name := range []string{"X-Hub-Signature-256", "X-GitHub-Event", "X-GitHub-Delivery"} {
		hdr[name] = r.Header.Get(name)
		if hdr[name] == "" {
			e := hub.E(http.StatusBadRequest, "invalid_body", "a required webhook header is missing")
			e.Details["header"] = name
			writeError(w, e)
			return
		}
	}
	delivery, event := hdr["X-GitHub-Delivery"], hdr["X-GitHub-Event"]
	if len(delivery) > maxProviderID {
		e := hub.E(http.StatusBadRequest, "invalid_body", "the delivery id is too long")
		e.Details["header"] = "X-GitHub-Delivery"
		writeError(w, e)
		return
	}
	if !ingress.VerifyGitHub(secret, hdr["X-Hub-Signature-256"], raw) {
		writeError(w, hub.E(http.StatusUnauthorized, "bad_signature", "the webhook signature does not verify"))
		return
	}
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	m, err := ingress.GitHubMessage(srcID, delivery, event, raw)
	if errors.Is(err, ingress.ErrBadPayload) {
		writeError(w, hub.E(http.StatusBadRequest, "invalid_body", "the webhook body is not a JSON object"))
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	msgID, dropped, err := s.Hub.IngestSource(r.Context(), m)
	if err != nil {
		writeError(w, err)
		return
	}
	if dropped {
		writeJSON(w, http.StatusAccepted, map[string]any{"dropped": true, "reason": "event_filtered"})
		return
	}
	s.kick()
	writeJSON(w, http.StatusAccepted, map[string]string{"message_id": msgID})
}

// adminAddSource is POST /v1/admin/sources {"kind", "name", "target",
// "event_allow"?, "priority"?}: 201 {"source_id", "ingress_url", "secret"}.
// The secret appears only in this response.
func (s *server) adminAddSource(w http.ResponseWriter, r *http.Request) {
	top, err := readObject(r, []string{"kind", "name", "target", "event_allow", "priority"}, []string{"kind", "name", "target"})
	if err != nil {
		writeError(w, err)
		return
	}
	var f [4]string
	for i, name := range []string{"kind", "name", "target", "priority"} {
		if f[i], err = stringField(top, name); err != nil {
			writeError(w, err)
			return
		}
	}
	allow, err := stringsField(top, "event_allow")
	if err != nil {
		writeError(w, err)
		return
	}
	id, url, secret, err := s.Hub.AddSource(r.Context(), f[0], f[1], f[2], allow, f[3])
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]string{"source_id": id, "ingress_url": url, "secret": secret})
}

// adminListSources is GET /v1/admin/sources: {"sources": [...]}, without
// any secret.
func (s *server) adminListSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.Hub.ListSources(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sources []hub.SourceInfo `json:"sources"`
	}{list})
}

// adminDisableSource is POST /v1/admin/sources/{id}/disable: 204.
func (s *server) adminDisableSource(w http.ResponseWriter, r *http.Request) {
	if err := readNoBody(r); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Hub.DisableSource(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
