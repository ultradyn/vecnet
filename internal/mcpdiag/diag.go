// Package mcpdiag stores only allowlisted, sanitized operator diagnostics.
// It never retains request bodies, credentials or errors. Refused OAuth
// redirects expose sanitized callback paths so operators can allow them.
package mcpdiag

import (
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/XertroV/vecnet/internal/logging"
)

const Capacity = 1000

type Record struct {
	Sequence uint64         `json:"sequence"`
	Time     time.Time      `json:"time"`
	Kind     string         `json:"kind"`
	Fields   map[string]any `json:"fields"`
}
type Filter struct {
	Peer, Method string
	Limit        int
	After        uint64
	// AfterSet distinguishes an explicit zero cursor from an initial snapshot.
	AfterSet bool
}
type Buffer struct {
	mu      sync.Mutex
	records [Capacity]Record
	next    uint64
	count   int
}

var shared Buffer

// Emit uses one process-wide ring for the MCP, OAuth and Events surfaces.
func Emit(kind string, fields map[string]any) { shared.Emit(kind, fields) }
func Snapshot(f Filter) []Record              { return shared.Snapshot(f) }

var kinds = words("mcp.request oauth.request oauth.metadata oauth.register oauth.client_registered oauth.redirect_refused oauth.cimd oauth.authorize oauth.token oauth.refresh oauth.consent oauth.link events.subscribe_parsed events.verification events.activation events.refresh events.expiry events.pause events.unsubscribe events.delivery")
var codes = words("ok error none invalid expired insufficient_scope success rejected failed pending verified cached refused active refreshed paused unsubscribed delivered retry dead not_attempted challenge_failed http_4xx http_5xx timeout invalid_url internal_error egress_refused token_expired delivery_failed unauthorized credential_rotated operator exhausted http_410 http_413 cancelled invalid_params invalid_request invalid_client_metadata invalid_redirect_uri temporarily_unavailable server_error unsupported_response_type invalid_scope invalid_target access_denied invalid_grant unsupported_grant_type consent_required link_invalid link_locked linked authorized fetch_failed http_error invalid_metadata rate_limited payload_too_large forbidden method_not_allowed not_found not_linked not_your_peer peer_required peer_disabled too_many unknown")

// Body-supplied names are fixed protocol vocabulary, never free-form text.
var eventNames = words("vecnet.message")
var deliveryModes = words("webhook")
var toolNames = words("vecnet_guide vecnet_send vecnet_reply vecnet_react vecnet_inbox vecnet_subscriptions vecnet_ack vecnet_receipt vecnet_presence vecnet_presence_subscribe vecnet_threads vecnet_thread vecnet_peers vecnet_peer_create vecnet_peer_invite_get vecnet_peer_invite_revoke vecnet_peer_set_disabled vecnet_groups vecnet_group_create vecnet_group_add vecnet_whoami vecnet_blob_put vecnet_blob_get vecnet_set_avatar vecnet_clear_avatar")

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// Emit sanitizes before BOTH destinations; a custom slog handler cannot bypass
// this boundary. Unknown keys and codes never enter the ring or logger.
func (b *Buffer) Emit(kind string, fields map[string]any) {
	if !kinds[kind] {
		return
	}
	safe := sanitize(fields)
	sanitizeOAuthMetadata(kind, fields, safe)
	b.mu.Lock()
	b.next++
	rec := Record{Sequence: b.next, Time: time.Now().UTC(), Kind: kind, Fields: safe}
	b.records[(b.next-1)%Capacity] = rec
	if b.count < Capacity {
		b.count++
	}
	b.mu.Unlock()
	keys := make([]string, 0, len(safe))
	for k := range safe {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		attrs = append(attrs, k, safe[k])
	}
	message := kind
	if kind == "mcp.request" {
		message = "mcp: request"
	}
	slog.Info(message, attrs...)
}

// Only these record kinds admit redirect details. Ordinary HTTP paths keep
// their fixed allowlist, and Events callback paths remain private.
func sanitizeOAuthMetadata(kind string, in, out map[string]any) {
	if kind != "oauth.redirect_refused" && kind != "oauth.client_registered" {
		return
	}
	if endpoint, ok := in["endpoint"].(string); ok && (endpoint == "/oauth/register" || endpoint == "/oauth/authorize") {
		out["endpoint"] = endpoint
	}
	if name, ok := in["client_name"].(string); ok {
		out["client_name"] = clean(name, 80)
	}
	if kind == "oauth.client_registered" {
		if prefix, ok := in["client_id_prefix"].(string); ok && strings.HasPrefix(prefix, "dcr_") {
			out["client_id_prefix"] = clean(prefix, 12)
		}
		if hosts, ok := in["redirect_hosts"].([]string); ok {
			safe := []string{}
			for _, h := range hosts {
				if len(safe) == 16 {
					break
				}
				safe = append(safe, host(h))
			}
			out["redirect_hosts"] = safe
		}
		return
	}
	// Cut before parsing so even malformed query escapes cannot reach either
	// destination. Never emit parse errors, which may contain the entire URI.
	uri, _ := in["redirect_uri"].(string)
	uri, _, _ = strings.Cut(uri, "#")
	uri, _, _ = strings.Cut(uri, "?")
	out["scheme"], out["host"], out["path"] = "", "", ""
	if u, err := url.Parse(uri); err == nil && u.Opaque == "" {
		out["scheme"], out["host"] = clean(u.Scheme, 32), host(u.Host)
		// Redact decoded paths too, so percent-encoded token prefixes cannot
		// evade redaction. Keep the original escaped spelling when unchanged.
		if path := logging.Redact(u.Path); path != u.Path {
			u.Path, u.RawPath = path, ""
		}
		out["path"] = u.EscapedPath()
	}
}
func (b *Buffer) Snapshot(f Filter) []Record {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := f.Limit
	if limit <= 0 || limit > Capacity {
		limit = Capacity
	}
	out := make([]Record, 0, limit)
	for i := b.count; i > 0; i-- {
		rec := b.records[(b.next-uint64(i))%Capacity]
		if rec.Sequence <= f.After || (f.Peer != "" && rec.Fields["peer_id"] != f.Peer) || (f.Method != "" && rec.Fields["method"] != f.Method) {
			continue
		}
		rec.Fields = clone(rec.Fields)
		out = append(out, rec)
	}
	if len(out) > limit {
		if f.AfterSet || f.After > 0 {
			out = out[:limit]
		} else {
			out = out[len(out)-limit:]
		}
	}
	return out
}
func clone(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s, ok := v.([]string); ok {
			v = append([]string{}, s...)
		}
		out[k] = v
	}
	return out
}
func sanitize(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch k {
		case "http_status", "status", "duration_ms", "code", "unknown_argument_keys", "ttl_ms", "retry_count":
			switch n := v.(type) {
			case int:
				out[k] = n
			case int64:
				out[k] = n
			case uint64:
				out[k] = n
			case float64:
				out[k] = n
			}
		case "session_present", "origin_present", "secret_present", "secret_valid_shape", "cursor_present", "cursor_null", "retry", "dead":
			if x, ok := v.(bool); ok {
				out[k] = x
			}
		case "auth_outcome", "outcome", "reason", "result", "verification":
			if s, ok := v.(string); ok {
				if s == "" || codes[s] {
					out[k] = s
				} else {
					out[k] = "unknown"
				}
			}
		case "caller_kind":
			if s, ok := v.(string); ok && words("unauthenticated peer principal operator oauth")[s] {
				out[k] = s
			}
		case "id_type":
			if s, ok := v.(string); ok && words("absent null string number invalid")[s] {
				out[k] = s
			}
		case "path":
			if s, ok := v.(string); ok && words("/mcp /.well-known/oauth-protected-resource /.well-known/oauth-protected-resource/mcp /.well-known/oauth-authorization-server /oauth/register /oauth/authorize /oauth/token /oauth/consent /oauth/consent.css /oauth/link /v1/admin/dot-links")[s] {
				out[k] = s
			}
		case "callback_host", "cimd_host", "host":
			if s, ok := v.(string); ok {
				out[k] = host(s)
			}
		case "origin":
			if s, ok := v.(string); ok {
				u, e := url.Parse(s)
				if e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" {
					out[k] = clean(u.Scheme+"://"+u.Host, 100)
				} else {
					out[k] = ""
				}
			}
		case "token_scopes":
			if ss, ok := v.([]string); ok {
				safe := []string{}
				for _, s := range ss {
					if words("events:read events:write vecnet vecnet:read vecnet:write offline_access")[s] {
						safe = append(safe, s)
					}
				}
				sort.Strings(safe)
				out[k] = safe
			}
		case "argument_keys":
			if ss, ok := v.([]string); ok {
				safe := []string{}
				for _, s := range ss {
					if words("peer_id thread_id types")[s] {
						safe = append(safe, s)
					}
				}
				sort.Strings(safe)
				out[k] = safe
			}
		case "event_name", "delivery_mode", "tool_name":
			if s, ok := v.(string); ok {
				allowed := eventNames
				if k == "delivery_mode" {
					allowed = deliveryModes
				} else if k == "tool_name" {
					allowed = toolNames
				}
				if allowed[s] {
					out[k] = s
				} else {
					out[k] = "unknown"
				}
			}
		case "http_method", "method", "mcp_method", "mcp_name", "requested_protocol_version", "negotiated_protocol_version", "protocol_version", "peer_id", "subscription_id", "event_id":
			if s, ok := v.(string); ok {
				out[k] = clean(s, 128)
			}
		case "client_name", "client_version":
			if s, ok := v.(string); ok {
				out[k] = clean(s, 64)
			}
		case "accept":
			if s, ok := v.(string); ok {
				out[k] = clean(s, 100)
			}
		case "user_agent":
			if s, ok := v.(string); ok {
				out[k] = clean(s, 120)
			}
		}
	}
	return out
}
func clean(s string, n int) string {
	s = logging.Redact(s)
	// Named metadata headers are the only free text admitted; discard URL paths
	// and controls even when a client puts a credential URL in such a header.
	parts := strings.Fields(s)
	for i, p := range parts {
		if strings.Contains(p, "://") {
			if u, e := url.Parse(p); e == nil && u.Hostname() != "" {
				parts[i] = u.Scheme + "://" + u.Hostname()
			} else {
				// Malformed URLs may still contain userinfo and queries.
				// A parser failure must not preserve the original text.
				parts[i] = "[invalid-url]"
			}
		}
	}
	s = strings.Join(parts, " ")
	rr := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
	if len(rr) > n {
		rr = rr[:n]
	}
	return string(rr)
}
func host(s string) string {
	if strings.Contains(s, "://") {
		u, e := url.Parse(s)
		if e != nil {
			return ""
		}
		s = u.Hostname()
	}
	if strings.ContainsAny(s, "/@?#\\ \t\r\n") {
		return ""
	}
	return clean(s, 253)
}
