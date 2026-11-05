package opclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

var (
	ErrUnauthorized = errors.New("operator credential unauthorized")
	ErrForbidden    = errors.New("operator credential forbidden")
	ErrDisabled     = errors.New("operator peer disabled")
)

// Batch is applied synchronously before the cursor can be acknowledged.
// Reset asks the consumer to refetch all views before polling resumes.
type Batch struct {
	Events []envelope.Event
	Cursor string
	Reset  bool
}

func terminal(err error) error {
	var e *Error
	if errors.As(err, &e) {
		switch e.Status {
		case 401:
			return ErrUnauthorized
		case 403:
			return ErrForbidden
		}
	}
	return nil
}
func (c *Client) pause(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Restart hints. A hub draining for a restart answers a parked long-poll with
// hub_restarting: a transport-level reconnect hint, not a failure.
const (
	// maxRestartWait caps the hint, so a hub cannot park the tail for long.
	maxRestartWait = time.Minute
	// restartGrace is how long after the hinted wait the hub may stay
	// unreachable while it restarts before the tail calls it offline.
	restartGrace = time.Minute
)

// restartWait is retry_after_ms ±20%, so clients do not return in lockstep.
func restartWait(ms int64) time.Duration {
	d := min(max(time.Duration(ms)*time.Millisecond, 0), maxRestartWait)
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// Tail starts from the owner's shared acknowledgement, never a hard-coded
// epoch. Catch-up pages are immediate; an empty page switches to a 25s poll.
// A failed reload or acknowledgement preserves the cursor for safe replay.
//
// A page carrying hub_restarting reports "restarting", waits the hinted time
// with the cursor kept, and leaves the failure backoff alone. Until the hint
// and restartGrace have passed, failures to reach the hub still report
// "restarting" and retry at the base backoff; the next page reports "live".
func (c *Client) Tail(ctx context.Context, handle func(Batch) error, status func(string)) error {
	report := func(s string) {
		if status != nil {
			status(s)
		}
	}
	apply := func(b Batch) error {
		if handle != nil {
			return handle(b)
		}
		return nil
	}
	report("connecting")
	cursor := ""
	wait := 0
	backoff := time.Second
	// grace is what remains of a restart's allowance for an unreachable hub.
	grace := time.Duration(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{"wait": {strconv.Itoa(wait)}}
		if cursor != "" {
			q.Set("after", cursor)
		}
		resp, err := c.request(ctx, "GET", queryPath("/v1/events", q), nil)
		var page struct {
			Events     []envelope.Event `json:"events"`
			Cursor     string           `json:"next_cursor"`
			Restarting *struct {
				RetryAfterMS int64 `json:"retry_after_ms"`
			} `json:"hub_restarting"`
		}
		if err == nil {
			if resp.StatusCode == 204 {
				resp.Body.Close()
				return ErrDisabled
			}
			err = json.NewDecoder(resp.Body).Decode(&page)
			resp.Body.Close()
			if err == nil && page.Cursor == "" {
				err = fmt.Errorf("event page is missing next_cursor")
			}
			if err == nil && page.Restarting != nil && len(page.Events) == 0 {
				// The hub answered with no events and an unchanged cursor:
				// nothing to apply or acknowledge.
				cursor = page.Cursor
				wait = 0
				report("restarting")
				d := restartWait(page.Restarting.RetryAfterMS)
				grace = d + restartGrace
				if err = c.pause(ctx, d); err != nil {
					return err
				}
				continue
			}
			if err == nil {
				err = apply(Batch{Events: page.Events, Cursor: page.Cursor})
			}
			if err == nil && len(page.Events) > 0 {
				err = c.Ack(ctx, page.Cursor)
			}
			if err == nil {
				cursor = page.Cursor
				wait = 25
				if len(page.Events) > 0 {
					wait = 0
				}
				backoff = time.Second
				grace = 0
				report("live")
				continue
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stop := terminal(err); stop != nil {
			return stop
		}
		var apiErr *Error
		if errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Code == "cursor_purged" {
			if oldest, ok := apiErr.Details["oldest_cursor"].(string); ok && oldest != "" {
				if err = apply(Batch{Cursor: oldest, Reset: true}); err == nil {
					cursor = oldest
					wait = 0
					continue
				}
				if stop := terminal(err); stop != nil {
					return stop
				}
			}
		}
		if grace > 0 {
			// Still inside a restart: the hub is expected back, so this is
			// neither offline nor a reason to back off further.
			report("restarting")
			d := time.Second
			grace -= d
			if err = c.pause(ctx, d); err != nil {
				return err
			}
			continue
		}
		report("offline")
		if err = c.pause(ctx, backoff*4/5+rand.N(backoff*2/5+1)); err != nil {
			return err
		}
		backoff *= 2
		if backoff > 20*time.Second {
			backoff = 20 * time.Second
		}
	}
}
