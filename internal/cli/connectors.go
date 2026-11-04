package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Connector admin commands (PROTOCOL §2.12, §10, §13.7): dot link codes,
// inbound webhook sources, grokbot routes and the dots health table.

const (
	grokbotKind      = "grokbot"
	grokbotKeyPrefix = "crsr_"
	// maxKeyFile bounds a key file: it is far above any real key, and a file
	// this size is the wrong file.
	maxKeyFile = 4 << 10
)

// peerLink is `peer link <peer>`: the 12-character code, shown as
// ABCD-EFGH-JKMN, that the dot's principal enters on first login
// (PROTOCOL §4.5, §20.1).
func (a *app) peerLink(peer string) error {
	var out map[string]any
	if err := a.call(http.MethodPost, "/v1/admin/dot-links", map[string]string{"peer_id": peer}, &out); err != nil {
		return err
	}
	return a.emitObject(out, "code", "expires_at")
}

// routeAddBody builds the POST /v1/admin/routes body. A grokbot key comes
// from --key-file only: argv is visible in ps and shell history.
func (a *app) routeAddBody(o opts) (map[string]string, error) {
	body := map[string]string{"owner": o.str("owner"), "url": o.str("url")}
	kind := o.str("kind")
	if kind != "" {
		body["kind"] = kind
	}
	if kind != grokbotKind {
		if o.has("key-file") {
			return nil, usageErr("route add: --key-file needs --kind " + grokbotKind)
		}
		return body, nil
	}
	if o.str("key-file") == "" {
		return nil, usageErr("route add: --kind " + grokbotKind + " needs --key-file PATH")
	}
	key, err := a.readKeyFile(o.str("key-file"))
	if err != nil {
		return nil, err
	}
	body["key"] = key
	return body, nil
}

// readKeyFile returns the crsr_ key in path. It stops locally on a file that
// is not one, so pointing it at the wrong file sends nothing to the hub, and
// no error quotes the contents. A file others can read warns but is accepted.
func (a *app) readKeyFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(raw))
	if len(raw) > maxKeyFile || !strings.HasPrefix(key, grokbotKeyPrefix) {
		return "", usageErr("route add: " + path + " does not hold a " + grokbotKeyPrefix + " key")
	}
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	// Windows reports no meaningful group or other bits.
	if mode := info.Mode().Perm(); mode&0o077 != 0 && runtime.GOOS != "windows" {
		a.warn("key file %s has mode %04o, readable by others; use chmod 600", path, mode)
	}
	return key, nil
}

func (a *app) cmdSource(args []string) error {
	o, err := parseFlags("source", args, map[string]flagKind{
		"kind": strFlag, "name": strFlag, "route": strFlag, "events": multiFlag, "priority": strFlag,
	})
	if err != nil {
		return err
	}
	const usage = "usage: vecnet source add --kind github --name N --route ID [--events A,B] [--priority P] | source list"
	sub := ""
	if len(o.pos) > 0 {
		sub = o.pos[0]
	}
	switch {
	case sub == "add" && len(o.pos) == 1 && o.str("kind") != "" && o.str("name") != "" && o.str("route") != "":
		body := map[string]any{"kind": o.str("kind"), "name": o.str("name"), "target": o.str("route")}
		if o.has("events") {
			// An absent allowlist admits every event, so an empty --events
			// (an unset shell variable, say) must not reach the hub as one.
			events := o.list("events")
			if len(events) == 0 {
				return usageErr("source add: --events names no event")
			}
			body["event_allow"] = events
		}
		if p := o.str("priority"); p != "" {
			body["priority"] = p
		}
		var out map[string]any
		if err := a.call(http.MethodPost, "/v1/admin/sources", body, &out); err != nil {
			return err
		}
		return a.emitObject(out, "source_id", "ingress_url", "secret")
	case sub == "list" && len(o.pos) == 1:
		items, err := a.getList("/v1/admin/sources", nil, "sources")
		if err != nil {
			return err
		}
		if a.json {
			return a.emitList(items)
		}
		for _, it := range items {
			var s struct {
				SourceID   string   `json:"source_id"`
				Kind       string   `json:"kind"`
				Name       string   `json:"name"`
				Target     string   `json:"target"`
				EventAllow []string `json:"event_allow"`
				Priority   string   `json:"priority"`
				Disabled   bool     `json:"disabled"`
			}
			if json.Unmarshal(it, &s) != nil {
				continue
			}
			allowed := "*"
			if len(s.EventAllow) > 0 {
				allowed = flatten(strings.Join(s.EventAllow, ","))
			}
			fmt.Fprintf(a.stdout, "%s %s %s target=%s events=%s priority=%s disabled=%t\n",
				s.SourceID, s.Kind, flatten(s.Name), s.Target, allowed, s.Priority, s.Disabled)
		}
		return nil
	}
	return usageErr(usage)
}

func (a *app) cmdSubscriptions(args []string) error {
	o, err := parseFlags("subscriptions", args, nil)
	if err != nil {
		return err
	}
	switch {
	case len(o.pos) == 1 && o.pos[0] == "list":
		var items []json.RawMessage
		if err := a.call(http.MethodGet, "/v1/admin/subscriptions", nil, &items); err != nil {
			return err
		}
		if a.json {
			return a.emitList(items)
		}
		for _, it := range items {
			var s struct {
				SubID        string  `json:"sub_id"`
				PeerID       string  `json:"peer_id"`
				Host         string  `json:"url"` // the hub sends the host alone
				Cursor       string  `json:"cursor"`
				ExpiresAt    *string `json:"expires_at"`
				PausedReason *string `json:"paused_reason"`
				LastRefresh  *string `json:"last_refresh_at"`
				LastStatus   *int    `json:"last_status"`
			}
			if json.Unmarshal(it, &s) != nil {
				continue
			}
			expires, paused, refreshed, status := "-", "-", "-", "-"
			if s.ExpiresAt != nil {
				expires = flatten(*s.ExpiresAt)
			}
			if s.LastRefresh != nil {
				refreshed = flatten(*s.LastRefresh)
			}
			if s.PausedReason != nil {
				paused = flatten(*s.PausedReason)
			}
			if s.LastStatus != nil {
				status = strconv.Itoa(*s.LastStatus)
			}
			fmt.Fprintf(a.stdout, "%s %s %s cursor=%s expires=%s paused=%s last_refresh=%s last_status=%s\n",
				s.SubID, s.PeerID, flatten(s.Host), s.Cursor, expires, paused, refreshed, status)
		}
		return nil
	case len(o.pos) == 2 && o.pos[0] == "pause":
		if err := a.call(http.MethodPost, "/v1/admin/subscriptions/"+pathEscape(o.pos[1])+"/pause", nil, nil); err != nil {
			return err
		}
		return a.ok("paused " + o.pos[1])
	case len(o.pos) == 2 && o.pos[0] == "resume":
		if err := a.call(http.MethodPost, "/v1/admin/subscriptions/"+pathEscape(o.pos[1])+"/resume", nil, nil); err != nil {
			return err
		}
		return a.ok("resumed " + o.pos[1])
	}
	return usageErr("usage: vecnet subscriptions list | subscriptions pause <id> | subscriptions resume <id>")
}
