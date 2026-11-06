package oauth

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
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
// a dead end showing Code and Error. PeerName and PeerID name the peer a
// returning cookie would approve. NeedCode is a first login; a returning
// page leaves it false and still offers a link code.
type page struct {
	ClientName, RedirectHost string
	Request                  string
	NeedCode                 bool
	PeerName, PeerID         string
	NewClient                bool // a returning principal, and a client it has not approved here
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
	if p.Code != "" {
		oauthReason(w, p.Code)
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
		oauthReason(w, "server_error")
	}
}

// deadEnd is a 400 page: used until redirect_uri is proven, after which errors
// go back to the client, never to an unproven URI (RFC 6749 §4.1.2.1).
func deadEnd(w http.ResponseWriter, code, message string) {
	writePage(w, http.StatusBadRequest, page{Code: code, Error: message}, "")
}

func serverError(w http.ResponseWriter, err error) {
	writePage(w, http.StatusInternalServerError, page{Code: "server_error", Error: "Something went wrong on the hub."}, "")
}

// authRequest is an authorize request that passed validation, sealed into the
// consent form so that POST trusts exactly what GET checked. Shown is the
// consent-cookie subject the page displayed, empty when it showed no
// returning peer. Cookie-only approval must match it.
type authRequest struct {
	Purpose     string `json:"p"`
	ClientID    string `json:"c"`
	RedirectURI string `json:"r"`
	Scope       string `json:"s"`
	Challenge   string `json:"h"`
	Resource    string `json:"a"`
	State       string `json:"t"`
	ExpiresMs   int64  `json:"e"`
	Shown       string `json:"d,omitempty"`
}

// errConsentChanged means the cookie-only approval would bind a different
// subject from the one the form displayed.
var errConsentChanged = errors.New("oauth: displayed consent subject changed")

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

// principal is a bound OAuth principal and the peer the consent page names.
type principal struct {
	PrnID, Subject string
	PeerID, Name   string
}

// subjectFor is the subject of a principal first approved through clientID.
func subjectFor(clientID, random string) string { return "oauth:" + clientID + ":" + random }

// approved reports whether clientID is the client this principal first
// approved. A client's name is its own to choose, so one link could dress
// another client as a familiar one; the page says so when they differ.
func (p *principal) approved(clientID string) bool {
	return strings.HasPrefix(p.Subject, subjectFor(clientID, ""))
}

// querier is the store or a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// cookiePrincipal resolves the consent cookie to a principal that is still
// bound, to a peer still enabled and still its own. Anything else is no
// principal: the page asks for a link code.
func (s *Server) cookiePrincipal(ctx context.Context, q querier, r *http.Request) (*principal, error) {
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
	err = q.QueryRowContext(ctx,
		`SELECT o.prn_id, p.peer_id, p.name FROM oauth_principals o JOIN peers p ON p.peer_id = o.peer_id
		 WHERE o.subject = ? AND p.disabled = 0 AND p.prn_id = o.prn_id`, sub.Subject).Scan(&p.PrnID, &p.PeerID, &p.Name)
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
	if reason := params["error"]; reason != "" {
		oauthReason(w, reason)
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

// getAuthorize validates the request and shows the consent page. A first login
// asks for a link code. A returning principal is shown by peer name and id
// and may approve as that peer, or use a link code instead (PROTOCOL §4.5).
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
		logRedirectRefused(redirectURI, c.Name, "/oauth/authorize")
		deadEnd(w, "invalid_request", "This redirect address is not registered for the application.")
		return
	}

	state := q.Get("state")
	refuse := func(code string) {
		redirectWith(w, r, redirectURI, map[string]string{"error": code, "state": state, "iss": s.Issuer})
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
	// Fresh consent grants the complete dot capability set, including when an
	// older client still asks only for vecnet. Refresh never adds these scopes.
	scope = AuthorizationScopes
	p, err := s.cookiePrincipal(r.Context(), s.DB, r)
	if err != nil {
		serverError(w, err)
		return
	}
	shown := ""
	if p != nil {
		shown = p.Subject
	}
	sealed, err := s.seal(authRequest{
		Purpose: purposeRequest, ClientID: clientID, RedirectURI: redirectURI, Scope: scope,
		Challenge: q.Get("code_challenge"), Resource: s.resource(), State: state,
		ExpiresMs: s.now().Add(requestTTL).UnixMilli(), Shown: shown,
	})
	if err != nil {
		serverError(w, err)
		return
	}
	oauthReason(w, "consent_required")
	pg := page{
		ClientName: clientLabel(c), RedirectHost: redirectHost(redirectURI), Request: sealed, NeedCode: p == nil,
		NewClient: p != nil && !p.approved(clientID),
	}
	if p != nil {
		pg.PeerName, pg.PeerID = p.Name, p.PeerID
	}
	writePage(w, http.StatusOK, pg, redirectURI)
}

// postAuthorize is the consent form: deny, approve as a returning principal,
// or spend a link code. A non-empty link_code is bound and the cookie
// principal is ignored; on success setConsentCookie stores that new subject
// (PROTOCOL §4.5). An empty code still approves the cookie's own peer. A link
// code, its principal and the authorization code commit together, so a failure
// leaves the link code unspent. The cookie path reads the cookie in the same
// transaction, so a link code that retires that principal first leaves the
// cookie unable to mint another code.
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
		redirectWith(w, r, req.RedirectURI, map[string]string{"error": "access_denied", "state": req.State, "iss": s.Issuer})
		return
	}
	ctx := r.Context()
	var p *principal
	var code string
	var bound string // the peer a link code just bound, to wake its readers
	switch worth, err := s.linkCodeWorth(ctx, r); {
	case err != nil:
		serverError(w, err)
		return
	case worth == linkLocked:
		s.lockedPage(w, r, req, sealed, s.Hub.DotLinkLocked())
		return
	case worth == linkRefused:
		s.refuseLink(w, r, req, sealed)
		return
	}
	submitted := strings.TrimSpace(r.PostForm.Get("link_code")) != ""
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if submitted {
			if p, bound, err = s.bindLink(ctx, tx, req.ClientID, r.PostForm.Get("link_code")); err != nil {
				return err
			}
		} else if p, err = s.cookiePrincipal(ctx, tx, r); err != nil {
			return err
		} else if p == nil || p.Subject != req.Shown {
			// No cookie and no displayed peer still needs a link code. Any
			// other mismatch would approve someone the page did not show.
			if p == nil && req.Shown == "" {
				return hub.ErrDotLink
			}
			return errConsentChanged
		}
		if p == nil {
			return hub.ErrDotLink
		}
		code, err = s.mintCode(ctx, tx, req, p.PrnID)
		return err
	})
	if errors.Is(err, errConsentChanged) {
		s.refreshConsent(w, r, req)
		return
	}
	if errors.Is(err, hub.ErrDotLink) {
		s.refuseLink(w, r, req, sealed)
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
	if bound != "" {
		oauthReason(w, "linked")
	} else {
		oauthReason(w, "authorized")
	}
	oauthScopes(w, strings.Fields(req.Scope))
	redirectWith(w, r, req.RedirectURI, map[string]string{"code": code, "state": req.State, "iss": s.Issuer})
}

// linkWorth is linkCodeWorth's answer.
type linkWorth int

const (
	linkLive    linkWorth = iota // an empty code with a good cookie, or a code that could be spent
	linkRefused                  // a code that is not live, or no cookie and no code
	linkLocked                   // no cookie, and link codes are locked out (§20.1)
)

// linkCodeWorth reports whether this request can get anywhere in a write
// transaction. A non-empty link code is worth a transaction only when it
// could be spent; a consent cookie does not stand in for it. An empty code
// is worth a transaction only when the cookie names a live principal. This
// form is public, so a stream of guesses must not take the store's one write
// lock. Without a cookie, a lockout refuses every code before it is looked at
// (PROTOCOL §20.1). A returning principal is never locked out.
func (s *Server) linkCodeWorth(ctx context.Context, r *http.Request) (linkWorth, error) {
	code := strings.TrimSpace(r.PostForm.Get("link_code"))
	p, err := s.cookiePrincipal(ctx, s.DB, r)
	if err != nil {
		return linkRefused, err
	}
	if code != "" {
		if p == nil && s.Hub.DotLinkLocked() > 0 {
			return linkLocked, nil
		}
		live, err := s.Hub.DotLinkLive(ctx, code)
		if err != nil || !live {
			return linkRefused, err
		}
		return linkLive, nil
	}
	if p != nil {
		return linkLive, nil
	}
	if s.Hub.DotLinkLocked() > 0 {
		return linkLocked, nil
	}
	return linkRefused, nil
}

// refuseLink answers a refused link code: the retry page, after the refusal
// is counted (hub.DotLinkFailed) when a code was typed and no valid consent
// cookie is present (PROTOCOL §20.1). A wrong code with a live cookie is
// refused without falling back to that cookie and without spending the budget.
// The refusal that spends the budget gets the lockout page instead.
func (s *Server) refuseLink(w http.ResponseWriter, r *http.Request, req authRequest, sealed string) {
	if strings.TrimSpace(r.PostForm.Get("link_code")) != "" {
		p, err := s.cookiePrincipal(r.Context(), s.DB, r)
		if err != nil {
			serverError(w, err)
			return
		}
		if p == nil {
			if locked := s.Hub.DotLinkFailed(); locked > 0 {
				s.lockedPage(w, r, req, sealed, locked)
				return
			}
		}
	}
	s.retryPage(w, r, req, sealed)
}

// refreshConsent refuses a cookie-only approval whose cookie subject is no
// longer the one the form displayed, and re-seals the form for the subject
// the browser would approve now. The original expiry is kept.
func (s *Server) refreshConsent(w http.ResponseWriter, r *http.Request, req authRequest) {
	c, err := s.storedClient(r.Context(), req.ClientID)
	if err != nil {
		serverError(w, err)
		return
	}
	p, err := s.cookiePrincipal(r.Context(), s.DB, r)
	if err != nil {
		serverError(w, err)
		return
	}
	req.Shown = ""
	msg := "The peer this browser would approve changed. Enter a link code to continue."
	if p != nil {
		req.Shown = p.Subject
		msg = "The peer this browser would approve changed. Review the peer shown above, then approve again."
	}
	sealed, err := s.seal(req)
	if err != nil {
		serverError(w, err)
		return
	}
	oauthReason(w, "consent_changed")
	pg := page{
		ClientName: clientLabel(c), RedirectHost: redirectHost(req.RedirectURI), Request: sealed,
		NeedCode: p == nil, NewClient: p != nil && !p.approved(req.ClientID), Error: msg,
	}
	if p != nil {
		pg.PeerName, pg.PeerID = p.Name, p.PeerID
	}
	writePage(w, http.StatusBadRequest, pg, req.RedirectURI)
}

// retryPage is the 400 for a link code that was refused: the same form again.
func (s *Server) retryPage(w http.ResponseWriter, r *http.Request, req authRequest, sealed string) {
	oauthReason(w, "link_invalid")
	s.formPage(w, r, req, sealed, http.StatusBadRequest, "That link code is not valid. Ask your operator for a new one.")
}

// lockedPage is the 429 while link codes are locked out: too many wrong codes
// were tried, every live code was cancelled, and a new one from the operator
// ends the lockout.
func (s *Server) lockedPage(w http.ResponseWriter, r *http.Request, req authRequest, sealed string, retry time.Duration) {
	secs := int(retry.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	oauthReason(w, "link_locked")
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.formPage(w, r, req, sealed, http.StatusTooManyRequests,
		"Too many wrong link codes were tried, so every link code was cancelled. Ask your operator for a new one.")
}

func (s *Server) formPage(w http.ResponseWriter, r *http.Request, req authRequest, sealed string, status int, msg string) {
	c, err := s.storedClient(r.Context(), req.ClientID)
	if err != nil {
		serverError(w, err)
		return
	}
	pg := page{
		ClientName: clientLabel(c), RedirectHost: redirectHost(req.RedirectURI), Request: sealed, NeedCode: true,
		Error: msg,
	}
	if p, err := s.cookiePrincipal(r.Context(), s.DB, r); err != nil {
		serverError(w, err)
		return
	} else if p != nil {
		pg.NeedCode = false
		pg.PeerName, pg.PeerID = p.Name, p.PeerID
	}
	writePage(w, status, pg, req.RedirectURI)
}

// bindLink spends a link code and binds its peer to a new principal, which it
// returns with the peer. Nothing about a ChatGPT login reaches the hub, so a
// code cannot show that its holder is the account already bound to the peer:
// each code starts a new account and ends the one before it (retirePrincipal).
// That is also how an operator re-homes a dot or cuts an account off. A
// returning account gets here only when the form submits a link code; the
// cookie principal is ignored, and a successful bind replaces the cookie.
func (s *Server) bindLink(ctx context.Context, tx *sql.Tx, clientID, linkCode string) (*principal, string, error) {
	peer, err := s.Hub.ConsumeDotLinkTx(ctx, tx, linkCode)
	if err != nil {
		return nil, "", err
	}
	if err := s.retirePrincipal(ctx, tx, peer); err != nil {
		return nil, "", err
	}
	p := principal{Subject: subjectFor(clientID, randomID(""))}
	if p.PrnID, err = s.Hub.BindDotPrincipalTx(ctx, tx, peer); err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_principals(prn_id, subject, peer_id, created_at) VALUES (?, ?, ?, ?)`,
		p.PrnID, p.Subject, peer, envelope.FormatTime(s.now())); err != nil {
		return nil, "", fmt.Errorf("oauth: insert principal: %w", err)
	}
	return &p, peer, nil
}

// retirePrincipal ends the grant of the principal bound to peer, if any: its
// tokens and unspent codes end here, and its row stays, unbound, which is what
// stops its consent cookie.
func (s *Server) retirePrincipal(ctx context.Context, tx *sql.Tx, peer string) error {
	var prn string
	err := tx.QueryRowContext(ctx, `SELECT prn_id FROM oauth_principals WHERE peer_id = ?`, peer).Scan(&prn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("oauth: find principal: %w", err)
	}
	now := envelope.FormatTime(s.now())
	for _, step := range []struct{ query, name string }{
		{`UPDATE oauth_principals SET peer_id = NULL WHERE prn_id = ?2`, "unbind principal"},
		{`UPDATE oauth_tokens SET revoked_at = ?1 WHERE principal_id = ?2 AND revoked_at IS NULL`, "revoke tokens"},
		{`UPDATE oauth_codes SET used_at = ?1 WHERE principal_id = ?2 AND used_at IS NULL`, "spend codes"},
	} {
		if _, err := tx.ExecContext(ctx, step.query, now, prn); err != nil {
			return fmt.Errorf("oauth: %s: %w", step.name, err)
		}
	}
	return audit.Record(ctx, tx, "hub", "oauth_principal_retire", []string{peer, prn}, nil)
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
