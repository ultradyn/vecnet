package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
)

func openTemp(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func now() string { return envelope.FormatTime(time.Now()) }

func mustExec(t *testing.T, db *store.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func addPrincipal(t *testing.T, db *store.DB) string {
	t.Helper()
	prn := ids.New(ids.Prn)
	mustExec(t, db, `INSERT INTO principals(prn_id, name, created_at) VALUES (?, ?, ?)`, prn, "p", now())
	return prn
}

func addPeer(t *testing.T, db *store.DB, prn, kind string) string {
	t.Helper()
	peer := ids.New(ids.Peer)
	mustExec(t, db, `INSERT INTO peers(peer_id, prn_id, name, kind, enrolled_at) VALUES (?, ?, ?, ?, ?)`,
		peer, prn, "n", kind, now())
	return peer
}

func addCred(t *testing.T, db *store.DB, kind, subject string) string {
	t.Helper()
	plain, hash := auth.NewSecret(kind)
	mustExec(t, db, `INSERT INTO credentials(cred_id, kind, subject_id, secret_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		"cred_"+hash[:16], kind, subject, hash, now())
	return plain
}

func TestNewSecretShapeAndHash(t *testing.T) {
	plain, hash := auth.NewSecret("peer")
	if !regexp.MustCompile(`^vn_peer_[A-Za-z0-9_-]{43}$`).MatchString(plain) {
		t.Fatalf("plain %q has wrong shape", plain)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
		t.Fatalf("hash %q is not sha256 hex", hash)
	}
	if auth.HashSecret(plain) != hash {
		t.Fatalf("HashSecret(plain) != hash")
	}
	p2, h2 := auth.NewSecret("peer")
	if p2 == plain || h2 == hash {
		t.Fatalf("two secrets are equal")
	}
}

func TestResolvePeerAndPrincipal(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	r := &auth.Resolver{DB: db}
	prn := addPrincipal(t, db)
	peer := addPeer(t, db, prn, "session")
	peerSecret := addCred(t, db, "peer", peer)
	prnSecret := addCred(t, db, "principal", prn)

	c, err := r.Resolve(ctx, peerSecret)
	if err != nil {
		t.Fatalf("Resolve(peer): %v", err)
	}
	if c.Kind != "peer" || c.PeerID != peer || c.PrincipalID != prn || c.PeerKind != "session" {
		t.Fatalf("peer caller = %+v", c)
	}

	c, err = r.Resolve(ctx, prnSecret)
	if err != nil {
		t.Fatalf("Resolve(principal): %v", err)
	}
	if c.Kind != "principal" || c.PeerID != "" || c.PrincipalID != prn {
		t.Fatalf("principal caller = %+v", c)
	}

	// The operator credential belongs to the principal of the operator peer.
	opPrn := addPrincipal(t, db)
	opPeer := addPeer(t, db, opPrn, "operator")
	opSecret := addCred(t, db, "operator", opPrn)
	c, err = r.Resolve(ctx, opSecret)
	if err != nil {
		t.Fatalf("Resolve(operator): %v", err)
	}
	if c.Kind != "operator" || c.PrincipalID != opPrn || c.PeerID != opPeer || c.PeerKind != "operator" {
		t.Fatalf("operator caller = %+v", c)
	}
}

func TestResolveRevokedAndGarbage(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	r := &auth.Resolver{DB: db}
	prn := addPrincipal(t, db)
	peer := addPeer(t, db, prn, "session")
	secret := addCred(t, db, "peer", peer)
	if _, err := r.Resolve(ctx, secret); err != nil {
		t.Fatalf("Resolve before revoke: %v", err)
	}

	mustExec(t, db, `UPDATE credentials SET revoked_at = ? WHERE secret_hash = ?`, now(), auth.HashSecret(secret))
	if _, err := r.Resolve(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked: err = %v, want ErrUnauthenticated", err)
	}
	for _, b := range []string{"nope", ""} {
		if _, err := r.Resolve(ctx, b); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Resolve(%q): err = %v, want ErrUnauthenticated", b, err)
		}
	}

	// A disabled peer's live credential is also refused.
	peer2 := addPeer(t, db, prn, "session")
	secret2 := addCred(t, db, "peer", peer2)
	mustExec(t, db, `UPDATE peers SET disabled = 1 WHERE peer_id = ?`, peer2)
	if _, err := r.Resolve(ctx, secret2); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled: err = %v, want ErrUnauthenticated", err)
	}
}

func issue(t *testing.T, db *store.DB, r *auth.Resolver, peer string, ttl time.Duration) string {
	t.Helper()
	var tok string
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		var err error
		tok, _, err = r.IssueEnrolToken(context.Background(), tx, peer, ttl)
		return err
	})
	if err != nil {
		t.Fatalf("IssueEnrolToken: %v", err)
	}
	return tok
}

func TestEnrolIsSingleUse(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	r := &auth.Resolver{DB: db}
	prn := addPrincipal(t, db)
	peer := addPeer(t, db, prn, "session")

	tok := issue(t, db, r, peer, 0)
	if !strings.HasPrefix(tok, "vn_enrol_") {
		t.Fatalf("token %q lacks vn_enrol_ prefix", tok)
	}
	// Only the hash is stored, and the default TTL is 24 h.
	var stored, expires string
	if err := db.QueryRowContext(ctx, `SELECT token_hash, expires_at FROM enrol_tokens WHERE peer_id = ?`, peer).Scan(&stored, &expires); err != nil {
		t.Fatalf("read token row: %v", err)
	}
	if stored != auth.HashSecret(tok) {
		t.Fatalf("stored token_hash is not sha256(token)")
	}
	exp, err := envelope.ParseTime(expires)
	if err != nil {
		t.Fatalf("expires_at %q: %v", expires, err)
	}
	if d := time.Until(exp); d < 23*time.Hour+59*time.Minute || d > 24*time.Hour+time.Minute {
		t.Fatalf("default ttl = %v, want ~24h", d)
	}

	gotPeer, secret, err := r.Enrol(ctx, tok)
	if err != nil {
		t.Fatalf("Enrol: %v", err)
	}
	if gotPeer != peer {
		t.Fatalf("Enrol peer = %q, want %q", gotPeer, peer)
	}
	if !strings.HasPrefix(secret, "vn_peer_") {
		t.Fatalf("secret %q lacks vn_peer_ prefix", secret)
	}
	c, err := r.Resolve(ctx, secret)
	if err != nil || c.PeerID != peer {
		t.Fatalf("Resolve(enrolled secret) = %+v, %v", c, err)
	}
	var usedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT used_at FROM enrol_tokens WHERE token_hash = ?`, auth.HashSecret(tok)).Scan(&usedAt); err != nil || !usedAt.Valid {
		t.Fatalf("used_at not set: %v %v", usedAt, err)
	}

	if _, _, err := r.Enrol(ctx, tok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("second Enrol: err = %v, want ErrUnauthenticated", err)
	}

	expired := issue(t, db, r, peer, time.Hour)
	mustExec(t, db, `UPDATE enrol_tokens SET expires_at = ? WHERE token_hash = ?`,
		envelope.FormatTime(time.Now().Add(-time.Minute)), auth.HashSecret(expired))
	if _, _, err := r.Enrol(ctx, expired); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired Enrol: err = %v, want ErrUnauthenticated", err)
	}

	if _, _, err := r.Enrol(ctx, "vn_enrol_unknown"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("unknown Enrol: err = %v, want ErrUnauthenticated", err)
	}

	// No plaintext secret or token is stored anywhere we write.
	var leaks int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM credentials WHERE secret_hash IN (?, ?)) +
		(SELECT count(*) FROM enrol_tokens WHERE token_hash IN (?, ?)) +
		(SELECT count(*) FROM audit WHERE detail LIKE ? OR detail LIKE ? OR target_ids LIKE ? OR target_ids LIKE ?)`,
		secret, tok, secret, tok, "%"+secret+"%", "%"+tok+"%", "%"+secret+"%", "%"+tok+"%").Scan(&leaks); err != nil {
		t.Fatalf("leak query: %v", err)
	}
	if leaks != 0 {
		t.Fatalf("plaintext secret stored (%d rows)", leaks)
	}
}

func TestRotateRevokesOld(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	r := &auth.Resolver{DB: db}
	prn := addPrincipal(t, db)
	peer := addPeer(t, db, prn, "session")
	old := addCred(t, db, "peer", peer)

	fresh, err := r.Rotate(ctx, "peer", peer)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !strings.HasPrefix(fresh, "vn_peer_") || fresh == old {
		t.Fatalf("fresh secret %q", fresh)
	}
	if _, err := r.Resolve(ctx, old); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old after rotate: err = %v, want ErrUnauthenticated", err)
	}
	if c, err := r.Resolve(ctx, fresh); err != nil || c.PeerID != peer {
		t.Fatalf("fresh after rotate = %+v, %v", c, err)
	}

	if _, err := r.Rotate(ctx, "bogus", peer); err == nil {
		t.Fatalf("Rotate with bogus kind succeeded")
	}
	if _, err := r.Rotate(ctx, "peer", ids.New(ids.Peer)); err == nil {
		t.Fatalf("Rotate of unknown peer succeeded")
	}
}

func maySend(t *testing.T, db *store.DB, r *auth.Resolver, from, to string) bool {
	t.Helper()
	var ok bool
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		var err error
		ok, err = r.MaySend(context.Background(), tx, from, to)
		return err
	})
	if err != nil {
		t.Fatalf("MaySend: %v", err)
	}
	return ok
}

func TestScopes(t *testing.T) {
	db := openTemp(t)
	r := &auth.Resolver{DB: db}
	a, b, c, d := ids.New(ids.Peer), ids.New(ids.Peer), ids.New(ids.Peer), ids.New(ids.Peer)

	// No scope rows: allow all.
	if !maySend(t, db, r, a, b) {
		t.Fatalf("empty scopes: want true")
	}

	// Deny row for the target: false; other targets unaffected.
	mustExec(t, db, `INSERT INTO scopes(peer_id, mode, target_id) VALUES (?, 'deny', ?)`, a, b)
	if maySend(t, db, r, a, b) {
		t.Fatalf("deny row: want false")
	}
	if !maySend(t, db, r, a, c) {
		t.Fatalf("deny row for b must not block c")
	}

	// Any allow rows: only allow matches pass.
	mustExec(t, db, `INSERT INTO scopes(peer_id, mode, target_id) VALUES (?, 'allow', ?)`, c, d)
	if maySend(t, db, r, c, b) {
		t.Fatalf("allow row for other target: want false for this target")
	}
	if !maySend(t, db, r, c, d) {
		t.Fatalf("allow row for d: want true")
	}

	// Deny beats allow for the same target.
	mustExec(t, db, `INSERT INTO scopes(peer_id, mode, target_id) VALUES (?, 'deny', ?)`, c, d)
	if maySend(t, db, r, c, d) {
		t.Fatalf("deny and allow for same target: want false")
	}
}
