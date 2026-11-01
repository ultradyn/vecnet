package oauth

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// cookieName holds the sealed subject of a returning principal.
const cookieName = "vecnet_oauth_sub"

// Sealed values carry a purpose: both kinds share a key, and a consent form's
// request, which anyone can obtain, must not open as a cookie.
const (
	purposeRequest = "authz_request"
	purposeCookie  = "consent_subject"
)

//go:embed consent.html
var consentHTML string

//go:embed consent.css
var consentCSSBody []byte

var consentPage = template.Must(template.New("consent").Parse(consentHTML))

// page is what consent.html renders: a consent form when Request is set, else
// a dead end showing Code and Error.
type page struct {
	ClientName, RedirectHost string
	Request                  string
	NeedCode                 bool
	Error, Code              string
}

// consentCSS serves the page's only style: the CSP forbids inline ones.
func (s *Server) consentCSS(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(consentCSSBody)
}

// writePage renders p. The form may only post here, and may only be
// redirected on to redirectURI's origin: browsers apply form-action to a
// form's redirect too, and default-src does not cover form-action.
func writePage(w http.ResponseWriter, status int, p page, redirectURI string) {
	formAction := "'self'"
	if u, err := url.Parse(redirectURI); err == nil && u.Host != "" {
		formAction += " " + u.Scheme + "://" + u.Host
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action "+formAction)
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if err := consentPage.Execute(w, p); err != nil {
		slog.Error("oauth: render consent page", "err", err)
	}
}

// deadEnd is a 400 page: used until redirect_uri is proven, after which errors
// go back to the client, never to an unproven URI (RFC 6749 §4.1.2.1).
func deadEnd(w http.ResponseWriter, code, message string) {
	writePage(w, http.StatusBadRequest, page{Code: code, Error: message}, "")
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("oauth: authorize", "err", err)
	writePage(w, http.StatusInternalServerError, page{Code: "server_error", Error: "Something went wrong on the hub."}, "")
}

// authRequest is an authorize request that passed validation, sealed into the
// consent form so that POST trusts exactly what GET checked.
type authRequest struct {
	Purpose     string `json:"p"`
	ClientID    string `json:"c"`
	RedirectURI string `json:"r"`
	Scope       string `json:"s"`
	Challenge   string `json:"h"`
	Resource    string `json:"a"`
	State       string `json:"t"`
	ExpiresMs   int64  `json:"e"`
}

// consentSubject is the sealed vecnet_oauth_sub value.
type consentSubject struct {
	Purpose   string `json:"p"`
	Subject   string `json:"s"`
	ExpiresMs int64  `json:"e"`
}

func (s *Server) seal(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("oauth: seal: %w", err)
	}
	return s.Hub.Seal(raw)
}

// openRequest unseals a consent form's request; ok is false for anything that
// is not one of ours, in date.
func (s *Server) openRequest(sealed string) (req authRequest, ok bool) {
	raw, err := s.Hub.Unseal(sealed)
	if err != nil || json.Unmarshal(raw, &req) != nil {
		return authRequest{}, false
	}
	return req, req.Purpose == purposeRequest && s.now().UnixMilli() < req.ExpiresMs
}

// principal is a bound OAuth principal.
type principal struct {
	PrnID, Subject string
}

// cookiePrincipal resolves the consent cookie to a principal whose peer is
// still enabled and still its own. Anything else is no principal: the page
// asks for a link code.
func (s *Server) cookiePrincipal(ctx context.Context, r *http.Request) (*principal, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, nil
	}
	raw, err := s.Hub.Unseal(c.Value)
	if err != nil {
		return nil, nil
	}
	var sub consentSubject
	if json.Unmarshal(raw, &sub) != nil || sub.Purpose != purposeCookie || s.now().UnixMilli() >= sub.ExpiresMs {
		return nil, nil
	}
	p := principal{Subject: sub.Subject}
	err = s.DB.QueryRowContext(ctx,
		`SELECT o.prn_id FROM oauth_principals o JOIN peers p ON p.peer_id = o.peer_id
		 WHERE o.subject = ? AND p.disabled = 0 AND p.prn_id = o.prn_id`, sub.Subject).Scan(&p.PrnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("oauth: find principal: %w", err)
	}
	return &p, nil
}

// clientLabel is what the page calls the client: its name, then a URL's host.
func clientLabel(c *client) string {
	if c.Name != "" {
		return c.Name
	}
	if u, err := url.Parse(c.ID); err == nil && u.Host != "" {
		return u.Host
	}
	return "An application"
}

func redirectHost(uri string) string {
	if u, err := url.Parse(uri); err == nil {
		return u.Host
	}
	return uri
}

// redirectWith sends the browser to uri with params added to its query.
func redirectWith(w http.ResponseWriter, r *http.Request, uri string, params map[string]string) {
	u, err := url.Parse(uri)
	if err != nil { // uri passed redirectAllowed, so it parsed before
		deadEnd(w, "invalid_request", "The redirect address is not valid.")
		return
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// getAuthorize validates the request and shows the consent page: a link code
// field at first login, Approve alone for a returning principal.
func (s *Server) getAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if repeated(q) {
		deadEnd(w, "invalid_request", "A parameter was given twice.")
		return
	}
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	if clientID == "" || redirectURI == "" {
		deadEnd(w, "invalid_request", "client_id and redirect_uri are required.")
		return
	}
	c, err := s.loadClient(r.Context(), clientID)
	switch {
	case errors.Is(err, errUnknownClient):
		deadEnd(w, "invalid_request", "This application is not known to the hub.")
		return
	case errors.Is(err, errTooManyClients):
		deadEnd(w, "temporarily_unavailable", "The hub is not accepting new applications right now.")
		return
	case err != nil:
		serverError(w, err)
		return
	}
	if !slices.Contains(c.Redirects, redirectURI) || !s.redirectAllowed(redirectURI) {
		deadEnd(w, "invalid_request", "This redirect address is not registered for the application.")
		return
	}

	state := q.Get("state")
	refuse := func(code string) {
		redirectWith(w, r, redirectURI, map[string]string{"error": code, "state": state})
	}
	scope := q.Get("scope")
	resource := q.Get("resource")
	switch {
	case q.Get("response_type") != "code":
		refuse("unsupported_response_type")
		return
	case q.Get("code_challenge_method") != "S256", !validChallenge(q.Get("code_challenge")):
		refuse("invalid_request")
		return
	case !scopeOK(scope):
		refuse("invalid_scope")
		return
	case resource != "" && resource != s.resource():
		refuse("invalid_target")
		return
	}
	if scope == "" {
		scope = Scope
	}
	sealed, err := s.seal(authRequest{
		Purpose: purposeRequest, ClientID: clientID, RedirectURI: redirectURI, Scope: scope,
		Challenge: q.Get("code_challenge"), Resource: s.resource(), State: state,
		ExpiresMs: s.now().Add(requestTTL).UnixMilli(),
	})
	if err != nil {
		serverError(w, err)
		return
	}
	p, err := s.cookiePrincipal(r.Context(), r)
	if err != nil {
		serverError(w, err)
		return
	}
	writePage(w, http.StatusOK, page{
		ClientName: clientLabel(c), RedirectHost: redirectHost(redirectURI), Request: sealed, NeedCode: p == nil,
	}, redirectURI)
}

// postAuthorize is the consent form: deny, approve as a returning principal,
// or spend a link code. A link code, its principal and the authorization code
// commit together, so a failure leaves the link code unspent.
func (s *Server) postAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || repeated(r.PostForm) {
		deadEnd(w, "invalid_request", "The form could not be read.")
		return
	}
	sealed := r.PostForm.Get("request")
	req, ok := s.openRequest(sealed)
	if !ok {
		deadEnd(w, "invalid_request", "This form has expired.")
		return
	}
	if r.PostForm.Get("approve") != "1" {
		redirectWith(w, r, req.RedirectURI, map[string]string{"error": "access_denied", "state": req.State})
		return
	}
	ctx := r.Context()
	p, err := s.cookiePrincipal(ctx, r)
	if err != nil {
		serverError(w, err)
		return
	}
	var code string
	var bound string // the peer a link code just bound, to wake its readers
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if p == nil {
			var err error
			if p, bound, err = s.bindLink(ctx, tx, req.ClientID, r.PostForm.Get("link_code")); err != nil {
				return err
			}
		}
		var err error
		code, err = s.mintCode(ctx, tx, req, p.PrnID)
		return err
	})
	if errors.Is(err, hub.ErrDotLink) {
		s.retryPage(w, r, req, sealed)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if bound != "" {
		s.Hub.Log.Notify(bound, p.PrnID) // the principal's peer set changed
	}
	if err := s.setConsentCookie(w, p.Subject); err != nil {
		serverError(w, err)
		return
	}
	redirectWith(w, r, req.RedirectURI, map[string]string{"code": code, "state": req.State})
}

// retryPage is the 400 for a link code that was refused: the same form again.
func (s *Server) retryPage(w http.ResponseWriter, r *http.Request, req authRequest, sealed string) {
	c, err := s.storedClient(r.Context(), req.ClientID)
	if err != nil {
		serverError(w, err)
		return
	}
	writePage(w, http.StatusBadRequest, page{
		ClientName: clientLabel(c), RedirectHost: redirectHost(req.RedirectURI), Request: sealed, NeedCode: true,
		Error: "That link code is not valid. Ask your operator for a new one.",
	}, req.RedirectURI)
}

// bindLink spends a link code and returns the principal it binds, and the
// peer: the principal already bound to that peer, so a lost cookie or a second
// browser stays the same account, or a new one.
func (s *Server) bindLink(ctx context.Context, tx *sql.Tx, clientID, linkCode string) (*principal, string, error) {
	peer, err := s.Hub.ConsumeDotLinkTx(ctx, tx, linkCode)
	if err != nil {
		return nil, "", err
	}
	var p principal
	err = tx.QueryRowContext(ctx, `SELECT prn_id, subject FROM oauth_principals WHERE peer_id = ?`, peer).
		Scan(&p.PrnID, &p.Subject)
	switch {
	case err == nil:
		if _, err := s.Hub.BindDotPrincipalTx(ctx, tx, peer, p.PrnID); err != nil {
			return nil, "", err
		}
		return &p, peer, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, "", fmt.Errorf("oauth: find principal: %w", err)
	}
	if p.PrnID, err = s.Hub.BindDotPrincipalTx(ctx, tx, peer, ""); err != nil {
		return nil, "", err
	}
	p.Subject = "oauth:" + clientID + ":" + randomID("")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_principals(prn_id, subject, peer_id, created_at) VALUES (?, ?, ?, ?)`,
		p.PrnID, p.Subject, peer, envelope.FormatTime(s.now())); err != nil {
		return nil, "", fmt.Errorf("oauth: insert principal: %w", err)
	}
	return &p, peer, nil
}

// mintCode stores a single-use code for req and returns it, once.
func (s *Server) mintCode(ctx context.Context, tx *sql.Tx, req authRequest, prn string) (string, error) {
	plain, hash := newSecret("vac_")
	now := s.now()
	// Expired codes are of no further use: a replay of one is refused as unknown.
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE expires_at <= ?`, envelope.FormatTime(now)); err != nil {
		return "", fmt.Errorf("oauth: prune codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_codes(code_hash, client_id, redirect_uri, scope, code_challenge, resource, principal_id, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hash, req.ClientID, req.RedirectURI, req.Scope, req.Challenge, req.Resource, prn,
		envelope.FormatTime(now.Add(codeTTL))); err != nil {
		return "", fmt.Errorf("oauth: insert code: %w", err)
	}
	return plain, nil
}

// setConsentCookie remembers the principal in this browser. SameSite=Lax sends
// it on the top-level GET from chatgpt.com and withholds it from a cross-site
// POST, so another site cannot approve for the user.
func (s *Server) setConsentCookie(w http.ResponseWriter, subject string) error {
	sealed, err := s.seal(consentSubject{
		Purpose: purposeCookie, Subject: subject, ExpiresMs: s.now().Add(consentCookieTTL).UnixMilli(),
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: sealed, Path: "/oauth/", MaxAge: int(consentCookieTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: strings.HasPrefix(s.Issuer, "https://"),
	})
	return nil
}
