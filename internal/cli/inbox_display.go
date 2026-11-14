package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
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
		if err != nil || validateCodexStateJSON(raw) != nil || json.Unmarshal(raw, &stored) != nil || stored == nil ||
			stored.Hub != id.Hub || stored.As != id.As || stored.Cred != id.Cred ||
			stored.Caller == "" || (id.Caller != "" && stored.Caller != id.Caller) ||
			json.Unmarshal(raw, &fields) != nil {
			return nil, nil
		}
		// Match strict state readers without adopting, repairing, or rewriting
		// a binding. A malformed sidecar/embedded binding leaves proof uncertain.
		bindingRaw, bindingErr := os.ReadFile(bindingPath(path))
		if errors.Is(bindingErr, os.ErrNotExist) {
			bindingRaw, bindingErr = raw, nil
		}
		var binding hookBinding
		if bindingErr != nil || validateCodexStateJSON(bindingRaw) != nil || json.Unmarshal(bindingRaw, &binding) != nil {
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
	// Strict validation rejects aliases occurring together, but accepts a
	// single case variant. Update that exact key rather than creating an alias.
	fieldKey := func(want string) string {
		for key := range fields {
			if strings.EqualFold(key, want) {
				return key
			}
		}
		return want
	}
	replayKey, displayedKey := fieldKey("replay"), fieldKey("displayed")
	var eligible []string
	for _, delivery := range ids {
		if !slices.ContainsFunc(s.Claims, func(ref claimRef) bool { return ref.ID == delivery }) {
			eligible = append(eligible, delivery)
		}
	}
	// Filter original elements rather than rebuilding unread references from
	// replayRef, which cannot represent metadata added by a newer client.
	var kept []json.RawMessage
	retired := false
	if len(eligible) > 0 && len(fields[replayKey]) > 0 {
		var replay []json.RawMessage
		if json.Unmarshal(fields[replayKey], &replay) != nil {
			return write()
		}
		for _, raw := range replay {
			var ref struct{ ID string }
			if json.Unmarshal(raw, &ref) != nil {
				return write()
			}
			if slices.Contains(eligible, ref.ID) {
				retired = true
			} else {
				kept = append(kept, raw)
			}
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
	// Change only positive proof and exactly retired replay references. Preserve
	// unknown fields as JSON values; never rewrite the separate session binding.
	fields[displayedKey], _ = json.Marshal(s.Displayed)
	if retired {
		if len(kept) == 0 {
			delete(fields, replayKey)
		} else {
			fields[replayKey], _ = json.Marshal(kept)
		}
	}
	raw, err := json.Marshal(fields)
	if err == nil {
		err = writeCodexDurable(path, raw)
	}
	if err != nil {
		// Stdout cannot be rolled back or retried. Do not acknowledge after a
		// failed proof publication, even though the page reached the consumer.
		return fmt.Errorf("vecnet: inbox display state commit failed: %w", err)
	}
	return nil
}
