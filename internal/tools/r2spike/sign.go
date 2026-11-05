//go:build spike

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type credential struct{ account, bucket, endpoint, access, secret string }

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}
func escape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func canonicalQuery(q url.Values) string {
	var pairs []string
	for k, values := range q {
		for _, v := range values {
			pairs = append(pairs, escape(k)+"="+escape(v))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// presign keeps capabilities exclusively in memory. Callers must never format its
// return value or errors from an HTTP client (url.Error includes the full URL).
func presign(c credential, method, key string, h http.Header, now time.Time, ttl int, extra url.Values) (string, string, error) {
	if ttl < 1 || ttl > 604800 {
		return "", "", fmt.Errorf("invalid TTL")
	}
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return "", "", fmt.Errorf("invalid endpoint")
	}
	u.Path = "/" + c.bucket
	if key != "" {
		u.Path += "/" + key
	}
	// S3 SigV4 encodes plus signs even though net/url permits them in paths.
	parts := strings.Split(u.Path, "/")
	for i := range parts {
		parts[i] = escape(parts[i])
	}
	u.RawPath = strings.Join(parts, "/")
	names := []string{"host"}
	headers := map[string]string{"host": u.Host}
	for k, values := range h {
		k = strings.ToLower(k)
		if k == "host" || k == "authorization" {
			return "", "", fmt.Errorf("reserved header")
		}
		names = append(names, k)
		headers[k] = strings.Join(strings.Fields(strings.Join(values, ",")), " ")
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + headers[k] + "\n")
	}
	now = now.UTC()
	day := now.Format("20060102")
	scope := day + "/auto/s3/aws4_request"
	q := url.Values{}
	for k, v := range extra {
		q[k] = append([]string(nil), v...)
	}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", c.access+"/"+scope)
	q.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	q.Set("X-Amz-Expires", fmt.Sprint(ttl))
	q.Set("X-Amz-SignedHeaders", strings.Join(names, ";"))
	canonical := method + "\n" + u.EscapedPath() + "\n" + canonicalQuery(q) + "\n" + canon.String() + "\n" + strings.Join(names, ";") + "\nUNSIGNED-PAYLOAD"
	signingKey := mac(mac(mac(mac([]byte("AWS4"+c.secret), day), "auto"), "s3"), "aws4_request")
	q.Set("X-Amz-Signature", hex.EncodeToString(mac(signingKey, "AWS4-HMAC-SHA256\n"+now.Format("20060102T150405Z")+"\n"+scope+"\n"+digest([]byte(canonical)))))
	u.RawQuery = canonicalQuery(q)
	return u.String(), canonical, nil
}

func getTTL(now time.Time, expiry *time.Time) int {
	if expiry == nil {
		return 600
	}
	n := int(expiry.Sub(now) / time.Second)
	if n > 600 {
		n = 600
	}
	return n
}
