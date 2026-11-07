package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const shownWindow = 1024

// hookState is shared by `vecnet hook` and `watch --for-monitor`. Hub,
// Caller, As and Cred bind it to one identity (PROTOCOL §13.9): Hub is the
// normalized hub URL, Caller the authenticated peer id (the principal id for
// a principal credential) from GET /v1/peers/me, As the --as peer, and Cred
// a fingerprint of the credential that Caller was verified for. A state
// without Hub predates binding.
//
// hookBinding (hooksession.go) binds the profile's hook to one Claude Code
// session (PROTOCOL §13.9, issue 38). It is stored in its own file beside
// the state, never in it: a vecnet from before binding rewrites the state
// file with only the fields it knows, which would silently unbind it.
type hookState struct {
	Hub        string      `json:"hub,omitempty"`
	Caller     string      `json:"caller,omitempty"`
	As         string      `json:"as,omitempty"`
	Cred       string      `json:"cred,omitempty"`
	AppCursor  string      `json:"app_cursor,omitempty"`
	Replay     []replayRef `json:"replay,omitempty"`
	Claims     []claimRef  `json:"claims,omitempty"`
	PollCursor string      `json:"poll_cursor,omitempty"`
	// AckAfter retains the predecessor of the first undisplayed reservation.
	// Hooks reread from it even after displaying later events, so no future
	// invocation can cumulatively acknowledge past the reservation.
	AckAfter string   `json:"ack_after,omitempty"`
	Shown    []string `json:"shown"`
	// Displayed contains successful hook/monitor output and trusted receipt
	// proof. Shown also contains app-server reservations and legacy watch
	// claims made before stdout was written; neither proves display.
	Displayed        []string  `json:"displayed,omitempty"`
	LastPostToolPoll time.Time `json:"last_post_tool_poll,omitzero"`
	hookBinding      `json:"-"`
}

// bindingPath is the binding file beside a state file: <key>+session.json.
// '+' never appears in a state key (statePath), so it cannot collide.
func bindingPath(statePath string) string {
	return strings.TrimSuffix(statePath, ".json") + "+session.json"
}

// normalizeHub canonicalises a hub URL for identity comparison: lower-case
// scheme and host, no default port, no trailing slash.
func normalizeHub(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if (scheme == "https" && strings.HasSuffix(host, ":443")) || (scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndexByte(host, ':')]
	}
	return scheme + "://" + host + strings.TrimRight(u.Path, "/")
}

// hookCredential fingerprints a secret: a changed fingerprint means the
// caller must be verified again. The secret itself is never stored.
func hookCredential(secret string) string {
	sum := sha256.Sum256([]byte("vecnet-hook-cred\x00" + secret))
	return fmt.Sprintf("%x", sum[:8])
}

// Outstanding claims and positive proof can outlive their Shown entries.
// Both must keep fencing duplicate injection while their references remain.
func (s *hookState) has(deliveryID string) bool {
	return slices.Contains(s.Shown, deliveryID) || slices.Contains(s.Displayed, deliveryID) ||
		slices.ContainsFunc(s.Claims, func(r claimRef) bool { return r.ID == deliveryID })
}

// claim retains delivery ids in arrival order; cursors are never compared.
func (s *hookState) claim(deliveryID string) bool {
	if s.has(deliveryID) {
		return false
	}
	s.Shown = append(s.Shown, deliveryID)
	if len(s.Shown) > shownWindow {
		s.Shown = s.Shown[len(s.Shown)-shownWindow:]
	}
	return true
}

func (s *hookState) recordDisplay(deliveryID string) {
	// Successful output starts a fresh dedupe window, even if this delivery
	// was reserved long ago. Keep reservation age from evicting a fresh display.
	s.Shown = slices.DeleteFunc(s.Shown, func(id string) bool { return id == deliveryID })
	s.Shown = append(s.Shown, deliveryID)
	if len(s.Shown) > shownWindow {
		s.Shown = s.Shown[len(s.Shown)-shownWindow:]
	}
	if !slices.Contains(s.Displayed, deliveryID) {
		s.Displayed = append(s.Displayed, deliveryID)
		if len(s.Displayed) > shownWindow {
			s.Displayed = s.Displayed[len(s.Displayed)-shownWindow:]
		}
	}
}

// errDisplayDeferred means neither output nor acknowledgement is authorized.
// Ownership and positive proof must be checked again under the shared lock.
var errDisplayDeferred = errors.New("monitor display deferred")

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
	return f.update(ctx, func(s *hookState) error {
		if err := f.displayState(s, id, fresh); err == nil {
			return nil
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
	})
}

// displayFor serializes the proof/ownership check, complete stdout write and
// publication with all hook/receipt/release writers. Reservations never prove
// display and an unresolved claim cannot authorize a competing print.
func (f stateFile) displayFor(ctx context.Context, id *hookIdentity, deliveryID string, replay bool, display func() error) error {
	attempted := false
	var outputErr error
	err := f.update(ctx, func(s *hookState) error {
		if err := f.displayState(s, id, false); err != nil {
			return err
		}
		if !replay && slices.Contains(s.Displayed, deliveryID) {
			return nil
		}
		if slices.ContainsFunc(s.Claims, func(r claimRef) bool { return r.ID == deliveryID }) {
			return errDisplayDeferred
		}
		attempted = true
		outputErr = display()
		if outputErr != nil {
			return outputErr
		}
		s.recordDisplay(deliveryID)
		s.Replay = slices.DeleteFunc(s.Replay, func(r replayRef) bool { return r.ID == deliveryID })
		return nil
	})
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

type stateFile struct{ path string }

var stateComponent = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func statePath(getenv func(string) string, profile, as string) (string, error) {
	config, err := configPath(getenv)
	if err != nil {
		return "", err
	}
	component := func(s string) string {
		if stateComponent.MatchString(s) {
			return s
		}
		sum := sha256.Sum256([]byte(s))
		return fmt.Sprintf("x-%x", sum[:8])
	}
	base := component(profile)
	if as != "" {
		base += "@" + component(as)
	}
	return filepath.Join(filepath.Dir(config), "state", base+".json"), nil
}

var errStateBusy = errors.New("vecnet: state file busy")

// update serializes the complete read/modify/atomic-write transaction with
// other processes as well as other stateFile values in this process.
func (f stateFile) update(ctx context.Context, fn func(*hookState) error) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	unlock, err := lockPath(ctx, f.path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	var s hookState
	raw, err := os.ReadFile(f.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if json.Unmarshal(raw, &s) != nil {
		s = hookState{}
	}
	// The binding file wins; without one, adopt a binding that 8631cc0
	// kept inside the state file. A corrupt binding reads as unbound.
	bpath := bindingPath(f.path)
	braw, err := os.ReadFile(bpath)
	switch {
	case err == nil:
		if json.Unmarshal(braw, &s.hookBinding) != nil {
			s.hookBinding = hookBinding{}
		}
	case errors.Is(err, os.ErrNotExist):
		if json.Unmarshal(raw, &s.hookBinding) != nil {
			s.hookBinding = hookBinding{}
		}
	default:
		return err
	}
	if err := fn(&s); err != nil {
		return err
	}
	nb, err := json.Marshal(s.hookBinding)
	if err != nil {
		return err
	}
	if !bytes.Equal(nb, braw) {
		if err := writeFileAtomic(bpath, nb); err != nil {
			return err
		}
	}
	raw, err = json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(f.path, raw)
}

// claimOne fails open: inability to persist a claim must not lose a delivery.
func (f stateFile) claimOne(ctx context.Context, deliveryID string) bool {
	return f.claimFor(ctx, nil, deliveryID)
}

// claimFor binds the state to id (when non-nil) before claiming, so a
// shown-set left by another identity cannot suppress this one's deliveries.
func (f stateFile) claimFor(ctx context.Context, id *hookIdentity, deliveryID string) bool {
	claimed := false
	err := f.update(ctx, func(s *hookState) error {
		if id != nil {
			s.bind(*id)
		}
		claimed = s.claim(deliveryID)
		return nil
	})
	return err != nil || claimed
}

// hookIdentity is who a hook or watch run reads the stream as.
type hookIdentity struct{ Hub, Caller, As, Cred string }

// bind ties s to id. A different hub or --as, or a different verified
// caller, discards the poll cursor and shown ids, so the hook takes its
// first-run path (summary, then the log head); it reports that reset. A
// state from before binding adopts id once. An unverified caller (id.Caller
// "") neither resets nor records the credential, so the next run asks again.
func (s *hookState) bind(id hookIdentity) bool {
	reset := s.Hub != "" && (s.Hub != id.Hub || s.As != id.As || (s.Caller != "" && id.Caller != "" && s.Caller != id.Caller))
	if reset {
		s.PollCursor, s.AckAfter, s.Shown, s.Caller, s.Cred = "", "", nil, "", ""
		s.Displayed = nil
		s.AppCursor, s.Replay, s.Claims = "", nil, nil
	}
	s.Hub, s.As = id.Hub, id.As
	if id.Caller != "" {
		s.Caller, s.Cred = id.Caller, id.Cred
	}
	return reset
}

// resolveIdentity returns the identity c reads the stream as. An unchanged
// hub, --as and credential still authenticate the recorded caller, so the
// hub is asked (GET /v1/peers/me) only when one of them changed, or when s
// is nil or has no verified caller.
func (a *app) resolveIdentity(c *client, s *hookState) hookIdentity {
	id := hookIdentity{Hub: normalizeHub(c.base), As: a.as, Cred: hookCredential(c.secret)}
	// A known operator role authorizes monitor output, not app-server or
	// hook peer/principal identity. Revalidate instead of promoting its cache.
	if s != nil && s.Hub == id.Hub && s.As == id.As && s.Cred == id.Cred && s.Caller != "" && s.Caller != monitorOperatorCaller {
		id.Caller = s.Caller
		return id
	}
	id.Caller = a.whoami(c)
	return id
}

// whoami is the authenticated subject: the peer id, or the principal id for
// a principal credential; "" when the hub cannot say (PROTOCOL §2.10).
func (a *app) whoami(c *client) string {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	var me struct {
		Kind        string `json:"kind"`
		PeerID      string `json:"peer_id"`
		PrincipalID string `json:"principal_id"`
	}
	if c.do(ctx, http.MethodGet, "/v1/peers/me", nil, nil, &me) != nil {
		return ""
	}
	subject := me.PrincipalID
	if me.Kind == "peer" {
		subject = me.PeerID
	} else if me.Kind != "principal" {
		return ""
	}
	if !hookID.MatchString(subject) {
		return ""
	}
	return subject
}
