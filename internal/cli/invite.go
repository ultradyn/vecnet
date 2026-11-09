package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/onboarding"
)

// guideText is the agent manual `vecnet guide` prints, the one document an
// invited agent reads (docs/connectors link it). It needs no config.
//
//go:embed guide.md
var guideText string

// profileRE is the profile shape the hook prints verbatim (PROTOCOL §13.9).
var profileRE = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)

// cmdGuide prints the agent manual. Given a profile (--net or
// $VECNET_PROFILE) of a safe shape it fills that in, and the hub and peer id
// when the config holds the profile; otherwise the placeholders stay.
func (a *app) cmdGuide(args []string) error {
	if len(args) != 0 {
		return usageErr("usage: vecnet [--net PROFILE] guide")
	}
	_, err := fmt.Fprint(a.stdout, renderGuide(a.namedProfile(), a.guideProfile))
	return err
}

// guideProfile reads profile name from the config, quietly: the guide must
// work with none.
func (a *app) guideProfile(name string) (Profile, bool) {
	path, err := configPath(a.getenv)
	if err != nil {
		return Profile{}, false
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return Profile{}, false
	}
	p, ok := cfg.Profiles[name]
	return p, ok
}

var peerIDRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,80}$`)

// renderGuide fills the guide for profile; lookup finds its hub and peer.
func renderGuide(profile string, lookup func(string) (Profile, bool)) string {
	if !profileRE.MatchString(profile) {
		return guideText
	}
	r := []string{"<profile>", profile}
	if p, ok := lookup(profile); ok {
		if hub, ok := printableHub(p.Hub, p.AllowInsecureHTTP); ok && safeURL(hub) {
			r = append(r, "<hub>", hub)
		}
		if peerIDRE.MatchString(p.PeerID) {
			r = append(r, "<peer_id>", p.PeerID)
		}
	}
	return strings.NewReplacer(r...).Replace(guideText)
}

// safeURL holds an http(s) URL with no spaces or shell or markup characters.
func safeURL(u string) bool {
	return (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) && !strings.ContainsAny(u, " \t\n'\"`$;<>|&")
}

// inviteSlug turns a peer name into a profile name. The registry owns the
// rule; internal/ui/static/invite-model.js mirrors it, and testdata/invite.json
// pins both.
func inviteSlug(name string) string {
	return onboarding.Slug(name)
}

// invite is what the paste block says. Name is set only for an invite that
// leaves the display name to the agent: the suggestion, or "" for none.
type invite struct {
	Owner, HubURL, Profile, Token, Expires string
	Open                                   bool
	Name                                   string
}

// inviteBlock is the short paste-ready text for an invited agent. The
// registry renders it; internal/ui/static/invite-model.js and
// testdata/invite.json pin the same words. The command is one plain
// pipeline: no variables, ";" or "$(…)", which agents' shell guards refuse.
func inviteBlock(in invite) string {
	open := "false"
	if in.Open {
		open = "true"
	}
	text, err := onboarding.RenderPrompt("session", onboarding.PhaseInvite, map[string]string{
		"owner": in.Owner, "hub_url": in.HubURL, "profile": in.Profile,
		"token": in.Token, "expires": in.Expires, "open": open, "name": in.Name,
	})
	if err != nil {
		return ""
	}
	return text
}

// parseTTL reads --ttl as a Go duration or a whole number of days ("7d"),
// in whole seconds; "" is 0, the hub's default (24 h).
func parseTTL(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	if d, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			return n * 86400, nil
		}
	} else if d, err := time.ParseDuration(s); err == nil && d >= time.Second && d%time.Second == 0 {
		return int(d / time.Second), nil
	}
	return 0, usageErr("--ttl must be a duration such as 1h, 24h or 7d (at most 7d)")
}

const peerInviteUsage = "usage: vecnet peer invite [--name N [--open-name]] [--kind K] [--principal PRN] [--ttl 24h] [--profile P] [--copy] | " +
	"peer invite <peer> [--ttl 24h] [--copy]   (re-issue for a peer that has not enrolled) | peer invite status|revoke <peer>"

// inviteAnswer is the hub's answer to a peer create or a token re-issue.
type inviteAnswer struct {
	PeerID         string `json:"peer_id"`
	Name           string `json:"name"`
	NameOpen       bool   `json:"name_open"`
	PrincipalID    string `json:"principal_id"`
	EnrolToken     string `json:"enrol_token"`
	EnrolExpiresAt string `json:"enrol_expires_at"`
	HubURL         string `json:"hub_url"`
	HubOwner       string `json:"hub_owner"`
}

// peerInvite creates a peer (or, given an existing peer id, re-issues its
// token) and prints the paste block on stdout, alone; the peer id and expiry
// go to stderr. --copy also puts the block on the clipboard. Without --name,
// or with --open-name, the agent picks its name at enrol and --name is only
// a suggestion.
func (a *app) peerInvite(o opts) error {
	ttl, err := parseTTL(o.str("ttl"))
	if err != nil {
		return err
	}
	var out inviteAnswer
	profile := o.str("profile")
	suggested := false
	switch {
	case len(o.pos) >= 2 && (o.pos[1] == "status" || o.pos[1] == "revoke"):
		if len(o.pos) != 3 || len(o.vals) != 0 {
			return usageErr(inviteUsage)
		}
		return a.inviteState(o.pos[1], o.pos[2])
	case len(o.pos) == 1:
		kind := o.str("kind")
		if kind == "" {
			kind = "session"
		}
		if kind == "operator" || kind == "dot" || kind == "source" {
			return usageErr("peer invite: kind " + kind + " cannot be invited; use `vecnet peer add`")
		}
		open := o.str("name") == "" || o.has("open-name")
		suggested = open && o.str("name") != ""
		body := map[string]any{"kind": kind}
		if n := o.str("name"); n != "" {
			body["name"] = n
		}
		if open {
			body["name_open"] = true
		}
		if p := o.str("principal"); p != "" {
			body["principal_id"] = p
		}
		if ttl > 0 {
			body["enrol_ttl_seconds"] = ttl
		}
		if err := a.call(http.MethodPost, "/v1/admin/peers", body, &out); err != nil {
			return err
		}
	case len(o.pos) == 2 && !o.has("name") && !o.has("open-name"):
		var body any
		if ttl > 0 {
			body = map[string]any{"enrol_ttl_seconds": ttl}
		}
		if err := a.call(http.MethodPost, "/v1/admin/peers/"+pathEscape(o.pos[1])+"/enrol-token", body, &out); err != nil {
			return err
		}
		suggested = out.NameOpen && !strings.HasPrefix(out.Name, placeholderPrefix)
	default:
		return usageErr(peerInviteUsage)
	}
	if profile == "" {
		profile = inviteSlug(out.Name) // an open invite's suggestion or placeholder
	}
	if !profileRE.MatchString(profile) {
		return usageErr("peer invite: --profile must match " + profileRE.String())
	}
	hubURL := out.HubURL
	if hubURL == "" { // an older hub: fall back to this profile's hub
		if c, _, _, err := a.connect(); err == nil {
			hubURL = c.base
		}
	}
	// Check the effective URL without normalizing it or contacting its hub.
	_, _, selectedProfile, err := a.profileSelection()
	if err != nil || checkHubURL(hubURL, selectedProfile.AllowInsecureHTTP || a.getenv(insecureEnv) == "1") != nil {
		return usageErr("invalid invitation hub URL")
	}
	in := invite{Owner: out.HubOwner, HubURL: hubURL, Profile: profile, Token: out.EnrolToken, Expires: out.EnrolExpiresAt, Open: out.NameOpen}
	if suggested {
		in.Name = out.Name
	}
	block := inviteBlock(in)
	if block == "" {
		return usageErr("peer invite: the invitation text could not be rendered")
	}
	if a.json {
		return a.emitValue(map[string]any{"peer_id": out.PeerID, "principal_id": out.PrincipalID, "name": out.Name, "name_open": out.NameOpen,
			"profile": profile, "hub_url": hubURL, "enrol_token": out.EnrolToken, "enrol_expires_at": out.EnrolExpiresAt, "invite": block})
	}
	if _, err := fmt.Fprint(a.stdout, block); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "invited peer %s (profile %s); the token works once and expires %s\n", out.PeerID, profile, out.EnrolExpiresAt)
	if o.has("copy") {
		if tool, err := copyClipboard(a.ctx, a.getenv, block); err != nil {
			fmt.Fprintf(a.stderr, "not copied: %v\n", err)
		} else {
			fmt.Fprintf(a.stderr, "copied to the clipboard (%s)\n", tool)
		}
	}
	return nil
}

// placeholderPrefix starts the name the hub gives an open invite with no
// suggestion (hub.AddPeerWith).
const placeholderPrefix = "invited-"

// clipboardTools are tried in order: Wayland, X11, macOS.
var clipboardTools = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"pbcopy"}}

// copyClipboard pipes text into the first clipboard tool found on $PATH.
func copyClipboard(ctx context.Context, getenv func(string) string, text string) (string, error) {
	for _, t := range clipboardTools {
		bin, err := lookPath(t[0], getenv("PATH"))
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(cctx, bin, t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		cmd.WaitDelay = 100 * time.Millisecond
		err = cmd.Run()
		cancel()
		if err != nil {
			return "", fmt.Errorf("%s: %w", t[0], err)
		}
		return t[0], nil
	}
	return "", errors.New("no clipboard tool found (wl-copy, xclip or pbcopy)")
}

// lookPath finds name in the PATH list path (the CLI resolves its
// environment only through getenv).
func lookPath(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

const inviteUsage = "usage: vecnet invite status <peer> | invite revoke <peer>   (also: vecnet peer invite status|revoke <peer>)"

// inviteStatus is GET or DELETE /v1/admin/peers/{id}/enrol-token: an
// invite's state, never its token.
type inviteStatus struct {
	PeerID    string `json:"peer_id"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at"`
	RevokedAt string `json:"revoked_at"`
	Revoked   *bool  `json:"revoked,omitempty"`
}

// cmdInvite is `vecnet invite status|revoke <peer>`, the same commands as
// `vecnet peer invite status|revoke <peer>`.
func (a *app) cmdInvite(args []string) error {
	o, err := parseFlags("invite", args, map[string]flagKind{})
	if err != nil {
		return err
	}
	if len(o.pos) != 2 || (o.pos[0] != "status" && o.pos[0] != "revoke") {
		return usageErr(inviteUsage)
	}
	return a.inviteState(o.pos[0], o.pos[1])
}

// inviteState reads (status) or withdraws (revoke) peer's invite. A revoke
// with nothing live is a no-op that says so and exits 0; an enrolled peer's
// credential is never touched (that is `peer disable` or `peer rotate`).
func (a *app) inviteState(verb, peer string) error {
	path := "/v1/admin/peers/" + pathEscape(peer) + "/enrol-token"
	method := http.MethodGet
	if verb == "revoke" {
		method = http.MethodDelete
	}
	var st inviteStatus
	if err := a.call(method, path, nil, &st); err != nil {
		return err
	}
	if a.json {
		return a.emitValue(st)
	}
	if verb == "revoke" {
		if st.Revoked != nil && *st.Revoked {
			_, err := fmt.Fprintf(a.stdout, "revoked the invite of %s; its token no longer enrols\n", st.PeerID)
			return err
		}
		_, err := fmt.Fprintf(a.stdout, "%s has no live invite (%s); nothing to revoke\n", st.PeerID, st.State)
		if err == nil && st.State == "claimed" {
			fmt.Fprintf(a.stderr, "it has enrolled: stop it with `vecnet peer disable %s` or replace its secret with `vecnet peer rotate %s`\n", st.PeerID, st.PeerID)
		}
		return err
	}
	line := st.PeerID + "  " + st.State
	switch st.State {
	case "pending":
		line += "  expires " + st.ExpiresAt
	case "expired":
		line += "  at " + st.ExpiresAt + "; re-issue with `vecnet peer invite " + st.PeerID + "`"
	case "revoked":
		line += "  at " + st.RevokedAt + "; re-issue with `vecnet peer invite " + st.PeerID + "`"
	case "claimed":
		line += "  (enrolled)"
	case "linked":
		line += "  (an OAuth login; see `vecnet peer link`)"
	case "none":
		line += "  (no enrolment token issued)"
	}
	_, err := fmt.Fprintln(a.stdout, line)
	return err
}
