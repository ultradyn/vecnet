// Package hubid signs hub identity proofs and notices using canonical v1 bytes.
// It has no dependency on the hub database, so clients can verify proofs.
package hubid

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Key holds the private Ed25519 hub identity, separate from the symmetric hub key.
type Key struct{ private ed25519.PrivateKey }

// KeyFromSeedHex reads a 32-byte seed encoded as exactly 64 hex characters.
func KeyFromSeedHex(seed string) (Key, error) {
	if len(seed) != ed25519.SeedSize*2 {
		return Key{}, errors.New("hubid: seed must be 64 hex characters")
	}
	raw, err := hex.DecodeString(seed)
	if err != nil {
		return Key{}, fmt.Errorf("hubid: invalid seed: %w", err)
	}
	return Key{private: ed25519.NewKeyFromSeed(raw)}, nil
}

// Pubkey returns the canonical ed25519:<unpadded base64url> public key.
// An uninitialized Key has no public key.
func (k Key) Pubkey() string {
	if len(k.private) != ed25519.PrivateKeySize {
		return ""
	}
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(k.private.Public().(ed25519.PublicKey))
}

// DecodePubkey accepts only the canonical 32-byte Ed25519 public key encoding.
func DecodePubkey(s string) (ed25519.PublicKey, error) {
	if len(s) != 51 || !strings.HasPrefix(s, "ed25519:") {
		return nil, errors.New("hubid: invalid public key")
	}
	raw, err := decodeCanonical(s[8:], ed25519.PublicKeySize)
	if err != nil {
		return nil, fmt.Errorf("hubid: public key: %w", err)
	}
	return ed25519.PublicKey(raw), nil
}

// Fingerprint is SHA256:<unpadded standard base64 of sha256(public key bytes)>.
func Fingerprint(pubkey string) (string, error) {
	pub, err := DecodePubkey(pubkey)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:]), nil
}

// NormalizeURL normalizes an absolute HTTP(S) hub base URL. Credentials,
// queries and fragments are forbidden, including empty query/fragment markers.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("hubid: URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Opaque != "" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(raw, "?#\r\n\t") {
		return "", errors.New("hubid: expected absolute HTTP(S) URL without userinfo, query or fragment")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("hubid: empty port")
	}
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", errors.New("hubid: invalid port")
		}
		if scheme == "http" && p == 80 || scheme == "https" && p == 443 {
			port = ""
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	// EscapedPath preserves encoded slashes: /a%2Fb is distinct from /a/b.
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

var noticeID = regexp.MustCompile(`^ntc_[0-9A-HJKMNP-TV-Z]{26}$`)

// NoticeBytes returns the canonical signed record. Clear metadata and Sig are
// deliberately excluded. Text is hashed as its exact UTF-8 bytes, never JSON.
func NoticeBytes(n envelope.HubNotice) ([]byte, error) {
	if !noticeID.MatchString(n.NoticeID) {
		return nil, errors.New("hubid: invalid notice id")
	}
	switch n.Kind {
	case "maintenance", "restart", "moving":
	default:
		return nil, errors.New("hubid: invalid notice kind")
	}
	for _, ts := range []string{n.IssuedAt, n.ExpiresAt} {
		if _, err := envelope.ParseTime(ts); err != nil {
			return nil, fmt.Errorf("hubid: notice timestamp: %w", err)
		}
	}
	if n.At != "" {
		if _, err := envelope.ParseTime(n.At); err != nil {
			return nil, fmt.Errorf("hubid: notice at: %w", err)
		}
	}
	hub, err := NormalizeURL(n.Hub)
	if err != nil {
		return nil, err
	}
	var next string
	if n.NewURL != "" {
		if n.Kind != "moving" {
			return nil, errors.New("hubid: new_url is only valid for moving notices")
		}
		next, err = NormalizeURL(n.NewURL)
		if err != nil {
			return nil, err
		}
	} else if n.Kind == "moving" {
		return nil, errors.New("hubid: moving notice requires new_url")
	}
	hash := sha256.Sum256([]byte(n.Message))
	return fmt.Appendf(nil, "vecnet-hub-notice-v1\nnotice_id=%s\nkind=%s\nhub=%s\nissued_at=%s\nat=%s\nexpires_at=%s\nnew_url=%s\nmessage_sha256=%x\n", n.NoticeID, n.Kind, hub, n.IssuedAt, n.At, n.ExpiresAt, next, hash), nil
}

// SignNotice sets Sig without changing any of the notice's structured fields.
func (k Key) SignNotice(n *envelope.HubNotice) error {
	if n == nil {
		return errors.New("hubid: nil notice")
	}
	raw, err := NoticeBytes(*n)
	if err != nil {
		return err
	}
	n.Sig, err = k.sign(raw)
	return err
}

// VerifyNotice verifies the canonical record against the pinned public key.
func VerifyNotice(pubkey string, n envelope.HubNotice) error {
	raw, err := NoticeBytes(n)
	if err != nil {
		return err
	}
	return verify(pubkey, raw, n.Sig)
}

func challengeBytes(publicURL, challenge string) ([]byte, error) {
	if !ValidChallenge(challenge) {
		return nil, errors.New("hubid: invalid challenge")
	}
	normalized, err := NormalizeURL(publicURL)
	if err != nil {
		return nil, err
	}
	return []byte("vecnet-hub-challenge-v1\n" + normalized + "\n" + challenge + "\n"), nil
}

// SignChallenge binds a fresh challenge to the normalized public URL.
func (k Key) SignChallenge(publicURL, challenge string) (string, error) {
	raw, err := challengeBytes(publicURL, challenge)
	if err != nil {
		return "", err
	}
	return k.sign(raw)
}

// VerifyChallenge verifies the URL-bound proof of identity.
func VerifyChallenge(pubkey, publicURL, challenge, sig string) error {
	raw, err := challengeBytes(publicURL, challenge)
	if err != nil {
		return err
	}
	return verify(pubkey, raw, sig)
}

func rotationBytes(old, next, at string) ([]byte, error) {
	if _, err := DecodePubkey(old); err != nil {
		return nil, err
	}
	if _, err := DecodePubkey(next); err != nil {
		return nil, err
	}
	if _, err := envelope.ParseTime(at); err != nil {
		return nil, fmt.Errorf("hubid: rotation timestamp: %w", err)
	}
	return []byte("vecnet-hub-rotation-v1\nold=" + old + "\nnew=" + next + "\nrotated_at=" + at + "\n"), nil
}

// Endorse signs a single-generation key rotation. Invalid input returns "";
// this interface has no error result, and an empty endorsement never verifies.
func (k Key) Endorse(newPubkey, rotatedAt string) string {
	raw, err := rotationBytes(k.Pubkey(), newPubkey, rotatedAt)
	if err != nil {
		return ""
	}
	sig, _ := k.sign(raw)
	return sig
}

// VerifyEndorsement checks that the previous key endorsed the current one.
func VerifyEndorsement(prev envelope.HubKeyPrevious, current string) error {
	raw, err := rotationBytes(prev.Pubkey, current, prev.RotatedAt)
	if err != nil {
		return err
	}
	return verify(prev.Pubkey, raw, prev.Endorsement)
}

// ValidChallenge requires canonical unpadded base64url encoding of 16–64 bytes.
func ValidChallenge(c string) bool {
	if len(c) < 22 || len(c) > 86 {
		return false
	}
	raw, err := decodeCanonical(c, -1)
	return err == nil && len(raw) >= 16 && len(raw) <= 64
}

func decodeCanonical(s string, size int) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || size >= 0 && len(raw) != size || base64.RawURLEncoding.EncodeToString(raw) != s {
		return nil, errors.New("invalid canonical base64url encoding")
	}
	return raw, nil
}

func (k Key) sign(raw []byte) (string, error) {
	if len(k.private) != ed25519.PrivateKeySize {
		return "", errors.New("hubid: uninitialized key")
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.private, raw)), nil
}

func verify(pubkey string, raw []byte, sig string) error {
	pub, err := DecodePubkey(pubkey)
	if err != nil {
		return err
	}
	decoded, err := decodeCanonical(sig, ed25519.SignatureSize)
	if err != nil {
		return fmt.Errorf("hubid: signature: %w", err)
	}
	if !ed25519.Verify(pub, raw, decoded) {
		return errors.New("hubid: invalid signature")
	}
	return nil
}
