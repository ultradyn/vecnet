package cli

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Delivery requires the complete authority to be usable; no invalid row may be
// skipped in favor of a seemingly valid owner. Bind/unbind can still repair it.
func readCodexAuthority(path string) (codexBindingsFile, error) {
	var file codexBindingsFile
	raw, err := readPrivateCodexStatus(path)
	if err != nil {
		return file, err
	}
	node, err := parseJSONNode(raw)
	if err != nil {
		return file, err
	}
	if !codexAuthorityShape(node) {
		return file, usageErr("invalid Codex ownership schema")
	}
	if json.Unmarshal(raw, &file) != nil || file.Version != 1 || file.Bindings == nil || len(file.Bindings) > 1024 {
		return file, usageErr("invalid Codex ownership file")
	}
	recipients, threads, profiles := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, b := range file.Bindings {
		u, err := url.Parse(b.Hub)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
			!codexGeneration.MatchString(b.Generation) || !codexThreadID.MatchString(b.ThreadID) ||
			strings.TrimSpace(b.Profile) == "" || len(b.Profile) > 4096 || strings.ContainsAny(b.Profile, "\x00\r\n") || !hookID.MatchString(b.Caller) || (b.As != "" && !hookID.MatchString(b.As)) ||
			!filepath.IsAbs(b.ConfigPath) || !filepath.IsAbs(b.CodexHome) || b.CreatedAt.IsZero() || !b.ExpiresAt.After(b.CreatedAt) || b.IdleDormantHours < 0 {
			return file, usageErr("legacy or invalid Codex binding; explicitly bind again")
		}
		config, ce := canonicalCodexPath(b.ConfigPath)
		home, he := canonicalCodexPath(b.CodexHome)
		ci, cse := os.Stat(b.ConfigPath)
		hi, hse := os.Stat(b.CodexHome)
		if ce != nil || he != nil || cse != nil || hse != nil || config != b.ConfigPath || home != b.CodexHome || !ci.Mode().IsRegular() || !hi.IsDir() {
			return file, usageErr("unverifiable Codex config or home")
		}
		r := normalizeHub(b.Hub) + "\n" + codexRecipient(b)
		t := b.CodexHome + "\n" + b.ThreadID
		// State remains keyed by config directory/profile/as for compatibility.
		p := filepath.Dir(b.ConfigPath) + "\n" + b.Profile + "\n" + b.As
		if recipients[r] || threads[t] || profiles[p] {
			return file, usageErr("conflicting Codex ownership")
		}
		recipients[r], threads[t], profiles[p] = true, true, true
	}
	return file, nil
}

// encoding/json accepts case-insensitive struct keys and null scalars. Reject
// those before decoding so Go and the connector cannot read different owners.
func codexAuthorityShape(n *jsonNode) bool {
	if n == nil || n.kind != '{' || n.member("version") == nil {
		return false
	}
	for _, raw := range n.keys {
		var key string
		if json.Unmarshal(raw, &key) != nil || (key != "version" && key != "bindings") {
			return false
		}
	}
	rows := n.member("bindings")
	if rows == nil || rows.kind != '[' || len(rows.vals) > 1024 {
		return false
	}
	for _, row := range rows.vals {
		if row.kind != '{' {
			return false
		}
		for _, key := range []string{"config_path", "generation", "profile", "hub", "caller", "codex_home", "thread_id", "created_at", "expires_at"} {
			v := row.member(key)
			if v == nil || v.kind != 's' || len(v.raw) == 0 || v.raw[0] != '"' {
				return false
			}
		}
		for i, raw := range row.keys {
			var key string
			if json.Unmarshal(raw, &key) != nil {
				return false
			}
			v := row.vals[i]
			switch key {
			case "config_path", "generation", "profile", "hub", "caller", "codex_home", "thread_id", "created_at", "expires_at", "as":
				if v.kind != 's' || len(v.raw) == 0 || v.raw[0] != '"' {
					return false
				}
			case "attended":
				if !bytes.Equal(v.raw, []byte("true")) && !bytes.Equal(v.raw, []byte("false")) {
					return false
				}
			case "idle_dormant_hours":
				var value float64
				if bytes.Equal(v.raw, []byte("null")) || json.Unmarshal(v.raw, &value) != nil {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
