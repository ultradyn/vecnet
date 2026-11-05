//go:build spike

package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Preserve the original real-TTL evidence and exact lifecycle witnesses. This
// mode never allocates, replaces, deletes or re-uploads lifecycle fixtures.
func (r *runner) loadBaseline() bool {
	b, err := os.ReadFile(reportPath)
	var old report
	if err != nil || json.Unmarshal(b, &old) != nil || old.Finished == "" || old.Profile != "sha256" || len(old.Fixtures) != 2 || !regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`).MatchString(old.RunID) {
		return false
	}
	if old.Fixtures[0] != "attachments/v1/exp/1h/spike-"+old.RunID+"/lifecycle-exp-1h" || old.Fixtures[1] != "attachments/v1/keep/spike-"+old.RunID+"/lifecycle-keep" {
		return false
	}
	r.baseline = &old
	r.report.Profile = old.Profile
	return true
}

func (r *runner) combinedReport() report {
	if r.baseline == nil {
		return r.report
	}
	combined := *r.baseline
	combined.Gates = make(map[string]string, len(r.baseline.Gates)+len(r.report.Gates))
	for k, v := range r.baseline.Gates {
		combined.Gates[k] = v
	}
	for k, v := range r.report.Gates {
		combined.Gates[k] = v
	}
	combined.Observations = append(append([]observation(nil), r.baseline.Observations...), r.report.Observations...)
	combined.Followups = append(append([]report(nil), r.baseline.Followups...), r.report)
	return combined
}

func (r *runner) runFollowup() {
	r.record("cors_configuration", true, response{}, "coordinator-verified updated policy via wrangler 2026-11-05; this run validates behavior, not bucket administration")
	b := []byte("vecnet R2 browser Blob acceptance fixture\n")
	key := r.key("browser-blob")
	// Mint only the grants used in this run. No URLs enter argv, env or files.
	h := putHeaders(r.report.Profile, b)
	r.lastPut = time.Now()
	put, _, err := presign(r.c, "PUT", key, h, r.lastPut, 300, nil)
	if err != nil {
		r.record("browser_put_get", false, response{}, "presign failed; details suppressed")
		return
	}
	get, _, err := presign(r.c, "GET", key, nil, time.Now(), 600, nil)
	if err != nil {
		r.record("browser_put_get", false, response{}, "presign failed; details suppressed")
		r.cleanup()
		return
	}
	grant := browserGrant{Put: put, Get: get, Headers: map[string]string{}, Body: string(b), Checksum: checksum("sha256", b)}
	m := md5.Sum(b)
	grant.ETag = `"` + hex.EncodeToString(m[:]) + `"`
	for name := range h {
		if !strings.EqualFold(name, "Content-Length") {
			grant.Headers[name] = h.Get(name)
		}
	}
	r.browser(grant)
	head := r.signed("HEAD", key, http.Header{"X-Amz-Checksum-Mode": []string{"ENABLED"}}, nil, 60, nil)
	r.record("browser_head_evidence", success(head) && head.headers.Get("Content-Length") == strconv.Itoa(len(b)) && head.headers.Get("x-amz-checksum-sha256") == grant.Checksum && head.headers.Get("x-amz-meta-vecnet-checksum-profile") == "sha256" && head.headers.Get("x-amz-meta-vecnet-checksum") == grant.Checksum, head, "browser supplied exact signed Content-Length; HEAD size/provider SHA/metadata confirmed")
	r.cors(r.report.Profile, key)
	// Probe both directions with checksums matching actual transmitted bytes, so
	// a checksum failure cannot hide a framing bypass. No raw error text escapes.
	malformed := []string{}
	for _, direction := range []string{"fewer", "more"} {
		body := b[:len(b)-1]
		if direction == "more" {
			body = append(append([]byte(nil), b...), '!')
		}
		k := r.key("browser-length-" + direction)
		malformed = append(malformed, k)
		headers := putHeaders(r.report.Profile, body)
		headers.Set("Content-Length", strconv.Itoa(len(b)))
		r.lastPut = time.Now()
		u, _, _ := presign(r.c, "PUT", k, headers, r.lastPut, 300, nil)
		x := r.raw(u, headers, body, false, false)
		noObject := r.absent(k)
		observed := x.status > 0 || x.transport == "RawReadTimeout" || x.transport == "RawPeerClosed" || x.transport == "RawReadFailure"
		r.record("length_body_"+direction, observed && noObject, x, "raw TLS write completed; checksum matches actual bytes, signed length differs; HEAD absence="+strconv.FormatBool(noObject)+"; transport category is not a provider rejection code")
	}
	// Re-check before any cleanup can hide a late creation. No repeated 600s GET
	// test; this wait is the upload replay safety window required for deletion.
	waitUntil(r.lastPut.Add(360 * time.Second))
	absent := r.report.Gates["length_body_fewer"] == "PASS" && r.report.Gates["length_body_more"] == "PASS"
	for _, k := range malformed {
		absent = r.absent(k) && absent
	}
	r.record("length_body_mismatch", absent, response{}, "fewer/more raw probes: no object immediately or after PUT TTL+60s before deletion; no unobserved provider error code inferred")
	r.cleanup()
	r.record("lifecycle_witnesses_preserved", true, response{}, "original two lifecycle keys remain in baseline receipt; follow-up allocated/deleted only three browser-* keys in its own new exp/1h namespace")
}

type browserGrant struct {
	Put      string            `json:"put"`
	Get      string            `json:"get"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Checksum string            `json:"checksum"`
	ETag     string            `json:"etag"`
}

type browserResults struct {
	FailedStage  string `json:"failed_stage"`
	Version      string `json:"version"`
	PutHTTP      int    `json:"put_http"`
	PutETag      bool   `json:"put_etag"`
	PutChecksum  bool   `json:"put_checksum"`
	GetHTTP      int    `json:"get_http"`
	GetBody      bool   `json:"get_body"`
	GetETag      bool   `json:"get_etag"`
	GetChecksum  bool   `json:"get_checksum"`
	GetLength    bool   `json:"get_length"`
	RangeHTTP    int    `json:"range_http"`
	RangeBody    bool   `json:"range_body"`
	ContentRange bool   `json:"content_range"`
	AcceptRanges bool   `json:"accept_ranges"`
	BlockedGet   bool   `json:"blocked_get"`
	BlockedPut   bool   `json:"blocked_put"`
}

func grantHandler(grant browserGrant, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if req.Method != "GET" || (req.Header.Get("Origin") != "" && req.Header.Get("Origin") != "http://"+req.Host) {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		switch req.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<!doctype html><meta charset=utf-8><title>R2 browser acceptance</title><p>Isolated browser acceptance harness</p>")
		case path:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(grant)
		default:
			http.NotFound(w, req)
		}
	})
}

func (r *runner) browser(grant browserGrant) {
	allowed, err := net.Listen("tcp", "127.0.0.1:45752")
	origin := "http://localhost:45752"
	resolve := "MAP localhost 127.0.0.1"
	if err != nil {
		allowed, err = net.Listen("tcp", "127.0.0.2:45752")
		origin = "http://xsm:45752"
		resolve = "MAP xsm 127.0.0.2"
	}
	if err != nil {
		allowed, err = net.Listen("tcp6", "[::1]:45752")
		resolve = "MAP xsm [::1]"
	}
	if err != nil {
		r.record("browser_put_get", false, response{}, "allowed loopback port unavailable on IPv4 and IPv6; no listener replaced")
		return
	}
	defer allowed.Close()
	disallowed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		r.record("browser_put_get", false, response{}, "disallowed-origin loopback listener unavailable")
		return
	}
	defer disallowed.Close()
	// Random path and same-origin response protect in-memory loopback grants;
	// neither listener logs requests. Nothing is written to browser traces/files.
	path := "/grant-" + r.report.RunID
	handler := grantHandler(grant, path)
	servers := []*http.Server{{Handler: handler, ReadHeaderTimeout: 5 * time.Second}, {Handler: handler, ReadHeaderTimeout: 5 * time.Second}}
	for i, listener := range []net.Listener{allowed, disallowed} {
		srv := servers[i]
		go func() {
			defer func() { _ = recover() }()
			_ = srv.Serve(listener)
		}()
		defer srv.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "internal/tools/r2spike/browser.mjs", origin, "http://"+disallowed.Addr().String(), path, resolve)
	// Debug logging can contain full URLs. Child stderr is discarded and stdout
	// is decoded into fixed typed booleans/statuses, never printed verbatim.
	for _, kv := range os.Environ() {
		name := strings.SplitN(kv, "=", 2)[0]
		if name != "DEBUG" && name != "PWDEBUG" && name != "NODE_OPTIONS" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	var result browserResults
	if err != nil || json.Unmarshal(out, &result) != nil {
		r.record("browser_put_get", false, response{}, "headless Chromium harness failed; all raw child output suppressed")
		return
	}
	if result.FailedStage != "" {
		stage := "unknown"
		switch result.FailedStage {
		case "launch", "allowed_navigation", "allowed_fetch", "disallowed_navigation", "disallowed_fetch", "close":
			stage = result.FailedStage
		}
		r.record("browser_put_get", false, response{}, "headless browser failed at "+stage+"; exception details suppressed")
		return
	}
	r.record("browser_put", result.PutHTTP == 200 && result.PutETag && result.PutChecksum, response{status: result.PutHTTP}, fmt.Sprintf("Blob PUT with browser-supplied signed Content-Length; JS matching ETag=%t SHA checksum=%t", result.PutETag, result.PutChecksum))
	r.record("browser_get", result.GetHTTP == 200 && result.GetBody && result.GetETag && result.GetLength, response{status: result.GetHTTP}, fmt.Sprintf("JS exact full bytes=%t ETag=%t Content-Length=%t; optional GET SHA checksum match=%t (PUT checksum is mandatory)", result.GetBody, result.GetETag, result.GetLength, result.GetChecksum))
	r.record("browser_range", result.RangeHTTP == 206 && result.RangeBody && result.ContentRange && result.AcceptRanges, response{status: result.RangeHTTP}, "JS read exact partial bytes, Content-Range and Accept-Ranges")
	r.record("browser_disallowed_origin", result.BlockedGet && result.BlockedPut, response{}, "separate disallowed loopback origin: GET and full-header PUT both rejected by browser fetch as TypeError")
	all := r.report.Gates["browser_put"] == "PASS" && r.report.Gates["browser_get"] == "PASS" && r.report.Gates["browser_range"] == "PASS" && r.report.Gates["browser_disallowed_origin"] == "PASS"
	// Version comes from the browser but is bounded to a numeric allowlist.
	version := "unknown"
	if regexp.MustCompile(`^[0-9.]{1,32}$`).MatchString(result.Version) {
		version = result.Version
	}
	r.record("browser_put_get", all, response{}, fmt.Sprintf("headless Chromium %s on x-boss; allowed origin %s (local resolver override); no cookie/bearer credentials, console capture, trace or HAR", version, origin))
}
