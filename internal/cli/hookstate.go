package cli

import (
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
type hookState struct {
	Hub              string    `json:"hub,omitempty"`
	Caller           string    `json:"caller,omitempty"`
	As               string    `json:"as,omitempty"`
	Cred             string    `json:"cred,omitempty"`
	PollCursor       string    `json:"poll_cursor,omitempty"`
	Shown            []string  `json:"shown"`
	LastPostToolPoll time.Time `json:"last_post_tool_poll,omitzero"`
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

func (s *hookState) has(deliveryID string) bool { return slices.Contains(s.Shown, deliveryID) }

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
	if err := fn(&s); err != nil {
		return err
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
		s.PollCursor, s.Shown, s.Caller, s.Cred = "", nil, "", ""
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
	if s != nil && s.Hub == id.Hub && s.As == id.As && s.Cred == id.Cred && s.Caller != "" {
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
