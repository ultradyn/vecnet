package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"regexp"
)

// RFC 7636: a verifier is 43–128 unreserved characters; an S256 challenge is
// the base64url of a SHA-256 digest, always 43 characters.
var (
	verifierRE  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

func validChallenge(c string) bool { return challengeRE.MatchString(c) }

// verifyPKCE reports whether verifier answers an S256 challenge, in constant
// time. A malformed verifier never does.
func verifyPKCE(verifier, challenge string) bool {
	if !verifierRE.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}
