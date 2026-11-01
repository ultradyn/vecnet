// Package logging is the hub's redacting log handler. PROTOCOL §12 puts
// redaction in the logger: call sites should keep secrets out of their
// records, and this catches the one that does not.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"regexp"
)

// secretRE matches the opaque credentials the hub mints or holds, by prefix:
// bearer and enrolment secrets, OAuth tokens and codes, webhook keys, Grok Bot
// routine keys and blob read tokens. A prefix keeps its place in the output, so
// a line still shows what kind of thing was there.
var secretRE = regexp.MustCompile(`\b(vn_[a-z]+_|vat_|vrt_|vac_|whsec_|crsr_|rt_)[A-Za-z0-9_+/=.-]{6,}`)

// Redact masks every credential in s.
func Redact(s string) string { return secretRE.ReplaceAllString(s, "${1}[redacted]") }

// NewHandler is a text handler writing to w that redacts the message and every
// string or error attribute.
func NewHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: replace})
}

func replace(_ []string, a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Redact(v.String()))
	case slog.KindAny:
		a.Value = slog.StringValue(Redact(fmt.Sprint(v.Any())))
	}
	return a
}
