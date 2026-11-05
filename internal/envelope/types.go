// Package envelope holds the Go form of every wire object in
// docs/protocol/envelope.schema.json ($id urn:vecnet:envelope:1) and
// validates raw JSON against the embedded copy of that schema
// (PROTOCOL §1.2). The schema is normative; these structs follow it.
//
// Timestamps are kept as strings in the wire format of FormatTime so a
// Message round-trips byte-identically; use ParseTime to read them.
package envelope

import (
	"encoding/json"
	"time"

	"github.com/XertroV/vecnet/internal/blobwire"
)

// SchemaVersion is the schema_version carried by Message and Delivery.
const SchemaVersion = 1

// TimeLayout is the wire timestamp format: millisecond UTC (PROTOCOL §1.2).
const TimeLayout = "2006-01-02T15:04:05.000Z"

// FormatTime renders t as a wire timestamp: UTC, milliseconds, truncated.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// ParseTime parses a wire timestamp. Anything other than exactly
// YYYY-MM-DDTHH:MM:SS.sssZ is an error. The result is in UTC.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return time.Time{}, err
	}
	// Defensive: the wire format has a fixed width.
	if len(s) != len(TimeLayout) {
		return time.Time{}, &time.ParseError{Layout: TimeLayout, Value: s, Message: ": not millisecond precision"}
	}
	return t.UTC(), nil
}

// Origin is a bridged-conversation reference ($defs/origin).
type Origin struct {
	System    string `json:"system"`
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
}

// Attachment references blob bytes by hash ($defs/attachment).
type Attachment struct {
	BlobID         string              `json:"blob_id,omitempty"`
	RetentionClass string              `json:"retention_class,omitempty"`
	ExpiresAt      *string             `json:"expires_at"`
	SHA256Verified *bool               `json:"sha256_verified,omitempty"`
	Integrity      *blobwire.Integrity `json:"integrity,omitempty"`
	SHA256         string              `json:"sha256"`
	Size           int64               `json:"size"`
	MediaType      string              `json:"media_type"`
	Name           string              `json:"name,omitempty"`
	OriginURL      string              `json:"origin_url,omitempty"`
	BlobURL        string              `json:"blob_url,omitempty"`
}

// MarshalJSON preserves legacy disk bytes while emitting null for kept R2 objects.
func (a Attachment) MarshalJSON() ([]byte, error) {
	type plain Attachment
	if a.BlobID != "" {
		return json.Marshal(plain(a))
	}
	return json.Marshal(struct {
		plain
		ExpiresAt *string `json:"expires_at,omitempty"`
	}{plain: plain(a)})
}

// Loop is the hub-set loop-control block of a Message ($defs/loop).
type Loop struct {
	Depth       int      `json:"depth"`
	MaxDepth    int      `json:"max_depth"`
	ExpiresAt   string   `json:"expires_at"`
	SenderChain []string `json:"sender_chain"`
}

// MarshalJSON emits sender_chain as [] rather than null when empty.
func (l Loop) MarshalJSON() ([]byte, error) {
	type plain Loop
	if l.SenderChain == nil {
		l.SenderChain = []string{}
	}
	return json.Marshal(plain(l))
}

// Content is a Message's text ($defs/content). SendRequest carries its own
// larger, truncated-less shape: SendContent.
type Content struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// SendContent is SendRequest.content (inline in the schema): text only, up
// to 64 KiB, auto-spilled by the hub past 4096 bytes.
type SendContent struct {
	Text string `json:"text"`
}

// JobRequest asks the recipient to run a job ($defs/jobRequest).
type JobRequest struct {
	Version      int             `json:"version"`
	JobKey       string          `json:"job_key"`
	Operation    string          `json:"operation"`
	Instructions string          `json:"instructions"`
	Input        json.RawMessage `json:"input"`
}

// JobStatus reports progress on a job ($defs/jobStatus).
type JobStatus struct {
	JobID    string          `json:"job_id"`
	Sequence int64           `json:"sequence"`
	State    string          `json:"state"`
	Output   json.RawMessage `json:"output,omitempty"`
}

// ApprovalRequest asks a peer for a decision ($defs/approvalRequest).
type ApprovalRequest struct {
	ApprovalID         string   `json:"approval_id"`
	Question           string   `json:"question"`
	Options            []string `json:"options"`
	ExpiresAt          string   `json:"expires_at"`
	EscalateToOperator *bool    `json:"escalate_to_operator,omitempty"` // schema default true
}

// ApprovalResponse answers an ApprovalRequest ($defs/approvalResponse).
type ApprovalResponse struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason,omitempty"`
}

// SenderInfo is Message.sender_info ($defs/senderInfo): the sender's
// directory name and kind, snapshotted by the hub when the Message was sent
// (PROTOCOL §2.2 step 12, §8.2). Untrusted display data: name is whatever
// the sender's directory entry said; kind is as trustworthy as the
// operator's enrolment. For the hub's own messages it is {"hub", "hub"}.
type SenderInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Message is the logical message, identical across its Deliveries
// ($defs/message). Nullable required fields are pointers without omitempty.
type Message struct {
	SchemaVersion    int               `json:"schema_version"`
	MessageID        string            `json:"message_id"`
	ThreadID         string            `json:"thread_id"`
	InReplyTo        *string           `json:"in_reply_to"`
	Origin           *Origin           `json:"origin"`
	Kind             string            `json:"kind"`
	Sender           string            `json:"sender"`
	SenderInfo       *SenderInfo       `json:"sender_info,omitempty"` // hub-set at send; absent from older hubs
	Recipients       []string          `json:"recipients"`
	Priority         string            `json:"priority"`
	CreatedAt        string            `json:"created_at"`
	Content          Content           `json:"content"`
	Attachments      []Attachment      `json:"attachments"`
	Metadata         map[string]any    `json:"metadata"`
	Loop             Loop              `json:"loop"`
	JobRequest       *JobRequest       `json:"job_request,omitempty"`
	JobStatus        *JobStatus        `json:"job_status,omitempty"`
	ApprovalRequest  *ApprovalRequest  `json:"approval_request,omitempty"`
	ApprovalResponse *ApprovalResponse `json:"approval_response,omitempty"`
	Reaction         *ReactionRef      `json:"reaction,omitempty"`
	Reactions        []ReactionGroup   `json:"reactions,omitempty"` // Thread history only; never in a Delivery.
}

// MarshalJSON emits empty attachments/metadata as []/{} rather than null,
// which the schema's array/object types would reject.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if m.Attachments == nil {
		m.Attachments = []Attachment{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	return json.Marshal(plain(m))
}

// ThreadSummary is the thread context on every Delivery and the data of a
// thread_updated Event ($defs/threadSummary).
type ThreadSummary struct {
	ThreadID           string   `json:"thread_id"`
	Title              *string  `json:"title"`
	Participants       []string `json:"participants"`
	Group              *string  `json:"group"`
	MessageCount       int      `json:"message_count"`
	BudgetRemaining    *int     `json:"budget_remaining"`
	HistoryAccess      string   `json:"history_access"`
	LockedParticipants bool     `json:"locked_participants,omitempty"`
}

// MarshalJSON emits empty participants as [] rather than null.
func (s ThreadSummary) MarshalJSON() ([]byte, error) {
	type plain ThreadSummary
	if s.Participants == nil {
		s.Participants = []string{}
	}
	return json.Marshal(plain(s))
}

// Delivery is one Message to one recipient peer ($defs/delivery).
type Delivery struct {
	SchemaVersion int           `json:"schema_version"`
	DeliveryID    string        `json:"delivery_id"`
	Recipient     string        `json:"recipient"`
	Via           *string       `json:"via"`
	AlsoVia       []string      `json:"also_via,omitempty"`
	Attempt       int           `json:"attempt"`
	Ingress       string        `json:"ingress"`
	Thread        ThreadSummary `json:"thread"`
	Message       Message       `json:"message"`
}

// Receipt is the progress of one Delivery; an Event payload ($defs/receipt).
type Receipt struct {
	DeliveryID string `json:"delivery_id"`
	MessageID  string `json:"message_id"`
	ThreadID   string `json:"thread_id"`
	State      string `json:"state"`
	By         string `json:"by"`
	At         string `json:"at"`
	Note       string `json:"note,omitempty"`
}

// Presence is a peer's self-reported state ($defs/presence).
type Presence struct {
	PeerID       string         `json:"peer_id"`
	State        string         `json:"state"`
	At           string         `json:"at"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Quota        map[string]any `json:"quota,omitempty"`
	Location     string         `json:"location,omitempty"`
	Note         string         `json:"note,omitempty"`
}

// Event is one entry in a durable log ($defs/event). Data is the raw
// payload whose shape depends on Type.
type Event struct {
	Cursor string          `json:"cursor"`
	Type   string          `json:"type"`
	At     string          `json:"at"`
	Data   json.RawMessage `json:"data"`
}

// SendRequest is the body of POST /v1/messages ($defs/sendRequest).
// Optional fields are omitted when empty; the schema forbids null for them.
type SendRequest struct {
	IdempotencyKey     string            `json:"idempotency_key"`
	As                 string            `json:"as,omitempty"`
	ThreadID           *string           `json:"thread_id,omitempty"`
	ThreadTitle        string            `json:"thread_title,omitempty"`
	HistoryAccess      string            `json:"history_access,omitempty"`
	LockedParticipants *bool             `json:"locked_participants,omitempty"`
	InReplyTo          *string           `json:"in_reply_to,omitempty"`
	Origin             *Origin           `json:"origin,omitempty"`
	Recipients         []string          `json:"recipients,omitempty"`
	Kind               string            `json:"kind,omitempty"` // schema default "message"
	Priority           string            `json:"priority"`
	Content            SendContent       `json:"content"`
	Attachments        []Attachment      `json:"attachments,omitempty"`
	Metadata           map[string]any    `json:"metadata,omitempty"`
	MaxDepth           *int              `json:"max_depth,omitempty"`
	ExpiresAt          string            `json:"expires_at,omitempty"`
	JobRequest         *JobRequest       `json:"job_request,omitempty"`
	JobStatus          *JobStatus        `json:"job_status,omitempty"`
	ApprovalRequest    *ApprovalRequest  `json:"approval_request,omitempty"`
	ApprovalResponse   *ApprovalResponse `json:"approval_response,omitempty"`
}
