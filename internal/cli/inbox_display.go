package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// displayInbox publishes proof only for a complete page printed by this
// command. Manual replay still prints previously displayed deliveries. Unlike
// hook bootstrap, ordinary inbox output must never adopt or repair state.
func (a *app) displayInbox(c *client, profile string, events []envelope.Event, output []byte) error {
	write := func() error {
		n, err := a.stdout.Write(output)
		if err == nil && n != len(output) {
			err = io.ErrShortWrite
		}
		return err
	}
	var ids []string
	for _, ev := range events {
		if ev.Type == "delivery" {
			if id := eventFieldsOf(ev).deliveryID; id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	path, err := statePath(a.getenv, profile, a.as)
	if err != nil || len(ids) == 0 {
		return write()
	}
	id := hookIdentity{Hub: normalizeHub(c.base), As: a.as, Cred: hookCredential(c.secret)}
	// Read both before authentication and under the lock. The first check
	// avoids creating shared state or querying identity for unrelated inboxes.
	matching := func() (*hookState, map[string]json.RawMessage) {
		raw, err := os.ReadFile(path)
		var stored *hookState
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &stored) != nil || stored == nil ||
			stored.Hub != id.Hub || stored.As != id.As || stored.Cred != id.Cred ||
			stored.Caller == "" || (id.Caller != "" && stored.Caller != id.Caller) ||
			json.Unmarshal(raw, &fields) != nil {
			return nil, nil
		}
		return stored, fields
	}
	if s, _ := matching(); s == nil {
		return write()
	}
	id.Caller = a.whoami(c) // A cached caller cannot authorize new proof.
	if id.Caller == "" {
		return write()
	}
	ctx, cancel := context.WithTimeout(a.ctx, 250*time.Millisecond)
	defer cancel()
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		return write() // State uncertainty must not make ordinary inbox unusable.
	}
	defer unlock()
	s, fields := matching()
	if s == nil {
		return write()
	}
	var eligible []string
	for _, delivery := range ids {
		if !slices.ContainsFunc(s.Claims, func(ref claimRef) bool { return ref.ID == delivery }) {
			eligible = append(eligible, delivery)
		}
	}
	if err := write(); err != nil {
		return err
	}
	if len(eligible) == 0 {
		return nil
	}
	for _, delivery := range eligible {
		s.recordDisplay(delivery)
	}
	s.Replay = slices.DeleteFunc(s.Replay, func(ref replayRef) bool { return slices.Contains(eligible, ref.ID) })
	// Change only proof fields. Preserve unknown/legacy fields verbatim as
	// JSON values and never open or rewrite the separate session binding.
	fields["shown"], _ = json.Marshal(s.Shown)
	fields["displayed"], _ = json.Marshal(s.Displayed)
	if len(s.Replay) == 0 {
		delete(fields, "replay")
	} else {
		fields["replay"], _ = json.Marshal(s.Replay)
	}
	raw, err := json.Marshal(fields)
	if err == nil {
		err = writeFileAtomic(path, raw)
	}
	if err != nil {
		// Stdout cannot be rolled back or retried. Do not acknowledge after a
		// failed proof publication, even though the page reached the consumer.
		return fmt.Errorf("vecnet: inbox display state commit failed: %w", err)
	}
	return nil
}
