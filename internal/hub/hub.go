// Package hub is the vecnet service: every write goes through a Hub method
// inside one store transaction. The HTTP layer (internal/httpapi) only maps
// requests onto these methods and their *APIError results onto the wire.
package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
)

// Config holds the hub's tunables. DefaultConfig returns the Global
// Constraints values of the core-hub plan (PROTOCOL §2.2, §6, §16).
type Config struct {
	PublicURL       string        // externally visible origin, no trailing slash
	DataDir         string        // holds hub.db and blobs/
	DefaultMaxDepth int           // loop.max_depth when a send omits it (range 1–64)
	DefaultTTL      time.Duration // loop.expires_at = now + DefaultTTL when omitted
	ThreadBudget    int           // budget_remaining of a new thread
	TextCap         int           // Message content.text bytes before auto-spill
	SendsPerMin     int           // per-peer send rate limit
	ThreadsPerHour  int           // per-peer new-thread rate limit
	BlobTokenTTL    time.Duration // lifetime of a blob URL token
}

// DefaultConfig returns the plan's defaults; PublicURL and DataDir are empty.
func DefaultConfig() Config {
	return Config{
		DefaultMaxDepth: 32,
		DefaultTTL:      24 * time.Hour,
		ThreadBudget:    500,
		TextCap:         4096,
		SendsPerMin:     60,
		ThreadsPerHour:  20,
		BlobTokenTTL:    7 * 24 * time.Hour,
	}
}

// Hub is the service. The blob store (Task 9) joins this struct when its
// package exists.
type Hub struct {
	DB   *store.DB
	Log  *eventlog.Log
	Auth *auth.Resolver
	Cfg  Config
	Now  func() time.Time
}

// New builds a Hub over an opened store. A nil now means time.Now.
func New(db *store.DB, cfg Config, now func() time.Time) (*Hub, error) {
	if db == nil {
		return nil, errors.New("hub: nil store")
	}
	if now == nil {
		now = time.Now
	}
	log, err := eventlog.New(db)
	if err != nil {
		return nil, fmt.Errorf("hub: %w", err)
	}
	return &Hub{DB: db, Log: log, Auth: &auth.Resolver{DB: db}, Cfg: cfg, Now: now}, nil
}

// PeerKinds are the peers.kind values the hub accepts (core-hub plan Task 5).
var PeerKinds = map[string]bool{
	"grokbot": true, "dot": true, "openclaw": true, "pagchat-bot": true,
	"session": true, "operator": true, "source": true,
}

func (h *Hub) nowStr() string { return envelope.FormatTime(h.Now()) }

// EnsureOperator creates the operator principal, its operator peer
// (peers.kind = 'operator') and its operator credential, once. When an
// operator peer already exists it returns that peer with secret "" and
// created false; the secret is returned only by the call that created it.
func (h *Hub) EnsureOperator(ctx context.Context, name string) (peerID, secret string, created bool, err error) {
	if name == "" {
		return "", "", false, E(http.StatusUnprocessableEntity, "invalid_value", "operator name is required")
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT p.peer_id FROM peers p JOIN credentials c ON c.subject_id = p.prn_id AND c.kind = 'operator'
			 WHERE p.kind = 'operator' ORDER BY p.peer_id LIMIT 1`).Scan(&peerID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("hub: find operator: %w", err)
		}
		now := h.nowStr()
		prn := ids.New(ids.Prn)
		if err := insertPrincipal(ctx, tx, prn, name, now); err != nil {
			return err
		}
		peerID = ids.New(ids.Peer)
		if err := insertPeer(ctx, tx, peerID, prn, name, "operator", now); err != nil {
			return err
		}
		if secret, err = insertCredential(ctx, tx, "operator", prn, now); err != nil {
			return err
		}
		created = true
		return audit.Record(ctx, tx, "operator", "operator_init", []string{prn, peerID}, nil)
	})
	if err != nil {
		return "", "", false, err
	}
	return peerID, secret, created, nil
}

// AddPrincipal creates a principal named name and its principal credential,
// returning the "prn_" id and the secret, once.
func (h *Hub) AddPrincipal(ctx context.Context, name string) (prnID, secret string, err error) {
	if name == "" {
		return "", "", E(http.StatusUnprocessableEntity, "invalid_value", "principal name is required")
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := h.nowStr()
		prnID = ids.New(ids.Prn)
		if err := insertPrincipal(ctx, tx, prnID, name, now); err != nil {
			return err
		}
		if secret, err = insertCredential(ctx, tx, "principal", prnID, now); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "principal_add", []string{prnID}, nil)
	})
	if err != nil {
		return "", "", err
	}
	return prnID, secret, nil
}

// AddPeer creates a peer of kind under prnID and returns its id and a
// one-time enrolment token. prnID "" creates an implicit single-peer
// principal named like the peer (PROTOCOL §2.12). An unknown kind or
// principal is a 422 invalid_value.
func (h *Hub) AddPeer(ctx context.Context, kind, name, prnID string) (peerID, enrolToken string, err error) {
	if !PeerKinds[kind] {
		e := E(http.StatusUnprocessableEntity, "invalid_value", "unknown peer kind")
		e.Details["field"] = "kind"
		return "", "", e
	}
	if name == "" {
		e := E(http.StatusUnprocessableEntity, "invalid_value", "peer name is required")
		e.Details["field"] = "name"
		return "", "", e
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := h.nowStr()
		if prnID == "" {
			prnID = ids.New(ids.Prn)
			if err := insertPrincipal(ctx, tx, prnID, name, now); err != nil {
				return err
			}
		} else {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM principals WHERE prn_id = ?`, prnID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				e := E(http.StatusUnprocessableEntity, "invalid_value", "unknown principal")
				e.Details["field"] = "principal_id"
				return e
			}
			if err != nil {
				return fmt.Errorf("hub: find principal: %w", err)
			}
		}
		peerID = ids.New(ids.Peer)
		if err := insertPeer(ctx, tx, peerID, prnID, name, kind, now); err != nil {
			return err
		}
		var err error
		if enrolToken, err = h.Auth.IssueEnrolToken(ctx, tx, peerID, 0); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "peer_add", []string{peerID, prnID}, map[string]any{"kind": kind})
	})
	if err != nil {
		return "", "", err
	}
	return peerID, enrolToken, nil
}

func insertPrincipal(ctx context.Context, tx *sql.Tx, prn, name, now string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO principals(prn_id, name, created_at) VALUES (?, ?, ?)`, prn, name, now); err != nil {
		return fmt.Errorf("hub: insert principal: %w", err)
	}
	return nil
}

func insertPeer(ctx context.Context, tx *sql.Tx, peer, prn, name, kind, now string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO peers(peer_id, prn_id, name, kind, enrolled_at) VALUES (?, ?, ?, ?, ?)`,
		peer, prn, name, kind, now); err != nil {
		return fmt.Errorf("hub: insert peer: %w", err)
	}
	return nil
}

// insertCredential stores the hash of a fresh secret of kind for subject and
// returns the plaintext.
func insertCredential(ctx context.Context, tx *sql.Tx, kind, subject, now string) (string, error) {
	plain, hash := auth.NewSecret(kind)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credentials(cred_id, kind, subject_id, secret_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		ids.New(ids.Prefix("cred")), kind, subject, hash, now); err != nil {
		return "", fmt.Errorf("hub: insert credential: %w", err)
	}
	return plain, nil
}
