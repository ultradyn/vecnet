package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// setOperatorProfileLocked assumes path+".lock" is already held.
func setOperatorProfileLocked(path, name, quoted string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return writeFileAtomic(path, []byte("operator_profile = "+quoted+"\n"))
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return usageErr(fmt.Sprintf("config %s is a symlink; refusing to change the link or its target", path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if len(raw) == 0 {
		return commitOperatorProfile(path, info, raw, []byte("operator_profile = "+quoted+"\n"))
	}
	out, err := rewriteOperatorProfile(raw, name, quoted)
	if err != nil {
		return err
	}
	return commitOperatorProfile(path, info, raw, out)
}

// commitOperatorProfile replaces path only when it is still the same regular
// file that was read. A concurrent writer that changed the bytes wins; this
// refuses rather than putting the stale snapshot back.
func commitOperatorProfile(path string, info os.FileInfo, raw, out []byte) error {
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if now.Mode()&os.ModeSymlink != 0 {
		return usageErr(fmt.Sprintf("config %s is a symlink; refusing to change the link or its target", path))
	}
	again, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, again) || !now.ModTime().Equal(info.ModTime()) || now.Size() != info.Size() {
		return usageErr(fmt.Sprintf("config %s changed while operator_profile was being updated; refusing to overwrite", path))
	}
	return writeFileAtomic(path, out)
}

// rewriteOperatorProfile replaces only the value span of each root
// operator_profile assignment. Nested keys, comments and every other byte
// stay. A non-string root value is refused and nothing is returned to write.
func rewriteOperatorProfile(raw []byte, name, quoted string) ([]byte, error) {
	var before Config
	beforeMD, err := toml.Decode(string(raw), &before)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if before.Profiles == nil {
		before.Profiles = map[string]Profile{}
	}
	if len(before.Profiles) > 0 {
		if _, ok := before.Profiles[name]; !ok {
			return nil, usageErr(fmt.Sprintf("operator_profile %q is not a profile in the config", name))
		}
	}
	var tree map[string]any
	if _, err = toml.Decode(string(raw), &tree); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	spans, bad, err := rootOperatorProfileSpans(raw)
	if err != nil {
		return nil, err
	}
	if bad {
		return nil, usageErr("operator_profile is not a string value; refusing to rewrite it")
	}
	switch tree["operator_profile"].(type) {
	case nil:
		if len(spans) != 0 {
			return nil, usageErr("operator_profile assignment could not be read; refusing to rewrite it")
		}
	case string:
		if len(spans) == 0 {
			return nil, usageErr("operator_profile assignment could not be located; refusing to rewrite it")
		}
	default:
		return nil, usageErr("operator_profile is not a string value; refusing to rewrite it")
	}

	var out []byte
	if len(spans) == 0 {
		nl := "\n"
		if bytes.Contains(raw, []byte("\r\n")) {
			nl = "\r\n"
		}
		head := []byte("operator_profile = " + quoted + nl)
		out = append(head, raw...)
		if !bytes.Equal(out[len(head):], raw) {
			return nil, fmt.Errorf("operator_profile insert changed existing bytes")
		}
	} else {
		out = append([]byte(nil), raw...)
		rep := []byte(quoted)
		for i := len(spans) - 1; i >= 0; i-- {
			s, e := spans[i][0], spans[i][1]
			next := make([]byte, 0, len(out)-(e-s)+len(rep))
			next = append(next, out[:s]...)
			next = append(next, rep...)
			next = append(next, out[e:]...)
			out = next
		}
		if !sameOutside(raw, out, spans, len(rep)) {
			return nil, fmt.Errorf("operator_profile rewrite changed bytes outside the value")
		}
	}

	var after Config
	afterMD, err := toml.Decode(string(out), &after)
	if err != nil {
		return nil, fmt.Errorf("operator_profile rewrite is not valid TOML: %w", err)
	}
	if after.OperatorProfile != name {
		return nil, fmt.Errorf("operator_profile rewrite did not set %q", name)
	}
	if !reflect.DeepEqual(beforeMD.Undecoded(), afterMD.Undecoded()) {
		return nil, fmt.Errorf("operator_profile rewrite changed undecoded keys")
	}
	var beforeTree, afterTree map[string]any
	if _, err = toml.Decode(string(raw), &beforeTree); err != nil {
		return nil, err
	}
	if _, err = toml.Decode(string(out), &afterTree); err != nil {
		return nil, err
	}
	delete(beforeTree, "operator_profile")
	delete(afterTree, "operator_profile")
	if !reflect.DeepEqual(beforeTree, afterTree) {
		return nil, fmt.Errorf("operator_profile rewrite changed unrelated TOML")
	}
	return out, nil
}

// sameOutside reports that out differs from raw only inside spans, each of
// which was replaced by replLen bytes.
func sameOutside(raw, out []byte, spans [][2]int, replLen int) bool {
	shift := 0
	prev := 0
	for _, sp := range spans {
		if sp[0] < prev || sp[1] < sp[0] || sp[1] > len(raw) {
			return false
		}
		if !bytes.Equal(raw[prev:sp[0]], out[prev+shift:sp[0]+shift]) {
			return false
		}
		shift += replLen - (sp[1] - sp[0])
		prev = sp[1]
	}
	return bytes.Equal(raw[prev:], out[prev+shift:])
}

// rootOperatorProfileSpans returns the value spans of root operator_profile
// keys. bad is set when that key's value is not a string. Scanning stops at
// the first table header, so a nested operator_profile is left alone.
func rootOperatorProfileSpans(raw []byte) (spans [][2]int, bad bool, err error) {
	i := 0
	for {
		i = skipWSComments(raw, i)
		if i >= len(raw) {
			return spans, bad, nil
		}
		if raw[i] == '[' {
			return spans, bad, nil
		}
		single, dotted, next, keyErr := parseTOMLKey(raw, i)
		if keyErr != nil {
			return nil, false, fmt.Errorf("operator_profile: %w", keyErr)
		}
		i = skipWSComments(raw, next)
		if i >= len(raw) || raw[i] != '=' {
			return nil, false, fmt.Errorf("operator_profile: expected '='")
		}
		i = skipWSComments(raw, i+1)
		end, kind, valErr := parseTOMLValue(raw, i)
		if valErr != nil {
			return nil, false, fmt.Errorf("operator_profile: %w", valErr)
		}
		if !dotted && single == "operator_profile" {
			if kind != valString {
				bad = true
			} else {
				spans = append(spans, [2]int{i, end})
			}
		}
		i = end
	}
}

const (
	valString = iota
	valOther
)

func skipSpaces(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	return i
}

func skipWSComments(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '#':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
		default:
			return i
		}
	}
	return i
}

func isBareKey(b byte) bool {
	return b == '_' || b == '-' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func parseTOMLKey(raw []byte, i int) (single string, dotted bool, next int, err error) {
	var segs []string
	for {
		i = skipSpaces(raw, i)
		if i >= len(raw) {
			return "", false, i, fmt.Errorf("truncated key")
		}
		var seg string
		switch raw[i] {
		case '"', '\'':
			seg, i, err = parseQuotedKey(raw, i)
			if err != nil {
				return "", false, i, err
			}
		default:
			start := i
			for i < len(raw) && isBareKey(raw[i]) {
				i++
			}
			if start == i {
				return "", false, i, fmt.Errorf("invalid key")
			}
			seg = string(raw[start:i])
		}
		segs = append(segs, seg)
		j := skipSpaces(raw, i)
		if j < len(raw) && raw[j] == '.' {
			i = j + 1
			continue
		}
		if len(segs) == 1 {
			single = segs[0]
		}
		return single, len(segs) != 1, i, nil
	}
}

func parseQuotedKey(raw []byte, i int) (string, int, error) {
	end, err := skipString(raw, i)
	if err != nil {
		return "", i, err
	}
	var decoded map[string]string
	if _, err = toml.Decode("k = "+string(raw[i:end]), &decoded); err != nil {
		return "", i, err
	}
	return decoded["k"], end, nil
}

func parseTOMLValue(raw []byte, i int) (end, kind int, err error) {
	if i >= len(raw) {
		return i, valOther, fmt.Errorf("missing value")
	}
	switch {
	case bytes.HasPrefix(raw[i:], []byte(`"""`)):
		end, err = skipML(raw, i, []byte(`"""`), true)
		return end, valString, err
	case bytes.HasPrefix(raw[i:], []byte(`'''`)):
		end, err = skipML(raw, i, []byte(`'''`), false)
		return end, valString, err
	case raw[i] == '"', raw[i] == '\'':
		end, err = skipString(raw, i)
		return end, valString, err
	case raw[i] == '{':
		end, err = skipContainer(raw, i, '{', '}')
		return end, valOther, err
	case raw[i] == '[':
		end, err = skipContainer(raw, i, '[', ']')
		return end, valOther, err
	default:
		for i < len(raw) && raw[i] != '\n' && raw[i] != '#' {
			i++
		}
		return i, valOther, nil
	}
}

func skipString(raw []byte, i int) (int, error) {
	if i >= len(raw) {
		return i, fmt.Errorf("unterminated string")
	}
	quote := raw[i]
	i++
	for i < len(raw) {
		if quote == '"' && raw[i] == '\\' {
			if i+1 >= len(raw) {
				return i, fmt.Errorf("unterminated string")
			}
			i += 2
			continue
		}
		if raw[i] == quote {
			return i + 1, nil
		}
		if raw[i] == '\n' {
			return i, fmt.Errorf("unterminated string")
		}
		i++
	}
	return i, fmt.Errorf("unterminated string")
}

// skipML consumes a multiline string starting at the opening delimiter.
// Basic strings honour backslash escapes so an escaped closing delimiter
// stays inside the value.
func skipML(raw []byte, i int, delim []byte, basic bool) (int, error) {
	i += len(delim)
	for i < len(raw) {
		if basic && raw[i] == '\\' {
			if i+1 >= len(raw) {
				return i, fmt.Errorf("unterminated string")
			}
			if raw[i+1] == '\r' && i+2 < len(raw) && raw[i+2] == '\n' {
				i += 3
				continue
			}
			i += 2
			continue
		}
		if bytes.HasPrefix(raw[i:], delim) {
			return i + len(delim), nil
		}
		_, n := utf8.DecodeRune(raw[i:])
		if n < 1 {
			n = 1
		}
		i += n
	}
	return i, fmt.Errorf("unterminated string")
}

func skipContainer(raw []byte, i int, open, close byte) (int, error) {
	depth := 0
	for i < len(raw) {
		if raw[i] == '"' || raw[i] == '\'' {
			if bytes.HasPrefix(raw[i:], []byte(`"""`)) || bytes.HasPrefix(raw[i:], []byte(`'''`)) {
				delim := []byte(`"""`)
				basic := true
				if raw[i] == '\'' {
					delim = []byte(`'''`)
					basic = false
				}
				end, err := skipML(raw, i, delim, basic)
				if err != nil {
					return i, err
				}
				i = end
				continue
			}
			end, err := skipString(raw, i)
			if err != nil {
				return i, err
			}
			i = end
			continue
		}
		if raw[i] == '#' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		switch raw[i] {
		case open:
			depth++
		case close:
			depth--
			i++
			if depth == 0 {
				return i, nil
			}
			continue
		}
		i++
	}
	return i, fmt.Errorf("unterminated value")
}
