package ingress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SummaryMax is the byte cap of a source Message's content.text (PROTOCOL
// §10).
const SummaryMax = 4096

// originThreadMax is $defs/origin's maxLength for thread_id.
const originThreadMax = 192

// ErrBadPayload reports a signed body that is not a JSON object.
var ErrBadPayload = errors.New("ingress: payload is not a JSON object")

// newMAC is hmac.New; a test swaps it to count HMAC runs.
var newMAC = hmac.New

// GitHubEvent is the part of a webhook payload a Message needs. Number is
// the issue or PR number, 0 when the event has none. Ref is set on push.
// Excerpt is the first line of a comment or review, which says what this
// event added when Title only names the issue or PR. Delivery comes from the
// X-GitHub-Delivery header, not from the body.
type GitHubEvent struct {
	Delivery, Event, Action string
	Repo                    string
	Number                  int
	Ref                     string
	Title, Actor, URL       string
	Excerpt                 string
}

// VerifyGitHub reports whether sigHeader, an X-Hub-Signature-256 value, is
// "sha256=" plus the hex HMAC-SHA256 of body under secret. The HMAC runs and
// the comparison uses hmac.Equal whatever the header looks like, so a
// malformed header costs what a wrong signature costs.
func VerifyGitHub(secret string, sigHeader string, body []byte) bool {
	m := newMAC(sha256.New, []byte(secret))
	m.Write(body)
	want := m.Sum(nil)
	sig, prefixed := strings.CutPrefix(sigHeader, "sha256=")
	got, err := hex.DecodeString(sig)
	equal := hmac.Equal(want, got)
	return prefixed && err == nil && equal
}

// ParseGitHub reads the fields of a webhook of type event (the
// X-GitHub-Event header). It is tolerant by design: GitHub adds fields and
// the events it does not model still arrive, so only a body that is not a
// JSON object is an error (ErrBadPayload). Unknown events carry the repo,
// action and actor alone.
func ParseGitHub(event string, body []byte) (GitHubEvent, error) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return GitHubEvent{}, ErrBadPayload
	}
	e := GitHubEvent{
		Event:  event,
		Action: str(top, "action"),
		Repo:   str(top, "repository", "full_name"),
		Actor:  str(top, "sender", "login"),
	}
	item := func(key, urlKey string) {
		e.Number = num(top, key, "number")
		e.Title = str(top, key, "title")
		if e.URL = str(top, urlKey, "html_url"); e.URL == "" {
			e.URL = str(top, key, "html_url")
		}
		if urlKey != key {
			e.Excerpt = firstLine(str(top, urlKey, "body"))
		}
	}
	switch event {
	case "issues":
		item("issue", "issue")
	case "issue_comment":
		item("issue", "comment")
	case "pull_request":
		item("pull_request", "pull_request")
	case "pull_request_review":
		item("pull_request", "review")
	case "pull_request_review_comment":
		item("pull_request", "comment")
	case "push":
		e.Ref = str(top, "ref")
		e.URL = str(top, "compare")
		e.Title, _, _ = strings.Cut(str(top, "head_commit", "message"), "\n")
	case "release":
		if e.Title = str(top, "release", "name"); e.Title == "" {
			e.Title = str(top, "release", "tag_name")
		}
		e.URL = str(top, "release", "html_url")
	case "workflow_run":
		e.Title = str(top, "workflow_run", "name")
		e.URL = str(top, "workflow_run", "html_url")
	}
	return e, nil
}

// firstLine is the first line of s that is not blank, trimmed.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// str walks path through nested objects and returns the string there, or "".
func str(v any, path ...string) string {
	s, _ := walk(v, path).(string)
	return s
}

// num is str for a whole number, 0 when absent.
func num(v any, path ...string) int {
	f, _ := walk(v, path).(float64)
	return int(f)
}

func walk(v any, path []string) any {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// Name is the event's dotted name: "issue_comment.created", or "push" for
// an event without an action. It is the vecnet.event metadata value.
func (e GitHubEvent) Name() string {
	if e.Action == "" {
		return e.Event
	}
	return e.Event + "." + e.Action
}

// OriginThread is the external conversation the event belongs to:
// "<repo>#<n>", "<repo>@<ref>" for a push, and "<repo>#<event>" for an event
// without a number. Each issue or PR threads separately (PROTOCOL §10).
func (e GitHubEvent) OriginThread() string {
	var s string
	switch {
	case e.Number != 0:
		s = e.Repo + "#" + strconv.Itoa(e.Number)
	case e.Event == "push" && e.Ref != "":
		s = e.Repo + "@" + e.Ref
	default:
		s = e.Repo + "#" + e.Event
	}
	return cut(s, originThreadMax)
}

// SummaryGitHub renders content.text: `[repo] event.action on #N by actor:
// "title" url`, in at most SummaryMax bytes. The quoted part is the Excerpt
// when there is one (PROTOCOL EX08). An oversize quote is shortened first, so
// the URL survives; every cut lands on a rune boundary.
func SummaryGitHub(e GitHubEvent) string {
	var head strings.Builder
	head.WriteString("[" + e.Repo + "] " + e.Name())
	switch {
	case e.Number != 0:
		head.WriteString(" on #" + strconv.Itoa(e.Number))
	case e.Ref != "":
		head.WriteString(" on " + e.Ref)
	}
	if e.Actor != "" {
		head.WriteString(" by " + e.Actor)
	}
	tail := ""
	if e.URL != "" {
		tail = " " + e.URL
	}
	quote := e.Title
	if e.Excerpt != "" {
		quote = e.Excerpt
	}
	if quote == "" {
		return cut(head.String()+tail, SummaryMax)
	}
	const open, closing = `: "`, `"`
	room := SummaryMax - head.Len() - len(open) - len(closing) - len(tail)
	if room < 0 {
		room = 0
	}
	return cut(head.String()+open+cut(quote, room)+closing+tail, SummaryMax)
}

// cut shortens s to at most n bytes without splitting a rune.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
