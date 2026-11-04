package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

const shownWindow = 1024

type hookState struct {
	PollCursor       string    `json:"poll_cursor,omitempty"`
	Shown            []string  `json:"shown"`
	LastPostToolPoll time.Time `json:"last_post_tool_poll,omitzero"`
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
	claimed := false
	err := f.update(ctx, func(s *hookState) error { claimed = s.claim(deliveryID); return nil })
	return err != nil || claimed
}
