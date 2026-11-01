package oauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

// Limits on client registration, which anyone may attempt. maxClients is the
// durable bound: at it, clients nothing has used for clientIdleAge give way
// (storeClient), and with none left DCR and new CIMD clients are refused, while
// clients already stored, and every issued token, keep working.
const (
	maxClients      = 10000
	maxClientBytes  = 16 << 10 // a registration body or CIMD document
	maxRedirectURIs = 16
	maxClientName   = 200
	cimdTimeout     = 30 * time.Second // the egress total [Global Constraints]
	clientIdleAge   = time.Hour        // far longer than a consent form lasts
)

var (
	// errUnknownClient is a client_id with no usable client behind it.
	errUnknownClient = errors.New("oauth: unknown client")
	// errTooManyClients is maxClients reached.
	errTooManyClients = errors.New("oauth: client limit reached")
	// callbackIDRE is what "{callback_id}" matches: one plain path segment.
	callbackIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// client is a stored or fetched OAuth client.
type client struct {
	ID        string
	Kind      string
	Name      string
	Redirects []string
	Fetched   time.Time // created_at: for a CIMD row, when it was last fetched
}

// redirectAllowed reports whether uri may ever be redirected to, against
// AllowedRedirects (DefaultRedirects explains entries). The URI is parsed, not
// prefix-matched: "http://127.0.0.1:1@evil.example/" starts like a loopback
// URI and is not one.
func (s *Server) redirectAllowed(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || strings.Contains(uri, "#") {
		return false
	}
	for _, a := range s.AllowedRedirects {
		if base, ok := strings.CutSuffix(a, ":*"); ok {
			b, err := url.Parse(base)
			if err == nil && u.Scheme == b.Scheme && u.Hostname() == b.Hostname() && validPort(u.Port()) {
				return true
			}
			continue
		}
		if base, ok := strings.CutSuffix(a, "{callback_id}"); ok {
			if id, ok := strings.CutPrefix(uri, base); ok && callbackIDRE.MatchString(id) {
				return true
			}
			continue
		}
		if uri == a {
			return true
		}
	}
	return false
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// registration is the RFC 7591 metadata the hub reads. Anything else a client
// sends is dropped, so what is stored stays bounded and known.
type registration struct {
	ClientID     string   `json:"client_id,omitempty"` // CIMD only
	ClientName   string   `json:"client_name,omitempty"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types,omitempty"`
	ResponseType []string `json:"response_types,omitempty"`
	AuthMethod   string   `json:"token_endpoint_auth_method,omitempty"`
	Scope        string   `json:"scope,omitempty"`
}

// check refuses a client that asks for anything but the code flow, which is
// how machine-to-machine grants are kept out (PROTOCOL §4.5), or whose name
// is oversized.
func (m *registration) check() error {
	for _, g := range m.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return errors.New("only authorization_code and refresh_token are supported")
		}
	}
	for _, r := range m.ResponseType {
		if r != "code" {
			return errors.New("only response_type code is supported")
		}
	}
	if !scopeOK(m.Scope) {
		return errors.New("only scope vecnet is supported")
	}
	if len(m.ClientName) > maxClientName {
		return errors.New("client_name is too long")
	}
	return nil
}

// register is RFC 7591 dynamic client registration. Every client is public:
// a requested client-secret method is replaced by "none" (§3.2.1 lets the
// server do that, and the response says so).
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxClientBytes))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "the metadata is too large or unreadable")
		return
	}
	var m registration
	if err := json.Unmarshal(raw, &m); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "the metadata is not a JSON object")
		return
	}
	if err := m.check(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	if len(m.RedirectURIs) == 0 || len(m.RedirectURIs) > maxRedirectURIs {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give between 1 and 16 redirect_uris")
		return
	}
	for _, u := range m.RedirectURIs {
		if !s.redirectAllowed(u) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "a redirect URI is not allowed")
			return
		}
	}
	m.ClientID = randomID("dcr_")
	m.AuthMethod = "none"
	if len(m.GrantTypes) == 0 {
		m.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	if len(m.ResponseType) == 0 {
		m.ResponseType = []string{"code"}
	}
	if m.Scope == "" {
		m.Scope = Scope
	}
	switch err := s.storeClient(r.Context(), "dcr", &m); {
	case errors.Is(err, errTooManyClients):
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "registration is closed for now")
		return
	case err != nil:
		slog.Error("oauth: register", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "internal")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  m.ClientID,
		"client_id_issued_at":        s.now().Unix(),
		"client_name":                m.ClientName,
		"redirect_uris":              m.RedirectURIs,
		"grant_types":                m.GrantTypes,
		"response_types":             m.ResponseType,
		"token_endpoint_auth_method": m.AuthMethod,
		"scope":                      m.Scope,
	})
}

// randomID is prefix + base64url of 32 random bytes.
func randomID(prefix string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("oauth: crypto/rand failed: " + err.Error())
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// evictIdleClients deletes DCR and CIMD rows older than clientIdleAge that no
// code or token names, so filling the table locks nobody out for good: such a
// client has done nothing, and it can register, or be fetched, again. It
// returns how many went.
func (s *Server) evictIdleClients(ctx context.Context, tx *sql.Tx) (int, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM oauth_clients WHERE kind IN ('dcr', 'cimd') AND created_at < ?
		   AND client_id NOT IN (SELECT client_id FROM oauth_codes)
		   AND client_id NOT IN (SELECT client_id FROM oauth_tokens)`,
		envelope.FormatTime(s.now().Add(-clientIdleAge)))
	if err != nil {
		return 0, fmt.Errorf("oauth: evict clients: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("oauth: evict clients: %w", err)
	}
	return int(n), nil
}

// storeClient writes m, replacing a CIMD row already cached under its id. A
// new row at maxClients evicts idle clients, and is errTooManyClients if that
// makes no room.
func (s *Server) storeClient(ctx context.Context, kind string, m *registration) error {
	meta, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("oauth: encode client: %w", err)
	}
	redirects, err := json.Marshal(m.RedirectURIs)
	if err != nil {
		return fmt.Errorf("oauth: encode client: %w", err)
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM oauth_clients WHERE client_id = ?), (SELECT count(*) FROM oauth_clients)`,
			m.ClientID).Scan(&exists, &n); err != nil {
			return fmt.Errorf("oauth: count clients: %w", err)
		}
		if !exists && n >= maxClients {
			gone, err := s.evictIdleClients(ctx, tx)
			if err != nil {
				return err
			}
			if n-gone >= maxClients {
				return errTooManyClients
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO oauth_clients(client_id, kind, metadata, redirect_uris, created_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(client_id) DO UPDATE SET metadata = excluded.metadata,
			   redirect_uris = excluded.redirect_uris, created_at = excluded.created_at`,
			m.ClientID, kind, string(meta), string(redirects), envelope.FormatTime(s.now())); err != nil {
			return fmt.Errorf("oauth: store client: %w", err)
		}
		return nil
	})
}

// storedClient reads a client row. No row is errUnknownClient.
func (s *Server) storedClient(ctx context.Context, id string) (*client, error) {
	var meta, redirects, at string
	c := &client{ID: id}
	err := s.DB.QueryRowContext(ctx,
		`SELECT kind, metadata, redirect_uris, created_at FROM oauth_clients WHERE client_id = ?`, id).
		Scan(&c.Kind, &meta, &redirects, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errUnknownClient
	}
	if err != nil {
		return nil, fmt.Errorf("oauth: find client: %w", err)
	}
	var m registration
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		return nil, fmt.Errorf("oauth: client metadata: %w", err)
	}
	if err := json.Unmarshal([]byte(redirects), &c.Redirects); err != nil {
		return nil, fmt.Errorf("oauth: client redirects: %w", err)
	}
	c.Name = m.ClientName
	if c.Fetched, err = envelope.ParseTime(at); err != nil {
		return nil, fmt.Errorf("oauth: client time: %w", err)
	}
	return c, nil
}

// isCIMD reports a client_id that is a metadata document URL.
func isCIMD(id string) bool { return strings.HasPrefix(id, "https://") }

// loadClient resolves client_id at authorize. A URL is a Client ID Metadata
// Document: served from its cached row for an hour, then fetched again. Any
// failure to get a usable document is errUnknownClient.
func (s *Server) loadClient(ctx context.Context, id string) (*client, error) {
	c, err := s.storedClient(ctx, id)
	switch {
	case err == nil && (c.Kind != "cimd" || s.now().Sub(c.Fetched) < cimdCacheTTL):
		return c, nil
	case err != nil && !(errors.Is(err, errUnknownClient) && isCIMD(id)):
		return nil, err
	}
	m, err := s.fetchCIMD(ctx, id)
	if err != nil {
		// The URL is attacker-chosen and may carry a credential: never logged.
		slog.Warn("oauth: client metadata document refused", "err", hub.EgressErr(err))
		return nil, errUnknownClient
	}
	if err := s.storeClient(ctx, "cimd", m); err != nil {
		return nil, err
	}
	return s.storedClient(ctx, id)
}

// fetchCIMD gets and validates the document at id through the egress guard:
// public addresses only, pinned, redirects refused, size capped. The document
// must name itself as client_id and be a public client of the code flow.
func (s *Server) fetchCIMD(ctx context.Context, id string) (*registration, error) {
	u, err := s.Egress.CheckURL(id)
	if err != nil {
		return nil, err
	}
	if u.Path == "" || u.Path == "/" || slices.ContainsFunc(strings.Split(u.Path, "/"), func(seg string) bool {
		return seg == "." || seg == ".."
	}) {
		return nil, errors.New("oauth: client metadata URL needs a plain path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, errors.New("oauth: bad client metadata URL")
	}
	req.Header.Set("Accept", "application/json")
	hc := s.Egress.Client(cimdTimeout)
	defer hc.CloseIdleConnections()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: client metadata status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxClientBytes+1))
	if err != nil {
		return nil, fmt.Errorf("oauth: read client metadata: %w", err)
	}
	if len(raw) > maxClientBytes {
		return nil, errors.New("oauth: client metadata too large")
	}
	var m registration
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("oauth: client metadata is not JSON")
	}
	if m.ClientID != id {
		return nil, errors.New("oauth: client metadata names another client_id")
	}
	if m.AuthMethod != "" && m.AuthMethod != "none" {
		return nil, errors.New("oauth: client metadata asks for client authentication")
	}
	if len(m.RedirectURIs) > maxRedirectURIs {
		return nil, errors.New("oauth: client metadata lists too many redirect URIs")
	}
	if err := m.check(); err != nil {
		return nil, fmt.Errorf("oauth: client metadata: %w", err)
	}
	return &m, nil
}
