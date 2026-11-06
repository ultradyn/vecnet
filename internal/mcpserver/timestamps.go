package mcpserver

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Only schema-declared timestamp arguments are adapted. Application metadata,
// content and opaque job payloads must retain their original bytes and meaning.
var toolTimestampPaths = map[string][]string{
	"vecnet_threads":  {"since"},
	"vecnet_presence": {"at"},
	"vecnet_send":     {"expires_at", "attachments.*.expires_at", "approval_request.expires_at"},
	"vecnet_reply":    {"expires_at", "attachments.*.expires_at"},
}

// Go's RFC3339 parser also accepts commas and out-of-range numeric offsets.
// Restrict those extensions before parsing; RFC3339 permits lowercase t/z.
var rfc3339Timestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?([Zz]|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

// normalizeToolTimestamps runs only at MCP ingress, before the unchanged
// prechecks, schemas and hub handlers. Invalid values stay for those validators
// to refuse, with their existing paths and error ordering.
func normalizeToolTimestamps(name string, raw []byte) []byte {
	for _, path := range toolTimestampPaths[name] {
		raw = normalizeTimestampPath(raw, strings.Split(path, "."))
	}
	return raw
}

func normalizeTimestampPath(raw []byte, path []string) []byte {
	if len(path) == 0 {
		var value string
		if json.Unmarshal(raw, &value) != nil || !rfc3339Timestamp.MatchString(value) {
			return raw
		}
		parsed, err := time.Parse(time.RFC3339Nano, strings.ToUpper(value))
		if err != nil {
			return raw
		}
		canonical, _ := json.Marshal(envelope.FormatTime(parsed))
		return canonical
	}
	if path[0] == "*" {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return raw
		}
		changed := false
		for i, item := range items {
			updated := normalizeTimestampPath(item, path[1:])
			changed = changed || !bytes.Equal(item, updated)
			items[i] = updated
		}
		if changed {
			// Preserve raw values, including Unicode and number spellings.
			var out bytes.Buffer
			out.WriteByte('[')
			for i, item := range items {
				if i > 0 {
					out.WriteByte(',')
				}
				out.Write(item)
			}
			out.WriteByte(']')
			return out.Bytes()
		}
		return raw
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	if value, ok := fields[path[0]]; ok {
		updated := normalizeTimestampPath(value, path[1:])
		if !bytes.Equal(value, updated) {
			fields[path[0]] = updated
			return encodeFields(fields)
		}
	}
	return raw
}
