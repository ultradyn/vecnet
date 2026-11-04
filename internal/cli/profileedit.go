package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var profileKeyLine = regexp.MustCompile(`^([ \t]*)([A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*"|'[^']*')([ \t]*=[ \t]*)(.*)$`)

// editProfiles accepts only explicit profile tables and single-line string
// values. Empty edit values remove the key (used to drop hub_pubkey). It proves
// both the typed config and the entire TOML tree before writing any backup.
func editProfiles(path string, edits map[string]map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var before Config
	beforeMD, err := toml.Decode(string(original), &before)
	if err != nil {
		return err
	}
	var expected map[string]any
	if _, err = toml.Decode(string(original), &expected); err != nil {
		return err
	}
	profiles, ok := expected["profiles"].(map[string]any)
	if !ok {
		return fmt.Errorf("profile edit: explicit profile tables required")
	}
	for name, keys := range edits {
		p, exists := before.Profiles[name]
		if !exists {
			return fmt.Errorf("profile edit: profile %q absent", name)
		}
		table, ok := profiles[name].(map[string]any)
		if !ok {
			return fmt.Errorf("profile edit: invalid profile %q", name)
		}
		for key, value := range keys {
			switch key {
			case "hub":
				p.Hub = value
			case "hub_pubkey":
				p.HubPubkey = value
			case "secret":
				p.Secret = value
			case "peer_id":
				p.PeerID = value
			default:
				return fmt.Errorf("profile edit: unsupported key %q", key)
			}
			if value == "" {
				delete(table, key)
			} else {
				table[key] = value
			}
		}
		before.Profiles[name] = p
	}
	// Multiline TOML needs a full token editor; refuse rather than guessing
	// where a value ends or mistaking its contents for a profile header.
	if bytes.Contains(original, []byte(`"""`)) || bytes.Contains(original, []byte(`'''`)) {
		return fmt.Errorf("profile edit: multiline values are unsafe")
	}
	lines := strings.SplitAfter(string(original), "\n")
	var out strings.Builder
	target := ""
	seen := map[string]bool{}
	found := map[string]bool{}
	newline := "\n"
	if bytes.Contains(original, []byte("\r\n")) {
		newline = "\r\n"
	}
	flush := func() error {
		keys := make([]string, 0, len(edits[target]))
		for key := range edits[target] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := edits[target][key]
			if seen[key] || value == "" {
				continue
			}
			encoded, e := profileString(value)
			if e != nil {
				return e
			}
			if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
				out.WriteString(newline)
			}
			out.WriteString(key + " = " + encoded + newline)
		}
		return nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			if err = flush(); err != nil {
				return err
			}
			target = ""
			seen = map[string]bool{}
			// Decode the header itself: quoted dots and escapes stay one name.
			var header struct {
				Profiles map[string]map[string]any `toml:"profiles"`
			}
			if _, e := toml.Decode(line+"\n__vecnet_profile_marker__ = true\n", &header); e == nil && len(header.Profiles) == 1 {
				for name, table := range header.Profiles {
					if table["__vecnet_profile_marker__"] == true {
						if _, wanted := edits[name]; wanted {
							if found[name] {
								return fmt.Errorf("profile edit: duplicate table")
							}
							found[name] = true
							target = name
						}
					}
				}
			}
			out.WriteString(line)
			continue
		}
		if target == "" || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out.WriteString(line)
			continue
		}
		match := profileKeyLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if match == nil {
			out.WriteString(line)
			continue
		}
		var decoded map[string]string
		if _, e := toml.Decode(match[2]+` = ""`, &decoded); e != nil {
			return e
		}
		key := ""
		for name := range decoded {
			key = name
		}
		value, wanted := edits[target][key]
		if !wanted {
			out.WriteString(line)
			continue
		}
		if seen[key] {
			return fmt.Errorf("profile edit: duplicate key")
		}
		seen[key] = true
		literal, comment, e := profileLiteral(match[4])
		if e != nil {
			return e
		}
		var old struct{ Value string }
		if _, e = toml.Decode("Value = "+literal, &old); e != nil {
			return fmt.Errorf("profile edit: unsafe value for %s", key)
		}
		if value == "" {
			continue
		}
		encoded, e := profileString(value)
		if e != nil {
			return e
		}
		ending := strings.TrimPrefix(line, strings.TrimRight(line, "\r\n"))
		out.WriteString(match[1] + match[2] + match[3] + encoded + comment + ending)
	}
	if err = flush(); err != nil {
		return err
	}
	for name := range edits {
		if !found[name] {
			return fmt.Errorf("profile edit: no explicit table for %q", name)
		}
	}
	result := []byte(out.String())
	var after Config
	afterMD, err := toml.Decode(string(result), &after)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeMD.Undecoded(), afterMD.Undecoded()) {
		return fmt.Errorf("profile edit: decoded profiles or unknown keys changed")
	}
	var actual map[string]any
	if _, err = toml.Decode(string(result), &actual); err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, actual) {
		return fmt.Errorf("profile edit: unrelated TOML data changed")
	}
	if bytes.Equal(original, result) {
		return nil
	}
	if err = writeFileAtomic(path+".bak", original); err != nil {
		return err
	}
	return writeFileAtomic(path, result)
}
func profileString(value string) (string, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]string{"v": value}); err != nil {
		return "", err
	}
	_, literal, ok := strings.Cut(b.String(), " = ")
	if !ok {
		return "", fmt.Errorf("profile edit: cannot encode string")
	}
	return strings.TrimSuffix(literal, "\n"), nil
}

// Split an inline comment outside the quoted string; keep its whitespace.
func profileLiteral(s string) (literal, comment string, err error) {
	var quote rune
	escaped := false
	for i, c := range s {
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '#' {
			value := strings.TrimRight(s[:i], " \t")
			return value, s[len(value):], nil
		}
	}
	if quote != 0 {
		return "", "", fmt.Errorf("profile edit: unterminated string")
	}
	value := strings.TrimRight(s, " \t")
	return value, s[len(value):], nil
}
