package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubnotice"
)

func noticeText(n envelope.HubNotice, cleared bool) string {
	if hubnotice.ValidateNotice(n, true) != nil {
		return hubnotice.ErrInvalidNotice.Error()
	}
	label := n.Kind
	if cleared {
		label = "cleared " + label
	} else {
		if n.At != "" {
			label += " " + n.At
		}
		if n.NewURL != "" {
			label += " -> " + n.NewURL
		}
	}
	return "[" + label + "] " + n.Message
}

// receivedHTTP comes only from the selected profile or explicit environment opt-in.
func (a *app) receivedHTTP(c *client) bool {
	return (c != nil && c.allowHTTP) || (a.getenv != nil && a.getenv(insecureEnv) == "1")
}

func (a *app) activeNotices(c *client) ([]envelope.HubNotice, error) {
	// profile follow constructs its client directly. Resolve that selected
	// profile's explicit policy too, without lending it to a different client.
	if !c.allowHTTP && a.getenv != nil {
		_, _, p, err := a.profileSelection()
		if err == nil && p.AllowInsecureHTTP && strings.TrimRight(p.Hub, "/") == c.base && p.Secret == c.secret {
			copy := *c
			copy.allowHTTP = true
			c = &copy
		}
	}
	var body struct {
		Notices []json.RawMessage `json:"notices"`
	}
	if err := c.do(a.ctx, http.MethodGet, "/v1/hub/notices", nil, nil, &body); err != nil {
		return nil, err
	}
	notices := make([]envelope.HubNotice, 0, len(body.Notices))
	for _, raw := range body.Notices {
		n, err := hubnotice.DecodeNotice(raw, a.receivedHTTP(c))
		if err != nil {
			return nil, err
		}
		notices = append(notices, n)
	}
	return notices, nil
}

func (a *app) cmdNotice(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: vecnet notice post|list|clear")
	}
	var spec map[string]flagKind
	switch args[0] {
	case "post":
		spec = map[string]flagKind{"kind": strFlag, "message": strFlag, "at": strFlag, "new-url": strFlag, "expires": strFlag}
	case "list":
		spec = map[string]flagKind{}
	case "clear":
		spec = map[string]flagKind{"kind": strFlag}
	default:
		return usageErr("usage: vecnet notice post|list|clear")
	}
	o, err := parseFlags("notice "+args[0], args[1:], spec)
	if err != nil {
		return err
	}
	if args[0] == "clear" {
		if len(o.pos) > 1 || (len(o.pos) == 1) == o.has("kind") {
			return usageErr("usage: vecnet notice clear ID | --kind K")
		}
	} else if len(o.pos) != 0 {
		return usageErr("notice: unexpected arguments")
	}
	if args[0] == "post" && (o.str("kind") == "" || !o.has("message")) {
		return usageErr("notice post requires --kind and --message")
	}
	if args[0] == "post" && o.has("new-url") {
		if err = checkHubURL(o.str("new-url"), a.getenv(insecureEnv) == "1"); err != nil {
			return err
		}
	}
	c, _, _, err := a.connect()
	if err != nil {
		return err
	}
	if args[0] != "list" && !strings.HasPrefix(strings.TrimSpace(c.secret), "vn_operator_") {
		return usageErr("forbidden: operator credential required")
	}
	switch args[0] {
	case "post":
		body := map[string]string{"kind": o.str("kind"), "message": o.str("message")}
		for flag, field := range map[string]string{"at": "at", "new-url": "new_url"} {
			if o.has(flag) {
				body[field] = o.str(flag)
			}
		}
		if o.has("expires") {
			value := o.str("expires")
			if d, e := time.ParseDuration(value); e == nil {
				if d <= 0 {
					return usageErr("--expires must be positive")
				}
				value = envelope.FormatTime(time.Now().Add(d))
			} else if _, e = envelope.ParseTime(value); e != nil {
				return usageErr("--expires needs a duration or wire timestamp")
			}
			body["expires_at"] = value
		}
		var result struct {
			Notice json.RawMessage `json:"notice"`
		}
		if err = c.do(a.ctx, http.MethodPost, "/v1/admin/notices", nil, body, &result); err != nil {
			return err
		}
		notice, err := hubnotice.DecodeNotice(result.Notice, a.receivedHTTP(c))
		if err != nil {
			return err
		}
		if a.json {
			return a.emitValue(notice)
		}
		fmt.Fprintf(a.stdout, "%s %s\n", notice.NoticeID, firstRunes(unbidi(flatten(noticeText(notice, false))), monitorTextRunes))
		return nil
	case "list":
		notices, e := a.activeNotices(c)
		if e != nil {
			return e
		}
		if a.json {
			if notices == nil {
				notices = []envelope.HubNotice{}
			}
			return a.emitValue(notices)
		}
		for _, n := range notices {
			fmt.Fprintf(a.stdout, "%s %s\n", n.NoticeID, firstRunes(unbidi(flatten(noticeText(n, false))), monitorTextRunes))
		}
		return nil
	default:
		ids := o.pos
		if o.has("kind") {
			if k := o.str("kind"); k != "moving" && k != "restart" && k != "maintenance" {
				return usageErr("unknown notice kind")
			}
			notices, e := a.activeNotices(c)
			if e != nil {
				return e
			}
			for _, n := range notices {
				if n.Kind == o.str("kind") {
					ids = append(ids, n.NoticeID)
				}
			}
			if len(ids) == 0 {
				return a.ok("no active " + o.str("kind") + " notice")
			}
		}
		for _, id := range ids {
			if err = c.do(a.ctx, http.MethodPost, "/v1/admin/notices/"+pathEscape(id)+"/clear", nil, nil, nil); err != nil {
				return err
			}
		}
		return a.ok("notice cleared")
	}
}

func noticeEventFields(data json.RawMessage) eventFields {
	if hubnotice.ValidateEvent(envelope.Event{Type: "hub_notice", Data: data}, true) != nil {
		return eventFields{sender: "hub", senderName: "hub", text: hubnotice.ErrInvalidNotice.Error()}
	}
	var n envelope.HubNoticeEvent
	if json.Unmarshal(data, &n) != nil {
		return eventFields{}
	}
	return eventFields{sender: "hub", senderName: "hub", text: noticeText(n.Notice, n.Change == "cleared")}
}
