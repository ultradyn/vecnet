package opclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Tail starts from the owner's shared acknowledgement, never a hard-coded
// epoch. Catch-up pages are immediate; an empty page switches to a 25s poll.
// A failed reload or acknowledgement preserves the cursor for safe replay.
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
			Events []envelope.Event `json:"events"`
			Cursor string           `json:"next_cursor"`
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
		report("offline")
		if err = c.pause(ctx, backoff); err != nil {
			return err
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}
