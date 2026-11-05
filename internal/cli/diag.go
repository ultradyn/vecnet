package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (a *app) cmdDiag(args []string) error {
	if len(args) == 0 || args[0] != "mcp" {
		return usageErr("usage: vecnet diag mcp [--peer ID] [--method M] [--limit N] [--follow]")
	}
	o, err := parseFlags("diag mcp", args[1:], map[string]flagKind{"peer": strFlag, "method": strFlag, "limit": strFlag, "follow": boolFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("diag mcp takes no positional arguments")
	}
	q := url.Values{}
	for _, k := range []string{"peer", "method", "limit"} {
		if s := o.str(k); s != "" {
			q.Set(k, s)
		}
	}
	pageLimit := 100
	if s := o.str("limit"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 1000 {
			return usageErr("--limit must be between 1 and 1000")
		}
		pageLimit = n
	}
	c, _, _, err := a.connect()
	if err != nil {
		return err
	}
	var after uint64
	continuing := false
	for {
		var page struct {
			Records []struct {
				Sequence uint64         `json:"sequence"`
				Time     string         `json:"time"`
				Kind     string         `json:"kind"`
				Fields   map[string]any `json:"fields"`
			} `json:"records"`
		}
		if continuing {
			q.Set("after", strconv.FormatUint(after, 10))
		}
		if err := c.do(a.ctx, http.MethodGet, "/v1/diag/mcp", q, nil, &page); err != nil {
			if a.ctx.Err() != nil && o.has("follow") {
				return nil
			}
			return err
		}
		continuing = true
		for _, rec := range page.Records {
			if rec.Sequence > after {
				after = rec.Sequence
			}
			if a.json {
				if err := json.NewEncoder(a.stdout).Encode(rec); err != nil {
					return err
				}
			} else {
				fields, _ := json.Marshal(rec.Fields)
				if _, err := fmt.Fprintf(a.stdout, "%s %s %s\n", rec.Time, rec.Kind, fields); err != nil {
					return err
				}
			}
		}
		if !o.has("follow") {
			return nil
		}
		// Drain full pages immediately before returning to the polling interval.
		if len(page.Records) == pageLimit {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
