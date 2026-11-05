package prefs

import (
	"slices"
	"sort"
)

// Definition describes one stable key. Owner/Help/Consumer are internal
// maintenance metadata; public clients receive the explicit wire fields only.
type Definition struct {
	Key               string   `json:"key"`
	Type              Type     `json:"type"`
	Default           Value    `json:"default"`
	Min               *int64   `json:"min,omitempty"`
	Max               *int64   `json:"max,omitempty"`
	Unit              string   `json:"unit"`
	Label             string   `json:"label"`
	Description       string   `json:"description"`
	Group             string   `json:"group"`
	SettableBy        string   `json:"settable_by"`
	Editable          bool     `json:"editable"`
	Runtime           string   `json:"runtime"`
	Danger            []string `json:"danger"`
	AllowedValues     []string `json:"allowed_values,omitempty"`
	DisabledValue     *int64   `json:"disabled_value,omitempty"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	Owner             string   `json:"-"`
	Help              string   `json:"-"`
	Consumer          string   `json:"-"`
}

const (
	GroupRetentionStorage      = "retention_storage"
	GroupFiles                 = "files"
	GroupNotificationsPresence = "notifications_presence"
	GroupAgentsDefaults        = "agents_defaults"
	// MaxExpirySeconds is the shipped PROTOCOL §18.3 hard cap, independent of
	// the configured (possibly lower) maximum used for complete-policy validation.
	MaxExpirySeconds int64 = 604800
	RegistryVersion        = "1"
)

var registry = makeRegistry()

func intPointer(n int64) *int64 { return &n }

func makeRegistry() []Definition {
	numeric := func(key string, kind Type, def, min, max int64, group, label, help, consumer string) Definition {
		value := Int(def)
		unit := ""
		if kind == TypeDuration {
			value = Duration(def)
			unit = "seconds"
		}
		return Definition{Key: key, Type: kind, Default: value, Min: intPointer(min), Max: intPointer(max), Unit: unit, Label: label, Description: help, Help: help, Group: group, SettableBy: "operator", Editable: true, Runtime: "live", Danger: []string{}, Owner: "hub", Consumer: consumer}
	}
	gated := func(d Definition, why string) Definition {
		d.Editable = false
		d.Runtime = "unavailable"
		d.UnavailableReason = why
		return d
	}
	later := func(d Definition, why string) Definition {
		d.Editable = false
		d.Runtime = "bootstrap"
		d.UnavailableReason = why
		return d
	}
	danger := func(d Definition, code string) Definition { d.Danger = []string{code}; return d }
	zero := func(d Definition) Definition { d.DisabledValue = intPointer(0); return d }
	defs := []Definition{
		gated(danger(zero(numeric("retention.ack_events", TypeDuration, 604800, 3600, 31536000, GroupRetentionStorage, "Acknowledged event retention", "Keep acknowledged operational events for this long; zero disables pruning. Owner floors and pins still apply.", "DB acknowledged prefix GC")), "retention_shortened"), "DB retention adapter is not installed."),
		gated(danger(zero(numeric("retention.history", TypeDuration, 0, 86400, 315360000, GroupRetentionStorage, "History retention", "Keep message history for this long; zero keeps it forever. Archive mode suppresses history expiry. Ordinary Save does not delete existing data.", "DB message mark/sweep")), "retention_shortened"), "DB retention adapter is not installed."),
		gated(Definition{Key: "retention.archive_mode", Type: TypeBool, Default: Bool(false), Label: "Archive mode", Description: "Protect message history and readable dependencies from expiry; acknowledged event retention remains independent. This does not export history or preserve attachment bytes forever.", Help: "Disabling archive mode requires confirmation; existing-data cleanup needs separate approval.", Group: GroupRetentionStorage, SettableBy: "operator", Danger: []string{"archive_disabled"}, Owner: "hub", Consumer: "DB archive retention adapter"}, "DB retention adapter is not installed."),
		gated(Definition{Key: "blobs.expiry", Type: TypeEnum, Default: Enum("1d"), Label: "Attachment expiry", Description: "Expiry for new attachment grants only. Existing object deadlines remain fixed. Off requires verified keep storage, quota and cost confirmation.", Help: "Object expiry is independent of short download capability deadlines.", Group: GroupFiles, SettableBy: "operator", Danger: []string{"blob_expiry_off"}, AllowedValues: []string{"1h", "6h", "12h", "1d", "7d", "30d", "off"}, Owner: "hub", Consumer: "R2 new attachment grants"}, "R2 expiry adapter is not installed."),
		numeric("events.refresh_interval", TypeDuration, 1800, 60, 86400, GroupNotificationsPresence, "Events refresh interval", "Refresh cadence for MCP Events subscriptions.", "Events refresh worker timers"),
		danger(numeric("presence.default_ttl", TypeDuration, 3600, 60, 86400, GroupNotificationsPresence, "Default presence TTL", "Presence becomes unknown after this interval. Shortening may release held wakes.", "Default presence and held-wake evaluation"), "presence_shortened"),
		danger(numeric("presence.session_ttl", TypeDuration, 900, 60, 86400, GroupNotificationsPresence, "Session presence TTL", "Session presence becomes unknown after this interval. Shortening may release held wakes.", "Session presence and held-wake evaluation"), "presence_shortened"),
		danger(numeric("presence.launcher_ttl", TypeDuration, 900, 60, 86400, GroupNotificationsPresence, "Launcher presence TTL", "Launcher presence becomes unknown after this interval. Shortening may release held wakes.", "Launcher presence and held-wake evaluation"), "presence_shortened"),
		numeric("agents.default_max_depth", TypeInt, 32, 1, 64, GroupAgentsDefaults, "Default maximum depth", "Default maximum loop depth for new messages only; existing loops are unchanged.", "New message loop defaults"),
		numeric("agents.default_ttl", TypeDuration, 86400, 60, 604800, GroupAgentsDefaults, "Default message TTL", "Default lifetime for new messages only; cannot exceed configured MaxExpiry. Existing deadlines are unchanged.", "New message loop defaults"),
		numeric("agents.thread_budget", TypeInt, 500, 1, 10000, GroupAgentsDefaults, "Default thread budget", "Message budget for new threads only. Existing thread budgets are unchanged.", "New thread budget"),
		numeric("rates.sends_per_minute", TypeInt, 60, 1, 600, GroupAgentsDefaults, "Default sends per minute", "For newly created agents; existing explicit per-peer limits remain authoritative.", "New peer send rate default"),
		numeric("rates.threads_per_hour", TypeInt, 20, 1, 200, GroupAgentsDefaults, "Default threads per hour", "For newly created agents; existing explicit per-peer limits remain authoritative.", "New peer thread rate default"),
		numeric("provisioning.creates_per_hour", TypeInt, 60, 1, 600, GroupAgentsDefaults, "Default creates per hour", "Fallback for new provisioning policies; existing explicit principal policies and allowed kinds remain authoritative.", "Principal provisioning policy fallback"),
		later(zero(numeric("provisioning.max_peers", TypeInt, 0, 0, 10000, GroupAgentsDefaults, "Default maximum peers", "Zero disables provisioning under that policy, even with allowed kinds. Absent principal policy still permits no provisioning. Explicit principal policies remain authoritative.", "Principal provisioning policy")), "Provisioning policy migration is deferred; upgrades must not widen authority."),
		later(numeric("rates.anonymous_burst", TypeInt, 30, 1, 300, GroupAgentsDefaults, "Anonymous burst", "Initial token allowance for unauthenticated clients.", "HTTP anonymous limiter"), "Limiter reconfiguration must preserve token balances and is deferred."),
		later(numeric("rates.anonymous_refill", TypeDuration, 2, 1, 60, GroupAgentsDefaults, "Anonymous refill interval", "Time between token refills for unauthenticated clients.", "HTTP anonymous limiter"), "Limiter reconfiguration must preserve token balances and is deferred."),
		later(numeric("rates.avatar_changes_per_hour", TypeInt, 10, 1, 100, GroupFiles, "Avatar changes per hour", "Maximum self-service avatar changes per peer per hour.", "Peer avatar rate limiter"), "Avatar rate consumers have not migrated to preferences."),
		later(numeric("agents.max_expiry", TypeDuration, 604800, 86400, 604800, GroupAgentsDefaults, "Maximum message expiry", "Explicit loop lifetime cap; constrained by the seven-day protocol hard limit.", "Explicit message expiry policy"), "Explicit loop expiry is a protocol policy cap; runtime editing is deferred."),
		later(numeric("agents.text_cap", TypeInt, 4096, 1024, 4096, GroupAgentsDefaults, "Message text cap", "Text bytes before automatic spill to durable attachment storage; bounded by the Message schema hard cap of 4096 bytes.", "Message spill and connector representation"), "Spill and connector representation review is required before editing."),
		later(numeric("files.read_token_ttl", TypeDuration, 604800, 60, 604800, GroupFiles, "File read token TTL", "Legacy file read capability lifetime; independent of object expiry and S3 download grants.", "Legacy blob read capabilities"), "Legacy token consumers have not migrated to preferences."),
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Key < defs[j].Key })
	return defs
}

func cloneDefinition(d Definition) Definition {
	if d.Min != nil {
		d.Min = intPointer(*d.Min)
	}
	if d.Max != nil {
		d.Max = intPointer(*d.Max)
	}
	if d.DisabledValue != nil {
		d.DisabledValue = intPointer(*d.DisabledValue)
	}
	d.Danger = append([]string{}, d.Danger...)
	d.AllowedValues = slices.Clone(d.AllowedValues)
	return d
}

// Definitions returns a stable key-ordered copy, including unavailable keys.
// Adapters may change availability on their own copy, never on this registry.
func Definitions() []Definition {
	out := make([]Definition, len(registry))
	for i, d := range registry {
		out[i] = cloneDefinition(d)
	}
	return out
}

func Lookup(key string) (Definition, error) {
	i := sort.Search(len(registry), func(i int) bool { return registry[i].Key >= key })
	if i == len(registry) || registry[i].Key != key {
		return Definition{}, unknownKey()
	}
	return cloneDefinition(registry[i]), nil
}

// Validate checks scalar constraints, independent of runtime availability.
// This allows unavailable descriptors to describe their real defaults while
// the hub service separately enforces availability and deployment authority.
func Validate(def Definition, value Value) error {
	if def.Type == TypeDuration && value.kind == TypeInt {
		value.kind = TypeDuration
	}
	if value.kind != def.Type {
		return invalid(def.Key, "type")
	}
	switch def.Type {
	case TypeBool:
		return nil
	case TypeInt, TypeDuration:
		n := value.integer
		if n < 0 {
			return invalid(def.Key, "nonnegative")
		}
		if n == 0 {
			if def.DisabledValue != nil && *def.DisabledValue == 0 {
				return nil
			}
			return invalid(def.Key, "zero_not_allowed")
		}
		if def.Min == nil || def.Max == nil || n < *def.Min || n > *def.Max {
			return invalid(def.Key, "range")
		}
		return nil
	case TypeEnum:
		if slices.Contains(def.AllowedValues, value.enum) && knownEnum(value.enum) {
			return nil
		}
		return invalid(def.Key, "enum")
	default:
		return invalid(def.Key, "type")
	}
}

func normalize(def Definition, value Value) Value {
	if def.Type == TypeDuration && value.kind == TypeInt {
		value.kind = TypeDuration
	}
	return value
}

// ValidatePolicy overlays values on defaults and validates the entire policy.
// maxExpirySeconds is the deployment's configured MaxExpiry, not a writable
// preference. A prospective lower agents.max_expiry further narrows the cap.
func ValidatePolicy(values map[string]Value, maxExpirySeconds int64) error {
	if maxExpirySeconds <= 0 || maxExpirySeconds > MaxExpirySeconds {
		return invalid("agents.max_expiry", "protocol_cap")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		d, err := Lookup(key)
		if err != nil {
			return err
		}
		if err := Validate(d, value); err != nil {
			return err
		}
	}
	ttlDef, _ := Lookup("agents.default_ttl")
	ttl := ttlDef.Default.integer
	if v, ok := values["agents.default_ttl"]; ok {
		ttl = v.integer
	}
	if v, ok := values["agents.max_expiry"]; ok && v.integer < maxExpirySeconds {
		maxExpirySeconds = v.integer
	}
	if ttl > maxExpirySeconds {
		return invalid("agents.default_ttl", "max_expiry")
	}
	return nil
}
