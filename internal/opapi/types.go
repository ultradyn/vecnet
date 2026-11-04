// Package opapi defines the operator API's wire contract (PROTOCOL §2.13).
// Its only project dependency is envelope; readers and clients can use these
// types without importing the hub. Nullable fields remain explicit JSON nulls.
package opapi

import (
	"encoding/json"

	"github.com/XertroV/vecnet/internal/envelope"
)

type Me struct {
	PeerID     string  `json:"peer_id"`
	Name       string  `json:"name"`
	Presence   string  `json:"presence"`
	PresenceAt *string `json:"presence_at"`
}

type InboxQuery struct {
	Limit      int    `json:"limit"`
	Before     string `json:"before"`
	UnreadOnly bool   `json:"unread_only"`
}

type OpenApproval struct {
	ApprovalID  string   `json:"approval_id"`
	ThreadID    string   `json:"thread_id"`
	ThreadTitle string   `json:"thread_title"`
	MessageID   string   `json:"message_id"`
	Requester   string   `json:"requester"`
	Question    string   `json:"question"`
	Text        string   `json:"text"`
	ExpiresAt   string   `json:"expires_at"`
	Options     []string `json:"options"`
}

// MarshalJSON keeps empty collections in OpenApproval non-null.
func (v OpenApproval) MarshalJSON() ([]byte, error) {
	type plain OpenApproval
	if v.Options == nil {
		v.Options = []string{}
	}
	return json.Marshal(plain(v))
}

type DeadLetter struct {
	DeliveryID string  `json:"delivery_id"`
	MessageID  string  `json:"message_id"`
	ThreadID   string  `json:"thread_id"`
	Recipient  string  `json:"recipient"`
	Sender     string  `json:"sender"`
	Excerpt    string  `json:"excerpt"`
	Class      string  `json:"class"`
	UpdatedAt  string  `json:"updated_at"`
	Attempt    int     `json:"attempt"`
	LastStatus *int    `json:"last_status"`
	LastError  *string `json:"last_error"`
}

type Attention struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	PeerID  string `json:"peer_id"`
	Health  string `json:"health"`
	Summary string `json:"summary"`
	Action  string `json:"action"`
	At      string `json:"at"`
}

type InboxDelivery struct {
	DeliveryID  string  `json:"delivery_id"`
	MessageID   string  `json:"message_id"`
	ThreadID    string  `json:"thread_id"`
	ThreadTitle string  `json:"thread_title"`
	Sender      string  `json:"sender"`
	Kind        string  `json:"kind"`
	Priority    string  `json:"priority"`
	Section     string  `json:"section"`
	State       string  `json:"state"`
	CreatedAt   string  `json:"created_at"`
	Excerpt     string  `json:"excerpt"`
	ReadAt      *string `json:"read_at"`
	Attachments int     `json:"attachments"`
}

type Inbox struct {
	Now         string          `json:"now"`
	Me          Me              `json:"me"`
	Approvals   []OpenApproval  `json:"approvals"`
	DeadLetters []DeadLetter    `json:"dead_letters"`
	Attention   []Attention     `json:"attention"`
	Deliveries  []InboxDelivery `json:"deliveries"`
	Unread      int             `json:"unread"`
	NextBefore  *string         `json:"next_before"`
}

// MarshalJSON keeps empty collections in Inbox non-null.
func (v Inbox) MarshalJSON() ([]byte, error) {
	type plain Inbox
	if v.Approvals == nil {
		v.Approvals = []OpenApproval{}
	}
	if v.DeadLetters == nil {
		v.DeadLetters = []DeadLetter{}
	}
	if v.Attention == nil {
		v.Attention = []Attention{}
	}
	if v.Deliveries == nil {
		v.Deliveries = []InboxDelivery{}
	}
	return json.Marshal(plain(v))
}

type ReadResult struct {
	Marked int `json:"marked"`
}

type ThreadQuery struct {
	Participant string `json:"participant"`
	// ExcludeParticipant drops threads that peer is in (the web sidebar's
	// Other Threads passes the operator's own peer id).
	ExcludeParticipant string `json:"exclude_participant"`
	Cursor             string `json:"cursor"`
	Limit              int    `json:"limit"`
}

type CutInfo struct {
	Reason string `json:"reason"`
	At     string `json:"at"`
}

type ThreadRow struct {
	Thread        envelope.ThreadSummary `json:"thread"`
	CreatedBy     string                 `json:"created_by"`
	CreatedAt     string                 `json:"created_at"`
	LastMessageAt string                 `json:"last_message_at"`
	Cut           *CutInfo               `json:"cut"`
}

type ThreadPage struct {
	Threads []ThreadRow `json:"threads"`
	Next    *string     `json:"next"`
}

// MarshalJSON keeps empty collections in ThreadPage non-null.
func (v ThreadPage) MarshalJSON() ([]byte, error) {
	type plain ThreadPage
	if v.Threads == nil {
		v.Threads = []ThreadRow{}
	}
	return json.Marshal(plain(v))
}

type ThreadFile struct {
	SHA256    string `json:"sha256"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	MessageID string `json:"message_id"`
	Sender    string `json:"sender"`
	CreatedAt string `json:"created_at"`
	Size      int64  `json:"size"`
	Spilled   bool   `json:"spilled"`
	Purged    bool   `json:"purged"`
}

type ThreadApproval struct {
	ApprovalID    string   `json:"approval_id"`
	MessageID     string   `json:"message_id"`
	ExpiresAt     string   `json:"expires_at"`
	Options       []string `json:"options"`
	AddressedToMe bool     `json:"addressed_to_me"`
}

// MarshalJSON keeps empty collections in ThreadApproval non-null.
func (v ThreadApproval) MarshalJSON() ([]byte, error) {
	type plain ThreadApproval
	if v.Options == nil {
		v.Options = []string{}
	}
	return json.Marshal(plain(v))
}

type ThreadDetail struct {
	Now           string           `json:"now"`
	Row           ThreadRow        `json:"row"`
	Files         []ThreadFile     `json:"files"`
	OpenApprovals []ThreadApproval `json:"open_approvals"`
}

// MarshalJSON keeps empty collections in ThreadDetail non-null.
func (v ThreadDetail) MarshalJSON() ([]byte, error) {
	type plain ThreadDetail
	if v.Files == nil {
		v.Files = []ThreadFile{}
	}
	if v.OpenApprovals == nil {
		v.OpenApprovals = []ThreadApproval{}
	}
	return json.Marshal(plain(v))
}

type MatrixCell struct {
	Recipient  string  `json:"recipient"`
	DeliveryID string  `json:"delivery_id"`
	State      string  `json:"state"`
	Display    string  `json:"display"`
	UpdatedAt  string  `json:"updated_at"`
	Attempt    int     `json:"attempt"`
	LastStatus *int    `json:"last_status"`
	ReadAt     *string `json:"read_at"`
}

type MatrixRow struct {
	MessageID string       `json:"message_id"`
	CreatedAt string       `json:"created_at"`
	Sender    string       `json:"sender"`
	Excerpt   string       `json:"excerpt"`
	Cells     []MatrixCell `json:"cells"`
}

// MarshalJSON keeps empty collections in MatrixRow non-null.
func (v MatrixRow) MarshalJSON() ([]byte, error) {
	type plain MatrixRow
	if v.Cells == nil {
		v.Cells = []MatrixCell{}
	}
	return json.Marshal(plain(v))
}

type Matrix struct {
	Recipients []string    `json:"recipients"`
	Rows       []MatrixRow `json:"rows"`
	NextAfter  *string     `json:"next_after"`
}

// MarshalJSON keeps empty collections in Matrix non-null.
func (v Matrix) MarshalJSON() ([]byte, error) {
	type plain Matrix
	if v.Recipients == nil {
		v.Recipients = []string{}
	}
	if v.Rows == nil {
		v.Rows = []MatrixRow{}
	}
	return json.Marshal(plain(v))
}

type Peer struct {
	CreatedBy    *string        `json:"created_by,omitempty"`
	PeerID       string         `json:"peer_id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Capabilities []string       `json:"capabilities"`
	Quota        map[string]any `json:"quota"`
	Location     *string        `json:"location"`
	Presence     string         `json:"presence"`
	Reachability string         `json:"reachability"`
	LastSeen     *string        `json:"last_seen"`
	EnrolledAt   *string        `json:"enrolled_at"`
	Teams        []string       `json:"teams"`
}

// MarshalJSON keeps empty collections in Peer non-null.
func (v Peer) MarshalJSON() ([]byte, error) {
	type plain Peer
	if v.Capabilities == nil {
		v.Capabilities = []string{}
	}
	if v.Teams == nil {
		v.Teams = []string{}
	}
	if v.Quota == nil {
		v.Quota = map[string]any{}
	}
	return json.Marshal(plain(v))
}

type RouteInfo struct {
	RouteID    string  `json:"route_id"`
	Kind       string  `json:"kind"`
	Host       string  `json:"host"`
	VerifiedAt *string `json:"verified_at"`
}

type PeerRow struct {
	Peer        Peer        `json:"peer"`
	PrincipalID string      `json:"principal_id"`
	Credential  string      `json:"credential"`
	Disabled    bool        `json:"disabled"`
	PresenceAt  *string     `json:"presence_at"`
	ReachVia    *string     `json:"reach_via"`
	Routes      []RouteInfo `json:"routes"`
	// Scopes are the peer's own allow and deny lists (both empty: allow
	// all). Limits are its send limits; null for a directory source.
	Scopes PeerScopes  `json:"scopes"`
	Limits *PeerLimits `json:"limits"`
}

// MarshalJSON keeps empty collections in PeerRow non-null.
func (v PeerRow) MarshalJSON() ([]byte, error) {
	type plain PeerRow
	if v.Routes == nil {
		v.Routes = []RouteInfo{}
	}
	return json.Marshal(plain(v))
}

// PeerScopes holds peer, team and group ids (PUT /v1/admin/peers/{id}/scopes).
type PeerScopes struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// MarshalJSON keeps empty collections in PeerScopes non-null.
func (v PeerScopes) MarshalJSON() ([]byte, error) {
	type plain PeerScopes
	if v.Allow == nil {
		v.Allow = []string{}
	}
	if v.Deny == nil {
		v.Deny = []string{}
	}
	return json.Marshal(plain(v))
}

// PeerLimits mirrors PUT /v1/admin/peers/{id}/limits.
type PeerLimits struct {
	SendsPerMin    int `json:"sends_per_min"`
	ThreadsPerHour int `json:"threads_per_hour"`
}

type LoopCut struct {
	At       string `json:"at"`
	ThreadID string `json:"thread_id"`
	Actor    string `json:"actor"`
	Reason   string `json:"reason"`
}

type LoopCuts struct {
	Since    string         `json:"since"`
	Hours    int            `json:"hours"`
	Total    int            `json:"total"`
	ByReason map[string]int `json:"by_reason"`
	Recent   []LoopCut      `json:"recent"`
}

// MarshalJSON keeps empty collections in LoopCuts non-null.
func (v LoopCuts) MarshalJSON() ([]byte, error) {
	type plain LoopCuts
	if v.Recent == nil {
		v.Recent = []LoopCut{}
	}
	if v.ByReason == nil {
		v.ByReason = map[string]int{}
	}
	return json.Marshal(plain(v))
}

type BlockedThread struct {
	Recipient     string  `json:"recipient"`
	ThreadID      string  `json:"thread_id"`
	DeliveryID    string  `json:"delivery_id"`
	Attempt       int     `json:"attempt"`
	Waiting       int     `json:"waiting"`
	NextAttemptAt *string `json:"next_attempt_at"`
	LastStatus    *int    `json:"last_status"`
	LastError     *string `json:"last_error"`
}

type FailedDelivery struct {
	DeliveryID string  `json:"delivery_id"`
	Recipient  string  `json:"recipient"`
	ThreadID   string  `json:"thread_id"`
	UpdatedAt  string  `json:"updated_at"`
	LastStatus *int    `json:"last_status"`
	LastError  *string `json:"last_error"`
}

type DeliveryHealth struct {
	Blocked []BlockedThread  `json:"blocked"`
	Failed  []FailedDelivery `json:"failed"`
}

// MarshalJSON keeps empty collections in DeliveryHealth non-null.
func (v DeliveryHealth) MarshalJSON() ([]byte, error) {
	type plain DeliveryHealth
	if v.Blocked == nil {
		v.Blocked = []BlockedThread{}
	}
	if v.Failed == nil {
		v.Failed = []FailedDelivery{}
	}
	return json.Marshal(plain(v))
}

type Subscription struct {
	SubID          string  `json:"sub_id"`
	Principal      string  `json:"principal"`
	PeerID         string  `json:"peer_id"`
	URL            string  `json:"url"`
	Name           string  `json:"name"`
	Cursor         string  `json:"cursor"`
	Health         string  `json:"health"`
	HealthNote     string  `json:"health_note"`
	ExpiresAt      *string `json:"expires_at"`
	RefreshBefore  *string `json:"refresh_before"`
	LastRefreshAt  *string `json:"last_refresh_at"`
	VerifiedAt     *string `json:"verified_at"`
	PausedReason   *string `json:"paused_reason"`
	LastDeliveryAt *string `json:"last_delivery_at"`
	LastStatus     *int    `json:"last_status"`
	Attempts       int     `json:"attempts"`
}

type AuditEntry struct {
	ID      int64          `json:"id"`
	At      string         `json:"at"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Targets []string       `json:"target_ids"`
	Detail  map[string]any `json:"detail"`
}

// MarshalJSON keeps empty collections in AuditEntry non-null.
func (v AuditEntry) MarshalJSON() ([]byte, error) {
	type plain AuditEntry
	if v.Targets == nil {
		v.Targets = []string{}
	}
	if v.Detail == nil {
		v.Detail = map[string]any{}
	}
	return json.Marshal(plain(v))
}

type AuditPage struct {
	Entries []AuditEntry `json:"entries"`
	Next    *string      `json:"next"`
}

// MarshalJSON keeps empty collections in AuditPage non-null.
func (v AuditPage) MarshalJSON() ([]byte, error) {
	type plain AuditPage
	if v.Entries == nil {
		v.Entries = []AuditEntry{}
	}
	return json.Marshal(plain(v))
}

type History struct {
	Messages  []envelope.Message `json:"messages"`
	NextAfter *string            `json:"next_after"`
}

// MarshalJSON keeps empty collections in History non-null.
func (v History) MarshalJSON() ([]byte, error) {
	type plain History
	if v.Messages == nil {
		v.Messages = []envelope.Message{}
	}
	return json.Marshal(plain(v))
}

type DeliveryRef struct {
	DeliveryID string  `json:"delivery_id"`
	Recipient  string  `json:"recipient"`
	Via        *string `json:"via"`
	State      string  `json:"state"`
}

// ReactionResult is returned for both changed and no-op reaction requests.
type ReactionResult struct {
	MessageID  string                   `json:"message_id"`
	ThreadID   string                   `json:"thread_id"`
	Emoji      string                   `json:"emoji"`
	Op         string                   `json:"op"`
	Changed    bool                     `json:"changed"`
	Reactions  []envelope.ReactionGroup `json:"reactions"`
	Superseded []string                 `json:"superseded"`
	Wake       *ReactionWake            `json:"wake"`
}

// ReactionWake describes a Delivery created by this reaction request.
type ReactionWake struct {
	MessageID  string `json:"message_id"`
	DeliveryID string `json:"delivery_id"`
	Recipient  string `json:"recipient"`
	State      string `json:"state"`
}

// MarshalJSON emits empty reaction and superseded collections as arrays.
func (v ReactionResult) MarshalJSON() ([]byte, error) {
	type plain ReactionResult
	if v.Reactions == nil {
		v.Reactions = []envelope.ReactionGroup{}
	}
	if v.Superseded == nil {
		v.Superseded = []string{}
	}
	return json.Marshal(plain(v))
}

type SendResult struct {
	MessageID  string        `json:"message_id"`
	ThreadID   string        `json:"thread_id"`
	Deliveries []DeliveryRef `json:"deliveries"`
	JobID      string        `json:"job_id,omitempty"`
	Spilled    bool          `json:"spilled"`
}

// MarshalJSON keeps empty collections in SendResult non-null.
func (v SendResult) MarshalJSON() ([]byte, error) {
	type plain SendResult
	if v.Deliveries == nil {
		v.Deliveries = []DeliveryRef{}
	}
	return json.Marshal(plain(v))
}

// Subscription health values shared by both operator clients.
const (
	HealthOK             = "ok"
	HealthFailing        = "failing"
	HealthLate           = "late"
	HealthExpired        = "expired"
	HealthPausedToken    = "paused_token"
	HealthPausedFailed   = "paused_failed"
	HealthPausedAuth     = "paused_auth"
	HealthPausedOperator = "paused_operator"
	HealthUnverified     = "unverified"
)

// PrincipalRow is the operator's provisioning policy and current usage (§18.4).
type PrincipalRow struct {
	PrincipalID     string   `json:"principal_id"`
	Name            string   `json:"name"`
	MaxPeers        int      `json:"max_peers"`
	CreatesPerHour  int      `json:"creates_per_hour"`
	Kinds           []string `json:"kinds"`
	Active          int      `json:"active"`
	Disabled        int      `json:"disabled"`
	CreatesLastHour int      `json:"creates_last_hour"`
	ScopedPeers     int      `json:"scoped_peers"`
	DefaultAllow    []string `json:"default_allow"`
	DefaultDeny     []string `json:"default_deny"`
}

// MarshalJSON keeps empty collections in PrincipalRow non-null.
func (v PrincipalRow) MarshalJSON() ([]byte, error) {
	type plain PrincipalRow
	if v.Kinds == nil {
		v.Kinds = []string{}
	}
	if v.DefaultAllow == nil {
		v.DefaultAllow = []string{}
	}
	if v.DefaultDeny == nil {
		v.DefaultDeny = []string{}
	}
	return json.Marshal(plain(v))
}
