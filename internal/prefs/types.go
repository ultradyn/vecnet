package prefs

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
)

type Source string

const (
	SourceDefault Source = "default"
	SourceFlag    Source = "flag"
	SourceStored  Source = "stored"
)

func (s Source) MarshalJSON() ([]byte, error) {
	switch s {
	case SourceDefault, SourceFlag, SourceStored:
		return json.Marshal(string(s))
	}
	return nil, invalid("", "source")
}

func (s *Source) UnmarshalJSON(data []byte) error {
	var next string
	if json.Unmarshal(data, &next) != nil {
		return invalid("", "source")
	}
	switch Source(next) {
	case SourceDefault, SourceFlag, SourceStored:
		*s = Source(next)
		return nil
	}
	return invalid("", "source")
}

// Effective is a value with provenance at one snapshot's revision.
type Effective struct {
	Value     Value  `json:"value"`
	Source    Source `json:"source"`
	Origin    string `json:"origin"`
	Revision  uint64 `json:"revision"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// Descriptor is the public GET row. Revision belongs to the enclosing snapshot;
// maintenance-only definition fields never appear on the wire.
type Descriptor struct {
	Definition
	Value     Value  `json:"value"`
	Source    Source `json:"source"`
	Origin    string `json:"origin"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

func (d Descriptor) MarshalJSON() ([]byte, error) {
	type plain Descriptor
	d.Definition = cloneDefinition(d.Definition)
	return json.Marshal(plain(d))
}

// Snapshot keeps defensive copies of definitions and scalars. ETag is carried
// separately as an HTTP header. Revisions and tags are set by the hub service;
// consumers must treat them as immutable, along with the rest of the snapshot.
type Snapshot struct {
	Revision    uint64 `json:"revision"`
	ETag        string `json:"-"`
	preferences []Descriptor
}

func NewSnapshot(revision uint64, etag string, definitions []Definition, values map[string]Effective) (Snapshot, error) {
	s := Snapshot{Revision: revision, ETag: etag, preferences: make([]Descriptor, 0, len(definitions))}
	seen := map[string]bool{}
	for _, def := range definitions {
		canonical, err := Lookup(def.Key)
		if err != nil {
			return Snapshot{}, err
		}
		if seen[def.Key] {
			return Snapshot{}, invalidBody()
		}
		seen[def.Key] = true
		if def.Type != canonical.Type {
			return Snapshot{}, invalid(def.Key, "type")
		}
		if err := Validate(def, def.Default); err != nil {
			return Snapshot{}, err
		}
		e, ok := values[def.Key]
		if !ok {
			e = Effective{Value: def.Default, Source: SourceDefault}
		}
		if err := Validate(def, e.Value); err != nil {
			return Snapshot{}, err
		}
		if err := validateProvenance(def.Key, e.Source, e.Origin); err != nil {
			return Snapshot{}, err
		}
		s.preferences = append(s.preferences, Descriptor{Definition: cloneDefinition(def), Value: normalize(def, e.Value), Source: e.Source, Origin: e.Origin, UpdatedAt: e.UpdatedAt, UpdatedBy: e.UpdatedBy})
	}
	for key := range values {
		if !seen[key] {
			return Snapshot{}, unknownKey()
		}
	}
	sort.Slice(s.preferences, func(i, j int) bool { return s.preferences[i].Key < s.preferences[j].Key })
	return s, nil
}

func validateProvenance(key string, source Source, origin string) error {
	switch source {
	case SourceDefault, SourceStored:
		if origin != "" {
			return invalid(key, "origin")
		}
	case SourceFlag:
	default:
		return invalid(key, "source")
	}
	if origin == "" {
		return nil
	}
	flag := strings.HasPrefix(origin, "--")
	name := origin
	if flag {
		name = strings.TrimPrefix(origin, "--")
	}
	if len(name) == 0 || len(name) > 128 {
		return invalid(key, "origin")
	}
	for i, c := range name {
		if flag {
			if !(c >= 'a' && c <= 'z' || i > 0 && (c == '-' || c >= '0' && c <= '9')) {
				return invalid(key, "origin")
			}
		} else if !(c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return invalid(key, "origin")
		}
	}
	return nil
}

func (s Snapshot) Get(key string) (Effective, error) {
	i := sort.Search(len(s.preferences), func(i int) bool { return s.preferences[i].Key >= key })
	if i == len(s.preferences) || s.preferences[i].Key != key {
		return Effective{}, unknownKey()
	}
	d := s.preferences[i]
	return Effective{Value: d.Value, Source: d.Source, Origin: d.Origin, Revision: s.Revision, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy}, nil
}

// Descriptors returns copies for clients which need metadata. Editing these
// copies cannot change the snapshot or its shared registry definitions.
func (s Snapshot) Descriptors() []Descriptor {
	out := make([]Descriptor, len(s.preferences))
	for i, d := range s.preferences {
		out[i] = d
		out[i].Definition = cloneDefinition(d.Definition)
	}
	return out
}

func (s Snapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Revision    uint64       `json:"revision"`
		Preferences []Descriptor `json:"preferences"`
	}{s.Revision, s.Descriptors()})
}

func (s *Snapshot) UnmarshalJSON(data []byte) error {
	if err := strictJSON(data); err != nil {
		return err
	}
	var wire struct {
		Revision    uint64       `json:"revision"`
		Preferences []Descriptor `json:"preferences"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return invalidBody()
	}
	defs := make([]Definition, 0, len(wire.Preferences))
	values := make(map[string]Effective, len(wire.Preferences))
	for _, d := range wire.Preferences {
		if _, ok := values[d.Key]; ok {
			return invalidBody()
		}
		d.Default = normalize(d.Definition, d.Default)
		defs = append(defs, d.Definition)
		values[d.Key] = Effective{Value: d.Value, Source: d.Source, Origin: d.Origin, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy}
	}
	next, err := NewSnapshot(wire.Revision, "", defs, values)
	if err != nil {
		return err
	}
	*s = next
	return nil
}

const (
	MaxPatchKeys  = 64
	MaxPatchBytes = 16 * 1024
)

// Patch describes a proposal only. Neither JSON decoding nor validation grants
// authority, changes availability, confirms a risk or writes persistent state.
type Patch struct {
	Set           map[string]Value `json:"set"`
	Reset         []string         `json:"reset"`
	Confirm       []string         `json:"confirm"`
	ApplyExisting bool             `json:"apply_existing"`
}

// Validate checks proposal shape and per-key scalar constraints. The hub must
// additionally check complete policy, availability, auth, ETag and confirmation.
func (p Patch) Validate() error {
	n := len(p.Set) + len(p.Reset)
	if n == 0 || n > MaxPatchKeys {
		return invalidBody()
	}
	seen := map[string]bool{}
	keys := make([]string, 0, len(p.Set))
	for key := range p.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		d, err := Lookup(key)
		if err != nil {
			return err
		}
		if err := Validate(d, p.Set[key]); err != nil {
			return err
		}
		seen[key] = true
	}
	for _, key := range p.Reset {
		if _, err := Lookup(key); err != nil {
			return err
		}
		if seen[key] {
			return invalidBody()
		}
		seen[key] = true
	}
	confirmations := map[string]bool{}
	for _, confirmation := range p.Confirm {
		if confirmation == "" || confirmations[confirmation] {
			return invalidBody()
		}
		confirmations[confirmation] = true
	}
	return nil
}

func (p *Patch) UnmarshalJSON(data []byte) error {
	if len(data) > MaxPatchBytes {
		return &Error{Code: CodePayloadTooLarge}
	}
	if err := strictJSON(data); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return invalidBody()
	}
	next := Patch{Set: map[string]Value{}, Reset: []string{}, Confirm: []string{}}
	// Exact field matching avoids encoding/json's case-insensitive struct fields.
	for key, raw := range fields {
		switch key {
		case "set":
			var scalars map[string]json.RawMessage
			if json.Unmarshal(raw, &scalars) != nil {
				return invalidBody()
			}
			keys := make([]string, 0, len(scalars))
			for key := range scalars {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				def, err := Lookup(key)
				if err != nil {
					return err
				}
				var value Value
				if err := json.Unmarshal(scalars[key], &value); err != nil {
					var scalarError *Error
					if errors.As(err, &scalarError) {
						return invalid(def.Key, scalarError.Details.Constraint)
					}
					return invalid(def.Key, "scalar")
				}
				next.Set[key] = value
			}
		case "reset":
			if json.Unmarshal(raw, &next.Reset) != nil {
				return invalidBody()
			}
		case "confirm":
			if json.Unmarshal(raw, &next.Confirm) != nil {
				return invalidBody()
			}
		case "apply_existing":
			if json.Unmarshal(raw, &next.ApplyExisting) != nil {
				return invalidBody()
			}
		default:
			return invalidBody()
		}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	for key, value := range next.Set {
		d, _ := Lookup(key)
		next.Set[key] = normalize(d, value)
	}
	*p = next
	return nil
}

func (p Patch) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type plain Patch
	if p.Set == nil {
		p.Set = map[string]Value{}
	}
	if p.Reset == nil {
		p.Reset = []string{}
	}
	if p.Confirm == nil {
		p.Confirm = []string{}
	}
	return json.Marshal(plain(p))
}

type Risk struct {
	Code    string   `json:"code"`
	Keys    []string `json:"keys"`
	Message string   `json:"message"`
}

// Impact's optional counts are estimates. Pointers preserve present zero vs
// absent. Timestamps and high-water bounds belong to the confirmation service.
type Impact struct {
	CutoffAt       string        `json:"cutoff_at,omitempty"`
	HighWater      *int64        `json:"high_water,omitempty"`
	NotBefore      string        `json:"not_before,omitempty"`
	Eligible       *int64        `json:"eligible,omitempty"`
	Pinned         *int64        `json:"pinned,omitempty"`
	EstimatedBytes *int64        `json:"estimated_bytes,omitempty"`
	Storage        *StorageUsage `json:"storage,omitempty"`
}

// StorageUsage is the hub-wide retained-storage estimate a hosted blob policy
// reports for operator review (R2 plan: used = reserved + retained, against a
// finite positive limit). Used may exceed Limit after the limit is lowered;
// that blocks new grants but deletes nothing.
type StorageUsage struct {
	RetainedBytes int64 `json:"retained_bytes"`
	ReservedBytes int64 `json:"reserved_bytes"`
	UsedBytes     int64 `json:"used_bytes"`
	LimitBytes    int64 `json:"limit_bytes"`
}

// Valid reports whether every figure is nonnegative, Used is exactly
// Retained+Reserved without overflow, and Limit is finite and positive.
func (s StorageUsage) Valid() bool {
	if s.RetainedBytes < 0 || s.ReservedBytes < 0 || s.LimitBytes <= 0 {
		return false
	}
	if s.RetainedBytes > math.MaxInt64-s.ReservedBytes {
		return false
	}
	return s.UsedBytes == s.RetainedBytes+s.ReservedBytes
}

type Preview struct {
	BaseRevision uint64 `json:"base_revision"`
	Risks        []Risk `json:"risks"`
	Impact       Impact `json:"impact"`
	ConfirmToken string `json:"confirm_token"`
	ExpiresAt    string `json:"expires_at"`
}

func (p Preview) MarshalJSON() ([]byte, error) {
	type plain Preview
	p.Risks = append([]Risk{}, p.Risks...)
	for i := range p.Risks {
		p.Risks[i].Keys = append([]string{}, p.Risks[i].Keys...)
	}
	return json.Marshal(plain(p))
}

// ErrorDetails contains only safe field names and constraint identifiers, never
// rejected values. The HTTP layer wraps it in the existing error envelope.
type ErrorDetails struct {
	Field      string   `json:"field,omitempty"`
	Constraint string   `json:"constraint,omitempty"`
	Keys       []string `json:"keys,omitempty"`
}

const (
	CodeInvalidBody          = "invalid_body"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnknownKey           = "prefs_unknown_key"
	CodeInvalidValue         = "prefs_invalid_value"
	CodeUnavailable          = "prefs_unavailable"
	CodePolicyLocked         = "prefs_policy_locked"
	CodePreconditionRequired = "prefs_precondition_required"
	CodeRevisionConflict     = "prefs_revision_conflict"
	CodeConfirmationRequired = "prefs_confirmation_required"
	CodeConfirmationStale    = "prefs_confirmation_stale"
	CodeStorageUnavailable   = "prefs_storage_unavailable"
)

var (
	ErrUnknownKey   = errors.New(CodeUnknownKey)
	ErrInvalidValue = errors.New(CodeInvalidValue)
	ErrInvalidBody  = errors.New(CodeInvalidBody)
)

type Error struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details ErrorDetails `json:"details"`
}

func (e *Error) Error() string { return e.Code }
func (e *Error) Unwrap() error {
	switch e.Code {
	case CodeUnknownKey:
		return ErrUnknownKey
	case CodeInvalidValue:
		return ErrInvalidValue
	case CodeInvalidBody:
		return ErrInvalidBody
	}
	return nil
}

func invalidBody() error {
	return &Error{Code: CodeInvalidBody, Message: "Invalid preference request."}
}
func unknownKey() error { return &Error{Code: CodeUnknownKey, Message: "Unknown preference key."} }
func invalid(key, constraint string) error {
	// Only registry keys may be echoed; arbitrary caller strings are suppressed.
	field := ""
	for _, d := range registry {
		if key == d.Key {
			field = key
			break
		}
	}
	return &Error{Code: CodeInvalidValue, Message: "Invalid preference value.", Details: ErrorDetails{Field: field, Constraint: constraint}}
}
