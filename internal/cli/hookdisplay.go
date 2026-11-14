package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
)

// errDisplayDeferred means neither output nor acknowledgement is authorized.
// Ownership and positive proof must be checked again under the shared lock.
var errDisplayDeferred = errors.New("monitor display deferred")
var hookCredentialPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// This monitor-only subject is outside hookID's peer/principal grammar.
const monitorOperatorCaller = "role:operator"

// displayState runs inside update's lock. update deliberately recovers broken
// state for ordinary hooks; a monitor must instead preserve that uncertainty.
func (f stateFile) displayState(s *hookState, id *hookIdentity, fresh bool) error {
	if id == nil || id.Hub == "" || id.Caller == "" {
		return errDisplayDeferred
	}
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) && fresh {
		s.bind(*id)
		return nil
	}
	var stored *hookState
	if err != nil || json.Unmarshal(raw, &stored) != nil || stored == nil {
		return errDisplayDeferred
	}
	if s.Hub != "" && (s.Hub != id.Hub || s.As != id.As ||
		(s.Caller != "" && s.Caller != id.Caller) || (s.Cred != "" && s.Cred != id.Cred)) {
		return errDisplayDeferred
	}
	s.bind(*id) // a valid legacy unbound state may adopt this verified identity
	return nil
}

// prepareDisplay can finish the initial bootstrap after a busy lock. Only
// initial absence permits creation; loss of already established state defers.
func (f stateFile) prepareDisplay(ctx context.Context, id *hookIdentity, fresh bool, movedFrom *hookIdentity) error {
	return f.updateMode(ctx, func(s *hookState) error {
		if err := f.displayState(s, id, fresh); err == nil {
			return nil
		}
		// Preparation follows positive /peers/me authentication. A rotated
		// bearer for the exact same bound identity may refresh provenance,
		// never reset delivery ownership/proof, cursors or session fields.
		if id != nil && id.Caller != "" && s.Caller == id.Caller && s.Hub == id.Hub && s.As == id.As &&
			hookCredentialPattern.MatchString(s.Cred) {
			prior := *id
			prior.Cred = s.Cred
			if err := f.displayState(s, &prior, false); err == nil {
				s.Cred = id.Cred
				return nil
			}
		}
		// The existing follow path has verified the destination before this
		// preparation. Carry only its exact previous authenticated scope, keeping
		// all proof, cursors and ownership rather than resetting foreign state.
		if id == nil || movedFrom == nil || id.Caller == "" ||
			movedFrom.Caller != id.Caller || movedFrom.As != id.As || movedFrom.Cred != id.Cred {
			return errDisplayDeferred
		}
		if err := f.displayState(s, movedFrom, false); err != nil {
			return err
		}
		s.Hub = id.Hub
		return nil
	}, true, fresh)
}

// displayFor serializes the proof/ownership check, complete stdout write and
// publication with all hook/receipt/release writers. Reservations never prove
// display and an unresolved claim cannot authorize a competing print.
func (f stateFile) displayFor(ctx context.Context, id *hookIdentity, deliveryID string, replay bool, display func() error) error {
	attempted := false
	var outputErr error
	err := f.updateMode(ctx, func(s *hookState) error {
		if err := f.displayState(s, id, false); err != nil {
			return err
		}
		if slices.ContainsFunc(s.Claims, func(r claimRef) bool { return r.ID == deliveryID }) {
			return errDisplayDeferred
		}
		if !replay && slices.Contains(s.Displayed, deliveryID) {
			return nil
		}
		attempted = true
		outputErr = display()
		if outputErr != nil {
			return outputErr
		}
		s.recordDisplay(deliveryID)
		s.Replay = slices.DeleteFunc(s.Replay, func(r replayRef) bool { return r.ID == deliveryID })
		return nil
	}, true, false)
	if outputErr != nil {
		return outputErr
	}
	if err != nil {
		if attempted {
			// Stdout cannot be rolled back. Stop before ack or a retry that
			// could print again after an unsuccessful proof publication.
			return fmt.Errorf("%w: display state commit failed", errWatchOutput)
		}
		return errDisplayDeferred
	}
	return nil
}
