// Package ui serves the operator's web inbox (plan Task 19): a plain
// HTML/JS/CSS page with no build step, embedded in the hub binary and
// mounted under /ui/. The page itself is public; it asks the operator for
// their secret and sends it only as a bearer header to /v1/*.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
)

//go:embed static
var static embed.FS

// CSP confines the page to the hub's own origin and forbids inline script
// and style, so every script and stylesheet is a file under static/. Image
// thumbnails are fetched with the bearer and shown from blob object URLs.
const CSP = "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob:; font-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// Policy is CSP with connect-src widened by exactly the given origins: the
// hub's configured R2 S3 endpoint, which the page PUTs to and GETs from with
// short signed URLs (R2 blobs plan, Task 7). Only a bare https origin is
// accepted, scheme and host with no path, query, credentials or wildcard;
// anything else is dropped, so the policy can never widen past one host.
func Policy(connect ...string) string {
	var extra []string
	seen := map[string]bool{}
	for _, origin := range connect {
		if exactOrigin(origin) && !seen[origin] {
			seen[origin] = true
			extra = append(extra, origin)
		}
	}
	if len(extra) == 0 {
		return CSP
	}
	return strings.Replace(CSP, "connect-src 'self';", "connect-src 'self' "+strings.Join(extra, " ")+";", 1)
}

func exactOrigin(s string) bool {
	if s == "" || strings.ContainsAny(s, "*'\";, \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == "" &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery && u.Scheme+"://"+u.Host == s
}

// Files returns the embedded UI files, rooted at the directory holding
// index.html.
func Files() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return sub
}

// notFound writes 404 with the §2 error envelope. The body is fixed, so it
// is written directly (httpapi, which owns writeError, imports this package).
func notFound(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such file","details":{}}}` + "\n"))
}

// Handler serves the UI for requests whose path starts with /ui/. devDir ""
// serves the embedded files; otherwise files are read from devDir on every
// request, so edits show on refresh. Every response carries the CSP (with
// connect, the exact direct-transfer origins; see Policy) and no-store
// caching; directory listings are never served.
func Handler(devDir string, connect ...string) http.Handler {
	csp := Policy(connect...)
	files := Files()
	if devDir != "" {
		files = os.DirFS(devDir)
	}
	fileServer := http.StripPrefix("/ui", http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if r.URL.Path == "/ui" {
			http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			return
		}
		// Only the page and its flat assets: no subdirectories, no listings,
		// no dotfiles. Anything else, or a name with no file, is the §2
		// error envelope (R01) like every other non-2xx.
		rest := strings.TrimPrefix(r.URL.Path, "/ui/")
		name := rest
		if name == "" {
			name = "index.html"
		}
		if strings.Contains(rest, "/") || strings.HasPrefix(rest, ".") {
			notFound(w)
			return
		}
		if st, err := fs.Stat(files, name); err != nil || st.IsDir() {
			notFound(w)
			return
		}
		if strings.HasSuffix(rest, ".js") {
			h.Set("Content-Type", "text/javascript; charset=utf-8")
		}
		fileServer.ServeHTTP(w, r)
	})
}
