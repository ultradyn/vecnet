package envelope

import "strings"

// ReactionEmoji is the fixed set in display order. Bare hearts are normalised
// by CanonicalEmoji; wire values always include the variation selector.
var ReactionEmoji = [8]string{"👍", "👎", "❤️", "😂", "👀", "✅", "❓", "❗"}

// ReactionNames maps client shorthand to the canonical emoji.
var ReactionNames = map[string]string{
	"+1": ReactionEmoji[0], "-1": ReactionEmoji[1], "heart": ReactionEmoji[2],
	"laugh": ReactionEmoji[3], "eyes": ReactionEmoji[4], "check": ReactionEmoji[5],
	"q": ReactionEmoji[6], "bang": ReactionEmoji[7],
}

// CanonicalEmoji accepts only an allowed emoji, normalising a bare heart.
func CanonicalEmoji(s string) (string, bool) {
	if s == "❤" {
		s = ReactionEmoji[2]
	}
	if ReactionIndex(s) < 0 {
		return "", false
	}
	return s, true
}

// ParseReaction accepts an emoji or a case-insensitive client shorthand.
func ParseReaction(s string) (string, bool) {
	if emoji, ok := CanonicalEmoji(s); ok {
		return emoji, true
	}
	emoji, ok := ReactionNames[strings.ToLower(s)]
	return emoji, ok
}

// ReactionName returns the shorthand for a canonical emoji, or "" if absent.
func ReactionName(emoji string) string {
	for name, value := range ReactionNames {
		if value == emoji {
			return name
		}
	}
	return ""
}

// ReactionIndex returns an emoji's display index, or -1 if absent.
func ReactionIndex(emoji string) int {
	for i, value := range ReactionEmoji {
		if value == emoji {
			return i
		}
	}
	return -1
}

// DefaultReactionPriority is the client default; the hub defaults to hold.
func DefaultReactionPriority(emoji string) string {
	if i := ReactionIndex(emoji); i >= 5 {
		return "wake_idle"
	}
	return "hold"
}

// ReactionList describes the allowed set in display order for client help.
func ReactionList() string {
	parts := make([]string, len(ReactionEmoji))
	for i, emoji := range ReactionEmoji {
		parts[i] = emoji + " " + ReactionName(emoji)
	}
	return strings.Join(parts, ", ")
}

// ReactionRef identifies the target of a hub-authored reaction Message.
type ReactionRef struct {
	Target string `json:"target"`
	Emoji  string `json:"emoji"`
}

// ReactionGroup is one emoji and its reactors, in thread history only.
type ReactionGroup struct {
	Emoji string   `json:"emoji"`
	Peers []string `json:"peers"`
}

// ReactionEvent announces an added or removed reaction to visible readers.
type ReactionEvent struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
	PeerID    string `json:"peer_id"`
	Emoji     string `json:"emoji"`
	Op        string `json:"op"`
	At        string `json:"at"`
}

// ReactionRequest is the body of POST /v1/reactions.
type ReactionRequest struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
	Op        string `json:"op"`
	As        string `json:"as,omitempty"`
	Priority  string `json:"priority,omitempty"`

	// PrioritySupplied distinguishes an explicit empty priority from omission
	// when transport adapters decode the request. Nonempty values are always
	// validated, regardless of this flag.
	PrioritySupplied bool `json:"-"`
}
