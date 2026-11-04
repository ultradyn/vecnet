package hub

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/store"
)

// loadIdentity takes one snapshot of the identity metadata at startup. Rotation
// is offline: a running hub keeps its loaded key until it is restarted.
func (h *Hub) loadIdentity(ctx context.Context) error {
	var seed string
	var previous sql.NullString
	if err := h.DB.QueryRowContext(ctx, `SELECT seed.value, created.value,
		(SELECT value FROM meta WHERE key = 'hub_identity_prev')
		FROM meta seed JOIN meta created ON created.key = 'hub_identity_created_at'
		WHERE seed.key = 'hub_identity_seed'`).Scan(&seed, &h.identityCreatedAt, &previous); err != nil {
		return fmt.Errorf("hub: load identity: %w", err)
	}
	key, err := hubid.KeyFromSeedHex(seed)
	if err != nil {
		return fmt.Errorf("hub: load identity: %w", err)
	}
	if _, err := envelope.ParseTime(h.identityCreatedAt); err != nil {
		return fmt.Errorf("hub: identity creation time: %w", err)
	}
	if previous.Valid {
		var prev envelope.HubKeyPrevious
		if err := json.Unmarshal([]byte(previous.String), &prev); err != nil {
			return fmt.Errorf("hub: previous identity: %w", err)
		}
		if err := hubid.VerifyEndorsement(prev, key.Pubkey()); err != nil {
			return fmt.Errorf("hub: previous identity: %w", err)
		}
		h.identityPrevious = &prev
	}
	h.identity = key
	return nil
}

// IdentityKey returns the signing identity, separate from meta.hub_key.
func (h *Hub) IdentityKey() hubid.Key { return h.identity }

// IdentityDoc returns the public identity and an optional URL-bound proof.
// An empty challenge requests metadata only.
func (h *Hub) IdentityDoc(challenge string) (envelope.HubIdentity, error) {
	if challenge != "" && !hubid.ValidChallenge(challenge) {
		return envelope.HubIdentity{}, invalidIdentityChallenge()
	}
	publicURL, err := hubid.NormalizeURL(h.Cfg.PublicURL)
	if err != nil {
		return envelope.HubIdentity{}, fmt.Errorf("hub: identity public URL: %w", err)
	}
	fp, err := hubid.Fingerprint(h.identity.Pubkey())
	if err != nil {
		return envelope.HubIdentity{}, err
	}
	doc := envelope.HubIdentity{Pubkey: h.identity.Pubkey(), Fingerprint: fp,
		PublicURL: publicURL, KeyCreatedAt: h.identityCreatedAt}
	if h.identityPrevious != nil {
		prev := *h.identityPrevious
		doc.Previous = &prev
	}
	if challenge != "" {
		doc.ChallengeSig, err = h.identity.SignChallenge(publicURL, challenge)
	}
	return doc, err
}

func invalidIdentityChallenge() *APIError {
	e := E(http.StatusUnprocessableEntity, "invalid_value", "challenge must be unpadded base64url encoding of 16–64 bytes")
	e.Details["field"] = "challenge"
	return e
}

// RotateIdentity replaces the identity seed and records one previous key's
// endorsement atomically. Call with the hub stopped. The symmetric hub_key,
// which seals route/source secrets and signs blob tokens, is never changed.
func RotateIdentity(ctx context.Context, db *store.DB, now time.Time) (oldFP, newFP string, err error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", "", fmt.Errorf("hub: identity randomness: %w", err)
	}
	seedHex := hex.EncodeToString(seed)
	next, err := hubid.KeyFromSeedHex(seedHex)
	if err != nil {
		return "", "", err
	}
	at := envelope.FormatTime(now)
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		var oldSeed string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'hub_identity_seed'`).Scan(&oldSeed); err != nil {
			return fmt.Errorf("hub: read identity for rotation: %w", err)
		}
		old, err := hubid.KeyFromSeedHex(oldSeed)
		if err != nil {
			return err
		}
		prev := envelope.HubKeyPrevious{Pubkey: old.Pubkey(), RotatedAt: at, Endorsement: old.Endorse(next.Pubkey(), at)}
		if err := hubid.VerifyEndorsement(prev, next.Pubkey()); err != nil {
			return fmt.Errorf("hub: endorse rotation: %w", err)
		}
		raw, err := json.Marshal(prev)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES
			('hub_identity_seed', ?), ('hub_identity_created_at', ?), ('hub_identity_prev', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, seedHex, at, string(raw)); err != nil {
			return fmt.Errorf("hub: write rotated identity: %w", err)
		}
		oldFP, err = hubid.Fingerprint(old.Pubkey())
		return err
	})
	if err != nil {
		return "", "", err
	}
	newFP, err = hubid.Fingerprint(next.Pubkey())
	return oldFP, newFP, err
}
