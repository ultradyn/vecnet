//go:build spike

// This manual provider acceptance tool is excluded from ordinary builds/tests.
// Run on x-boss through just bb; credentials and capabilities never leave memory.
package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type observation struct {
	Name      string `json:"name"`
	At        string `json:"at"`
	Status    string `json:"status"`
	HTTP      int    `json:"http,omitempty"`
	Code      string `json:"provider_code,omitempty"`
	Transport string `json:"transport,omitempty"`
	Note      string `json:"note,omitempty"`
}
type report struct {
	Started      string            `json:"started"`
	Finished     string            `json:"finished,omitempty"`
	RunID        string            `json:"run_id"`
	Profile      string            `json:"selected_checksum_profile"`
	Gates        map[string]string `json:"gates"`
	Observations []observation     `json:"observations"`
	Fixtures     []string          `json:"retained_fixtures"`
	Followups    []report          `json:"followups,omitempty"`
}
type response struct {
	status    int
	code      string
	transport string
	headers   http.Header
	body      []byte
}
type runner struct {
	c                     credential
	client                *http.Client
	report                report
	keys                  map[string]bool
	keep                  map[string]bool
	expPrefix, keepPrefix string
	lastPut               time.Time
	baseline              *report
}

var netDialer = net.Dialer{Timeout: 10 * time.Second}

const reportPath = "docs/plans/receipts/2026-11-05-r2-spike.data.json"

func main() {
	// Never let panic formatting expose a capability, credential, request or XML.
	defer func() {
		if recover() != nil {
			fmt.Fprintln(os.Stderr, "spike stopped: internal failure (details suppressed)")
			os.Exit(2)
		}
	}()
	if os.Getenv("VECNET_BB") != "1" {
		fmt.Fprintln(os.Stderr, "spike requires the build-box lane")
		os.Exit(2)
	}
	replacePrevious := false
	followup := false
	if len(os.Args) == 2 && os.Args[1] == "--replace-previous-fixtures" {
		replacePrevious = true
	} else if len(os.Args) == 2 && os.Args[1] == "--cors-browser-followup" {
		followup = true
	} else if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "unknown spike arguments")
		os.Exit(2)
	}
	c, err := loadCredential(os.Getenv("VECNET_R2_SPIKE_CREDENTIALS_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "credential file rejected before network access")
		os.Exit(2)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		fmt.Fprintln(os.Stderr, "randomness unavailable")
		os.Exit(2)
	}
	runID := time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(id[:])
	r := &runner{c: c, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: false}}, keys: map[string]bool{}, keep: map[string]bool{}, expPrefix: "attachments/v1/exp/1h/spike-" + runID + "/", keepPrefix: "attachments/v1/keep/spike-" + runID + "/"}
	r.report = report{Started: stamp(), RunID: runID, Profile: "STOP", Gates: map[string]string{}}
	if followup && !r.loadBaseline() {
		fmt.Fprintln(os.Stderr, "completed baseline receipt rejected; no network access attempted")
		os.Exit(2)
	}
	if replacePrevious && !r.replacePreviousFixtures() {
		os.Exit(2)
	}
	if followup {
		r.runFollowup()
	} else {
		r.run()
	}
	r.report.Finished = stamp()
	r.save()
	fmt.Printf("spike finished; selected_checksum_profile=%s; sanitized receipt=%s\n", r.report.Profile, reportPath)
	for _, gate := range r.report.Gates {
		if gate == "FAIL" {
			os.Exit(1)
		}
	}
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339) }
func (r *runner) save() {
	b, err := json.MarshalIndent(r.combinedReport(), "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll("docs/plans/receipts", 0755) != nil {
		return
	}
	if os.WriteFile(reportPath, b, 0644) != nil {
		fmt.Fprintln(os.Stderr, "receipt write failed")
	}
}
func (r *runner) record(name string, ok bool, x response, note string) bool {
	status := "FAIL"
	if ok {
		status = "PASS"
	}
	r.report.Gates[name] = status
	r.report.Observations = append(r.report.Observations, observation{Name: name, At: stamp(), Status: status, HTTP: x.status, Code: x.code, Transport: x.transport, Note: note})
	r.save()
	fmt.Printf("%s %s http=%d code=%s transport=%s\n", name, status, x.status, x.code, x.transport)
	return ok
}
func (r *runner) outstanding(name, note string) {
	r.report.Gates[name] = "OUTSTANDING"
	r.report.Observations = append(r.report.Observations, observation{Name: name, At: stamp(), Status: "OUTSTANDING", Note: note})
	r.save()
}
func safeCode(body []byte) string {
	var e struct {
		Code string `xml:"Code"`
	}
	if xml.Unmarshal(body, &e) != nil {
		return ""
	}
	switch e.Code {
	case "ExpiredRequest", "RequestExpired", "Unauthorized", "Forbidden", "CORSForbidden", "AccessForbidden", "AuthorizationHeaderMalformed", "AccessDenied", "SignatureDoesNotMatch", "BadDigest", "InvalidDigest", "InvalidRequest", "InvalidArgument", "PreconditionFailed", "NoSuchKey", "NoSuchBucket", "NotImplemented", "MethodNotAllowed", "InvalidRange", "RequestTimeTooSkewed", "ExpiredToken", "MissingContentLength", "IncompleteBody", "InvalidAccessKeyId", "EntityTooLarge", "AuthorizationQueryParametersError":
		return e.Code
	}
	return "OtherProviderError"
}
func readResponse(resp *http.Response, err error) response {
	if err != nil {
		return response{code: "TransportFailure"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return response{status: resp.StatusCode, code: "BodyReadFailure"}
	}
	x := response{status: resp.StatusCode, headers: resp.Header, body: body}
	if x.status >= 400 {
		x.code = safeCode(body)
	}
	return x
}
func (r *runner) call(method, u string, h http.Header, body []byte) response {
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return response{code: "RequestFailure"}
	}
	req.Header = h.Clone()
	if n := h.Get("Content-Length"); n != "" {
		req.ContentLength, _ = strconv.ParseInt(n, 10, 64)
		req.Header.Del("Content-Length")
	}
	return readResponse(r.client.Do(req))
}
func (r *runner) signed(method, key string, h http.Header, body []byte, ttl int, q url.Values) response {
	u, _, err := presign(r.c, method, key, h, time.Now(), ttl, q)
	if err != nil {
		return response{code: "PresignFailure"}
	}
	return r.call(method, u, h, body)
}
func (r *runner) key(name string) string { k := r.expPrefix + name; r.keys[k] = true; return k }
func checksum(profile string, b []byte) string {
	if profile == "sha256" {
		h := sha256.Sum256(b)
		return base64.StdEncoding.EncodeToString(h[:])
	}
	h := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(h[:])
}
func putHeaders(profile string, b []byte) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("If-None-Match", "*")
	h.Set("x-amz-meta-vecnet-checksum-profile", profile)
	h.Set("x-amz-meta-vecnet-checksum", checksum(profile, b))
	field := "x-amz-checksum-sha256"
	if profile == "md5" {
		field = "Content-MD5"
	}
	h.Set(field, checksum(profile, b))
	return h
}
func success(x response) bool    { return x.status >= 200 && x.status < 300 }
func sigFailure(x response) bool { return x.status == 403 && x.code == "SignatureDoesNotMatch" }
func (r *runner) put(key, profile string, b []byte) response {
	r.lastPut = time.Now()
	return r.signed("PUT", key, putHeaders(profile, b), b, 300, nil)
}
func (r *runner) absent(key string) bool {
	return r.signed("HEAD", key, nil, nil, 60, nil).status == 404
}
func (r *runner) profile(profile string, b []byte) bool {
	field := "x-amz-checksum-sha256"
	if profile == "md5" {
		field = "Content-MD5"
	}
	key := r.key(profile + "-valid")
	x := r.put(key, profile, b)
	good := r.record(profile+"_match", success(x), x, "full production-intended signed headers")
	h := putHeaders(profile, b)
	changed := append([]byte(nil), b...)
	changed[0] ^= 1
	badkey := r.key(profile + "-mismatch")
	r.lastPut = time.Now()
	x = r.signed("PUT", badkey, h, changed, 300, nil)
	good = r.record(profile+"_body_mismatch", x.status == 400 && (x.code == "BadDigest" || x.code == "InvalidDigest") && r.absent(badkey), x, "equal-length changed body; HEAD confirms no object") && good
	for _, op := range []string{"removed", "changed"} {
		k := r.key(profile + "-header-" + op)
		u, _, _ := presign(r.c, "PUT", k, h, time.Now(), 300, nil)
		mut := h.Clone()
		if op == "removed" {
			mut.Del(field)
		} else {
			mut.Set(field, checksum(profile, changed))
		}
		r.lastPut = time.Now()
		x = r.call("PUT", u, mut, b)
		good = r.record(profile+"_header_"+op, sigFailure(x) && r.absent(k), x, "signed checksum header binding; no object") && good
	}
	x = r.signed("HEAD", key, http.Header{"X-Amz-Checksum-Mode": []string{"ENABLED"}}, nil, 60, nil)
	if !success(x) {
		x = r.signed("HEAD", key, nil, nil, 60, nil)
	}
	mh := md5.Sum(b)
	etag := strings.Trim(x.headers.Get("ETag"), "\"")
	evidence := success(x) && x.headers.Get("Content-Length") == strconv.Itoa(len(b)) && x.headers.Get("x-amz-meta-vecnet-checksum-profile") == profile && x.headers.Get("x-amz-meta-vecnet-checksum") == checksum(profile, b)
	if returned := x.headers.Get("x-amz-checksum-sha256"); returned != "" && profile == "sha256" {
		evidence = evidence && returned == checksum(profile, b)
	}
	if profile == "md5" {
		evidence = evidence && etag == hex.EncodeToString(mh[:])
	}
	good = r.record(profile+"_head_evidence", evidence, x, fmt.Sprintf("signed metadata/size; provider_checksum_returned=%t; single_part_md5_etag=%t", x.headers.Get("x-amz-checksum-sha256") == checksum("sha256", b), etag == hex.EncodeToString(mh[:]))) && good
	return good
}

// raw sends deliberately invalid/forbidden browser framing without net/http
// normalizing Content-Length. No wire data or transport errors are ever logged.
func (r *runner) raw(u string, h http.Header, b []byte, omitLength, chunked bool) response {
	p, err := url.Parse(u)
	if err != nil {
		return response{code: "RequestFailure"}
	}
	conn, err := tls.DialWithDialer(&netDialer, "tcp", p.Host+":443", &tls.Config{ServerName: p.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return response{code: "TransportFailure"}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	var wire bytes.Buffer
	fmt.Fprintf(&wire, "PUT %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", p.RequestURI(), p.Host)
	for k, v := range h {
		if strings.EqualFold(k, "Content-Length") && (omitLength || chunked) {
			continue
		}
		fmt.Fprintf(&wire, "%s: %s\r\n", k, strings.Join(v, ","))
	}
	if chunked {
		wire.WriteString("Transfer-Encoding: chunked\r\n")
	}
	wire.WriteString("\r\n")
	if chunked {
		fmt.Fprintf(&wire, "%x\r\n", len(b))
		wire.Write(b)
		wire.WriteString("\r\n0\r\n\r\n")
	} else {
		wire.Write(b)
	}
	if _, err = conn.Write(wire.Bytes()); err != nil {
		return response{code: "TransportFailure"}
	}
	if !chunked && !omitLength {
		conn.CloseWrite()
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "PUT"})
	if err != nil {
		code := "RawReadFailure"
		if os.IsTimeout(err) {
			code = "RawReadTimeout"
		} else if err == io.EOF || err == io.ErrUnexpectedEOF {
			code = "RawPeerClosed"
		}
		return response{transport: code}
	}
	return readResponse(resp, err)
}
func (r *runner) bindings(profile string, b []byte) bool {
	h := putHeaders(profile, b)
	ok := true
	for _, op := range []string{"length_changed", "length_missing", "chunked_same_size", "chunked_wrong_size", "length_body_mismatch", "conditional_removed", "conditional_changed"} {
		k := r.key(op)
		signedHeaders := h.Clone()
		if op == "chunked_wrong_size" {
			altered := append(append([]byte(nil), b...), 33)
			signedHeaders = putHeaders(profile, altered)
			signedHeaders.Set("Content-Length", strconv.Itoa(len(b)))
		}
		u, _, _ := presign(r.c, "PUT", k, signedHeaders, time.Now(), 300, nil)
		mut := signedHeaders.Clone()
		body := b
		switch op {
		case "length_changed":
			body = append(append([]byte(nil), b...), 33)
			mut.Set("Content-Length", strconv.Itoa(len(body)))
		case "chunked_wrong_size":
			body = append(append([]byte(nil), b...), 33)
		case "length_body_mismatch":
			body = b[:len(b)-1]
		case "conditional_removed":
			mut.Del("If-None-Match")
		case "conditional_changed":
			mut.Set("If-None-Match", "different")
		}
		r.lastPut = time.Now()
		var x response
		switch op {
		case "length_missing", "chunked_same_size", "chunked_wrong_size":
			x = r.raw(u, mut, body, op == "length_missing", strings.HasPrefix(op, "chunked_"))
		case "length_body_mismatch":
			x = r.raw(u, mut, body, false, false)
		default:
			x = r.call("PUT", u, mut, body)
		}
		// An incomplete raw body can time out locally; absence alone is not proof
		// of provider rejection. Keep that diagnostic distinct from mandatory gates.
		if op == "length_body_mismatch" && x.status == 0 {
			r.outstanding(op, "incomplete raw HTTP body produced no parseable HTTP response; no provider error code observed; HEAD absence="+strconv.FormatBool(r.absent(k)))
			continue
		}
		if op == "chunked_same_size" {
			head := r.signed("HEAD", k, nil, nil, 60, nil)
			valid := success(x) && success(head) && head.headers.Get("Content-Length") == strconv.Itoa(len(b))
			r.record(op, valid || (x.status >= 400 && x.status < 500 && r.absent(k)), x, "diagnostic: valid exact-size chunked body may be normalized by R2; wrong-size bypass tested separately")
			continue
		}
		rejected := x.status >= 400 && x.status < 500 && r.absent(k)
		if op == "length_changed" || strings.HasPrefix(op, "conditional_") {
			rejected = rejected && sigFailure(x)
		}
		ok = r.record(op, rejected, x, "provider rejects signed framing/conditional mutation; no object") && ok
	}
	k := r.key("replay")
	r.lastPut = time.Now()
	u, _, _ := presign(r.c, "PUT", k, h, time.Now(), 300, nil)
	first := r.call("PUT", u, h, b)
	second := r.call("PUT", u, h, b)
	ok = r.record("create_only_replay", success(first) && second.status == 412 && second.code == "PreconditionFailed", second, "same grant: first success, replay 412") && ok
	raceKey := r.key("race")
	r.lastPut = time.Now()
	raceURL, _, _ := presign(r.c, "PUT", raceKey, h, time.Now(), 300, nil)
	results := make(chan response, 4)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					results <- response{code: "InternalFailure"}
				}
			}()
			<-start
			results <- r.call("PUT", raceURL, h, b)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins, preconditions := 0, 0
	for x := range results {
		if success(x) {
			wins++
		}
		if x.status == 412 && x.code == "PreconditionFailed" {
			preconditions++
		}
	}
	ok = r.record("create_only_race", wins == 1 && preconditions == 3, response{}, fmt.Sprintf("writers=4 success=%d precondition_412=%d", wins, preconditions)) && ok
	return ok
}
func (r *runner) config() {
	for _, kind := range []string{"lifecycle", "cors"} {
		x := r.signed("GET", "", nil, nil, 60, url.Values{kind: []string{""}})
		if (x.status == 403 && x.code == "AccessDenied") || (x.status == 400 && x.code == "InvalidArgument") || x.status == 501 {
			r.record(kind+"_configuration", true, x, "coordinator-verified via wrangler; presigned object API cannot read config; not independently read back by spike")
		} else if success(x) {
			// Do not persist raw configuration responses. Later receipts distinguish
			// readable configuration from independently validated rule contents.
			r.record(kind+"_configuration_readable", true, x, "configuration accessible; raw response suppressed")
			r.outstanding(kind+"_configuration_validation", "coordinator-verified via wrangler; this harness does not parse bucket administration XML")
		} else {
			r.record(kind+"_configuration", false, x, "unexpected config read response; coordinator-verified via wrangler remains separate evidence")
		}
	}
}
func (r *runner) cors(profile, key string) {
	headers := "content-type,if-none-match,"
	if profile == "md5" {
		headers += "content-md5"
	} else {
		headers += "x-amz-checksum-sha256"
	}
	full := headers + ",cache-control,content-disposition,x-amz-meta-vecnet-checksum-profile,x-amz-meta-vecnet-checksum"
	u, _, _ := presign(r.c, "PUT", key, putHeaders(profile, []byte("cors")), time.Now(), 300, nil)
	for i, origin := range []string{"https://vecnet.xk.io", "http://localhost:45752", "http://xsm:45752"} {
		for _, p := range []struct{ name, method, headers string }{{"put_minimal", "PUT", headers}, {"put_full", "PUT", full}, {"get_range", "GET", "range"}, {"head", "HEAD", ""}} {
			h := http.Header{}
			h.Set("Origin", origin)
			h.Set("Access-Control-Request-Method", p.method)
			if p.headers != "" {
				h.Set("Access-Control-Request-Headers", p.headers)
			}
			x := r.call("OPTIONS", u, h, nil)
			approved := success(x) && x.headers.Get("Access-Control-Allow-Origin") == origin && containsHeader(x.headers.Get("Access-Control-Allow-Methods"), p.method)
			for _, field := range strings.Split(p.headers, ",") {
				if field != "" {
					approved = approved && containsHeader(x.headers.Get("Access-Control-Allow-Headers"), field)
				}
			}
			r.record(fmt.Sprintf("cors_origin_%d_%s", i+1, p.name), approved, x, "raw OPTIONS; allowed methods/headers checked")
		}
	}
	h := http.Header{}
	h.Set("Origin", "https://disallowed.invalid")
	h.Set("Access-Control-Request-Method", "PUT")
	h.Set("Access-Control-Request-Headers", full)
	x := r.call("OPTIONS", u, h, nil)
	r.record("cors_disallowed_origin", x.status > 0 && x.headers.Get("Access-Control-Allow-Origin") == "", x, "raw OPTIONS cannot establish browser enforcement")
	get, _, _ := presign(r.c, "GET", key, nil, time.Now(), 600, nil)
	h = http.Header{}
	h.Set("Origin", "https://vecnet.xk.io")
	x = r.call("GET", get, h, nil)
	exposed := x.headers.Get("Access-Control-Expose-Headers")
	r.record("cors_get_exposure", success(x) && x.headers.Get("Access-Control-Allow-Origin") == "https://vecnet.xk.io" && containsHeader(exposed, "etag") && containsHeader(exposed, "content-length") && containsHeader(exposed, "x-amz-checksum-sha256"), x, "ETag/length/checksum exposure checked")
	h.Set("Range", "bytes=0-5")
	x = r.call("GET", get, h, nil)
	exposed = x.headers.Get("Access-Control-Expose-Headers")
	r.record("cors_range_exposure", x.status == 206 && x.headers.Get("Content-Range") != "" && x.headers.Get("Accept-Ranges") == "bytes" && containsHeader(exposed, "content-range") && containsHeader(exposed, "accept-ranges"), x, "actual Range 206 exposes Content-Range and Accept-Ranges")
}
func containsHeader(list, value string) bool {
	for _, s := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(s), value) {
			return true
		}
	}
	return false
}
func (r *runner) run() {
	b := []byte("vecnet R2 isolated provider acceptance fixture\n")
	r.record("credential_scope", true, response{}, "0600 regular file, same inode/user, no symlink; exact test bucket/account endpoint; build-box lane")
	r.config()
	shaOK := r.profile("sha256", b)
	md5OK := r.profile("md5", b)
	profile := "sha256"
	if !shaOK {
		profile = "md5"
	}
	if !shaOK && !md5OK {
		r.record("checksum_profile", false, response{}, "STOP: neither profile passed")
		r.cleanup()
		return
	}
	selectedKey := r.expPrefix + profile + "-valid"
	// Start the real production TTL before the rest of the probes. No simulated
	// clock or backdated signature substitutes for this observation.
	minted := time.Now().UTC().Truncate(time.Second)
	longURL, _, _ := presign(r.c, "GET", selectedKey, nil, minted, 600, nil)
	x := r.call("GET", longURL, nil, nil)
	r.record("get_600_before_expiry", success(x) && bytes.Equal(x.body, b), x, "signed_at="+minted.Format(time.RFC3339)+"; X-Amz-Expires=600")
	providerDate, dateErr := http.ParseTime(x.headers.Get("Date"))
	skew := time.Since(providerDate)
	r.record("clock_skew", dateErr == nil && skew > -10*time.Second && skew < 10*time.Second, response{}, fmt.Sprintf("local minus response Date seconds=%.1f; expiry observation margin=10s", skew.Seconds()))
	shortAt := time.Now().UTC().Truncate(time.Second)
	shortURL, _, _ := presign(r.c, "GET", selectedKey, nil, shortAt, 3, nil)
	x = r.call("GET", shortURL, nil, nil)
	r.record("get_short_before_expiry", success(x), x, "diagnostic X-Amz-Expires=3")
	mandatory := r.bindings(profile, b)
	if mandatory {
		r.report.Profile = profile
		r.record("checksum_profile", true, response{}, "selected "+profile+" after checksum, metadata, length and create-only gates")
	} else {
		r.record("checksum_profile", false, response{}, "STOP: mandatory binding gate failed")
	}
	h := http.Header{}
	h.Set("Range", "bytes=0-5")
	x = r.call("GET", longURL, h, nil)
	r.record("range_206", x.status == 206 && bytes.Equal(x.body, b[:6]) && x.headers.Get("Content-Range") == fmt.Sprintf("bytes 0-5/%d", len(b)), x, "exact partial bytes and Content-Range")
	h.Set("Range", "bytes=99999-")
	x = r.call("GET", longURL, h, nil)
	r.record("range_416", x.status == 416 && x.code == "InvalidRange", x, "unsatisfiable range")
	x = r.call("GET", longURL, nil, nil)
	r.record("response_metadata", success(x) && x.headers.Get("Content-Type") == "application/octet-stream" && x.headers.Get("Content-Disposition") == "attachment" && x.headers.Get("Cache-Control") == "private, no-store", x, fmt.Sprintf("nosniff_present=%t; csp_present=%t; Accept-Ranges=%t", x.headers.Get("X-Content-Type-Options") == "nosniff", x.headers.Get("Content-Security-Policy") != "", x.headers.Get("Accept-Ranges") == "bytes"))
	x = r.signed("GET", selectedKey, nil, nil, 60, url.Values{"response-content-disposition": []string{"attachment; filename=spike.bin"}, "response-content-type": []string{"application/octet-stream"}})
	r.record("response_overrides", success(x) && x.headers.Get("Content-Disposition") == "attachment; filename=spike.bin", x, "signed S3 response-content-disposition/type override")
	x = r.call("HEAD", longURL, nil, nil)
	r.record("get_method_binding", x.status == 403, x, "GET capability used as HEAD")
	parsed, _ := url.Parse(longURL)
	parsed.Path += "-other"
	x = r.call("GET", parsed.String(), nil, nil)
	r.record("get_key_binding", sigFailure(x), x, "same capability with changed object key")
	r.cors(profile, selectedKey)
	// Only two fixtures intentionally survive this run. Never list/delete objects
	// outside the exact keys allocated by this runner.
	for _, k := range []string{r.key("lifecycle-exp-1h"), r.keepPrefix + "lifecycle-keep"} {
		r.keys[k] = true
		x = r.put(k, profile, b)
		isKeep := strings.HasPrefix(k, r.keepPrefix)
		r.record("lifecycle_fixture_"+strconv.FormatBool(isKeep), success(x), x, "physical deletion/keep survival requires later real observation")
		if success(x) {
			r.keep[k] = true
			r.report.Fixtures = append(r.report.Fixtures, k)
		}
		if isKeep {
			keepURL, _, _ := presign(r.c, "GET", k, nil, time.Now(), getTTL(time.Now(), nil), nil)
			got := r.call("GET", keepURL, nil, nil)
			r.record("keep_get_600", success(got) && bytes.Equal(got.body, b), got, "null object deadline; X-Amz-Expires=600")
		}
	}
	r.outstanding("browser_put_get", "actual browser Blob automatic Content-Length, selected signed headers, cross-origin PUT/GET and disallowed-origin enforcement; coordinator Playwright follow-up")
	r.outstanding("lifecycle_real_deletion", "one exp/1h fixture and one keep fixture retained; subday rule uses 1 day plus eventual provider deletion; longer classes not observed")
	r.outstanding("account_billing_checkout", "account subscription/payment/free-tier headroom are operator checks, not inferred from object API access")
	waitUntil(shortAt.Add(5 * time.Second))
	x = r.call("GET", shortURL, nil, nil)
	r.record("get_short_after_expiry", x.status == 403, x, "real clock past 3s diagnostic TTL; prior request succeeded")
	fmt.Println("waiting for real 600-second capability expiry; no capabilities persisted")
	waitUntil(minted.Add(610 * time.Second))
	x = r.call("GET", longURL, nil, nil)
	r.record("get_600_after_expiry", x.status == 403, x, fmt.Sprintf("real_elapsed_seconds=%d; 10s post-TTL skew margin", int(time.Since(minted).Seconds())))
	x = r.signed("GET", selectedKey, nil, nil, 600, nil)
	r.record("get_remint_after_expiry", success(x) && bytes.Equal(x.body, b), x, "fresh 600s capability for the same retained object")
	r.cleanup()
}
func waitUntil(target time.Time) {
	for time.Now().Before(target) {
		d := time.Until(target)
		if d > 45*time.Second {
			d = 45 * time.Second
		}
		time.Sleep(d)
		if time.Now().Before(target) {
			fmt.Println("expiry observation pending (real clock)")
		}
	}
}
func (r *runner) cleanup() {
	initialOK := true
	for k := range r.keys {
		if !r.keep[k] {
			initialOK = success(r.signed("DELETE", k, nil, nil, 60, nil)) && initialOK
		}
	}
	r.record("initial_cleanup", initialOK, response{}, "deleted all allocated non-fixture keys; final delete and HEAD absence follows upload TTL+60s")
	// A deleted key can be recreated by a still-live PUT grant. Wait for every
	// minted upload grant's 300s TTL plus 60s before final deletion/absence.
	waitUntil(r.lastPut.Add(360 * time.Second))
	all := true
	count := 0
	for k := range r.keys {
		if r.keep[k] {
			continue
		}
		count++
		x := r.signed("DELETE", k, nil, nil, 60, nil)
		ok := success(x) && r.absent(k)
		all = all && ok
		if !ok {
			r.record("cleanup_key_"+strconv.Itoa(count), false, x, "allocated key deletion/HEAD absence failed")
		}
	}
	r.record("cleanup", all, response{}, fmt.Sprintf("deleted and HEAD-confirmed %d exact allocated keys after upload TTL+60s; retained %d lifecycle fixtures", count, len(r.keep)))
}

// Explicitly replacing a completed receipt's two lifecycle fixtures is useful
// when rerunning corrected probes. Never list the bucket or broaden deletion.
func (r *runner) replacePreviousFixtures() bool {
	b, err := os.ReadFile(reportPath)
	var old report
	if err != nil || json.Unmarshal(b, &old) != nil || old.Finished == "" || len(old.Fixtures) != 2 || !regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`).MatchString(old.RunID) {
		fmt.Fprintln(os.Stderr, "previous receipt rejected; no previous fixture deletion attempted")
		return false
	}
	exp := "attachments/v1/exp/1h/spike-" + old.RunID + "/lifecycle-exp-1h"
	keep := "attachments/v1/keep/spike-" + old.RunID + "/lifecycle-keep"
	if !((old.Fixtures[0] == exp && old.Fixtures[1] == keep) || (old.Fixtures[0] == keep && old.Fixtures[1] == exp)) {
		fmt.Fprintln(os.Stderr, "previous fixture scope rejected; no previous fixture deletion attempted")
		return false
	}
	// The original run finished only after every upload's TTL+60s elapsed.
	finished, err := time.Parse(time.RFC3339, old.Finished)
	if err != nil || time.Now().Before(finished) {
		fmt.Fprintln(os.Stderr, "previous receipt completion rejected")
		return false
	}
	for _, k := range old.Fixtures {
		x := r.signed("DELETE", k, nil, nil, 60, nil)
		if !success(x) || !r.absent(k) {
			r.record("prior_fixture_cleanup", false, x, "previous receipt exact fixture deletion failed")
			return false
		}
	}
	return r.record("prior_fixture_cleanup", true, response{}, "deleted the previous completed run's two exact lifecycle fixtures; HEAD absence confirmed")
}
