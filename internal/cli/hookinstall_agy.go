package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/XertroV/vecnet/internal/platform"
)

// The Antigravity (agy) installer. agy reads workspace hooks from
// <workspace>/.agents/hooks.json — a top-level map of named hook groups,
// where SessionStart and PreInvocation take flat handler arrays — and
// workspace MCP servers from <workspace>/.agents/mcp_config.json's
// mcpServers object (docs/research/2026-11-20-agy-autodelivery.md §2.3,
// §2.4). The installer owns exactly one group, vecnet-<profile>, and at
// most one MCP server, also vecnet-<profile>; both are identified by name
// and replaced in place, so other groups and servers — and their order —
// are never touched. It never writes agy's own settings under ~/.gemini.

// agyHookEntry is one flat handler inside a named hooks.json group.
type agyHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// agyHookGroup is the vecnet-<profile> group. Only SessionStart and
// PreInvocation are installed: PreInvocation covers both turn starts and
// mid-turn runs (hook_agy.go tells them apart by invocationNum), and any
// further event would only add runs that must stay silent.
type agyHookGroup struct {
	Enabled       bool           `json:"enabled"`
	SessionStart  []agyHookEntry `json:"SessionStart"`
	PreInvocation []agyHookEntry `json:"PreInvocation"`
}

// agyHookCommand is an installed agy handler's command; mcp, when not
// empty, is the MCP server name its hints name (`hook install --mcp`).
// agy runs hook commands through sh -c (cmd /c on Windows), so the binary
// takes POSIX shell quoting. The group is replaced by name, so no other
// command spelling is ever treated as ours.
func agyHookCommand(bin, profile, event, mcp string) string {
	command := hookShellWord(bin) + " hook --client agy --profile " + profile + " --event " + event
	if mcp != "" {
		command += " --mcp " + mcp
	}
	return command
}

// mergeAgyHooks merges the installer's vecnet-<profile> group into an agy
// .agents/hooks.json. The group is identified by its name: an existing
// group of that name is replaced in place whatever it holds, and every
// other group, and the file's key order, is preserved.
func mergeAgyHooks(existing []byte, bin, profile, mcp string) (out []byte, changed bool, err error) {
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
	raw, err := json.Marshal(agyHookGroup{
		Enabled:       true,
		SessionStart:  []agyHookEntry{{Type: "command", Command: agyHookCommand(bin, profile, "session-start", mcp), Timeout: 5}},
		PreInvocation: []agyHookEntry{{Type: "command", Command: agyHookCommand(bin, profile, "pre-invocation", mcp), Timeout: 5}},
	})
	if err != nil {
		return nil, false, err
	}
	group, err := parseJSONNode(raw)
	if err != nil {
		return nil, false, err
	}
	replaceOrAppendMember(root, "vecnet-"+profile, group)
	root.raw = existing
	out = root.encode(detectIndent(existing))
	return out, !bytes.Equal(out, existing), nil
}

// agyMCPTool configures one MCP tool. The core tools are eager so they are
// declared up front; vecnet_wait also runs as a background task, so agy
// wakes the idle session when it returns (§2.4).
type agyMCPTool struct {
	Eager      bool   `json:"eager"`
	Background string `json:"background,omitempty"`
}

// agyMCPServer is the installer's mcpServers entry. timeoutSeconds bounds
// each call, including a vecnet_wait that waits out its window.
type agyMCPServer struct {
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
	Tools          struct {
		Inbox agyMCPTool `json:"vecnet_inbox"`
		Reply agyMCPTool `json:"vecnet_reply"`
		Ack   agyMCPTool `json:"vecnet_ack"`
		Send  agyMCPTool `json:"vecnet_send"`
		Wait  agyMCPTool `json:"vecnet_wait"`
	} `json:"tools"`
}

// mergeAgyMCP merges the installer's vecnet-<profile> MCP server into an
// agy .agents/mcp_config.json. The entry is identified by name and replaced
// in place; other servers, and their order, are never touched. env always
// carries VECNET_MCP_CLIENT=agy, the relay's opt-in for its agy extensions
// (vecnet_wait, toolSummary stripping), and also VECNET_CONFIG when it is
// set at install time, so the MCP relay reads the installer's config.
func mergeAgyMCP(existing []byte, bin, profile, mcp string, getenv func(string) string) (out []byte, changed bool, err error) {
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
	servers := root.member("mcpServers")
	if servers == nil {
		servers = &jsonNode{kind: '{'}
		root.appendMember("mcpServers", servers)
	}
	if servers.kind != '{' {
		return nil, false, errors.New("hook install: mcpServers must be an object")
	}
	entry := agyMCPServer{
		Command:        bin,
		Args:           []string{"--net", profile, "mcp"},
		Env:            map[string]string{"VECNET_MCP_CLIENT": "agy"},
		TimeoutSeconds: 1860,
	}
	entry.Tools.Inbox = agyMCPTool{Eager: true}
	entry.Tools.Reply = agyMCPTool{Eager: true}
	entry.Tools.Ack = agyMCPTool{Eager: true}
	entry.Tools.Send = agyMCPTool{Eager: true}
	entry.Tools.Wait = agyMCPTool{Eager: true, Background: "always"}
	if conf := getenv("VECNET_CONFIG"); conf != "" {
		if abs, err := filepath.Abs(conf); err == nil {
			conf = abs
		}
		entry.Env["VECNET_CONFIG"] = conf
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, false, err
	}
	node, err := parseJSONNode(raw)
	if err != nil {
		return nil, false, err
	}
	replaceOrAppendMember(servers, mcp, node)
	root.raw = existing
	out = root.encode(detectIndent(existing))
	return out, !bytes.Equal(out, existing), nil
}

// replaceOrAppendMember sets name to value in an object node, replacing an
// existing member in place (keeping its position) or appending it, and
// reports whether the node was usable.
func replaceOrAppendMember(n *jsonNode, name string, value *jsonNode) bool {
	if n == nil || n.kind != '{' {
		return false
	}
	if n.member(name) == nil {
		n.appendMember(name, value)
		return true
	}
	for i, raw := range n.keys {
		var key string
		if json.Unmarshal(raw, &key) == nil && key == name {
			n.vals[i] = value
		}
	}
	return true
}

// agyUntrackedAdvice extends a tracked-target refusal: agy loads
// .agents/hooks.json from the repository, so a tracked file would share the
// profile with everyone who opens it.
func agyUntrackedAdvice(rel string) string {
	return "; to keep this file personal, git rm --cached " + rel + " and list it in .git/info/exclude"
}

// installAgyHooks merges the vecnet-<profile> group into path and, with
// --mcp, the vecnet-<profile> server into mcpPath (the same directory).
// Guards mirror the Codex installer's strictness: without --settings a
// default target must not be a symlink and must be provably untracked
// before --write. A dry run (no --write, or --dry-run) prints diffs and
// writes nothing.
func (a *app) installAgyHooks(path, mcpPath, root string, unknown error, bin, profile, mcp string, o opts) error {
	if o.has("write") && !o.has("settings") {
		if unknown != nil {
			return fmt.Errorf("hook install: cannot tell whether %s is tracked by git (repository discovery failed: %s); pass --settings %s to write it anyway", path, hookGitReason(unknown), path)
		}
		if root != "" {
			if err := a.checkUntracked(root, ".agents/hooks.json", path, agyUntrackedAdvice(".agents/hooks.json")); err != nil {
				return err
			}
			if mcp != "" {
				if err := a.checkUntracked(root, ".agents/mcp_config.json", mcpPath, agyUntrackedAdvice(".agents/mcp_config.json")); err != nil {
					return err
				}
			}
		}
		if err := defaultSettingsRedirect(path, "the workspace .agents/hooks.json"); err != nil {
			return err
		}
		if mcp != "" {
			// The .agents directory itself was checked above.
			if err := symlinkSettingsRefused(mcpPath, "the workspace .agents/mcp_config.json", path); err != nil {
				return err
			}
		}
	}
	if err := a.updateAgyJSON(path, func(existing []byte) ([]byte, bool, error) {
		return mergeAgyHooks(existing, bin, profile, mcp)
	}, "(SessionStart, PreInvocation)", o); err != nil {
		return err
	}
	if mcp == "" {
		return nil
	}
	return a.updateAgyJSON(mcpPath, func(existing []byte) ([]byte, bool, error) {
		return mergeAgyMCP(existing, bin, profile, mcp, a.getenv)
	}, "(MCP server "+mcp+")", o)
}

// updateAgyJSON reads, merges, previews or writes one agy JSON target. A
// dry run prints a full-file diff and writes nothing; an unchanged file is
// reported and left alone. Symlinked explicit targets are written through,
// never replaced.
func (a *app) updateAgyJSON(path string, merge func([]byte) ([]byte, bool, error), wrote string, o opts) error {
	target, err := platform.Canonical(path)
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
	if runtime.GOOS == "windows" {
		mode = 0666
	}
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	merged, changed, err := merge(existing)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(a.stderr, "unchanged %s\n", path)
		return nil
	}
	if !o.has("write") {
		if err := printHookDiff(a.stdout, path, existing, merged); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "would update %s; rerun with --write\n", path)
		return nil
	}
	if err := writeHookSettings(target, merged, mode); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "wrote %s %s\n", path, wrote)
	return nil
}

// agyAdvice prints the installer's stderr advice. agy discovers
// .agents/hooks.json from the workspace root, so every git worktree needs
// its own install (§5 H8); permission rules are the user's alone, so the
// vecnet_wait allow rule is only ever printed (§2.4).
func (a *app) agyAdvice(mcp string) {
	fmt.Fprintln(a.stderr, "agy: each git worktree needs its own install; agy reads .agents/hooks.json from the workspace root, not the main checkout's")
	if mcp != "" {
		fmt.Fprintf(a.stderr, "agy: agy asks before each vecnet_wait call unless you add the allow rule mcp(%s/vecnet_wait)\n", mcp)
	}
}
