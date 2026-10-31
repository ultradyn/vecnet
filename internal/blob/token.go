package blob

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// TokenPrefix starts every blob read token.
const TokenPrefix = "rt_"

// tokenEnc is unpadded base64url in strict mode: non-zero trailing bits are
// rejected, so every token has exactly one valid spelling.
var tokenEnc = base64.RawURLEncoding.Strict()

func (s *Store) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write(payload)
	return m.Sum(nil)
}

// Token returns a read token for sha scoped to recipient, valid until exp
// (whole seconds): "rt_" + base64url(exp_unix|recipient|sha|HMAC-SHA256(Key,
// exp_unix|recipient|sha)). PROTOCOL §11, §14.6.
func (s *Store) Token(recipient, sha string, exp time.Time) string {
	payload := []byte(strconv.FormatInt(exp.Unix(), 10) + "|" + recipient + "|" + sha)
	raw := append(append(payload, '|'), s.mac(payload)...)
	return TokenPrefix + tokenEnc.EncodeToString(raw)
}

// VerifyToken reports the recipient tok was issued to when tok is authentic,
// names exactly sha, and now is before its expiry.
func (s *Store) VerifyToken(tok, sha string, now time.Time) (recipient string, ok bool) {
	if !ValidSHA(sha) {
		return "", false
	}
	enc, found := strings.CutPrefix(tok, TokenPrefix)
	if !found {
		return "", false
	}
	raw, err := tokenEnc.DecodeString(enc)
	const macLen = sha256.Size
	if err != nil || len(raw) < macLen+1 || raw[len(raw)-macLen-1] != '|' {
		return "", false
	}
	payload, mac := raw[:len(raw)-macLen-1], raw[len(raw)-macLen:]
	if !hmac.Equal(mac, s.mac(payload)) {
		return "", false
	}
	parts := bytes.Split(payload, []byte("|"))
	if len(parts) != 3 || string(parts[2]) != sha || len(parts[1]) == 0 {
		return "", false
	}
	exp, err := strconv.ParseInt(string(parts[0]), 10, 64)
	if err != nil || !now.Before(time.Unix(exp, 0)) {
		return "", false
	}
	return string(parts[1]), true
}

// URL is the blob_url of sha for recipient: publicURL + "/v1/blobs/" + sha +
// "?t=" + Token.
func (s *Store) URL(publicURL, recipient, sha string, exp time.Time) string {
	return publicURL + "/v1/blobs/" + sha + "?t=" + s.Token(recipient, sha, exp)
}
