// Package webhook implements Standard Webhooks signing and verification
// for vecnet push routes [PROTOCOL §3.1, §3.5].
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header names carried on every push request [§3.1].
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

const (
	secretPrefix  = "whsec_"
	sigPrefix     = "v1,"
	minKeyBytes   = 24
	maxKeyBytes   = 64
	maxSignatures = 8
	tolerance     = 300 // seconds, either direction
)

var (
	// ErrBadSecret: the secret lacks the whsec_ prefix, is not base64, or
	// does not decode to 24..64 bytes.
	ErrBadSecret = errors.New("webhook: bad secret")
	// ErrTimestamp: the timestamp header is not an integer or lies outside ±300 s.
	ErrTimestamp = errors.New("webhook: timestamp outside tolerance")
	// ErrSignature: no signature matches any of the given secrets.
	ErrSignature = errors.New("webhook: no matching signature")
)

// NewSecret returns "whsec_" + base64(32 random bytes).
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("webhook: crypto/rand failed: " + err.Error())
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(b)
}

// KeyBytes strips "whsec_" and base64-decodes the remainder; the result is
// the HMAC key, never the literal secret string.
func KeyBytes(secret string) ([]byte, error) {
	rest, ok := strings.CutPrefix(secret, secretPrefix)
	if !ok {
		return nil, ErrBadSecret
	}
	k, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(k) < minKeyBytes || len(k) > maxKeyBytes {
		return nil, ErrBadSecret
	}
	return k, nil
}

func mac(key []byte, id, ts string, body []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id))
	m.Write([]byte{'.'})
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	return m.Sum(nil)
}

// Sign returns "v1," + base64(HMAC-SHA256(key, id+"."+unix+"."+body)).
func Sign(secret, id string, ts time.Time, body []byte) (string, error) {
	key, err := KeyBytes(secret)
	if err != nil {
		return "", err
	}
	unix := strconv.FormatInt(ts.Unix(), 10)
	return sigPrefix + base64.StdEncoding.EncodeToString(mac(key, id, unix, body)), nil
}

// SignAll signs with every secret and space-joins the results, so pushes
// during a rotation overlap carry both signatures [§3.5].
func SignAll(secrets []string, id string, ts time.Time, body []byte) (string, error) {
	sigs := make([]string, 0, len(secrets))
	for _, s := range secrets {
		sig, err := Sign(s, id, ts, body)
		if err != nil {
			return "", err
		}
		sigs = append(sigs, sig)
	}
	return strings.Join(sigs, " "), nil
}

// Verify checks a push against the raw body bytes. The timestamp must lie
// within ±300 s of now (ErrTimestamp); the header may carry at most 8
// space-separated "v1," signatures, each 32 bytes, compared in constant time;
// any signature matching any secret → nil, otherwise ErrSignature.
// Secrets that fail KeyBytes are skipped.
func Verify(secrets []string, id, tsHeader, sigHeader string, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	if d := now.Unix() - ts; d > tolerance || d < -tolerance {
		return ErrTimestamp
	}
	parts := strings.Fields(sigHeader)
	if len(parts) == 0 || len(parts) > maxSignatures {
		return ErrSignature
	}
	sigs := make([][]byte, 0, len(parts))
	for _, p := range parts {
		enc, ok := strings.CutPrefix(p, sigPrefix)
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(raw) != sha256.Size {
			continue
		}
		sigs = append(sigs, raw)
	}
	if len(sigs) == 0 {
		return ErrSignature
	}
	matched := false
	for _, s := range secrets {
		key, err := KeyBytes(s)
		if err != nil {
			continue
		}
		want := mac(key, id, tsHeader, body) // signed content uses the header exactly as sent
		for _, got := range sigs {
			if hmac.Equal(got, want) {
				matched = true
			}
		}
	}
	if !matched {
		return ErrSignature
	}
	return nil
}
