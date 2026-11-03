package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

var hookInstallProfile = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var hookShellSafe = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

func hookCommand(bin, profile, event string, autoAck bool) string {
	if !hookShellSafe.MatchString(bin) {
		bin = "'" + strings.ReplaceAll(bin, "'", "'\"'\"'") + "'"
	}
	profileFlag := "--profile " + profile
	if strings.HasPrefix(profile, "-") {
		// Keep flag-like values away from the global flag parser.
		profileFlag = "--profile=" + profile
	}
	command := bin + " hook " + profileFlag + " --event " + event
	if autoAck {
		command += " --auto-ack"
	}
	return command
}

// defaultBin returns a persistent executable path when it is the vecnet
// binary. Test executables and go-run binaries use PATH's vecnet instead.
// The warning is returned so installation can send it only to stderr.
func defaultBin(exe string) (bin, warning string) {
	if filepath.Base(exe) != "vecnet" {
		return "vecnet", ""
	}
	absolute, err := filepath.Abs(exe)
	if err != nil {
		return "vecnet", "cannot resolve executable; using vecnet from PATH"
	}
	rel, err := filepath.Rel(os.TempDir(), absolute)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "vecnet", "executable is temporary; using vecnet from PATH"
	}
	if strings.Contains(filepath.ToSlash(absolute), "/.worktrees/") {
		return absolute, "executable is in a worktree; the path will disappear when the worktree is cleaned up"
	}
	return absolute, ""
}

func (a *app) cmdHookInstall(args []string) error {
	o, err := parseFlags("hook install", args, map[string]flagKind{
		"profile": strFlag, "write": boolFlag, "settings": strFlag, "auto-ack": boolFlag, "bin": strFlag,
	})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("hook install: unexpected positional arguments")
	}
	profile := o.str("profile")
	if !o.has("profile") {
		_, profile, _, err = a.connect()
		if err != nil {
			return err
		}
	}
	if !hookInstallProfile.MatchString(profile) {
		return usageErr("hook install: profile must match [A-Za-z0-9._-]{1,64}")
	}
	bin := o.str("bin")
	if o.has("bin") {
		if bin == "" {
			return usageErr("hook install: --bin must not be empty")
		}
		if strings.Contains(filepath.ToSlash(bin), "/.worktrees/") {
			a.warn("executable is in a worktree; the path will disappear when the worktree is cleaned up")
		}
	} else {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		var warning string
		bin, warning = defaultBin(exe)
		if warning != "" {
			a.warn("%s", warning)
		}
	}
	path := o.str("settings")
	if o.has("settings") {
		if path == "" {
			return usageErr("hook install: --settings must not be empty")
		}
		fmt.Fprintf(a.stderr, "settings target %s (explicit --settings)\n", path)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
		cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
		// Also bound pipe teardown if a misbehaving git child retains stdout.
		cmd.WaitDelay = 100 * time.Millisecond
		rootBytes, gitErr := cmd.Output()
		cancel()
		root := strings.TrimSuffix(string(rootBytes), "\n")
		reason := "git top-level; personal settings.local.json"
		if gitErr != nil || root == "" {
			root = cwd
			reason = "cwd fallback; personal settings.local.json"
			fmt.Fprintf(a.stderr, "not in a git repository; using %s\n", cwd)
		}
		path = filepath.Join(root, ".claude", "settings.local.json")
		fmt.Fprintf(a.stderr, "settings target %s (%s)\n", path, reason)
	}
	if !o.has("write") {
		stanza, _, err := mergeHookSettings(nil, bin, profile, o.has("auto-ack"))
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "%s\n", stanza); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "would merge into %s; rerun with --write\n", path)
		return nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Do not replace a dangling symlink with an ordinary file.
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return err
		}
		target = path
	}
	mode := os.FileMode(0644)
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		mode = info.Mode()
	}
	merged, changed, err := mergeHookSettings(existing, bin, profile, o.has("auto-ack"))
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(a.stderr, "unchanged %s\n", path)
		return nil
	}
	if err := writeHookSettings(target, merged, mode); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "wrote %s (UserPromptSubmit, PostToolUse)\n", path)
	return nil
}

// Settings have different permissions from secret config and shown-state,
// so do not use writeFileAtomic (which always writes 0600) here.
func writeHookSettings(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Objects retain source-ordered raw keys; scalars retain their original
// spelling. At the root raw also retains the source's trailing LF policy.
type jsonNode struct {
	kind byte // '{', '[' or 's'
	keys [][]byte
	vals []*jsonNode
	raw  []byte
}

func parseJSONNode(src []byte) (*jsonNode, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	var read func() (*jsonNode, error)
	token := func() (json.Token, []byte, error) {
		start := dec.InputOffset()
		value, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		return value, bytes.Trim(src[start:dec.InputOffset()], " \t\r\n,:"), nil
	}
	read = func() (*jsonNode, error) {
		value, raw, err := token()
		if err != nil {
			return nil, err
		}
		delim, container := value.(json.Delim)
		if !container {
			return &jsonNode{kind: 's', raw: raw}, nil
		}
		if delim != '{' && delim != '[' {
			return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
		n := &jsonNode{kind: byte(delim)}
		// Compare decoded keys so escaped-equivalent spellings cannot hide
		// the effective value from member lookup or downstream consumers.
		seen := make(map[string]struct{})
		for dec.More() {
			if delim == '{' {
				key, raw, err := token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("JSON object key is not a string")
				}
				if _, duplicate := seen[name]; duplicate {
					return nil, fmt.Errorf("duplicate JSON object key %q", name)
				}
				seen[name] = struct{}{}
				n.keys = append(n.keys, raw)
			}
			child, err := read()
			if err != nil {
				return nil, err
			}
			n.vals = append(n.vals, child)
		}
		closing, _, err := token()
		if err != nil {
			return nil, err
		}
		want := json.Delim('}')
		if delim == '[' {
			want = ']'
		}
		if closing != want {
			return nil, errors.New("mismatched JSON delimiter")
		}
		return n, nil
	}
	n, err := read()
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("trailing JSON data")
	}
	n.raw = src
	return n, nil
}

func detectIndent(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		count := 0
		for count < len(line) && line[count] == ' ' {
			count++
		}
		if count == 0 && len(line) > 0 && line[0] == '\t' {
			return "\t"
		}
		if count > 0 && count < len(line) {
			return string(line[:count])
		}
	}
	return "  "
}

func (n *jsonNode) encode(indent string) []byte {
	var out bytes.Buffer
	var write func(*jsonNode, int)
	write = func(node *jsonNode, depth int) {
		if node.kind == 's' {
			out.Write(bytes.TrimSpace(node.raw))
			return
		}
		out.WriteByte(node.kind)
		for i, value := range node.vals {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteByte('\n')
			out.WriteString(strings.Repeat(indent, depth+1))
			if node.kind == '{' {
				out.Write(node.keys[i])
				out.WriteString(": ")
			}
			write(value, depth+1)
		}
		if len(node.vals) > 0 {
			out.WriteByte('\n')
			out.WriteString(strings.Repeat(indent, depth))
		}
		if node.kind == '{' {
			out.WriteByte('}')
		} else {
			out.WriteByte(']')
		}
	}
	write(n, 0)
	if bytes.HasSuffix(n.raw, []byte("\n")) {
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func (n *jsonNode) member(name string) *jsonNode {
	if n == nil || n.kind != '{' {
		return nil
	}
	for i, raw := range n.keys {
		var key string
		if json.Unmarshal(raw, &key) == nil && key == name {
			return n.vals[i]
		}
	}
	return nil
}
func (n *jsonNode) appendMember(name string, value *jsonNode) {
	key, _ := json.Marshal(name)
	n.keys = append(n.keys, key)
	n.vals = append(n.vals, value)
}
func (n *jsonNode) scalarString() string {
	if n == nil || n.kind != 's' {
		return ""
	}
	var value string
	_ = json.Unmarshal(n.raw, &value)
	return value
}

// shellFirstWord decodes quoting without evaluating any shell expressions.
// It recognises the quoting emitted by hookCommand, including apostrophes.
func shellFirstWord(command string) (wordValue, rest string, ok bool) {
	command = strings.TrimLeft(command, " \t\r\n")
	var word strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote == 0 && strings.ContainsRune(" \t\r\n", rune(c)) {
			return word.String(), strings.TrimSpace(command[i:]), true
		}
		if c == '\\' && quote != '\'' {
			i++
			if i == len(command) {
				return "", "", false
			}
			word.WriteByte(command[i])
		} else if c == quote {
			quote = 0
		} else if quote == 0 && (c == '\'' || c == '"') {
			quote = c
		} else {
			word.WriteByte(c)
		}
	}
	return word.String(), "", quote == 0
}

func oursHook(handler *jsonNode, bin, profile, event string) bool {
	if handler.member("type").scalarString() != "command" {
		return false
	}
	command := handler.member("command").scalarString()
	word, args, ok := shellFirstWord(command)
	// Keep recognising vecnet installations after their binary path changes,
	// and recognise renamed executables only at the configured path. The latter
	// is required for the protocol's unconditional repeat-install idempotence.
	if !ok || (filepath.Base(word) != "vecnet" && word != bin) {
		return false
	}
	var words []string
	for args != "" {
		word, rest, ok := shellFirstWord(args)
		if !ok {
			return false
		}
		words = append(words, word)
		args = rest
	}
	// Accept supported global and hook flags, including repeats and either
	// value syntax. Parsing values avoids substring matches for other profiles
	// or events. Keep recognising legacy space-separated flag-like profiles.
	o, err := parseFlags("hook", words, map[string]flagKind{
		"profile": strFlag, "event": strFlag, "auto-ack": boolFlag,
		"as": strFlag, "net": strFlag, "json": boolFlag,
	})
	return err == nil && len(o.pos) == 1 && o.pos[0] == "hook" &&
		o.str("profile") == profile && o.str("event") == event
}

func mergeHookSettings(existing []byte, bin, profile string, autoAck bool) (out []byte, changed bool, err error) {
	src := existing
	if len(bytes.TrimSpace(src)) == 0 {
		src = []byte("{}")
	}
	root, err := parseJSONNode(src)
	if err != nil {
		return nil, false, fmt.Errorf("hook install: invalid settings JSON: %w", err)
	}
	if root.kind != '{' {
		return nil, false, errors.New("hook install: settings must be an object")
	}
	hooks := root.member("hooks")
	if hooks == nil {
		hooks = &jsonNode{kind: '{'}
		root.appendMember("hooks", hooks)
	}
	if hooks.kind != '{' {
		return nil, false, errors.New("hook install: hooks must be an object")
	}
	for i, event := range hooks.vals {
		if event.kind != '[' {
			var key string
			_ = json.Unmarshal(hooks.keys[i], &key)
			return nil, false, fmt.Errorf("hook install: hooks.%s must be an array", key)
		}
	}
	for _, event := range []struct{ key, name, matcher string }{
		{"UserPromptSubmit", "user-prompt", ""}, {"PostToolUse", "post-tool", "*"},
	} {
		groups := hooks.member(event.key)
		if groups == nil {
			groups = &jsonNode{kind: '['}
			hooks.appendMember(event.key, groups)
		}
		// Ordered structs determine every new node's key order.
		raw, err := json.Marshal(hookGroup{Matcher: event.matcher, Hooks: []hookHandler{{Type: "command", Command: hookCommand(bin, profile, event.name, autoAck), Timeout: 5}}})
		if err != nil {
			return nil, false, err
		}
		group, err := parseJSONNode(raw)
		if err != nil {
			return nil, false, err
		}
		replacement := group.member("hooks").vals[0]
		replaced := false
		for _, oldGroup := range groups.vals {
			handlers := oldGroup.member("hooks")
			if handlers == nil || handlers.kind != '[' {
				continue
			}
			for i, handler := range handlers.vals {
				if oursHook(handler, bin, profile, event.name) {
					handlers.vals[i] = replacement
					replaced = true
				}
			}
		}
		if !replaced {
			groups.vals = append(groups.vals, group)
		}
	}
	// Even a whitespace-only file keeps its original trailing LF policy.
	root.raw = existing
	out = root.encode(detectIndent(existing))
	return out, !bytes.Equal(out, existing), nil
}
