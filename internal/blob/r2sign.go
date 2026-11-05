package blob

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrR2Signing deliberately contains no endpoint, key, credentials or capability.
var ErrR2Signing = errors.New("blob: invalid R2 signing input")

var r2BucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// PresignPUT binds a create-only single PUT to exact length, checksum and safe
// metadata. It performs no HTTP; metadata transport and redirect policy belong
// to the R2 adapter. Callers must not log the returned bearer capability.
func PresignPUT(endpoint, bucket, key, accessID, secret string, now time.Time, ttl time.Duration, headers http.Header) (string, error) {
	signed, _, _, err := r2Presign(http.MethodPut, endpoint, bucket, key, accessID, secret, now, ttl, headers)
	return signed, err
}

// PresignGET signs only GET on the specified object, capped to 600 seconds.
// Supply R2DownloadTTL(now, object.ExpiresAt) to enforce the logical deadline.
func PresignGET(endpoint, bucket, key, accessID, secret string, now time.Time, ttl time.Duration) (string, error) {
	signed, _, _, err := r2Presign(http.MethodGet, endpoint, bucket, key, accessID, secret, now, ttl, nil)
	return signed, err
}

func r2Presign(method, endpoint, bucket, key, accessID, secret string, now time.Time, ttl time.Duration, headers http.Header) (string, string, string, error) {
	fail := func() (string, string, string, error) { return "", "", "", ErrR2Signing }
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(endpoint, "#") {
		return fail()
	}
	if !r2BucketPattern.MatchString(bucket) || strings.Contains(bucket, "..") || key == "" || len(key) > 1024 || !r2SafeText(key) || accessID == "" || secret == "" || !r2SafeText(accessID) || !r2SafeText(secret) || strings.ContainsAny(accessID, "/ ") || now.IsZero() {
		return fail()
	}
	if method == http.MethodPut {
		if ttl != R2UploadTTL {
			return fail()
		}
	} else if method == http.MethodGet {
		if ttl > R2DownloadMaxTTL {
			ttl = R2DownloadMaxTTL
		}
	} else {
		return fail()
	}
	if ttl < time.Second {
		return fail()
	}
	seconds := int64(ttl / time.Second)
	values := map[string]string{"host": u.Host}
	if method == http.MethodPut {
		var ok bool
		values, ok = r2PUTHeaders(headers, u.Host)
		if !ok {
			return fail()
		}
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + values[name] + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	date := now.UTC().Format("20060102")
	stamp := now.UTC().Format("20060102T150405Z")
	scope := date + "/auto/s3/aws4_request"
	query := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {accessID + "/" + scope},
		"X-Amz-Date":          {stamp},
		"X-Amz-Expires":       {strconv.FormatInt(seconds, 10)},
		"X-Amz-SignedHeaders": {signedHeaders},
	}
	// Encode bytes with AWS's unreserved set, preserving every key slash and
	// never applying path.Clean (empty and dot segments are object key bytes).
	path := r2Escape("/"+bucket+"/"+key, true)
	canonical := method + "\n" + path + "\n" + r2CanonicalQuery(query) + "\n" + canonicalHeaders.String() + "\n" + signedHeaders + "\nUNSIGNED-PAYLOAD"
	digest := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	signingKey := r2HMAC([]byte("AWS4"+secret), date)
	signingKey = r2HMAC(signingKey, "auto")
	signingKey = r2HMAC(signingKey, "s3")
	signingKey = r2HMAC(signingKey, "aws4_request")
	query.Set("X-Amz-Signature", hex.EncodeToString(r2HMAC(signingKey, stringToSign)))
	// Construct from validated origin and the exact encoded path to avoid URL
	// normalization or a '+' path being treated as unescaped by net/url.
	return u.Scheme + "://" + u.Host + path + "?" + r2CanonicalQuery(query), canonical, stringToSign, nil
}

func r2PUTHeaders(headers http.Header, host string) (map[string]string, bool) {
	required := map[string]string{
		"content-type":                       "application/octet-stream",
		"content-disposition":                "attachment",
		"cache-control":                      "private, no-store",
		"if-none-match":                      "*",
		"x-amz-meta-vecnet-checksum-profile": "sha256",
	}
	allowed := map[string]bool{"content-length": true, "x-amz-checksum-sha256": true, "x-amz-meta-vecnet-checksum": true, "host": true}
	for k := range required {
		allowed[k] = true
	}
	values := map[string]string{}
	for name, list := range headers {
		lower := strings.ToLower(name)
		if !allowed[lower] || len(list) != 1 || !r2SafeText(list[0]) {
			return nil, false
		}
		if _, exists := values[lower]; exists {
			return nil, false
		}
		values[lower] = strings.Join(strings.Fields(list[0]), " ")
	}
	for name, want := range required {
		if values[name] != want {
			return nil, false
		}
	}
	length, err := strconv.ParseInt(values["content-length"], 10, 64)
	if err != nil || length < 1 || length > DefaultMaxSize || strconv.FormatInt(length, 10) != values["content-length"] {
		return nil, false
	}
	checksum, err := base64.StdEncoding.Strict().DecodeString(values["x-amz-checksum-sha256"])
	if err != nil || len(checksum) != sha256.Size || base64.StdEncoding.EncodeToString(checksum) != values["x-amz-checksum-sha256"] || values["x-amz-meta-vecnet-checksum"] != values["x-amz-checksum-sha256"] {
		return nil, false
	}
	if given, ok := values["host"]; ok && given != host {
		return nil, false
	}
	values["host"] = host
	return values, true
}

func r2SafeText(value string) bool {
	for _, b := range []byte(value) {
		if b < 32 || b == 127 {
			return false
		}
	}
	return true
}

func r2Escape(value string, slashes bool) string {
	const digits = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(value) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c)) || slashes && c == '/' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(digits[c>>4])
			b.WriteByte(digits[c&15])
		}
	}
	return b.String()
}

func r2CanonicalQuery(values url.Values) string {
	type pair struct{ name, value string }
	var pairs []pair
	for k, list := range values {
		for _, v := range list {
			pairs = append(pairs, pair{r2Escape(k, false), r2Escape(v, false)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name != pairs[j].name {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].value < pairs[j].value
	})
	encoded := make([]string, len(pairs))
	for i, p := range pairs {
		encoded[i] = p.name + "=" + p.value
	}
	return strings.Join(encoded, "&")
}

func r2HMAC(key []byte, value string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(value))
	return m.Sum(nil)
}
