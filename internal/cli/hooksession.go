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
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/platform"
)

// Session binding (PROTOCOL §13.9, issue 38). Claude Code sessions in one
// project share its settings.local.json, so every session runs every
// installed profile's hook. A profile's hook state is therefore bound to
// one session: its session_id (hook stdin) and, from Claude Code v2.1.214,
// its process (CLAUDE_PID). Runs from other sessions claim nothing.

const (
	// hookRebindWindow is how long `hook rebind` without a session id
	// leaves the binding open for the next hook run to take.
	hookRebindWindow = 10 * time.Minute
	// hookNoticedMax caps the foreign sessions remembered as notified.
	hookNoticedMax = 32
	// hookSessionsMax caps the sessions in the shared session registry.
	hookSessionsMax = 64
)

// How a binding was made. Only an automatic one is released when its
// session turns out to run several profiles' hooks.
const (
	bindAuto     = "auto"     // the session's first hook run
	bindDeclared = "declared" // VECNET_PROFILE named this profile
	bindRebind   = "rebind"   // `vecnet hook rebind`
)

var hookSessionID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// hookSession normalises a Claude Code session_id for storage: a plain id
// is kept, anything else becomes a fingerprint, "" stays "" (no session).
func hookSession(raw string) string {
	if raw == "" || hookSessionID.MatchString(raw) {
		return raw
	}
	sum := sha256.Sum256([]byte("vecnet-hook-session\x00" + raw))
	return fmt.Sprintf("x-%x", sum[:8])
}

// hookProc identifies a Claude Code process: CLAUDE_PID, its start time
// where the OS reports one (so a reused pid does not match), and the host.
// A session's id changes on /clear while its process stays the same.
type hookProc struct {
	PID   int    `json:"pid,omitempty"`
	Start string `json:"start,omitempty"`
	Host  string `json:"host,omitempty"`
}

// currentProc is the Claude Code process that ran this command, or the
// zero value when CLAUDE_PID is absent (older Claude Code, a terminal).
// The agy equivalent, agyHookProc, is platform-split: it reads /proc.
func currentProc(getenv func(string) string) hookProc {
	pid, err := strconv.Atoi(strings.TrimSpace(getenv("CLAUDE_PID")))
	if err != nil || pid <= 1 {
		return hookProc{}
	}
	host, _ := os.Hostname()
	return hookProc{PID: pid, Start: procStart(pid), Host: host}
}

// same reports whether p and q are known to be one process.
func (p hookProc) same(q hookProc) bool {
	return p.PID != 0 && p == q
}

// gone reports whether p is known to have exited: same host, and the pid
// is dead or now belongs to a later process. Unknown means not gone.
func (p hookProc) gone() bool {
	if p.PID == 0 {
		return false
	}
	if host, err := os.Hostname(); err != nil || host != p.Host {
		return false
	}
	alive, known := procAlive(p.PID)
	if known && !alive {
		return true
	}
	start := procStart(p.PID)
	return known && p.Start != "" && start != "" && start != p.Start
}

// hookBinding is a profile's session binding, kept in bindingPath beside
// its state file. Session is the owning session_id, SessionAt when it was
// bound, SessionHow how (bindAuto, bindDeclared or bindRebind), Proc its
// Claude Code process, RebindUntil the end of a pending `hook rebind`
// window, and Noticed the sessions already told the profile is not theirs.
type hookBinding struct {
	Session     string    `json:"session,omitempty"`
	SessionAt   time.Time `json:"session_at,omitzero"`
	SessionHow  string    `json:"session_how,omitempty"`
	Proc        hookProc  `json:"claude_proc,omitzero"`
	RebindUntil time.Time `json:"rebind_until,omitzero"`
	Noticed     []string  `json:"noticed,omitempty"`
}

// hookOwner is the session a hook run speaks for.
type hookOwner struct {
	session  string   // normalised session_id; "" when Claude Code sent none
	proc     hookProc // its Claude Code process, if known
	declared bool     // VECNET_PROFILE names this profile
	prompt   bool     // a UserPromptSubmit run: only the top-level session fires it
	profiles []string // every profile this session's hooks run (registry)
	agy      bool     // an agy hook run: notices delegate rebind to the user, and only a prompt may take a rebind window
}

func (o hookOwner) ambiguous() bool { return !o.declared && len(o.profiles) > 1 }

type hookAdmit int

const (
	admitNoSession hookAdmit = iota // no session_id: today's behaviour
	admitOwn                        // already bound to this session
	admitBound                      // newly bound to this session
	admitCleared                    // same Claude Code process, new session id (/clear)
	admitRebound                    // took a pending `hook rebind`
	admitAmbiguous                  // unbound, and this session runs several profiles
	admitForeign                    // bound to another live session
	admitUnbound                    // unbound, and not a user-prompt run: never binds
)

// admit decides whether o may claim deliveries for s, binding or
// releasing s as needed. The caller holds the state lock, so of two first
// runs exactly one binds.
//
// Invariants: only a user-prompt run binds (subagents and workflow agents
// fire tool hooks only, sometimes with their own session id), apart from
// taking an explicit rebind window — and an agy run takes even that only
// with a prompt, since agy subagents share the workspace hooks and must
// never claim an open window. Another session never unbinds or takes a
// bound profile unless its Claude Code process has provably exited. A
// session releases only an automatic binding of its own, for ambiguity.
func (s *hookState) admit(o hookOwner, now time.Time) hookAdmit {
	switch {
	case o.session == "":
		return admitNoSession
	case s.Session == o.session:
		if s.SessionHow == bindAuto && o.ambiguous() {
			s.release()
			return admitAmbiguous
		}
		if o.proc.PID != 0 && s.Proc != o.proc {
			s.Proc = o.proc // --resume keeps the id in a new process
		}
		return admitOwn
	case o.prompt && s.Session != "" && s.Proc.same(o.proc):
		how := s.SessionHow
		if how == bindAuto && o.ambiguous() {
			s.release()
			return admitAmbiguous
		}
		s.bindSession(o, how, now)
		return admitCleared
	case (!o.agy || o.prompt) && now.Before(s.RebindUntil):
		s.bindSession(o, bindRebind, now)
		return admitRebound
	case s.Session == "" || s.Proc.gone():
		if !o.prompt {
			return admitUnbound
		}
		if o.ambiguous() {
			return admitAmbiguous
		}
		how := bindAuto
		if o.declared {
			how = bindDeclared
		}
		s.bindSession(o, how, now)
		return admitBound
	}
	return admitForeign
}

// bindSession gives the profile to o and closes any rebind window.
func (s *hookState) bindSession(o hookOwner, how string, now time.Time) {
	s.Session, s.SessionAt, s.SessionHow, s.Proc, s.RebindUntil = o.session, now.UTC(), how, o.proc, time.Time{}
	s.Noticed = slices.DeleteFunc(s.Noticed, func(id string) bool { return id == o.session })
}

// release unbinds s; the next unambiguous session binds it.
func (s *hookState) release() {
	s.Session, s.SessionAt, s.SessionHow, s.Proc = "", time.Time{}, "", hookProc{}
}

// notice records that session was told it does not own the profile; it
// reports false when it already was.
func (s *hookState) notice(session string) bool {
	if slices.Contains(s.Noticed, session) {
		return false
	}
	s.Noticed = append(s.Noticed, session)
	if len(s.Noticed) > hookNoticedMax {
		s.Noticed = s.Noticed[len(s.Noticed)-hookNoticedMax:]
	}
	return true
}

// hookSessions is the shared registry, beside the state files, of which
// profiles' hooks each recent session has run. A session that runs more
// than one is ambiguous: it binds none of them automatically.
type hookSessions struct {
	Sessions []hookSeen `json:"sessions"`
}
type hookSeen struct {
	ID       string    `json:"id"`
	Profiles []string  `json:"profiles"`
	Seen     time.Time `json:"seen"`
}

// sessionsPath is the registry file. '+' never appears in a state file's
// name (statePath), so no profile can collide with it.
func sessionsPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "+sessions.json")
}

// registerSession records that session ran the hook for key (a state file
// basename) and returns every key it has run, sorted. It writes only when
// something changed or the entry is an hour stale.
func registerSession(ctx context.Context, path, session, key string, now time.Time) ([]string, error) {
	if err := platform.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	var r hookSessions
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if json.Unmarshal(raw, &r) != nil {
		r = hookSessions{}
	}
	i := slices.IndexFunc(r.Sessions, func(e hookSeen) bool { return e.ID == session })
	if i < 0 {
		r.Sessions = append(r.Sessions, hookSeen{ID: session})
		i = len(r.Sessions) - 1
	}
	e := &r.Sessions[i]
	changed := false
	if !slices.Contains(e.Profiles, key) {
		e.Profiles = append(e.Profiles, key)
		slices.Sort(e.Profiles)
		changed = true
	}
	if now.Sub(e.Seen) > time.Hour {
		e.Seen, changed = now.UTC(), true
	}
	profiles := slices.Clone(e.Profiles)
	if !changed {
		return profiles, nil
	}
	if len(r.Sessions) > hookSessionsMax {
		slices.SortStableFunc(r.Sessions, func(a, b hookSeen) int { return b.Seen.Compare(a.Seen) })
		r.Sessions = r.Sessions[:hookSessionsMax]
	}
	raw, err = json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return profiles, writeFileAtomic(path, raw)
}

// stateKey is a state file's profile key: its basename without ".json".
func stateKey(statePath string) string {
	return strings.TrimSuffix(filepath.Base(statePath), ".json")
}
