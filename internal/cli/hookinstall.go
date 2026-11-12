package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type hookHandler struct {
	Type                   string `json:"type"`
	Command                string `json:"command"`
	Timeout                int    `json:"timeout"`
	AdditionalContextLimit int    `json:"additionalContextLimit,omitempty"`
}

type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

// Each context has at most hookMaxUnits UTF-16 units, or 3*hookMaxUnits
// UTF-8 bytes. A token covers at least one byte, so this threshold keeps
// Codex from spilling peer text to hook_outputs. Pipes also cap output at
// hookPipeAtomicBytes.
const hookCodexContextLimit = 3 * hookMaxUnits

type hookInstallEvent struct {
	key, name, matcher string
	contextLimit       int
}

var claudeHookEvents = []hookInstallEvent{
	{"UserPromptSubmit", "user-prompt", "", 0},
	{"PostToolUse", "post-tool", "*", 0},
}

var codexHookEvents = []hookInstallEvent{
	{"SessionStart", "session-start", "", hookCodexContextLimit},
	{"UserPromptSubmit", "user-prompt", "", hookCodexContextLimit},
	{"PostToolUse", "post-tool", "*", hookCodexContextLimit},
}

// Profiles never start with '-', so an installed command cannot be read
// as flags by the global flag parser.
var hookInstallProfile = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)
var hookShellSafe = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

// hookShellWord is the only shell quoting hookCommand uses.
func hookShellWord(bin string) string {
	if hookShellSafe.MatchString(bin) {
		return bin
	}
	return "'" + strings.ReplaceAll(bin, "'", `'"'"'`) + "'"
}

// hookCommand is an installed handler's command; mcp, when not empty, is
// the MCP server name its hints name (`hook install --mcp`).
func hookCommand(bin, profile, event string, autoAck bool, mcp string) string {
	command := hookShellWord(bin) + " hook --profile " + profile + " --event " + event
	if autoAck {
		command += " --auto-ack"
	}
	if mcp != "" {
		command += " --mcp " + mcp
	}
	return command
}

// hookOwnedCommand matches exactly the commands hookCommand can emit, so a
// re-run with or without --mcp replaces them in place.
var hookOwnedCommand = regexp.MustCompile(`^([A-Za-z0-9/._-]+|'(?:[^']|'"'"')*') hook --profile ([^ ]+) --event ([^ ]+)( --auto-ack)?( --mcp vecnet-[A-Za-z0-9_-]{1,24})?$`)

// errHookInstallMCP refuses `hook install --mcp` for a profile with no MCP
// server name, or under a global --as the installed hooks never carry.
var errHookInstallMCP = usageErr("hook install --mcp: profile must be 1-24 letters, digits, _ or - (MCP server names), and the installed hooks never take --as")

// hookClaudeTimeout bounds each `claude mcp` run of `hook install --mcp --write`.
var hookClaudeTimeout = 10 * time.Second

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
		"client": strFlag, "profile": strFlag, "write": boolFlag, "settings": strFlag, "auto-ack": boolFlag, "bin": strFlag, "mcp": boolFlag, "dry-run": boolFlag, "rollback": strFlag,
	})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("hook install: unexpected positional arguments")
	}
	client := "claude"
	if o.has("client") {
		client = o.str("client")
	}
	if client != "claude" && client != "codex" {
		return usageErr("hook install: --client must be claude or codex")
	}
	if client == "codex" && o.has("mcp") {
		return usageErr("hook install: --mcp registration is supported only for --client claude")
	}
	if o.has("write") && o.has("dry-run") {
		return usageErr("hook install: --write and --dry-run are mutually exclusive")
	}
	if client != "codex" && (o.has("rollback") || o.has("dry-run")) {
		return usageErr("hook install: --rollback and --dry-run require --client codex")
	}
	if client == "codex" && o.has("auto-ack") {
		return usageErr("hook install: Codex hooks never claim or acknowledge messages; omit --auto-ack")
	}
	codexAdvice := func() {
		if client == "codex" {
			fmt.Fprintln(a.stderr, "codex: trust these hooks in /hooks before they run; new or changed hooks are skipped until trusted")
			fmt.Fprintln(a.stderr, "codex: project hooks load only in a trusted project; to keep this file personal, list .codex/hooks.json in .git/info/exclude")
		}
	}
	profile := o.str("profile")
	if client != "codex" && !o.has("profile") {
		// A hook is an agent's identity: never the operator of a bare run.
		if a.namedProfile() == "" {
			return usageErr("hook install: name the agent's profile with --profile P (or --net P, or $VECNET_PROFILE)")
		}
		_, profile, _, err = a.connect()
		if err != nil {
			return err
		}
	}
	if client != "codex" && !hookInstallProfile.MatchString(profile) {
		return usageErr("hook install: profile must match [A-Za-z0-9._][A-Za-z0-9._-]{0,63}")
	}
	mcp := ""
	if o.has("mcp") {
		if mcp = mcpServerName(profile, ""); mcp == "" || a.as != "" {
			return errHookInstallMCP
		}
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
	// The start directory picks the default target, and is where `claude
	// mcp` registers a local-scope server. Claude Code exports
	// CLAUDE_PROJECT_DIR, the directory the session started in, to hooks;
	// it stays put when the session's cwd moves.
	cwd, cwdErr := os.Getwd()
	start, from := cwd, "cwd"
	projectDir := a.getenv("CLAUDE_PROJECT_DIR")
	projectOK := false
	if client == "claude" && projectDir != "" {
		if info, err := os.Stat(projectDir); err == nil && info.IsDir() {
			start, from, projectOK = projectDir, "CLAUDE_PROJECT_DIR", true
		}
	}
	if cwdErr != nil && !projectOK && (!o.has("settings") || mcp != "") {
		return cwdErr
	}
	codexRoot := ""
	// codexUnknown records a repository-discovery failure that is not git's
	// own "not a git repository" answer (git missing, timed out, refused the
	// repository): the default target's tracking status is then unknown.
	var codexUnknown error
	if o.has("settings") {
		if path == "" {
			return usageErr("hook install: --settings must not be empty")
		}
		fmt.Fprintf(a.stderr, "settings target %s (explicit --settings)\n", path)
	} else if client == "codex" {
		root, gitErr := a.hookGit(cwd, "--show-toplevel")
		reason := "git top-level; Codex project hooks"
		if gitErr != nil || root == "" {
			if hookNotGitRepo(gitErr) {
				fmt.Fprintf(a.stderr, "not in a git repository; using %s\n", cwd)
			} else {
				codexUnknown = gitErr
				if codexUnknown == nil {
					codexUnknown = errors.New("git printed no top-level")
				}
				fmt.Fprintf(a.stderr, "could not determine the git repository (%v); using %s\n", hookGitReason(codexUnknown), cwd)
			}
			root = cwd
			reason = "cwd fallback; Codex project hooks"
		} else {
			codexRoot = root
		}
		path = filepath.Join(root, ".codex", "hooks.json")
		fmt.Fprintf(a.stderr, "settings target %s (%s)\n", path, reason)
	} else {
		if projectOK && projectDir != cwd {
			fmt.Fprintf(a.stderr, "using CLAUDE_PROJECT_DIR %s rather than the current directory\n", projectDir)
		} else if !projectOK && projectDir != "" {
			a.warn("CLAUDE_PROJECT_DIR %s is not a directory; using the current directory", projectDir)
		}
		root, gitErr := a.hookGit(start, "--show-toplevel")
		reason := from + " git top-level; personal settings.local.json"
		if gitErr != nil || root == "" {
			root = start
			reason = from + " fallback; personal settings.local.json"
			fmt.Fprintf(a.stderr, "not in a git repository; using %s\n", start)
		} else if main := a.hookMainCheckout(start); main != "" {
			want := filepath.Join(main, ".claude", "settings.local.json")
			a.warn("%s is a linked git worktree; Claude Code reads settings.local.json from the main checkout's root, so sessions here most likely read %s. Rerun from %s, or pass --settings %s", root, want, main, want)
		}
		path = filepath.Join(root, ".claude", "settings.local.json")
		fmt.Fprintf(a.stderr, "settings target %s (%s)\n", path, reason)
	}
	if client == "codex" {
		if o.has("write") && !o.has("settings") {
			if codexUnknown != nil {
				return fmt.Errorf("hook install: cannot tell whether %s is tracked by git (repository discovery failed: %s); pass --settings %s to write it anyway", path, hookGitReason(codexUnknown), path)
			}
			if codexRoot != "" {
				if err := a.checkCodexUntracked(codexRoot, path); err != nil {
					return err
				}
			}
			if err := defaultSettingsRedirect(path, "the project .codex/hooks.json"); err != nil {
				return err
			}
		}
		err := a.installCodexHooks(path, bin, o)
		if err == nil {
			codexAdvice()
			fmt.Fprintln(a.stderr, "codex: generic hooks never deliver peer content; use vecnet --net P codex setup for exact-thread app-server delivery")
		}
		return err
	}
	if raw, err := os.ReadFile(path); err == nil {
		if others := otherHookProfiles(raw, profile); len(others) > 0 {
			also := ""
			if mcp != "" {
				also = "; every session there also sees each registered vecnet MCP server's tools"
			}
			a.warn("%s already has vecnet hooks for profile(s) %s; every Claude Code session that reads it runs all of them. Session binding keeps each profile's deliveries in one session, and a session that runs several profiles' hooks binds none of them until its agent runs `vecnet hook rebind --profile P` (PROTOCOL §13.9). Launch each session with its own profile, e.g. VECNET_PROFILE=%s claude, so the other profiles' hooks stay silent there%s", path, strings.Join(others, ", "), profile, also)
		}
	}
	a.warnNestedCheckouts(path)
	var register []string
	if mcp != "" {
		register = []string{"mcp", "add", "--scope", "local", mcp}
		// --env follows the name: a variadic option before it would take it.
		if conf := a.getenv("VECNET_CONFIG"); conf != "" {
			if abs, err := filepath.Abs(conf); err == nil {
				conf = abs
			}
			register = append(register, "--env", "VECNET_CONFIG="+conf)
		}
		register = append(register, "--", bin, "--net", profile, "mcp")
	}
	if !o.has("write") {
		stanza, _, err := mergeHooks(nil, bin, profile, o.has("auto-ack"), mcp, claudeHookEvents)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "%s\n", stanza); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "would merge into %s; rerun with --write\n", path)
		if register != nil {
			a.printMCPRegistration(register)
		}
		return nil
	}
	if !o.has("settings") {
		if err := defaultSettingsRedirect(path, "the personal settings.local.json"); err != nil {
			return err
		}
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
	merged, changed, err := mergeHooks(existing, bin, profile, o.has("auto-ack"), mcp, claudeHookEvents)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(a.stderr, "unchanged %s\n", path)
	} else {
		if err := writeHookSettings(target, merged, mode); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "wrote %s (UserPromptSubmit, PostToolUse)\n", path)
	}
	if register != nil {
		a.printMCPRegistration(register)
		a.registerMCP(start, mcp, register)
	}
	return nil
}

// printMCPRegistration prints the `claude mcp add` command for argv on
// stderr, shell-quoted as the hooks are.
func (a *app) printMCPRegistration(argv []string) {
	words := make([]string, len(argv))
	for i, w := range argv {
		if conf, ok := strings.CutPrefix(w, "VECNET_CONFIG="); ok {
			words[i] = "VECNET_CONFIG=" + hookShellWord(conf)
		} else {
			words[i] = hookShellWord(w)
		}
	}
	fmt.Fprintf(a.stderr, "MCP registration (local scope: this project only): claude %s\n", strings.Join(words, " "))
}

// registerMCP replaces the local-scope MCP server name in dir's project
// config with the add argv, when claude is on PATH. Local-scope MCP config
// is per project, so claude runs in the installer's start directory. A
// failure is reported; the hooks are already written.
func (a *app) registerMCP(dir, name string, add []string) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		a.warn("claude is not on PATH; run the command above to register MCP server %s", name)
		return
	}
	run := func(argv []string) error {
		ctx, cancel := context.WithTimeout(a.ctx, hookClaudeTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, claude, argv...)
		cmd.Dir = dir
		cmd.WaitDelay = 100 * time.Millisecond
		out, err := cmd.CombinedOutput()
		if err != nil {
			if msg := strings.TrimSpace(string(out)); msg != "" {
				return fmt.Errorf("%w: %s", err, inert(msg, 200))
			}
		}
		return err
	}
	_ = run([]string{"mcp", "remove", "--scope", "local", name}) // absent is fine
	if err := run(add); err != nil {
		a.warn("claude mcp add failed (%v); run the command above to register MCP server %s", err, name)
		return
	}
	fmt.Fprintf(a.stderr, "registered MCP server %s (local scope, %s)\n", name, dir)
}

// warnNestedCheckouts warns when the target's checkout sits inside, or
// contains, another checkout whose settings.local.json already has vecnet
// hooks: a Claude Code session in the inner one can load both files, so it
// runs both sets of hooks (issue 39). Ancestors are found by walking up;
// nested ones from `git worktree list`. Errors just skip the check.
func (a *app) warnNestedCheckouts(path string) {
	if filepath.Base(filepath.Dir(path)) != ".claude" {
		return
	}
	root := filepath.Dir(filepath.Dir(path))
	home := a.getenv("HOME")
	report := func(dir, rel string) {
		raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
		if err != nil {
			return
		}
		if profiles := otherHookProfiles(raw, ""); len(profiles) > 0 {
			a.warn("%s is %s %s, whose .claude/settings.local.json has vecnet hooks for profile(s) %s; a Claude Code session in the inner checkout can load both files and run both sets of hooks. Launch each session with VECNET_PROFILE=<its profile> (PROTOCOL §13.9)", root, rel, dir, strings.Join(profiles, ", "))
		}
	}
	for dir := filepath.Dir(root); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if dir != home {
			report(dir, "inside")
		}
	}
	list, err := a.hookGit(root, "--show-toplevel") // cheap probe: is root in git?
	if err != nil || list == "" {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain")
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return
	}
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	}
	for _, line := range strings.Split(string(out), "\n") {
		wt, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if r, err := filepath.EvalSymlinks(wt); err == nil {
			wt = r
		}
		if strings.HasPrefix(wt, realRoot+string(filepath.Separator)) {
			report(wt, "the parent of")
		}
	}
}

// hookGit runs one bounded `git -C dir rev-parse <arg>` and returns its
// trimmed output.
func (a *app) hookGit(dir, arg string) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", arg)
	// Untranslated messages, so hookNotGitRepo can recognise git's answer.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	// Also bound pipe teardown if a misbehaving git child retains stdout.
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("git timed out: %w", ctx.Err())
	}
	return strings.TrimSuffix(string(out), "\n"), err
}

// hookNotGitRepo reports whether err is git's own answer that the directory
// is outside every repository. Any other failure (git missing, a timeout, a
// refused repository such as dubious ownership) leaves the answer unknown.
func hookNotGitRepo(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 128 && bytes.Contains(exit.Stderr, []byte("not a git repository"))
}

// hookGitReason is a one-line description of a failed git probe.
func hookGitReason(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(exit.Stderr)), "\n"); line != "" {
			return line
		}
	}
	return err.Error()
}

// checkCodexUntracked allows only git's explicit untracked result. Discovery
// succeeded, so a failed status probe must not share a personal profile.
func (a *app) checkCodexUntracked(root, path string) error {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--error-unmatch", "--", ".codex/hooks.json")
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil {
		return nil
	}
	return fmt.Errorf("hook install: %s is tracked by git (or its status is unknown); writing it would share your profile with everyone; pass --settings %s to write it anyway", path, path)
}

// hookMainCheckout returns the main checkout's root when dir is in a
// linked worktree (its git dir differs from the common dir), else "".
func (a *app) hookMainCheckout(dir string) string {
	gitDir, err1 := a.hookGit(dir, "--git-dir")
	common, err2 := a.hookGit(dir, "--git-common-dir")
	if err1 != nil || err2 != nil || gitDir == "" || common == "" {
		return ""
	}
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	gitDir, common = abs(gitDir), abs(common)
	if gitDir == common {
		return ""
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common // a bare repository's worktree: name the common dir
}

// otherHookProfiles lists the profiles, other than profile, of handlers
// in exactly the form `hook install` writes. Unparseable settings list
// nothing; the merge reports them.
func otherHookProfiles(settings []byte, profile string) []string {
	root, err := parseJSONNode(settings)
	if err != nil {
		return nil
	}
	var found []string
	hooks := root.member("hooks")
	if hooks == nil || hooks.kind != '{' {
		return nil
	}
	for _, groups := range hooks.vals {
		if groups.kind != '[' {
			continue
		}
		for _, group := range groups.vals {
			handlers := group.member("hooks")
			if handlers == nil || handlers.kind != '[' {
				continue
			}
			for _, handler := range handlers.vals {
				if handler.member("type").scalarString() != "command" {
					continue
				}
				m := hookOwnedCommand.FindStringSubmatch(handler.member("command").scalarString())
				if m != nil && m[2] != profile && !slices.Contains(found, inert(m[2], 64)) {
					found = append(found, inert(m[2], 64))
				}
			}
		}
	}
	slices.Sort(found)
	return found
}

// defaultSettingsRedirect refuses a default target whose file or parent
// directory is a symlink, leaving the link and its target unchanged.
func defaultSettingsRedirect(path, what string) error {
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		resolved := p
		if r, err := filepath.EvalSymlinks(p); err == nil {
			resolved = r
		} else if l, err := os.Readlink(p); err == nil {
			resolved = l
		}
		return fmt.Errorf("hook install: %s is a symlink to %s; the default target is only %s, so rerun with --settings %s to write through it", p, resolved, what, path)
	}
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

// oursHook reports whether handler is one vecnet hook install wrote for
// this profile and event: the exact hookCommand form, with no shell parsing.
// Any other spelling, even one a shell would run identically, is foreign
// and preserved. The binary may be any vecnet, so installations survive a
// changed path, or a renamed binary at the --bin being installed.
func oursHook(handler *jsonNode, bin, profile, event string) bool {
	if handler.member("type").scalarString() != "command" {
		return false
	}
	m := hookOwnedCommand.FindStringSubmatch(handler.member("command").scalarString())
	if m == nil || m[2] != profile || m[3] != event {
		return false
	}
	word := m[1]
	if strings.HasPrefix(word, "'") {
		word = strings.ReplaceAll(word[1:len(word)-1], `'"'"'`, "'")
	}
	// Quoting must be exactly hookCommand's: 'vecnet' is not its spelling.
	if hookShellWord(word) != m[1] {
		return false
	}
	base := filepath.Base(word)
	return base == "vecnet" || base == "vecnet.exe" || word == bin
}

func mergeHookSettings(existing []byte, bin, profile string, autoAck bool, mcp string) (out []byte, changed bool, err error) {
	return mergeHooks(existing, bin, profile, autoAck, mcp, claudeHookEvents)
}

func mergeHooks(existing []byte, bin, profile string, autoAck bool, mcp string, events []hookInstallEvent) (out []byte, changed bool, err error) {
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
	for _, event := range events {
		groups := hooks.member(event.key)
		if groups == nil {
			groups = &jsonNode{kind: '['}
			hooks.appendMember(event.key, groups)
		}
		// Ordered structs determine every new node's key order.
		raw, err := json.Marshal(hookGroup{Matcher: event.matcher, Hooks: []hookHandler{{Type: "command", Command: hookCommand(bin, profile, event.name, autoAck, mcp), Timeout: 5, AdditionalContextLimit: event.contextLimit}}})
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

// Codex shared project hooks contain no recipient. Only exactly known legacy
// handlers are migrated; custom matchers, options and shell commands survive.
func mergeCodexHooks(existing []byte, bin string) ([]byte, bool, error) {
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
			return nil, false, fmt.Errorf("hook install: hooks.%s must be an array", hooks.keys[i])
		}
	}
	for _, event := range codexHookEvents {
		groups := hooks.member(event.key)
		if groups == nil {
			groups = &jsonNode{kind: '['}
			hooks.appendMember(event.key, groups)
		}
		kept := make([]*jsonNode, 0, len(groups.vals)+1)
		for _, group := range groups.vals {
			handlers := group.member("hooks")
			if !codexOwnedGroup(group, event.matcher) || handlers == nil || handlers.kind != '[' {
				kept = append(kept, group)
				continue
			}
			retained := make([]*jsonNode, 0, len(handlers.vals))
			removed := false
			for _, handler := range handlers.vals {
				if codexOwnedHandler(handler, bin, event.name) {
					removed = true
				} else {
					retained = append(retained, handler)
				}
			}
			if !removed || len(retained) > 0 {
				handlers.vals = retained
				kept = append(kept, group)
			}
		}
		raw, _ := json.Marshal(hookGroup{Matcher: event.matcher, Hooks: []hookHandler{{Type: "command", Command: hookShellWord(bin) + " hook --client codex --event " + event.name, Timeout: 5, AdditionalContextLimit: event.contextLimit}}})
		group, err := parseJSONNode(raw)
		if err != nil {
			return nil, false, err
		}
		groups.vals = append(kept, group)
	}
	root.raw = existing
	out := root.encode(detectIndent(existing))
	return out, !bytes.Equal(existing, out), nil
}

func codexOwnedGroup(group *jsonNode, matcher string) bool {
	if group == nil || group.kind != '{' || group.member("matcher").scalarString() != matcher {
		return false
	}
	// A non-string matcher is never one the installer wrote.
	if m := group.member("matcher"); m != nil {
		var value string
		if json.Unmarshal(m.raw, &value) != nil {
			return false
		}
	}
	for _, key := range group.keys {
		var name string
		_ = json.Unmarshal(key, &name)
		if name != "hooks" && name != "matcher" {
			return false
		}
	}
	return true
}

func codexOwnedHandler(handler *jsonNode, bin, event string) bool {
	if handler == nil || handler.kind != '{' || handler.member("type").scalarString() != "command" {
		return false
	}
	for _, key := range handler.keys {
		var name string
		_ = json.Unmarshal(key, &name)
		switch name {
		case "type", "command", "timeout", "additionalContextLimit":
		default:
			return false
		}
	}
	timeout := handler.member("timeout")
	if timeout == nil || string(timeout.raw) != "5" {
		return false
	}
	if limit := handler.member("additionalContextLimit"); limit != nil && string(limit.raw) != fmt.Sprint(hookCodexContextLimit) {
		return false
	}
	command := handler.member("command").scalarString()
	// Reuse exact command and binary recognition for generic commands too.
	if strings.Contains(command, " hook --client codex --event ") {
		command = strings.Replace(command, " hook --client codex --event ", " hook --profile generic --event ", 1)
		raw, _ := json.Marshal(hookHandler{Type: "command", Command: command})
		candidate, _ := parseJSONNode(raw)
		return command == hookCommand(bin, "generic", event, false, "") || (oursHook(candidate, bin, "generic", event) && !strings.Contains(command, " --auto-ack") && !strings.Contains(command, " --mcp"))
	}
	m := hookOwnedCommand.FindStringSubmatch(command)
	if m == nil || !hookInstallProfile.MatchString(m[2]) || m[3] != event {
		return false
	}
	// Old --mcp is owned only when it names that handler's profile.
	if m[5] != "" && m[5] != " --mcp "+mcpServerName(m[2], "") {
		return false
	}
	return oursHook(handler, bin, m[2], event)
}

type codexHookBackup struct {
	Version     int    `json:"version"`
	Target      string `json:"target"`
	Existed     bool   `json:"existed"`
	Mode        uint32 `json:"mode"`
	Before      []byte `json:"before"`
	AfterSHA256 string `json:"after_sha256"`
}

func hookDigest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

// The backup carries the exact preimage and the expected postimage hash. A
// rollback never overwrites changes made since installation, including modes.
func (a *app) installCodexHooks(path, bin string, o opts) error {
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if resolved, e := filepath.EvalSymlinks(target); e == nil {
		target = resolved
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	} else {
		if st, e := os.Lstat(target); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return errors.New("hook install: dangling settings symlink")
		}
		// Canonicalize an existing parent even for a new file.
		if parent, e := filepath.EvalSymlinks(filepath.Dir(target)); e == nil {
			target = filepath.Join(parent, filepath.Base(target))
		}
	}
	if o.has("write") {
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		unlock, err := lockPath(a.ctx, target+".vecnet.lock")
		if err != nil {
			return err
		}
		defer unlock()
	}
	before, err := os.ReadFile(target)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0644)
	if existed {
		st, e := os.Stat(target)
		if e != nil {
			return e
		}
		mode = st.Mode().Perm()
	}
	var after []byte
	remove := false
	if o.has("rollback") {
		raw, e := os.ReadFile(o.str("rollback"))
		if e != nil {
			return e
		}
		var backup codexHookBackup
		if _, e := parseJSONNode(raw); e != nil {
			return fmt.Errorf("invalid hook backup: %w", e)
		}
		if json.Unmarshal(raw, &backup) != nil || backup.Version != 1 || backup.Target != target || backup.Mode > 0777 {
			return errors.New("invalid hook backup or target mismatch")
		}
		if !existed || hookDigest(before) != backup.AfterSHA256 || uint32(mode) != backup.Mode {
			return errors.New("hook rollback refused: settings drift since migration")
		}
		after = backup.Before
		remove = !backup.Existed
	} else {
		if custom := customCodexHookCount(before, bin); custom > 0 {
			fmt.Fprintf(a.stderr, "codex: preserved %d custom vecnet-like handler(s); inspect their commands and trust explicitly before use\n", custom)
		}
		var changed bool
		after, changed, err = mergeCodexHooks(before, bin)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintf(a.stderr, "unchanged %s\n", path)
			return nil
		}
	}
	if !o.has("write") {
		if err := printHookDiff(a.stdout, path, before, after); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "would update %s; rerun with --write\n", path)
		return nil
	}
	// Recheck after preparing the new content; other installer processes are
	// serialized by the lock, and pre-existing external edits are refused.
	current, e := os.ReadFile(target)
	if (e == nil) != existed || (e != nil && !errors.Is(e, os.ErrNotExist)) || !bytes.Equal(current, before) {
		return errors.New("hook migration refused: settings drift while preparing update")
	}
	if !o.has("rollback") {
		backup := codexHookBackup{Version: 1, Target: target, Existed: existed, Mode: uint32(mode), Before: before, AfterSHA256: hookDigest(after)}
		raw, e := json.MarshalIndent(backup, "", "  ")
		if e != nil {
			return e
		}
		f, e := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".vecnet-backup-*")
		if e != nil {
			return e
		}
		name := f.Name()
		_, e = f.Write(raw)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			_ = os.Remove(name)
			return e
		}
		fmt.Fprintf(a.stderr, "backup %s; rollback: vecnet hook install --client codex --settings %s --rollback %s --write\n", name, hookShellWord(path), hookShellWord(name))
	}
	if remove {
		err = os.Remove(target)
	} else {
		err = writeHookSettings(target, after, mode)
	}
	if err != nil {
		return err
	}
	if o.has("rollback") {
		fmt.Fprintf(a.stderr, "restored %s\n", path)
	} else {
		fmt.Fprintf(a.stderr, "wrote %s (SessionStart, UserPromptSubmit, PostToolUse)\n", path)
	}
	return nil
}

// Full-file unified diff avoids invoking a shell or exposing hooks in a temp
// file. Original scalar spellings remain visible in the review.
func printHookDiff(w io.Writer, path string, before, after []byte) error {
	lines := func(b []byte) []string {
		if len(b) == 0 {
			return nil
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	old, newLines := lines(before), lines(after)
	var out strings.Builder
	oldStart, newStart := 1, 1
	if len(old) == 0 {
		oldStart = 0
	}
	if len(newLines) == 0 {
		newStart = 0
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", path, path, oldStart, len(old), newStart, len(newLines))
	for _, s := range old {
		fmt.Fprintf(&out, "-%s\n", s)
	}
	if len(before) > 0 && before[len(before)-1] != '\n' {
		out.WriteString("\\ No newline at end of file\n")
	}
	for _, s := range newLines {
		fmt.Fprintf(&out, "+%s\n", s)
	}
	if len(after) > 0 && after[len(after)-1] != '\n' {
		out.WriteString("\\ No newline at end of file\n")
	}
	_, err := io.WriteString(w, out.String())
	return err
}

func customCodexHookCount(raw []byte, bin string) int {
	root, err := parseJSONNode(raw)
	if err != nil {
		return 0
	}
	hooks := root.member("hooks")
	if hooks == nil || hooks.kind != '{' {
		return 0
	}
	count := 0
	for i, groups := range hooks.vals {
		var key string
		_ = json.Unmarshal(hooks.keys[i], &key)
		for _, group := range groups.vals {
			handlers := group.member("hooks")
			if handlers == nil || handlers.kind != '[' {
				continue
			}
			for _, handler := range handlers.vals {
				command := handler.member("command").scalarString()
				if !strings.Contains(command, "vecnet") || !strings.Contains(command, " hook") {
					continue
				}
				owned := false
				for _, event := range codexHookEvents {
					if event.key == key && codexOwnedGroup(group, event.matcher) && codexOwnedHandler(handler, bin, event.name) {
						owned = true
						break
					}
				}
				if !owned {
					count++
				}
			}
		}
	}
	return count
}
