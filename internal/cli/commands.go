// Package cli is the `vecnet` command line client (PROTOCOL §13.7). It
// talks to a hub over HTTP only. Its internal dependencies are envelope and,
// for `vecnet tui`, the operator client and TUI packages; none of them links
// the hub.
//
// Exit codes: 0 ok, 1 usage (or a local error such as an unreadable file),
// 2 the hub answered with an error (printed as "code: message"), 3 network.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/hubnotice"
	"github.com/XertroV/vecnet/internal/inboxview"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Exit codes.
const (
	exitOK      = 0
	exitUsage   = 1
	exitHub     = 2
	exitNetwork = 3
)

// uploadTimeout bounds one attachment upload (blobs are up to 25 MiB).
const uploadTimeout = 10 * time.Minute

const usageText = `usage: vecnet [--net PROFILE] [--as PEER_ID] [--json] <command> [args]

messaging:
  send <recipient…> "text" (--wake|--wake-idle|--hold) [--thread ID] [--reply-to MSG]
       [--attach FILE]… [--title T] [--idempotency-key K]       text "-" reads stdin
       recipients are peer ids (or group/team ids); names are refused
  reply <message_id> "text" (--wake|--wake-idle|--hold) [--thread ID] [--attach FILE]…
  react <message_id> <emoji|name> [--remove] [--wake|--wake-idle|--hold]
       default priority depends on the emoji: ✅ ❓ ❗ wake_idle, others hold
  mcp                                 stdio MCP relay to the hub's /mcp as this profile [--as X]; needs --net P
  inbox [--since CURSOR] [--ack] [--limit N] [--include-sender-chain]
  blob get SHA [--blob-id ID] --out PATH [--json]
       URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired.
  ack <cursor>
  watch [--for-monitor] [--since CURSOR] [--auto-ack] [--deliveries-only]
       does not ack; use --auto-ack or vecnet ack <cursor> to avoid replaying
       the log after a restart
  hook [--event user-prompt|post-tool|session-start|app-server] [--profile P] [--auto-ack] [--mcp vecnet-P]
       Claude Code / Codex hook: always exits 0; --mcp names the registered MCP server in its hints
  hook --event app-server --claim NONCE --priorities wake[,wake_idle] --expect-hub URL --expect-caller PEER_ID
  hook --release --claim NONCE
  hook install [--client claude|codex] [--profile P] [--write] [--settings PATH] [--auto-ack] [--mcp] (--mcp: Claude-only)
       --mcp: the hooks name MCP server vecnet-P, and --write registers it (claude mcp add)
  hook rebind [--profile P]
       bind the profile's hook to this Claude Code session (run from its Bash tool)
  codex bind --thread UUID [--codex-home DIR] [--ttl DUR] [--idle-dormant DUR] [--attended] [--renew|--takeover]
       opt in to app-server delivery; ttl defaults to 24h (1h–168h); unattended needs -a never or auto reviewer
  codex unbind [--thread UUID] | codex bindings [--json] | codex status
  launcher setup --project ALIAS --repo PATH --base-ref REF [--backend codex-app-server|acp] [--write] [--force]
       native: [--codex-home DIR] [--model M] [--effort E]; ACP: --acp-command PATH [--acp-arg ARG]… [--acp-provider claude] [--acp-mode default]
  launcher install [--source CHECKOUT] [--node PATH] [--vecnet-bin PATH] [--write] [--enable] [--start]
  launcher status | launcher check                 selected --net profile; setup/install preview unless --write
  threads [--limit N]
  thread <id> [--after MESSAGE_ID] [--limit N]
  thread budget <id> <n>                          set budget_remaining
  peers
  groups create <peer…> | groups add <grp> <peer…> | groups list | groups unarchive <grp>
  presence set <idle|working|blocked|offline> [--note T]
  presence subscribe <all|teams|none>
  avatar set <FILE|https://URL> | avatar clear       PNG, JPEG, GIF or WebP, at most 2 MiB;
       the hub crops it square and stores 128x128; a URL is fetched by the hub
  receipt <delivery_id> <processed|read|failed> [--note T]
  enrol --hub URL --token (T|-) --profile NAME      "-" reads the token from stdin;
       [--allow-insecure-http]                   http:// is refused off loopback/LAN without it
       (preferred: keeps it out of shell history and ps); else $VECNET_ENROL_TOKEN
       --name N picks your display name when the invite leaves it open (once);
       the profile then defaults to its lowercase slug
  profile show | pin [--accept FP] | repoint URL [--force] | follow
       inspect or pin the hub identity, or verify a new hub URL before repointing
  guide [--net P]                                    the agent manual, filled in for profile P; needs no config

admin (operator credential):
  notice post --kind K --message TEXT [--at TIME] [--new-url URL] [--expires DUR|TIME]
  notice list | clear (ID | --kind K)
  principal add --name N
  principal limits <prn> --max-peers N --creates-per-hour N --kinds K[,K]
  principal scope <prn> [--allow IDS] [--deny IDS]
  principal list
  peer add --kind K --name N [--principal PRN]
  peer invite [--name N [--open-name]] [--kind K] [--principal PRN] [--ttl 24h] [--profile P] [--copy]
       adds the peer and prints a short block to paste to the agent (token valid --ttl,
       max 7d); --copy also puts it on the clipboard; without --name, or with
       --open-name, the agent picks its name at enrol
  peer invite <peer> [--ttl 24h] [--copy]             re-issues a token for a peer not yet enrolled
  invite status <peer> | invite revoke <peer>         an invite's state (never its token), or withdraw it:
       the unredeemed token stops enrolling; no-op when none is live; never touches
       an enrolled peer's secret (peer disable / peer rotate). Also: peer invite status|revoke
  peer move <peer> --principal PRN
  peer link <peer>                                    prints a peer's 12-character OAuth link code
  agent kinds [--kind KIND]
       onboarding kinds and prompt templates from the hub
  agent add --kind KIND [--name N] [--open-name] [--ttl 24h] [--principal PRN]
       [--url URL] [--key-file PATH] [--connector-name NAME] [--skip-mcp]
       [--request-id ID] [--confirm-revoke]
       one hub submit; a routine key is read only from --key-file, never argv or the environment
       --skip-mcp selects CLI bearer and does not request a link
       the guided link revokes a live bearer and asks for --confirm-revoke first
  agent link <peer> [--confirm-revoke]
       guided link step, with the same bearer warning before peer link
  agent status REQUEST_ID
       safe onboarding status; queued is not connected
  peer avatar <peer> (FILE|https://URL|--clear)       set or clear any peer's avatar
  peer rotate <peer>
  peer scope <peer> [--allow IDS] [--deny IDS]        IDS: comma-separated or repeated
  peer limits <peer> --sends-per-min N --threads-per-hour N
  peer rename <id> <name>
  peer disable <id> | peer enable <id> | peer archive <id> | peer unarchive <id>
  route add --owner ID --url URL | route verify <route_id>
  route add --owner ID --kind grokbot --url URL --key-file PATH
  route list [--json] | route rotate <route_id> --key-file PATH | route remove <route_id>
       the crsr_ key is read only from a file, 0600, never from argv
  source add --kind github --name N --route ID [--events A,B] [--priority P]
       prints the ingress URL and the secret, once
  source list
  subscriptions list | subscriptions pause <id> | subscriptions resume <id>
  team add --name N [--members IDS]
  team list | team members <id> --add IDS         adds only; IDS: comma-separated or repeated
  dead-letters list | dead-letters retry <dly> | dead-letters discard <dly>
  notify set --url URL --filter F [--url URL --filter F]…
  diag mcp [--peer ID] [--method M] [--limit N] [--follow]
  audit [--limit N]
  prefs list | prefs get KEY | prefs set KEY VALUE [--preview] [--apply-existing]
       [--confirm-risk CODE]… | prefs set --reset KEY
       operator preferences; durations are whole seconds or a number plus s/m/h/d;
       a conflict is not retried, and --confirm-risk names each required risk
  tui                                                 the operator's terminal client

flags may appear anywhere; "--" ends flag parsing, so text that looks like a
flag is sent as is: vecnet send PEER --wake -- -h

config: $VECNET_CONFIG, else $XDG_CONFIG_HOME/vecnet/config.toml, else ~/.config/vecnet/config.toml
cache:  $XDG_CACHE_HOME/vecnet/, else ~/.cache/vecnet/
exit:   0 ok, 1 usage, 2 hub error, 3 network
`

// Run is the CLI entry point: args exclude the program name; config and
// cache paths are resolved only through getenv. SIGINT/SIGTERM end a
// running watch cleanly.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, args, stdin, stdout, stderr, getenv)
}

// RunContext is Run under ctx: cancelling it ends `watch` with exit 0 and
// aborts any other command.
func RunContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	a := &app{ctx: ctx, stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv}
	rest, help, err := a.parseGlobal(args)
	if err != nil {
		return a.exit(err)
	}
	if help || (len(rest) > 0 && rest[0] == "help") {
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	a.autoCodexAction(rest[0], rest[1:])
	return a.exit(a.dispatch(rest[0], rest[1:]))
}

// app is one invocation.
type app struct {
	ctx            context.Context
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string

	net, as       string // --net, --as
	json          bool   // --json
	inHook        bool   // hooks never TOFU or rewrite trust state
	stdinConsumed bool   // a retry cannot consume stdin again
	inMCP         bool   // stdio relay startup refusals must make no requests
	// retainedRequestID is the agent-add id for this process, generated or
	// explicit, so a verified hub follow redispatches the same submission.
	retainedRequestID string
	prefsCmd          bool    // preferences exit codes and JSON errors
	codexNotice       *string // worker returns a notice through completion, never a helper pipe
}

// exit prints err and maps it to an exit code. Preferences commands use the
// preferences contract (usage 2, validation/conflict/refusal 1); every other
// command keeps the mapping below.
func (a *app) exit(err error) int {
	if err == nil {
		return exitOK
	}
	if a.prefsCmd {
		return a.exitPrefs(err)
	}
	var ue *usageError
	var he *hubError
	var ne *netError
	var re *refusedError
	var me *mcpStartupError
	switch {
	case errors.Is(err, hubnotice.ErrInvalidNotice):
		fmt.Fprintln(a.stderr, "bad_response: invalid hub notice")
		return exitHub
	case errors.As(err, &me):
		fmt.Fprintln(a.stderr, me)
		return exitUsage
	case errors.As(err, &he):
		fmt.Fprintf(a.stderr, "%s: %s\n", he.Code, he.Message)
		if he.Hint != "" {
			fmt.Fprintln(a.stderr, he.Hint)
		}
		return exitHub
	case errors.As(err, &re):
		fmt.Fprintf(a.stderr, "vecnet: %s\n", re.msg)
		return exitHub
	case errors.As(err, &ne):
		fmt.Fprintf(a.stderr, "vecnet: %v\n", ne)
		return exitNetwork
	case errors.As(err, &ue):
		fmt.Fprintf(a.stderr, "vecnet: %s\n", ue.msg)
		return exitUsage
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(a.stderr, "vecnet: interrupted")
		return exitUsage
	default:
		fmt.Fprintf(a.stderr, "vecnet: %v\n", err)
		return exitUsage
	}
}

// parseGlobal pulls --net, --as, --json and --help out of args wherever
// they appear (before a "--"), returning the rest in order. Launcher setup's
// --acp-arg operand is opaque child argv, including global-looking values.
func (a *app) parseGlobal(args []string) (rest []string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--acp-arg" && len(rest) >= 2 && rest[0] == "launcher" && rest[1] == "setup" && i+1 < len(args) {
			rest = append(rest, arg, args[i+1])
			i++
			continue
		}
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, val, hasVal := strings.Cut(arg, "=")
		switch name {
		case "--json":
			if hasVal {
				return nil, false, usageErr("--json takes no value")
			}
			a.json = true
		case "--help", "-h":
			help = true
		case "--net", "--as":
			if !hasVal {
				if i+1 >= len(args) {
					return nil, false, usageErr(name + " needs a value")
				}
				i++
				val = args[i]
			}
			if name == "--net" {
				a.net = val
			} else {
				a.as = val
			}
		default:
			rest = append(rest, arg)
		}
	}
	return rest, help, nil
}

// flagKind is how a command flag takes values.
type flagKind int

const (
	boolFlag  flagKind = iota // --x
	strFlag                   // --x V (last one wins)
	multiFlag                 // --x V, repeatable
)

// opts is one command's parsed arguments.
type opts struct {
	pos  []string
	vals map[string][]string
}

func (o opts) has(name string) bool { _, ok := o.vals[name]; return ok }

func (o opts) str(name string) string {
	v := o.vals[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func (o opts) all(name string) []string { return o.vals[name] }

// list returns every value of a multi flag split on commas, blanks dropped.
func (o opts) list(name string) []string {
	out := []string{}
	for _, v := range o.vals[name] {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// parseFlags parses cmd's arguments against spec. Flags and positionals may
// interleave; "--" ends flags; "--x=v" and "--x v" both work. A lone "-" or
// a single-dash word (such as "-5") is positional.
func parseFlags(cmd string, args []string, spec map[string]flagKind) (opts, error) {
	o := opts{vals: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			o.pos = append(o.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			o.pos = append(o.pos, arg)
			continue
		}
		name, val, hasVal := strings.Cut(arg[2:], "=")
		kind, ok := spec[name]
		if !ok {
			return o, usageErr(fmt.Sprintf("%s: unknown flag --%s", cmd, name))
		}
		switch kind {
		case boolFlag:
			if hasVal {
				return o, usageErr(fmt.Sprintf("%s: --%s takes no value", cmd, name))
			}
			o.vals[name] = append(o.vals[name], "")
		default:
			if !hasVal {
				if i+1 >= len(args) {
					return o, usageErr(fmt.Sprintf("%s: --%s needs a value", cmd, name))
				}
				i++
				val = args[i]
			}
			o.vals[name] = append(o.vals[name], val)
		}
	}
	return o, nil
}

// connect loads the config and returns a client for the chosen profile,
// the profile name and the local cache. The profile is --net, else
// $VECNET_PROFILE; a run naming neither is bare (resolveBare): refused for
// an agent, the operator profile with a CLI label for a human. Any run with
// an operator credential sends the CLI label (operatorLabel).
func (a *app) connect() (*client, string, cache, error) {
	path, err := configPath(a.getenv)
	if err != nil {
		return nil, "", cache{}, err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, "", cache{}, err
	}
	if err := cfg.checkDefault(); err != nil {
		return nil, "", cache{}, err
	}
	name := a.namedProfile()
	var p Profile
	var label *envelope.OperatorClient
	if name != "" {
		name, p, err = cfg.profile(name)
		label = a.operatorLabel(p)
	} else {
		name, p, label, err = a.resolveBare(cfg)
	}
	if err != nil {
		return nil, "", cache{}, err
	}
	if err := a.useStoredHub(name, p); err != nil {
		return nil, "", cache{}, err
	}
	// MCP runs TOFU after its additional startup refusals in cmdMCP.
	if !a.inHook && !a.inMCP {
		a.ensurePin(name, p)
	}
	dir, err := cacheDir(a.getenv)
	if err != nil {
		dir = ""
	}
	c := newClient(p.Hub, p.Secret)
	c.allowInsecureHTTP = p.AllowInsecureHTTP || a.getenv(insecureEnv) == "1"
	if label != nil {
		c.clientHeader = label.Header()
	}
	return c, name, cache{dir: dir}, nil
}

// peerQuery narrows a principal's stream or ack to --as (PROTOCOL §2.3).
func (a *app) peerQuery() url.Values {
	q := url.Values{}
	if a.as != "" {
		q.Set("peer", a.as)
	}
	return q
}

// warn reports a non-fatal local problem (such as a cache write failure).
func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.stderr, "vecnet: warning: "+format+"\n", args...)
}

// emitJSON prints raw compactly on one line.
func (a *app) emitJSON(raw []byte) error {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return err
	}
	b.WriteByte('\n')
	_, err := a.stdout.Write(b.Bytes())
	return err
}

// emitValue prints v as one line of JSON.
func (a *app) emitValue(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return a.emitJSON(raw)
}

// emitList prints the array field of a hub list response as a JSON array
// ([] rather than null when empty).
func (a *app) emitList(items []json.RawMessage) error {
	if items == nil {
		items = []json.RawMessage{}
	}
	return a.emitValue(items)
}

// ok reports success of a command without output (a 204).
func (a *app) ok(human string) error {
	if a.json {
		return a.emitValue(map[string]bool{"ok": true})
	}
	fmt.Fprintln(a.stdout, human)
	return nil
}

// dispatch runs one command.
func (a *app) dispatch(cmd string, args []string) error {
	err := a.dispatchOnce(cmd, args)
	// enrol has no profile yet (a 410 there must not follow another one);
	// guide makes no hub request.
	switch cmd {
	case "watch", "hook", "mcp", "profile", "enrol", "enroll", "guide":
		return err
	}
	followed, followErr := a.followFailure(err)
	if !followed {
		return err
	}
	if followErr != nil {
		return followErr
	}
	if a.stdinConsumed {
		return usageErr("hub followed; re-run the command (stdin was consumed)")
	}
	return a.dispatchOnce(cmd, a.withRetainedRequestID(cmd, args))
}

// withRetainedRequestID puts the id from the first attempt back on a
// redispatched agent command. Without it, agent add generates a new id
// after a signed 410 and the follow creates a second peer.
func (a *app) withRetainedRequestID(cmd string, args []string) []string {
	if cmd != "agent" || a.retainedRequestID == "" || hasRequestIDFlag(args) {
		return args
	}
	out := make([]string, len(args), len(args)+2)
	copy(out, args)
	return append(out, "--request-id", a.retainedRequestID)
}

func hasRequestIDFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--request-id" || strings.HasPrefix(arg, "--request-id=") {
			return true
		}
	}
	return false
}

func (a *app) dispatchOnce(cmd string, args []string) error {
	switch cmd {
	case "enrol", "enroll":
		return a.cmdEnrol(args)
	case "notice":
		return a.cmdNotice(args)
	case "profile":
		return a.cmdProfile(args)
	case "guide":
		return a.cmdGuide(args)
	case "launcher":
		return a.cmdLauncher(args)
	case "blob":
		return a.cmdBlob(args)
	case "send":
		return a.cmdSend(args)
	case "reply":
		return a.cmdReply(args)
	case "react":
		return a.cmdReact(args)
	case "inbox":
		return a.cmdInbox(args)
	case "ack":
		return a.cmdAck(args)
	case "mcp":
		a.inMCP = true
		return a.cmdMCP(args)
	case "watch":
		return a.cmdWatch(args)
	case "hook":
		a.inHook = true
		return a.cmdHook(args)
	case "codex":
		return a.cmdCodex(args, time.Now)
	case "threads":
		return a.cmdThreads(args)
	case "thread":
		return a.cmdThread(args)
	case "peers":
		return a.cmdPeers(args)
	case "groups":
		return a.cmdGroups(args)
	case "presence":
		return a.cmdPresence(args)
	case "avatar":
		return a.cmdAvatar(args)
	case "receipt":
		return a.cmdReceipt(args)
	case "principal":
		return a.cmdPrincipal(args)
	case "peer":
		return a.cmdPeer(args)
	case "agent":
		return a.cmdAgent(args)
	case "invite":
		return a.cmdInvite(args)
	case "route":
		return a.cmdRoute(args)
	case "source":
		return a.cmdSource(args)
	case "subscriptions":
		return a.cmdSubscriptions(args)
	case "team":
		return a.cmdTeam(args)
	case "dead-letters":
		return a.cmdDeadLetters(args)
	case "notify":
		return a.cmdNotify(args)
	case "diag":
		return a.cmdDiag(args)
	case "audit":
		return a.cmdAudit(args)
	case "prefs":
		return a.cmdPrefs(args)
	case "tui":
		return a.cmdTUI(args)
	case "init-hub", "init":
		return usageErr("hub initialisation belongs to the hub binary: run `vecnet-hub init`")
	}
	return usageErr(fmt.Sprintf("unknown command %q; run `vecnet help`", cmd))
}

// --- enrol ---

func (a *app) cmdEnrol(args []string) error {
	o, err := parseFlags("enrol", args, map[string]flagKind{"hub": strFlag, "token": strFlag, "profile": strFlag, "name": strFlag, "allow-insecure-http": boolFlag})
	if err != nil {
		return err
	}
	hubURL, tok, name := o.str("hub"), o.str("token"), o.str("profile")
	display := strings.TrimSpace(o.str("name"))
	// An invite that leaves the name open takes it here; the profile then
	// defaults to its slug, which must not overwrite another profile.
	defaulted := name == "" && display != ""
	if defaulted {
		name = inviteSlug(display)
	}
	// The one-time token is best kept off the command line (shell history,
	// ps): "--token -" reads it from stdin, and VECNET_ENROL_TOKEN is the
	// fallback when --token is absent.
	switch tok {
	case "-":
		if tok, err = a.readText("-"); err != nil {
			return err
		}
	case "":
		tok = a.getenv("VECNET_ENROL_TOKEN")
	}
	tok = strings.TrimSpace(tok)
	if hubURL == "" || tok == "" || name == "" || len(o.pos) != 0 {
		return usageErr("usage: vecnet enrol --hub URL --token (T|-) (--profile P | --name N [--profile P]) [--allow-insecure-http]  (or $VECNET_ENROL_TOKEN)")
	}
	if err := checkEnrolToken(tok); err != nil {
		return err
	}
	insecure := o.has("allow-insecure-http")
	if err := checkHubURL(hubURL, insecure || a.getenv(insecureEnv) == "1"); err != nil {
		return err
	}
	path, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	if _, taken := cfg.Profiles[name]; taken && defaulted {
		return usageErr(fmt.Sprintf("enrol: profile %q already exists in %s; pass --profile to choose another", name, path))
	}
	var out struct {
		PeerID    string `json:"peer_id"`
		Secret    string `json:"secret"`
		Name      string `json:"name"`
		HubPubkey string `json:"hub_pubkey"`
	}
	req := map[string]string{"token": tok}
	if display != "" {
		req["name"] = display
	}
	c := newClient(hubURL, "")
	c.allowInsecureHTTP = insecure || a.getenv(insecureEnv) == "1"
	if err := c.do(a.ctx, http.MethodPost, "/v1/enrol", nil, req, &out); err != nil {
		// "unauthenticated" alone reads as a revoked invite; say what else
		// it covers and how to get a fresh one.
		var he *hubError
		if errors.As(err, &he) && he.Status == http.StatusUnauthorized {
			he.Hint = enrolUnauthHint
		}
		// A relocated hub (410 hub_moved) cannot enrol, and there is no
		// profile to follow yet: point at the new hub instead.
		if errors.As(err, &he) && he.Status == http.StatusGone && he.Code == "hub_moved" {
			if raw, ok := he.Details["new_url"].(string); ok {
				if next, ok := printableHub(raw, insecure || a.getenv(insecureEnv) == "1"); ok {
					he.Hint = "hub moved to " + next + "; ask for an invite there"
				}
			}
		}
		return err
	}
	// Only a canonical key becomes the config pin, which always wins over
	// the sidecar; otherwise the first command pins by first use (§21.5).
	pin := out.HubPubkey
	if _, e := hubid.DecodePubkey(pin); e != nil {
		pin = ""
	}
	cfg.Profiles[name] = Profile{Hub: strings.TrimRight(hubURL, "/"), HubPubkey: pin, Secret: out.Secret, PeerID: out.PeerID, AllowInsecureHTTP: insecure}
	if err := saveConfig(path, cfg); err != nil {
		// The secret was issued once and is never printed (PROTOCOL §12, §14
		// inv. 6): name the peer and how to get a fresh credential instead.
		fmt.Fprintf(a.stderr, "vecnet: could not write %s; %s is enrolled but its secret was not saved and is not shown. Fix the config path, then ask the operator to rotate the secret (vecnet peer rotate %s) and save the new one with --profile %s\n", path, out.PeerID, out.PeerID, name)
		return err
	}
	if a.json {
		return a.emitValue(map[string]string{"profile": name, "peer_id": out.PeerID, "name": out.Name, "config": path})
	}
	fmt.Fprintf(a.stdout, "enrolled %s as profile %s (%s)\n", out.PeerID, name, path)
	if display != "" {
		fmt.Fprintf(a.stdout, "display name %s; use --net %s on every vecnet command\n", out.Name, name)
	}
	return nil
}

const enrolUnauthHint = "the token may be mistyped, already used, re-issued or expired; ask the operator for a fresh invite (vecnet peer invite <peer>)"

// checkEnrolToken refuses, before any request, text that is the invite
// block (or part of it) rather than the bare token: whitespace inside it,
// several lines, or "vecnet enrol". It never pulls a token out of the
// block, since the block's own --hub would then choose where the token
// goes; the agent runs the block's printf line instead (issue 42).
func checkEnrolToken(tok string) error {
	if strings.IndexFunc(tok, unicode.IsSpace) >= 0 || strings.Contains(tok, "vecnet enrol") {
		return &refusedError{msg: "that looks like the whole invite block, not the token; run the block's printf line instead (or pass only the quoted token)"}
	}
	return nil
}

// --- send / reply ---

var priorityFlags = map[string]flagKind{"wake": boolFlag, "wake-idle": boolFlag, "hold": boolFlag}

// priority returns the one priority flag given; none or several is a usage
// error, since priority has no default (PROTOCOL §5.2).
func priority(o opts) (string, error) {
	var got []string
	for flag, p := range map[string]string{"wake": "wake", "wake-idle": "wake_idle", "hold": "hold"} {
		if o.has(flag) {
			got = append(got, p)
		}
	}
	if len(got) != 1 {
		return "", usageErr("exactly one of --wake|--wake-idle|--hold is required (there is no default)")
	}
	return got[0], nil
}

func mergeSpec(specs ...map[string]flagKind) map[string]flagKind {
	out := map[string]flagKind{}
	for _, s := range specs {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// readText returns text, or stdin when text is "-".
func (a *app) readText(text string) (string, error) {
	if text != "-" {
		return text, nil
	}
	a.stdinConsumed = true
	b, err := io.ReadAll(a.stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(b), nil
}

func (a *app) cmdSend(args []string) error {
	o, err := parseFlags("send", args, mergeSpec(priorityFlags, map[string]flagKind{
		"thread": strFlag, "reply-to": strFlag, "attach": multiFlag, "title": strFlag, "idempotency-key": strFlag,
	}))
	if err != nil {
		return err
	}
	if len(o.pos) == 0 {
		return usageErr(`usage: vecnet send <recipient…> "text" --wake|--wake-idle|--hold [--thread ID]`)
	}
	recipients, text := o.pos[:len(o.pos)-1], o.pos[len(o.pos)-1]
	if len(recipients) == 0 && o.str("thread") == "" {
		return usageErr("send needs at least one recipient or --thread")
	}
	prio, err := priority(o)
	if err != nil {
		return err
	}
	req := envelope.SendRequest{Recipients: recipients, Priority: prio, ThreadTitle: o.str("title")}
	if t := o.str("thread"); t != "" {
		req.ThreadID = &t
	}
	if r := o.str("reply-to"); r != "" {
		req.InReplyTo = &r
	}
	return a.send(req, text, o)
}

func (a *app) cmdReply(args []string) error {
	o, err := parseFlags("reply", args, mergeSpec(priorityFlags, map[string]flagKind{
		"thread": strFlag, "attach": multiFlag, "idempotency-key": strFlag,
	}))
	if err != nil {
		return err
	}
	if len(o.pos) != 2 {
		return usageErr(`usage: vecnet reply <message_id> "text" --wake|--wake-idle|--hold`)
	}
	msgID, text := o.pos[0], o.pos[1]
	prio, err := priority(o)
	if err != nil {
		return err
	}
	thread := o.str("thread")
	if thread == "" {
		dir, derr := cacheDir(a.getenv)
		if derr == nil {
			thread, _ = cache{dir: dir}.threadOf(msgID)
		}
		if thread == "" {
			return usageErr("unknown message id; pass --thread")
		}
	}
	req := envelope.SendRequest{Priority: prio, ThreadID: &thread, InReplyTo: &msgID}
	return a.send(req, text, o)
}

// reactPriority permits one override, otherwise using the emoji's client default.
func reactPriority(o opts, emoji string) (string, error) {
	got := envelope.DefaultReactionPriority(emoji)
	count := 0
	for flag, value := range map[string]string{"wake": "wake", "wake-idle": "wake_idle", "hold": "hold"} {
		if o.has(flag) {
			count += len(o.all(flag))
			got = value
		}
	}
	if count > 1 {
		return "", usageErr("at most one priority flag --wake|--wake-idle|--hold is allowed")
	}
	return got, nil
}

func (a *app) cmdReact(args []string) error {
	o, err := parseFlags("react", args, mergeSpec(priorityFlags, map[string]flagKind{"remove": boolFlag}))
	if err != nil {
		return err
	}
	if len(o.pos) != 2 {
		return usageErr("usage: vecnet react <message_id> <emoji|name> [--remove] [--wake|--wake-idle|--hold]")
	}
	emoji, ok := envelope.ParseReaction(o.pos[1])
	if !ok {
		return usageErr("reaction must be one of: " + envelope.ReactionList())
	}
	prio, err := reactPriority(o, emoji)
	if err != nil {
		return err
	}
	op := "add"
	if o.has("remove") {
		for flag := range priorityFlags {
			if o.has(flag) {
				return usageErr("--remove cannot be combined with a priority flag")
			}
		}
		op = "remove"
	}
	req := envelope.ReactionRequest{MessageID: o.pos[0], Emoji: emoji, Op: op, Priority: prio, As: a.as}
	var raw json.RawMessage
	if err := a.call(http.MethodPost, "/v1/reactions", req, &raw); err != nil {
		return err
	}
	if a.json {
		return a.emitJSON(raw)
	}
	var out struct {
		MessageID  string   `json:"message_id"`
		Emoji      string   `json:"emoji"`
		Changed    bool     `json:"changed"`
		Superseded []string `json:"superseded"`
		Wake       *struct {
			Recipient  string `json:"recipient"`
			DeliveryID string `json:"delivery_id"`
		} `json:"wake"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return &hubError{Code: "bad_response", Message: err.Error()}
	}
	verb := "reacted"
	if !out.Changed {
		verb = "no change"
	} else if op == "remove" {
		verb = "removed"
	}
	fmt.Fprintf(a.stdout, "%s %s %s", verb, out.Emoji, out.MessageID)
	if out.Wake != nil {
		fmt.Fprintf(a.stdout, " (wakes %s, %s)", out.Wake.Recipient, out.Wake.DeliveryID)
	}
	if len(out.Superseded) > 0 {
		fmt.Fprintf(a.stdout, " superseded %s", strings.Join(out.Superseded, ","))
	}
	fmt.Fprintln(a.stdout)
	return nil
}

// send fills the idempotency key, text, sender and attachments of req,
// posts it and prints the response.
func (a *app) send(req envelope.SendRequest, text string, o opts) error {
	text, err := a.readText(text)
	if err != nil {
		return err
	}
	req.Content = envelope.SendContent{Text: text}
	req.As = a.as
	req.IdempotencyKey = o.str("idempotency-key")
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = ulid.Make().String()
	}
	// Check every file before connecting, so a typo sends nothing.
	paths := o.all("attach")
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return usageErr("attach: " + err.Error())
		}
		if !st.Mode().IsRegular() {
			return usageErr("attach: " + p + " is not a regular file")
		}
	}
	c, _, ch, err := a.connect()
	if err != nil {
		return err
	}
	for _, p := range paths {
		att, err := a.upload(c, p)
		if err != nil {
			return err
		}
		// Send only immutable reference fields; verification belongs to the hub.
		req.Attachments = append(req.Attachments, envelope.Attachment{BlobID: att.BlobID, SHA256: att.SHA256, Size: att.Size, MediaType: att.MediaType, Name: att.Name, ExpiresAt: att.ExpiresAt, RetentionClass: att.RetentionClass})
	}
	var raw json.RawMessage
	if err := c.do(a.ctx, http.MethodPost, "/v1/messages", nil, req, &raw); err != nil {
		return err
	}
	var resp struct {
		MessageID  string            `json:"message_id"`
		ThreadID   string            `json:"thread_id"`
		Deliveries []json.RawMessage `json:"deliveries"`
		JobID      string            `json:"job_id"`
		Spilled    bool              `json:"spilled"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &hubError{Code: "bad_response", Message: err.Error()}
	}
	if err := ch.rememberMessages(map[string]string{resp.MessageID: resp.ThreadID}); err != nil {
		a.warn("message cache: %v", err)
	}
	if a.json {
		return a.emitJSON(raw)
	}
	fmt.Fprintf(a.stdout, "sent %s thread %s deliveries %d", resp.MessageID, resp.ThreadID, len(resp.Deliveries))
	for i, delivery := range resp.Deliveries {
		if i == 0 {
			fmt.Fprint(a.stdout, " to ")
		} else {
			fmt.Fprint(a.stdout, ", ")
		}
		fmt.Fprint(a.stdout, sendDestination(delivery))
	}
	if resp.Spilled {
		fmt.Fprint(a.stdout, " (spilled)")
	}
	if resp.JobID != "" {
		fmt.Fprint(a.stdout, " job "+resp.JobID)
	}
	fmt.Fprintln(a.stdout)
	return nil
}

// sendDestination uses only receipt metadata; name discovery must never add
// another operation or turn a successful send into a name-lookup failure.
func sendDestination(raw json.RawMessage) string {
	var destination struct {
		Recipient string  `json:"recipient"`
		Name      string  `json:"recipient_name"`
		Via       *string `json:"via"`
	}
	_ = json.Unmarshal(raw, &destination)
	name := peerName(destination.Name)
	if name == "" {
		name = "name unavailable"
	}
	label := strconv.QuoteToASCII(name) + "(" + unbidi(flatten(destination.Recipient)) + ")"
	if destination.Via != nil {
		label += " via " + unbidi(flatten(*destination.Via))
	}
	return label
}

// mediaTypeOf guesses a file's media type from its extension, without
// parameters; unknown extensions are application/octet-stream.
func mediaTypeOf(path string) string {
	mt := mime.TypeByExtension(filepath.Ext(path))
	if mt == "" {
		return "application/octet-stream"
	}
	if base, _, err := mime.ParseMediaType(mt); err == nil {
		return base
	}
	return mt
}

// upload uses the advertised backend and returns a completed attachment reference.
func (a *app) upload(c *client, path string) (envelope.Attachment, error) {
	f, err := os.Open(path)
	if err != nil {
		return envelope.Attachment{}, usageErr("attach: " + err.Error())
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return envelope.Attachment{}, usageErr("attach: " + err.Error())
	}
	mt := mediaTypeOf(path)
	ctx, cancel := context.WithTimeout(a.ctx, uploadTimeout)
	defer cancel()
	ref, err := c.blobs().Upload(ctx, f, st.Size(), mt, "")
	if err != nil {
		return envelope.Attachment{}, blobError(err)
	}
	return envelope.Attachment{BlobID: ref.BlobID, SHA256: ref.SHA256, Size: ref.Size, MediaType: mt, Name: attachmentName(path), ExpiresAt: ref.ExpiresAt, RetentionClass: ref.RetentionClass, SHA256Verified: ref.SHA256Verified, Integrity: ref.Integrity}, nil
}

// attachmentName is the name stored with an upload. It keeps one path
// component, strips controls, bidi, ANSI sequences and path separators, and
// caps the result at 255 code points, matching the TUI and Decisions §3.
func attachmentName(path string) string {
	name := cleanStoredName(filepath.Base(strings.ReplaceAll(path, "\\", "/")))
	if rs := []rune(name); len(rs) > 255 {
		name = string(rs[:255])
	}
	if name == "" {
		return "file"
	}
	return name
}

// cleanStoredName is the one-line form of the TUI cleaner for a single file
// component: line breaks become spaces, and controls, bidi and escape
// sequences are removed.
func cleanStoredName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		invalid := r == utf8.RuneError && n <= 1
		if invalid {
			r = rune(s[i])
		}
		next := i + n
		switch {
		case r == 0x1b:
			i = skipNameEsc(s, next)
			continue
		case r == 0x9b:
			i = skipNameCSI(s, next)
			continue
		case r == 0x9d:
			i = skipNameString(s, next, true)
			continue
		case r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f:
			i = skipNameString(s, next, false)
			continue
		}
		i = next
		switch {
		case invalid:
		case r == '\n' || r == '\t' || r == '\u2028' || r == '\u2029':
			b.WriteByte(' ')
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		case r == '/' || r == '\\':
		default:
			b.WriteString(s[i-n : i])
		}
	}
	return b.String()
}

func skipNameEsc(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch b := s[i]; {
	case b == '[':
		return skipNameCSI(s, i+1)
	case b == ']':
		return skipNameString(s, i+1, true)
	case b == 'P' || b == 'X' || b == '^' || b == '_':
		return skipNameString(s, i+1, false)
	case b >= 0x20 && b <= 0x2f:
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			i++
		}
		return i
	case b >= 0x30 && b <= 0x7e:
		return i + 1
	}
	return i
}

func skipNameCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

func skipNameString(s string, i int, osc bool) int {
	for j := i; j < len(s); {
		r, n := utf8.DecodeRuneInString(s[j:])
		if r == utf8.RuneError && n <= 1 {
			r = rune(s[j])
		}
		switch {
		case r == 0x07 && osc, r == 0x18, r == 0x1a, r == 0x9c:
			return j + n
		case r == 0x1b:
			if j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			return j
		}
		j += n
	}
	return i
}

// --- inbox / ack ---

// pollBody is the long-poll response of GET /v1/events.
type pollBody struct {
	Events        []envelope.Event `json:"events"`
	NextCursor    string           `json:"next_cursor"`
	HubRestarting *restartHint     `json:"hub_restarting,omitempty"`
}

// poll reads the stream once (wait seconds) after cursor. An empty cursor
// leaves out ?after=, so the hub starts at the stream's shared acked cursor
// (or its oldest retained event when nothing was acked) [PROTOCOL §2.3];
// the CLI never builds a cursor of its own (§1.3: cursors are opaque).
func (a *app) poll(c *client, after string, wait int) (pollBody, error) {
	q := a.peerQuery()
	if after != "" {
		q.Set("after", after)
	}
	q.Set("wait", strconv.Itoa(wait))
	var body pollBody
	ctx, cancel := context.WithTimeout(a.ctx, requestTimeout+time.Duration(wait)*time.Second)
	defer cancel()
	err := c.doNoTimeout(ctx, http.MethodGet, "/v1/events", q, nil, &body)
	return body, err
}

// oldestCursor returns details.oldest_cursor of a 409 cursor_purged.
func oldestCursor(err error) (string, bool) {
	var he *hubError
	if !errors.As(err, &he) || he.Code != "cursor_purged" {
		return "", false
	}
	s, ok := he.Details["oldest_cursor"].(string)
	return s, ok && s != ""
}

func (a *app) cmdInbox(args []string) error {
	o, err := parseFlags("inbox", args, map[string]flagKind{"since": strFlag, "ack": boolFlag, "limit": strFlag, "include-sender-chain": boolFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet inbox [--since CURSOR] [--ack] [--limit N] [--include-sender-chain]")
	}
	limit := 0
	if o.has("limit") {
		if limit, err = strconv.Atoi(o.str("limit")); err != nil || limit < 1 {
			return usageErr("inbox: --limit must be a positive whole number")
		}
	}
	c, name, ch, err := a.connect()
	if err != nil {
		return err
	}
	after, explicit := o.str("since"), o.has("since")
	body, err := a.poll(c, after, 0)
	if oldest, ok := oldestCursor(err); ok && !explicit {
		// The hub's acked cursor is below retention or from a rebuilt log.
		a.warn("acked cursor is no longer valid; reading from %s (re-sync threads with `vecnet thread`)", oldest)
		body, err = a.poll(c, oldest, 0)
	}
	if err != nil {
		return err
	}
	evs, next := body.Events, body.NextCursor
	if limit > 0 && len(evs) > limit {
		evs = evs[:limit]
	}
	if len(evs) > 0 {
		next = evs[len(evs)-1].Cursor
	}
	a.rememberEvents(ch, evs)
	view, err := inboxview.Project(evs, o.has("include-sender-chain"))
	if err != nil {
		return err
	}
	var output bytes.Buffer
	if a.json {
		raw, err := json.Marshal(pollBody{Events: view, NextCursor: next})
		if err != nil {
			return err
		}
		output.Write(raw)
		output.WriteByte('\n')
	} else {
		fmt.Fprintln(&output, inboxHeader)
		names := ch.peerNames()
		for _, ev := range view {
			line := inboxLine(ev, names)
			if o.has("include-sender-chain") && ev.Type == "delivery" {
				var data struct {
					Message struct {
						Loop struct {
							SenderChain []string `json:"sender_chain"`
						} `json:"loop"`
					} `json:"message"`
				}
				if err := json.Unmarshal(ev.Data, &data); err != nil {
					return err
				}
				chain := data.Message.Loop.SenderChain
				if chain == nil {
					chain = []string{}
				}
				raw, err := json.Marshal(chain)
				if err != nil {
					return err
				}
				line += " sender_chain=" + string(raw)
			}
			fmt.Fprintln(&output, line)
		}
	}
	if err := a.displayInbox(c, name, evs, output.Bytes()); err != nil {
		return err
	}
	if o.has("ack") && len(evs) > 0 {
		if err := a.ack(c, next); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "acked %s\n", next)
	}
	return nil
}

// ack posts cur to /v1/ack. The acked cursor is kept by the hub, per stream
// owner and scope, and shared by every consumer (PROTOCOL §2.3).
func (a *app) ack(c *client, cur string) error {
	return c.do(a.ctx, http.MethodPost, "/v1/ack", a.peerQuery(), map[string]string{"cursor": cur}, nil)
}

func (a *app) cmdAck(args []string) error {
	o, err := parseFlags("ack", args, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 1 {
		return usageErr("usage: vecnet ack <cursor>")
	}
	c, _, _, err := a.connect()
	if err != nil {
		return err
	}
	if err := a.ack(c, o.pos[0]); err != nil {
		return err
	}
	if a.json {
		return a.emitValue(map[string]string{"acked": o.pos[0]})
	}
	fmt.Fprintf(a.stdout, "acked %s\n", o.pos[0])
	return nil
}

// rememberEvents caches message → thread for every delivery in evs.
func (a *app) rememberEvents(ch cache, evs []envelope.Event) {
	pairs := map[string]string{}
	for _, ev := range evs {
		if ev.Type != "delivery" {
			continue
		}
		f := eventFieldsOf(ev)
		pairs[f.messageID] = f.thread
	}
	if err := ch.rememberMessages(pairs); err != nil {
		a.warn("message cache: %v", err)
	}
}

// --- threads / thread / peers / groups ---

// listField decodes field of a hub list response as raw items.
func listField(raw json.RawMessage, field string) ([]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, &hubError{Code: "bad_response", Message: err.Error()}
	}
	var items []json.RawMessage
	if v, ok := m[field]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &items); err != nil {
			return nil, &hubError{Code: "bad_response", Message: err.Error()}
		}
	}
	return items, nil
}

// getList GETs path and returns the array under field.
func (a *app) getList(path string, q url.Values, field string) ([]json.RawMessage, error) {
	c, _, _, err := a.connect()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.do(a.ctx, http.MethodGet, path, q, nil, &raw); err != nil {
		return nil, err
	}
	return listField(raw, field)
}

func limitQuery(o opts, q url.Values) error {
	if o.has("limit") {
		n, err := strconv.Atoi(o.str("limit"))
		if err != nil || n < 1 {
			return usageErr("--limit must be a positive whole number")
		}
		q.Set("limit", strconv.Itoa(n))
	}
	return nil
}

func (a *app) cmdThreads(args []string) error {
	o, err := parseFlags("threads", args, map[string]flagKind{"limit": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet threads [--limit N]")
	}
	q := url.Values{}
	if err := limitQuery(o, q); err != nil {
		return err
	}
	if a.as != "" {
		q.Set("participant", a.as)
	}
	items, err := a.getList("/v1/threads", q, "threads")
	if err != nil || a.json {
		if err != nil {
			return err
		}
		return a.emitList(items)
	}
	for _, it := range items {
		var t envelope.ThreadSummary
		if json.Unmarshal(it, &t) != nil {
			continue
		}
		title := "-"
		if t.Title != nil && *t.Title != "" {
			title = flatten(*t.Title)
		}
		fmt.Fprintf(a.stdout, "%s msgs=%d participants=%s %s\n", t.ThreadID, t.MessageCount, strings.Join(t.Participants, ","), title)
	}
	return nil
}

func (a *app) cmdThread(args []string) error {
	o, err := parseFlags("thread", args, map[string]flagKind{"after": strFlag, "limit": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) > 0 && o.pos[0] == "budget" {
		return a.threadBudget(o)
	}
	if len(o.pos) != 1 {
		return usageErr("usage: vecnet thread <id> [--after MESSAGE_ID] [--limit N] | thread budget <id> <n>")
	}
	q := url.Values{}
	if err := limitQuery(o, q); err != nil {
		return err
	}
	if v := o.str("after"); v != "" {
		q.Set("after", v)
	}
	c, _, ch, err := a.connect()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.do(a.ctx, http.MethodGet, "/v1/threads/"+pathEscape(o.pos[0])+"/messages", q, nil, &raw); err != nil {
		return err
	}
	var body opapi.History
	if err := json.Unmarshal(raw, &body); err != nil {
		return &hubError{Code: "bad_response", Message: err.Error()}
	}
	pairs := map[string]string{}
	for _, m := range body.Messages {
		pairs[m.MessageID] = m.ThreadID
	}
	if err := ch.rememberMessages(pairs); err != nil {
		a.warn("message cache: %v", err)
	}
	if a.json {
		return a.emitJSON(raw)
	}
	for _, m := range body.Messages {
		from := m.Sender
		if m.SenderInfo != nil {
			if name := peerName(m.SenderInfo.Name); name != "" {
				from = name + "(" + m.Sender + ")" // the send-time name, as in inbox lines
			}
		}
		fmt.Fprintf(a.stdout, "%s %s %s %s\n", m.MessageID, m.CreatedAt, from, flatten(m.Content.Text))
		for _, label := range blobLabels(m.Attachments, body.BlobStates) {
			fmt.Fprintln(a.stdout, label)
		}
		if len(m.Reactions) > 0 {
			groups := make([]string, 0, len(m.Reactions))
			for _, group := range m.Reactions {
				groups = append(groups, fmt.Sprintf("%s %d", group.Emoji, len(group.Peers)))
			}
			fmt.Fprintf(a.stdout, "  reactions: %s\n", strings.Join(groups, "  "))
		}
	}
	if body.NextAfter != nil {
		fmt.Fprintf(a.stderr, "more: vecnet thread %s --after %s\n", o.pos[0], *body.NextAfter)
	}
	return nil
}

// threadBudget is `thread budget <id> <n>`: PATCH /v1/admin/threads/{id}
// {"budget_remaining": n} (adminPatchThread). n is a non-negative integer.
func (a *app) threadBudget(o opts) error {
	const usage = "usage: vecnet thread budget <id> <n>"
	if len(o.pos) != 3 || o.has("after") || o.has("limit") {
		return usageErr(usage)
	}
	n, err := strconv.Atoi(o.pos[2])
	if err != nil || n < 0 {
		return usageErr(usage)
	}
	if err := a.call(http.MethodPatch, "/v1/admin/threads/"+pathEscape(o.pos[1]), map[string]int{"budget_remaining": n}, nil); err != nil {
		return err
	}
	return a.ok("budget " + o.pos[1] + " " + strconv.Itoa(n))
}

func (a *app) cmdPeers(args []string) error {
	o, err := parseFlags("peers", args, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet peers")
	}
	items, err := a.getList("/v1/peers", nil, "peers")
	if err != nil {
		return err
	}
	if a.json {
		return a.emitList(items)
	}
	for _, it := range items {
		var p struct {
			PeerID       string `json:"peer_id"`
			Name         string `json:"name"`
			Kind         string `json:"kind"`
			Presence     string `json:"presence"`
			Reachability string `json:"reachability"`
		}
		if json.Unmarshal(it, &p) != nil {
			continue
		}
		fmt.Fprintf(a.stdout, "%s %s %s presence=%s reach=%s\n", p.PeerID, p.Kind, flatten(p.Name), p.Presence, p.Reachability)
	}
	return nil
}

func (a *app) cmdGroups(args []string) error {
	o, err := parseFlags("groups", args, nil)
	if err != nil {
		return err
	}
	sub := ""
	if len(o.pos) > 0 {
		sub = o.pos[0]
	}
	// A principal profile acts for the peer named by --as (PROTOCOL §2.1).
	var asQ url.Values
	if a.as != "" {
		asQ = url.Values{"as": {a.as}}
	}
	switch {
	case sub == "create" && len(o.pos) >= 2:
		var out map[string]string
		if err := a.callQuery(http.MethodPost, "/v1/groups", asQ, map[string][]string{"members": o.pos[1:]}, &out); err != nil {
			return err
		}
		if a.json {
			return a.emitValue(out)
		}
		fmt.Fprintln(a.stdout, out["group_id"])
		return nil
	case sub == "add" && len(o.pos) >= 3:
		if err := a.callQuery(http.MethodPost, "/v1/groups/"+pathEscape(o.pos[1])+"/members", asQ, map[string][]string{"members": o.pos[2:]}, nil); err != nil {
			return err
		}
		return a.ok("ok")
	case sub == "unarchive" && len(o.pos) == 2:
		// Operator only: POST /v1/admin/groups/{id}/unarchive (204).
		if err := a.call(http.MethodPost, "/v1/admin/groups/"+pathEscape(o.pos[1])+"/unarchive", nil, nil); err != nil {
			return err
		}
		return a.ok("unarchived " + o.pos[1])
	case sub == "list" && len(o.pos) == 1:
		items, err := a.getList("/v1/groups", nil, "groups")
		if err != nil {
			return err
		}
		if a.json {
			return a.emitList(items)
		}
		for _, it := range items {
			var g struct {
				GroupID   string   `json:"group_id"`
				Members   []string `json:"members"`
				CreatedBy string   `json:"created_by"`
				Archived  bool     `json:"archived"`
			}
			if json.Unmarshal(it, &g) != nil {
				continue
			}
			arch := ""
			if g.Archived {
				arch = " archived"
			}
			fmt.Fprintf(a.stdout, "%s members=%s by=%s%s\n", g.GroupID, strings.Join(g.Members, ","), g.CreatedBy, arch)
		}
		return nil
	}
	return usageErr("usage: vecnet groups create <peer…> | groups add <grp> <peer…> | groups list | groups unarchive <grp>")
}

// call sends one request with the profile's credential.
func (a *app) call(method, path string, body, out any) error {
	return a.callQuery(method, path, nil, body, out)
}

// callQuery is call with query parameters.
func (a *app) callQuery(method, path string, q url.Values, body, out any) error {
	c, _, _, err := a.connect()
	if err != nil {
		return err
	}
	return c.do(a.ctx, method, path, q, body, out)
}

// --- presence / receipt ---

func (a *app) cmdPresence(args []string) error {
	o, err := parseFlags("presence", args, map[string]flagKind{"note": strFlag})
	if err != nil {
		return err
	}
	switch {
	case len(o.pos) == 2 && o.pos[0] == "set":
		body := map[string]string{"state": o.pos[1], "at": envelope.FormatTime(time.Now())}
		if o.has("note") {
			body["note"] = o.str("note")
		}
		// A principal names the peer it reports for with ?as= (PROTOCOL §2.6).
		var q url.Values
		if a.as != "" {
			q = url.Values{"as": {a.as}}
		}
		if err := a.callQuery(http.MethodPost, "/v1/presence", q, body, nil); err != nil {
			return err
		}
		return a.ok("presence " + o.pos[1])
	case len(o.pos) == 2 && o.pos[0] == "subscribe" && !o.has("note"):
		if err := a.call(http.MethodPatch, "/v1/peers/me", map[string]string{"subscribe_presence": o.pos[1]}, nil); err != nil {
			return err
		}
		return a.ok("subscribe_presence " + o.pos[1])
	}
	return usageErr("usage: vecnet presence set <idle|working|blocked|offline> [--note T] | presence subscribe <all|teams|none>")
}

func (a *app) cmdReceipt(args []string) error {
	o, err := parseFlags("receipt", args, map[string]flagKind{"note": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 2 {
		return usageErr("usage: vecnet receipt <delivery_id> <processed|read|failed> [--note T]")
	}
	body := map[string]string{"delivery_id": o.pos[0], "state": o.pos[1]}
	if o.has("note") {
		body["note"] = o.str("note")
	}
	if err := a.call(http.MethodPost, "/v1/receipts", body, nil); err != nil {
		return err
	}
	return a.ok("receipt " + o.pos[1] + " " + o.pos[0])
}

// --- admin ---

// emitObject prints an admin response: JSON as-is, else "key value" lines
// in keys order.
func (a *app) emitObject(out map[string]any, keys ...string) error {
	if a.json {
		return a.emitValue(out)
	}
	for _, k := range keys {
		if v, ok := out[k]; ok {
			fmt.Fprintf(a.stdout, "%s %v\n", k, v)
		}
	}
	return nil
}

func (a *app) cmdPrincipal(args []string) error {
	const usage = "usage: vecnet principal add --name N | principal limits <prn> --max-peers N --creates-per-hour N --kinds K[,K] | principal scope <prn> [--allow IDS] [--deny IDS] | principal list"
	specs := map[string]map[string]flagKind{
		"add":    {"name": strFlag},
		"limits": {"max-peers": strFlag, "creates-per-hour": strFlag, "kinds": strFlag},
		"scope":  {"allow": multiFlag, "deny": multiFlag},
		"list":   {},
	}
	// Parse the complete slice so flag values cannot be mistaken for a
	// subcommand, and flags may appear before or after any positional.
	allFlags := map[string]flagKind{}
	for _, spec := range specs {
		for name, kind := range spec {
			allFlags[name] = kind
		}
	}
	o, err := parseFlags("principal", args, allFlags)
	if err != nil {
		return err
	}
	if len(o.pos) == 0 {
		return usageErr(usage)
	}
	sub := o.pos[0]
	spec, ok := specs[sub]
	if !ok {
		return usageErr(usage)
	}
	for name := range o.vals {
		if _, ok := spec[name]; !ok {
			return usageErr(fmt.Sprintf("principal %s: unknown flag --%s", sub, name))
		}
	}
	o.pos = o.pos[1:]
	switch sub {
	case "add":
		if len(o.pos) != 0 || o.str("name") == "" {
			return usageErr(usage)
		}
		var out map[string]any
		if err := a.call(http.MethodPost, "/v1/admin/principals", map[string]string{"name": o.str("name")}, &out); err != nil {
			return err
		}
		return a.emitObject(out, "principal_id", "secret")
	case "limits":
		max, errMax := strconv.Atoi(o.str("max-peers"))
		rate, errRate := strconv.Atoi(o.str("creates-per-hour"))
		if len(o.pos) != 1 || errMax != nil || errRate != nil || !o.has("kinds") {
			return usageErr(usage)
		}
		id := o.pos[0]
		body := map[string]any{"max_peers": max, "creates_per_hour": rate, "kinds": o.list("kinds")}
		if err := a.call(http.MethodPut, "/v1/admin/principals/"+pathEscape(id)+"/limits", body, nil); err != nil {
			return err
		}
		// Read after the update so the warning reflects the operator's current policy.
		if max > 0 {
			rows, err := a.principalRows()
			if err != nil {
				return err
			}
			for _, p := range rows {
				if p.PrincipalID == id && p.ScopedPeers > 0 && len(p.DefaultAllow) == 0 && len(p.DefaultDeny) == 0 {
					fmt.Fprintf(a.stderr, "warning: %s has scoped peers but no default scopes; peers it creates will be unscoped (vecnet principal scope)\n", id)
				}
			}
		}
		return a.ok("limits set for " + id)
	case "scope":
		if len(o.pos) != 1 {
			return usageErr(usage)
		}
		body := map[string][]string{"allow": o.list("allow"), "deny": o.list("deny")}
		if err := a.call(http.MethodPut, "/v1/admin/principals/"+pathEscape(o.pos[0])+"/default-scopes", body, nil); err != nil {
			return err
		}
		return a.ok("scopes set for " + o.pos[0])
	case "list":
		if len(o.pos) != 0 {
			return usageErr(usage)
		}
		rows, err := a.principalRows()
		if err != nil {
			return err
		}
		if a.json {
			return a.emitValue(rows)
		}
		for _, p := range rows {
			scopes := "none"
			if len(p.DefaultAllow)+len(p.DefaultDeny) > 0 {
				scopes = fmt.Sprintf("allow %d deny %d", len(p.DefaultAllow), len(p.DefaultDeny))
			}
			fmt.Fprintf(a.stdout, "%s  %s  active %d/%d  disabled %d  creates/h %d/%d  kinds %s  default-scopes %s\n",
				p.PrincipalID, flatten(p.Name), p.Active, p.MaxPeers, p.Disabled, p.CreatesLastHour, p.CreatesPerHour, strings.Join(p.Kinds, ","), scopes)
		}
		return nil
	}
	return usageErr(usage)
}

func (a *app) principalRows() ([]opapi.PrincipalRow, error) {
	var body struct {
		Principals []opapi.PrincipalRow `json:"principals"`
	}
	if err := a.call(http.MethodGet, "/v1/admin/principals", nil, &body); err != nil {
		return nil, err
	}
	if body.Principals == nil {
		body.Principals = []opapi.PrincipalRow{}
	}
	return body.Principals, nil
}

func (a *app) cmdPeer(args []string) error {
	o, err := parseFlags("peer", args, map[string]flagKind{
		"kind": strFlag, "name": strFlag, "principal": strFlag,
		"allow": multiFlag, "deny": multiFlag,
		"sends-per-min": strFlag, "threads-per-hour": strFlag, "clear": boolFlag,
		"ttl": strFlag, "profile": strFlag, "open-name": boolFlag, "copy": boolFlag,
	})
	if err != nil {
		return err
	}
	sub := ""
	if len(o.pos) > 0 {
		sub = o.pos[0]
	}
	switch {
	case sub == "invite":
		return a.peerInvite(o)
	case sub == "add" && len(o.pos) == 1 && o.str("kind") != "" && o.str("name") != "":
		body := map[string]string{"kind": o.str("kind"), "name": o.str("name")}
		if p := o.str("principal"); p != "" {
			body["principal_id"] = p
		}
		var out map[string]any
		if err := a.call(http.MethodPost, "/v1/admin/peers", body, &out); err != nil {
			return err
		}
		if out["enrol_token"] == "" { // a dot: it holds no vecnet secret
			delete(out, "enrol_token")
			if err := a.emitObject(out, "peer_id", "principal_id"); err != nil {
				return err
			}
			if !a.json {
				fmt.Fprintf(a.stdout, "dots have no enrolment token; run `vecnet peer link %v` to connect it\n", out["peer_id"])
			}
			return nil
		}
		return a.emitObject(out, "peer_id", "enrol_token", "principal_id")
	case sub == "move" && len(o.pos) == 2 && o.str("principal") != "":
		if err := a.call(http.MethodPost, "/v1/admin/peers/"+pathEscape(o.pos[1])+"/move",
			map[string]string{"principal_id": o.str("principal")}, nil); err != nil {
			return err
		}
		return a.ok("moved " + o.pos[1] + " to " + o.str("principal"))
	case sub == "link" && len(o.pos) == 2:
		return a.peerLink(o.pos[1])
	case sub == "avatar" && (len(o.pos) == 2 || len(o.pos) == 3):
		return a.peerAvatar(o.pos[1], strings.Join(o.pos[2:], ""), o.has("clear"))
	case sub == "rotate" && len(o.pos) == 2:
		var out map[string]any
		if err := a.call(http.MethodPost, "/v1/admin/peers/"+pathEscape(o.pos[1])+"/rotate", nil, &out); err != nil {
			return err
		}
		return a.emitObject(out, "secret")
	case sub == "scope" && len(o.pos) == 2:
		body := map[string][]string{"allow": o.list("allow"), "deny": o.list("deny")}
		if err := a.call(http.MethodPut, "/v1/admin/peers/"+pathEscape(o.pos[1])+"/scopes", body, nil); err != nil {
			return err
		}
		return a.ok("scopes set for " + o.pos[1])
	case sub == "limits" && len(o.pos) == 2:
		perMin, err1 := strconv.Atoi(o.str("sends-per-min"))
		perHour, err2 := strconv.Atoi(o.str("threads-per-hour"))
		if err1 != nil || err2 != nil {
			return usageErr("usage: vecnet peer limits <peer> --sends-per-min N --threads-per-hour N")
		}
		body := map[string]int{"sends_per_min": perMin, "threads_per_hour": perHour}
		if err := a.call(http.MethodPut, "/v1/admin/peers/"+pathEscape(o.pos[1])+"/limits", body, nil); err != nil {
			return err
		}
		return a.ok("limits set for " + o.pos[1])
	case sub == "rename" && len(o.pos) == 3:
		// PATCH /v1/admin/peers/{id} {"name"} (204).
		if err := a.call(http.MethodPatch, "/v1/admin/peers/"+pathEscape(o.pos[1]),
			map[string]string{"name": o.pos[2]}, nil); err != nil {
			return err
		}
		return a.ok("renamed " + o.pos[1] + " to " + o.pos[2])
	case (sub == "disable" || sub == "enable" || sub == "archive" || sub == "unarchive") && len(o.pos) == 2:
		// POST /v1/admin/peers/{id}/disable|enable (204).
		if err := a.call(http.MethodPost, "/v1/admin/peers/"+pathEscape(o.pos[1])+"/"+sub, nil, nil); err != nil {
			return err
		}
		verb := map[string]string{"disable": "disabled", "enable": "enabled", "archive": "archived", "unarchive": "unarchived"}[sub]
		return a.ok(verb + " " + o.pos[1])
	}
	return usageErr("usage: vecnet peer add --kind K --name N [--principal PRN] | peer invite [--name N] [--ttl 24h] | peer move <peer> --principal PRN | " +
		"peer link <peer> | peer avatar <peer> (FILE|URL|--clear) | peer rotate <peer> | peer scope <peer> [--allow IDS] [--deny IDS] | peer limits <peer> --sends-per-min N --threads-per-hour N | " +
		"peer rename <id> <name> | peer disable <id> | peer enable <id> | peer archive <id> | peer unarchive <id>")
}

func (a *app) cmdRoute(args []string) error {
	o, err := parseFlags("route", args, map[string]flagKind{"owner": strFlag, "url": strFlag, "kind": strFlag, "key-file": strFlag})
	if err != nil {
		return err
	}
	switch {
	case len(o.pos) == 1 && o.pos[0] == "add" && o.str("owner") != "" && o.str("url") != "":
		body, err := a.routeAddBody(o)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := a.call(http.MethodPost, "/v1/admin/routes", body, &out); err != nil {
			return err
		}
		return a.emitObject(out, "route_id", "whsec")
	case len(o.pos) == 1 && o.pos[0] == "list":
		items, err := a.getList("/v1/admin/routes", nil, "routes")
		if err != nil {
			return err
		}
		if a.json {
			return a.emitList(items)
		}
		for _, item := range items {
			var route struct {
				RouteID     string  `json:"route_id"`
				OwnerKind   string  `json:"owner_kind"`
				OwnerID     string  `json:"owner_id"`
				Kind        string  `json:"kind"`
				Host        string  `json:"host"`
				VerifiedAt  *string `json:"verified_at"`
				RunsPerHour int     `json:"runs_per_hour"`
			}
			if err := json.Unmarshal(item, &route); err != nil {
				return err
			}
			verified := "-"
			if route.VerifiedAt != nil {
				verified = *route.VerifiedAt
			}
			fmt.Fprintf(a.stdout, "%s owner=%s:%s kind=%s host=%s verified_at=%s runs_per_hour=%d\n",
				route.RouteID, route.OwnerKind, route.OwnerID, route.Kind, flatten(route.Host), verified, route.RunsPerHour)
		}
		return nil
	case len(o.pos) == 2 && o.pos[0] == "rotate" && o.str("key-file") != "":
		key, err := a.readKeyFile(o.str("key-file"))
		if err != nil {
			return err
		}
		if err := a.call(http.MethodPost, "/v1/admin/routes/"+pathEscape(o.pos[1])+"/rotate", map[string]string{"key": key}, nil); err != nil {
			return err
		}
		return a.ok("rotated " + o.pos[1])
	case len(o.pos) == 2 && o.pos[0] == "remove":
		if err := a.call(http.MethodDelete, "/v1/admin/routes/"+pathEscape(o.pos[1]), nil, nil); err != nil {
			return err
		}
		return a.ok("removed " + o.pos[1])
	case len(o.pos) == 2 && o.pos[0] == "verify":
		if err := a.call(http.MethodPost, "/v1/admin/routes/"+pathEscape(o.pos[1])+"/verify", nil, nil); err != nil {
			return err
		}
		return a.ok("verified " + o.pos[1])
	}
	return usageErr("usage: vecnet route add --owner ID --url URL | " +
		"route add --owner ID --kind grokbot --url URL --key-file PATH | route verify <route_id> | route list [--json] | route rotate <route_id> --key-file PATH | route remove <route_id>")
}

func (a *app) cmdTeam(args []string) error {
	const usage = "usage: vecnet team add --name N [--members IDS] | team list | team members <id> --add IDS"
	o, err := parseFlags("team", args, map[string]flagKind{"name": strFlag, "members": multiFlag, "add": multiFlag})
	if err != nil {
		return err
	}
	sub := ""
	if len(o.pos) > 0 {
		sub = o.pos[0]
	}
	switch sub {
	case "add":
		if len(o.pos) != 1 || o.str("name") == "" || o.has("add") {
			return usageErr(usage)
		}
		body := map[string]any{"name": o.str("name"), "members": o.list("members")}
		var out map[string]any
		if err := a.call(http.MethodPost, "/v1/admin/teams", body, &out); err != nil {
			return err
		}
		return a.emitObject(out, "team_id")
	case "list":
		if len(o.pos) != 1 || o.has("name") || o.has("members") || o.has("add") {
			return usageErr(usage)
		}
		return a.teamList()
	case "members":
		// POST /v1/admin/teams/{id}/members {"members"} (adminTeamMembers).
		// The hub adds these peers and leaves existing members in place;
		// there is no remove or replace-roster endpoint yet. --add matches team
		// add's --members: comma-separated or repeated.
		if len(o.pos) != 2 || o.has("name") || o.has("members") || len(o.list("add")) == 0 {
			return usageErr(usage)
		}
		id := o.pos[1]
		body := map[string][]string{"members": o.list("add")}
		if err := a.call(http.MethodPost, "/v1/admin/teams/"+pathEscape(id)+"/members", body, nil); err != nil {
			return err
		}
		return a.ok("members added to " + id)
	}
	return usageErr(usage)
}

// teamList is `team list`: GET /v1/admin/teams.
func (a *app) teamList() error {
	items, err := a.getList("/v1/admin/teams", nil, "teams")
	if err != nil {
		return err
	}
	if a.json {
		return a.emitList(items)
	}
	for _, it := range items {
		var t struct {
			TeamID  string   `json:"team_id"`
			Name    string   `json:"name"`
			Members []string `json:"members"`
		}
		if json.Unmarshal(it, &t) != nil {
			continue
		}
		fmt.Fprintf(a.stdout, "%s %s members=%s\n", t.TeamID, flatten(t.Name), strings.Join(t.Members, ","))
	}
	return nil
}

func (a *app) cmdDeadLetters(args []string) error {
	o, err := parseFlags("dead-letters", args, nil)
	if err != nil {
		return err
	}
	switch {
	case len(o.pos) == 1 && o.pos[0] == "list":
		items, err := a.getList("/v1/admin/dead-letters", nil, "dead_letters")
		if err != nil {
			return err
		}
		if a.json {
			return a.emitList(items)
		}
		for _, it := range items {
			var d struct {
				DeliveryID string  `json:"delivery_id"`
				Recipient  string  `json:"recipient"`
				ThreadID   string  `json:"thread_id"`
				Attempt    int     `json:"attempt"`
				LastStatus *int    `json:"last_status"`
				LastError  *string `json:"last_error"`
				UpdatedAt  string  `json:"updated_at"`
			}
			if json.Unmarshal(it, &d) != nil {
				continue
			}
			status, lastErr := "-", "-"
			if d.LastStatus != nil {
				status = strconv.Itoa(*d.LastStatus)
			}
			if d.LastError != nil {
				lastErr = flatten(*d.LastError)
			}
			fmt.Fprintf(a.stdout, "%s %s %s attempt=%d status=%s %s %s\n", d.DeliveryID, d.Recipient, d.ThreadID, d.Attempt, status, d.UpdatedAt, lastErr)
		}
		return nil
	case len(o.pos) == 2 && (o.pos[0] == "retry" || o.pos[0] == "discard"):
		if err := a.call(http.MethodPost, "/v1/admin/dead-letters/"+pathEscape(o.pos[1])+"/"+o.pos[0], nil, nil); err != nil {
			return err
		}
		return a.ok(o.pos[0] + " " + o.pos[1])
	}
	return usageErr("usage: vecnet dead-letters list | dead-letters retry <dly> | dead-letters discard <dly>")
}

func (a *app) cmdNotify(args []string) error {
	o, err := parseFlags("notify", args, map[string]flagKind{"url": multiFlag, "filter": multiFlag})
	if err != nil {
		return err
	}
	urls, filters := o.all("url"), o.all("filter")
	if len(o.pos) != 1 || o.pos[0] != "set" || len(urls) != len(filters) {
		return usageErr("usage: vecnet notify set --url URL --filter F [--url URL --filter F]… (one --filter per --url)")
	}
	routes := make([]map[string]string, len(urls))
	for i := range urls {
		routes[i] = map[string]string{"url": urls[i], "filter": filters[i]}
	}
	var raw json.RawMessage
	if err := a.call(http.MethodPut, "/v1/admin/notify", map[string]any{"routes": routes}, &raw); err != nil {
		return err
	}
	if a.json {
		return a.emitJSON(raw)
	}
	items, err := listField(raw, "routes")
	if err != nil {
		return err
	}
	for _, it := range items {
		var r struct {
			ID     string `json:"id"`
			URL    string `json:"url"`
			Filter string `json:"filter"`
			Whsec  string `json:"whsec"`
		}
		if json.Unmarshal(it, &r) == nil {
			fmt.Fprintf(a.stdout, "%s %s filter=%s whsec=%s\n", r.ID, r.URL, r.Filter, r.Whsec)
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(a.stdout, "no notification routes")
	}
	return nil
}

func (a *app) cmdAudit(args []string) error {
	o, err := parseFlags("audit", args, map[string]flagKind{"limit": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet audit [--limit N]")
	}
	q := url.Values{}
	if err := limitQuery(o, q); err != nil {
		return err
	}
	items, err := a.getList("/v1/admin/audit", q, "entries")
	if err != nil {
		return err
	}
	if a.json {
		return a.emitList(items)
	}
	for _, it := range items {
		var e struct {
			At      string         `json:"at"`
			Actor   string         `json:"actor"`
			Action  string         `json:"action"`
			Targets []string       `json:"target_ids"`
			Detail  map[string]any `json:"detail"`
		}
		if json.Unmarshal(it, &e) != nil {
			continue
		}
		keys := make([]string, 0, len(e.Detail))
		for k := range e.Detail {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var det []string
		for _, k := range keys {
			det = append(det, fmt.Sprintf("%s=%v", k, e.Detail[k]))
		}
		fmt.Fprintf(a.stdout, "%s %s %s %s %s\n", e.At, e.Actor, e.Action, strings.Join(e.Targets, ","), strings.Join(det, " "))
	}
	return nil
}
