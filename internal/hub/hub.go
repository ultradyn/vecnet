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
	"path/filepath"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/egress"
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

	PresenceTTL        time.Duration // presence staleness for every kind but session (PROTOCOL §8)
	SessionPresenceTTL time.Duration // presence staleness for kind session

	// AllowPrivateEgress is the dev override (--allow-private-egress,
	// VECNET_ALLOW_PRIVATE_EGRESS=1): push, notify and other outbound URLs
	// may be http and may reach private and loopback addresses. Production
	// leaves it false (PROTOCOL §14.7).
	AllowPrivateEgress bool
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

		PresenceTTL:        DefaultPresenceTTL,
		SessionPresenceTTL: DefaultSessionPresenceTTL,
	}
}

// Hub is the service.
type Hub struct {
	DB    *store.DB
	Log   *eventlog.Log
	Blobs *blob.Store
	Auth  *auth.Resolver
	Cfg   Config
	Now   func() time.Time

	// IdleHook, when set, runs after a peer reports presence idle. New sets
	// it to OnIdle (the held wake_idle release); nil means no hook. It is
	// best-effort: SetPresence logs its error and still succeeds.
	IdleHook func(ctx context.Context, peerID string) error

	// NotifyClient posts operator notification webhooks; nil means
	// EgressPolicy().Client(NotifyTimeout): public-IP-only, pinned, never
	// following redirects.
	NotifyClient *http.Client
	// NotifyRetry is the wait before each retry of a failed notification;
	// nil means DefaultNotifyRetry (3 retries over 30 s).
	NotifyRetry []time.Duration

	reachMu sync.Mutex     // serialises reachability writes with streams
	streams map[string]int // peer → open SSE streams

	notifySem chan struct{}  // bounds notification goroutines (MaxNotifyInFlight)
	notifyWG  sync.WaitGroup // notification goroutines in flight

	notifyClientOnce sync.Once
	notifyClient     *http.Client // the default NotifyClient, built once
}

// EgressPolicy is the outbound-request guard every hub-initiated POST to a
// configured URL uses (PROTOCOL §14.7); Cfg.AllowPrivateEgress relaxes it.
func (h *Hub) EgressPolicy() egress.Policy {
	return egress.Policy{AllowPrivate: h.Cfg.AllowPrivateEgress}
}

// invalidURL is the 422 invalid_url for a configured URL the egress guard
// refuses; err comes from egress.Policy.CheckURL and never quotes the URL.
func invalidURL(field string, err error) *APIError {
	e := E(http.StatusUnprocessableEntity, "invalid_url",
		"the URL must be https, without credentials or fragment, to a public host")
	e.Details["field"] = field
	e.Details["reason"] = err.Error()
	return e
}

// New builds a Hub over an opened store; the blob store lives under
// Cfg.DataDir/blobs, so DataDir is required. A nil now means time.Now.
func New(db *store.DB, cfg Config, now func() time.Time) (*Hub, error) {
	if db == nil {
		return nil, errors.New("hub: nil store")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("hub: Config.DataDir is required")
	}
	if now == nil {
		now = time.Now
	}
	log, err := eventlog.New(db)
	if err != nil {
		return nil, fmt.Errorf("hub: %w", err)
	}
	blobs, err := blob.Open(filepath.Join(cfg.DataDir, "blobs"), db, now)
	if err != nil {
		return nil, fmt.Errorf("hub: %w", err)
	}
	// No stream survives a restart: a stored "connected" is stale. It falls
	// back to reachable as from an inbound call, so it decays 24 h after the
	// last_seen the stream recorded (EffectiveReachability).
	if _, err := db.ExecContext(context.Background(),
		`UPDATE peers SET reachability = 'reachable', reach_via = 'call' WHERE reachability = 'connected'`); err != nil {
		return nil, fmt.Errorf("hub: reset reachability: %w", err)
	}
	// No outbound attempt survives a restart either: release every dispatch
	// claim, so §17.5 can withdraw those wakes again (BeginDispatch).
	if _, err := db.ExecContext(context.Background(),
		`UPDATE deliveries SET dispatching = 0 WHERE dispatching > 0`); err != nil {
		return nil, fmt.Errorf("hub: reset dispatch claims: %w", err)
	}
	h := &Hub{DB: db, Log: log, Blobs: blobs, Auth: &auth.Resolver{DB: db, Now: now}, Cfg: cfg, Now: now,
		streams: map[string]int{}, notifySem: make(chan struct{}, MaxNotifyInFlight)}
	h.IdleHook = h.OnIdle // release held wake_idle deliveries (PROTOCOL §5.2)
	return h, nil
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
		if err := h.insertPeer(ctx, tx, peerID, prn, name, "operator", now); err != nil {
			return err
		}
		if secret, err = insertCredential(ctx, tx, "operator", prn, now); err != nil {
			return err
		}
		created = true
		if err := h.DirectoryUpdated(ctx, tx, peerID, "enrolled", enrolledFields); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "operator_init", []string{prn, peerID}, nil)
	})
	if err != nil {
		return "", "", false, err
	}
	if created {
		h.notifyEnabled(ctx)
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
// principal is a 422 invalid_value, and so is kind "operator" under any
// principal but the operator's.
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
		if kind == "operator" {
			// Operator powers key on peers.kind (PROTOCOL §16.1, §16.3), so
			// the kind is reserved to the operator principal: the one holding
			// an operator credential.
			var one int
			err := tx.QueryRowContext(ctx,
				`SELECT 1 FROM credentials WHERE subject_id = ? AND kind = 'operator' LIMIT 1`, prnID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				e := E(http.StatusUnprocessableEntity, "invalid_value", "kind operator is reserved to the operator principal")
				e.Details["field"] = "kind"
				return e
			}
			if err != nil {
				return fmt.Errorf("hub: find operator credential: %w", err)
			}
		}
		peerID = ids.New(ids.Peer)
		if err := h.insertPeer(ctx, tx, peerID, prnID, name, kind, now); err != nil {
			return err
		}
		// A dot authenticates by OAuth after a link code and never holds a
		// vecnet secret (PROTOCOL §12), so it gets nothing to enrol with.
		if kind != "dot" {
			var err error
			if enrolToken, err = h.Auth.IssueEnrolToken(ctx, tx, peerID, 0); err != nil {
				return err
			}
		}
		// "peer add" is enrolment (PROTOCOL §2.12): the peer is in the
		// directory from here on.
		if err := h.DirectoryUpdated(ctx, tx, peerID, "enrolled", enrolledFields); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "operator", "peer_add", []string{peerID, prnID}, map[string]any{"kind": kind})
	})
	if err != nil {
		return "", "", err
	}
	h.notifyEnabled(ctx)
	h.Log.Notify(prnID) // the principal's peer set changed; the new peer's log is empty
	return peerID, enrolToken, nil
}

func insertPrincipal(ctx context.Context, tx *sql.Tx, prn, name, now string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO principals(prn_id, name, created_at) VALUES (?, ?, ?)`, prn, name, now); err != nil {
		return fmt.Errorf("hub: insert principal: %w", err)
	}
	return nil
}

// insertPeer inserts a peer row whose rate limits are Cfg.SendsPerMin and
// Cfg.ThreadsPerHour (the column defaults, 60 and 20, when those are unset).
func (h *Hub) insertPeer(ctx context.Context, tx *sql.Tx, peer, prn, name, kind, now string) error {
	perMin, perHour := h.Cfg.SendsPerMin, h.Cfg.ThreadsPerHour
	if perMin <= 0 {
		perMin = 60
	}
	if perHour <= 0 {
		perHour = 20
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO peers(peer_id, prn_id, name, kind, enrolled_at, sends_per_min, threads_per_hour)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		peer, prn, name, kind, now, perMin, perHour); err != nil {
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
