package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

func noticeText(n envelope.HubNotice, cleared bool) string {
	label := n.Kind
	if cleared {
		label = "cleared " + label
	} else {
		if n.At != "" {
			label += " " + n.At
		}
		if n.NewURL != "" {
			if u, err := url.Parse(n.NewURL); err == nil {
				label += " -> " + u.Host
			}
		}
	}
	return "[" + label + "] " + n.Message
}

func (a *app) activeNotices(c *client) ([]envelope.HubNotice, error) {
	var body struct {
		Notices []envelope.HubNotice `json:"notices"`
	}
	err := c.do(a.ctx, http.MethodGet, "/v1/hub/notices", nil, nil, &body)
	return body.Notices, err
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
			Notice envelope.HubNotice `json:"notice"`
		}
		if err = c.do(a.ctx, http.MethodPost, "/v1/admin/notices", nil, body, &result); err != nil {
			return err
		}
		if a.json {
			return a.emitValue(result.Notice)
		}
		fmt.Fprintf(a.stdout, "%s %s\n", result.Notice.NoticeID, firstRunes(unbidi(flatten(noticeText(result.Notice, false))), monitorTextRunes))
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
	var n envelope.HubNoticeEvent
	if json.Unmarshal(data, &n) != nil {
		return eventFields{}
	}
	return eventFields{sender: "hub", senderName: "hub", text: noticeText(n.Notice, n.Change == "cleared")}
}
