package theme

import (
	"hash/fnv"
	"strings"
)

// The state maps of design §5.5: each state has one role on every screen.
// Every input is a hub enum, an id or a number, never peer-written text (§6.8).

// Priority is a message priority: wake → warn, wake_idle → info, hold → hold.
func Priority(p string) Role {
	switch p {
	case "wake":
		return Warn
	case "wake_idle":
		return Info
	case "hold":
		return Hold
	}
	return Muted
}

// Receipt is a receipt state: queued → muted, pushed → info, accepted →
// indigo, processed → success, read → text, failed and dead_letter → error,
// expired and anything unknown → muted.
func Receipt(state string) Role {
	switch state {
	case "pushed":
		return Info
	case "accepted":
		return Indigo
	case "processed":
		return Success
	case "read":
		return Text
	case "failed", "dead_letter":
		return Error
	}
	return Muted
}

// Conn is the header's connection state: live → success; connecting,
// reconnecting and restarting (the client retries) → warn; anything else
// (rejected, disabled, off) → error.
func Conn(state string) Role {
	switch state {
	case "live":
		return Success
	case "connecting", "reconnecting", "restarting":
		return Warn
	}
	return Error
}

// Presence is self-reported: working → text, idle → muted, blocked → warn,
// offline and unknown → dim.
func Presence(state string) Role {
	switch state {
	case "working":
		return Text
	case "idle":
		return Muted
	case "blocked":
		return Warn
	}
	return Dim
}

// Reach is hub-observed: connected → success, reachable → info, unreachable
// → error, unknown → dim.
func Reach(state string) Role {
	switch state {
	case "connected":
		return Success
	case "reachable":
		return Info
	case "unreachable":
		return Error
	}
	return Dim
}

// Health is a subscription's health. A value this client does not know is
// muted: it must not read as healthy or as broken.
func Health(h string) Role {
	switch h {
	case "ok":
		return Success
	case "late", "unverified", "paused_operator":
		return Warn
	case "failing", "expired", "paused_token", "paused_failed", "paused_auth":
		return Error
	}
	return Muted
}

// EventType is an operator event's type: receipt → success, approval_* →
// accent, failure types → error, everything else (presence, thread, group and
// directory updates included) → info.
func EventType(t string) Role {
	switch {
	case t == "receipt":
		return Success
	case strings.HasPrefix(t, "approval_"):
		return Accent
	case t == "dead_letter", strings.HasSuffix(t, "_failed"):
		return Error
	}
	return Info
}

// Quota is a remaining-quota level: lowAt or less → error, warnAt or less →
// warn, otherwise success (§5.5: "25 % or less → warn; 10 % or less → error").
func Quota(pct, warnAt, lowAt float64) Role {
	switch {
	case pct <= lowAt:
		return Error
	case pct <= warnAt:
		return Warn
	}
	return Success
}

// Count is a header count: hot when n > 0, muted at 0.
func Count(n int, hot Role) Role {
	if n > 0 {
		return hot
	}
	return Muted
}

// HTTPStatus is error from 400 up, text below.
func HTTPStatus(code int) Role {
	if code >= 400 {
		return Error
	}
	return Text
}

// Budget is a thread budget cell: a cut → error, 0 left → warn, else muted.
func Budget(remaining int, cut bool) Role {
	switch {
	case cut:
		return Error
	case remaining <= 0:
		return Warn
	}
	return Muted
}

// InboxTag is an inbox row's tag. SUB rows take their health's role instead.
func InboxTag(tag string) Role {
	switch tag {
	case "APR":
		return Accent
	case "WAKE":
		return Warn
	case "HOLD":
		return Hold
	case "DLQ":
		return Error
	}
	return Text
}

// PrefSource is where a preference's value came from: default → muted,
// stored → text, flag and env → info.
func PrefSource(src string) Role {
	switch src {
	case "default":
		return Muted
	case "flag", "env":
		return Info
	}
	return Text
}

// PrefState is a preference's runtime state: live → success, unavailable and
// lock → warn.
func PrefState(state string) Role {
	switch state {
	case "live":
		return Success
	case "unavailable", "lock", "locked":
		return Warn
	}
	return Muted
}

// Notice is a hub notice's head and detail roles: moving → accent, restart →
// warn, others → a bold text head with muted detail. The caller bolds the head.
func Notice(kind string) (head, detail Role) {
	switch kind {
	case "moving":
		return Accent, Accent
	case "restart":
		return Warn, Warn
	}
	return Bold, Muted
}

var idents = [...]Role{Ident1, Ident2, Ident3, Ident4, Ident5}

// Sender is a chat sender's identity hue: fnv32a of the hub-assigned peer id,
// never the display name, mod 5. Sender names are drawn bold.
func Sender(peerID string) Role {
	h := fnv.New32a()
	_, _ = h.Write([]byte(peerID))
	return idents[h.Sum32()%uint32(len(idents))]
}

// Kind is a peer kind's hue (design §5.4). The KIND column and the name carry
// the meaning, so a shared hue costs nothing.
func Kind(kind string) Role {
	switch kind {
	case "operator":
		return Accent
	case "session":
		return Ident1
	case "pagchat-bot":
		return Ident2
	case "openclaw", "dot":
		return Ident3
	case "grokbot":
		return Ident4
	case "launcher":
		return Ident5
	case "source":
		return Muted
	}
	return Text
}
