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

	"github.com/oklog/ulid/v2"

	"github.com/XertroV/vecnet/internal/envelope"
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
  inbox [--since CURSOR] [--ack] [--limit N]
  ack <cursor>
  watch [--for-monitor] [--since CURSOR] [--auto-ack] [--deliveries-only]
       does not ack; use --auto-ack or vecnet ack <cursor> to avoid replaying
       the log after a restart
  hook [--event user-prompt|post-tool] [--profile P] [--auto-ack]
       Claude Code hook: always exits 0
  hook install [--profile P] [--write] [--settings PATH] [--auto-ack]
  threads [--limit N]
  thread <id> [--after MESSAGE_ID] [--limit N]
  peers
  groups create <peer…> | groups add <grp> <peer…> | groups list
  presence set <idle|working|blocked|offline> [--note T]
  presence subscribe <all|teams|none>
  avatar set <FILE|https://URL> | avatar clear       PNG, JPEG, GIF or WebP, at most 2 MiB;
       the hub crops it square and stores 128x128; a URL is fetched by the hub
  receipt <delivery_id> <processed|read|failed> [--note T]
  enrol --hub URL --token (T|-) --profile NAME      "-" reads the token from stdin;
       [--allow-insecure-http]                   http:// is refused off loopback/LAN without it
       (preferred: keeps it out of shell history and ps); else $VECNET_ENROL_TOKEN
  guide                                              the agent manual; needs no config

admin (operator credential):
  principal add --name N
  principal limits <prn> --max-peers N --creates-per-hour N --kinds K[,K]
  principal scope <prn> [--allow IDS] [--deny IDS]
  principal list
  peer add --kind K --name N [--principal PRN]
  peer invite --name N [--kind K] [--principal PRN] [--ttl 24h] [--profile P]
       adds the peer and prints a block to paste to the agent (token valid --ttl, max 7d)
  peer invite <peer> --profile P [--ttl 24h]          re-issues a token for a peer not yet enrolled
  peer move <peer> --principal PRN
  peer link <peer>                                    prints a dot's 12-character link code
  peer avatar <peer> (FILE|https://URL|--clear)       set or clear any peer's avatar
  peer rotate <peer>
  peer scope <peer> [--allow IDS] [--deny IDS]        IDS: comma-separated or repeated
  peer limits <peer> --sends-per-min N --threads-per-hour N
  route add --owner ID --url URL | route verify <route_id>
  route add --owner ID --kind grokbot --url URL --key-file PATH
       the crsr_ key is read only from a file, 0600, never from argv
  source add --kind github --name N --route ID [--events A,B] [--priority P]
       prints the ingress URL and the secret, once
  source list
  subscriptions list | subscriptions pause <id> | subscriptions resume <id>
  team add --name N [--members IDS]
  dead-letters list | dead-letters retry <dly> | dead-letters discard <dly>
  notify set --url URL --filter F [--url URL --filter F]…
  audit [--limit N]
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
	return a.exit(a.dispatch(rest[0], rest[1:]))
}

// app is one invocation.
type app struct {
	ctx            context.Context
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string

	net, as string // --net, --as
	json    bool   // --json
}

// exit prints err and maps it to an exit code.
func (a *app) exit(err error) int {
	if err == nil {
		return exitOK
	}
	var ue *usageError
	var he *hubError
	var ne *netError
	switch {
	case errors.As(err, &he):
		fmt.Fprintf(a.stderr, "%s: %s\n", he.Code, he.Message)
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
// they appear (before a "--"), returning the rest in order.
func (a *app) parseGlobal(args []string) (rest []string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
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
	if err := checkHubURL(p.Hub, p.AllowInsecureHTTP || a.getenv(insecureEnv) == "1"); err != nil {
		return nil, "", cache{}, err
	}
	dir, err := cacheDir(a.getenv)
	if err != nil {
		dir = ""
	}
	c := newClient(p.Hub, p.Secret)
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
	switch cmd {
	case "enrol", "enroll":
		return a.cmdEnrol(args)
	case "guide":
		return a.cmdGuide(args)
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
	case "watch":
		return a.cmdWatch(args)
	case "hook":
		return a.cmdHook(args)
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
	case "audit":
		return a.cmdAudit(args)
	case "tui":
		return a.cmdTUI(args)
	case "init-hub", "init":
		return usageErr("hub initialisation belongs to the hub binary: run `vecnet-hub init`")
	}
	return usageErr(fmt.Sprintf("unknown command %q; run `vecnet help`", cmd))
}

// --- enrol ---

func (a *app) cmdEnrol(args []string) error {
	o, err := parseFlags("enrol", args, map[string]flagKind{"hub": strFlag, "token": strFlag, "profile": strFlag, "allow-insecure-http": boolFlag})
	if err != nil {
		return err
	}
	hubURL, tok, name := o.str("hub"), o.str("token"), o.str("profile")
	// The one-time token is best kept off the command line (shell history,
	// ps): "--token -" reads it from stdin, and VECNET_ENROL_TOKEN is the
	// fallback when --token is absent.
	switch tok {
	case "-":
		if tok, err = a.readText("-"); err != nil {
			return err
		}
		tok = strings.TrimSpace(tok)
	case "":
		tok = strings.TrimSpace(a.getenv("VECNET_ENROL_TOKEN"))
	}
	if hubURL == "" || tok == "" || name == "" || len(o.pos) != 0 {
		return usageErr("usage: vecnet enrol --hub URL --token (T|-) --profile NAME [--allow-insecure-http]  (or $VECNET_ENROL_TOKEN)")
	}
	insecure := o.has("allow-insecure-http")
	if err := checkHubURL(hubURL, insecure || a.getenv(insecureEnv) == "1"); err != nil {
		return err
	}
	path, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	var out struct {
		PeerID string `json:"peer_id"`
		Secret string `json:"secret"`
	}
	if err := newClient(hubURL, "").do(a.ctx, http.MethodPost, "/v1/enrol", nil, map[string]string{"token": tok}, &out); err != nil {
		return err
	}
	cfg.Profiles[name] = Profile{Hub: strings.TrimRight(hubURL, "/"), Secret: out.Secret, PeerID: out.PeerID, AllowInsecureHTTP: insecure}
	if err := saveConfig(path, cfg); err != nil {
		// The secret was issued once; show it so it is not lost.
		fmt.Fprintf(a.stderr, "vecnet: could not write %s; the secret for %s is %s\n", path, out.PeerID, out.Secret)
		return err
	}
	if a.json {
		return a.emitValue(map[string]string{"profile": name, "peer_id": out.PeerID, "config": path})
	}
	fmt.Fprintf(a.stdout, "enrolled %s as profile %s (%s)\n", out.PeerID, name, path)
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
		req.Attachments = append(req.Attachments, att)
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
	if resp.Spilled {
		fmt.Fprint(a.stdout, " (spilled)")
	}
	if resp.JobID != "" {
		fmt.Fprint(a.stdout, " job "+resp.JobID)
	}
	fmt.Fprintln(a.stdout)
	return nil
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

// upload PUTs one file to /v1/blobs and returns its attachment reference.
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
	var out struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := c.doNoTimeout(ctx, http.MethodPut, "/v1/blobs", nil, rawBody{r: f, contentType: mt, length: st.Size()}, &out); err != nil {
		return envelope.Attachment{}, err
	}
	return envelope.Attachment{SHA256: out.SHA256, Size: out.Size, MediaType: mt, Name: filepath.Base(path)}, nil
}

// --- inbox / ack ---

// pollBody is the long-poll response of GET /v1/events.
type pollBody struct {
	Events     []envelope.Event `json:"events"`
	NextCursor string           `json:"next_cursor"`
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
	o, err := parseFlags("inbox", args, map[string]flagKind{"since": strFlag, "ack": boolFlag, "limit": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet inbox [--since CURSOR] [--ack] [--limit N]")
	}
	limit := 0
	if o.has("limit") {
		if limit, err = strconv.Atoi(o.str("limit")); err != nil || limit < 1 {
			return usageErr("inbox: --limit must be a positive whole number")
		}
	}
	c, _, ch, err := a.connect()
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
	if a.json {
		if err := a.emitValue(pollBody{Events: evs, NextCursor: next}); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(a.stdout, inboxHeader)
		names := ch.peerNames()
		for _, ev := range evs {
			fmt.Fprintln(a.stdout, inboxLine(ev, names))
		}
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
	if len(o.pos) != 1 {
		return usageErr("usage: vecnet thread <id> [--after MESSAGE_ID] [--limit N]")
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
	var body struct {
		Messages  []envelope.Message `json:"messages"`
		NextAfter *string            `json:"next_after"`
	}
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
		fmt.Fprintf(a.stdout, "%s %s %s %s\n", m.MessageID, m.CreatedAt, m.Sender, flatten(m.Content.Text))
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
	return usageErr("usage: vecnet groups create <peer…> | groups add <grp> <peer…> | groups list")
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
		"ttl": strFlag, "profile": strFlag,
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
	}
	return usageErr("usage: vecnet peer add --kind K --name N [--principal PRN] | peer invite --name N [--ttl 24h] | peer move <peer> --principal PRN | " +
		"peer link <peer> | peer avatar <peer> (FILE|URL|--clear) | peer rotate <peer> | peer scope <peer> [--allow IDS] [--deny IDS] | peer limits <peer> --sends-per-min N --threads-per-hour N")
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
	case len(o.pos) == 2 && o.pos[0] == "verify":
		if err := a.call(http.MethodPost, "/v1/admin/routes/"+pathEscape(o.pos[1])+"/verify", nil, nil); err != nil {
			return err
		}
		return a.ok("verified " + o.pos[1])
	}
	return usageErr("usage: vecnet route add --owner ID --url URL | " +
		"route add --owner ID --kind grokbot --url URL --key-file PATH | route verify <route_id>")
}

func (a *app) cmdTeam(args []string) error {
	o, err := parseFlags("team", args, map[string]flagKind{"name": strFlag, "members": multiFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 1 || o.pos[0] != "add" || o.str("name") == "" {
		return usageErr("usage: vecnet team add --name N [--members IDS]")
	}
	body := map[string]any{"name": o.str("name"), "members": o.list("members")}
	var out map[string]any
	if err := a.call(http.MethodPost, "/v1/admin/teams", body, &out); err != nil {
		return err
	}
	return a.emitObject(out, "team_id")
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
