package prefs_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/prefs"
)

// Literal expectations come from Decision 4, checked against the merged
// protocol schema and hub/config constants. Zero is an explicit opt-out only.
func TestRegistryDefaultsAndBounds(t *testing.T) {
	rows := []struct {
		key, kind, def string
		min, max       int64
		zero, editable bool
	}{
		{"retention.ack_events", "duration", "604800", 3600, 31536000, true, false},
		{"retention.history", "duration", "0", 86400, 315360000, true, false},
		{"retention.archive_mode", "bool", "false", 0, 0, false, false},
		{"blobs.expiry", "enum", `"1d"`, 0, 0, false, false},
		{"events.refresh_interval", "duration", "1800", 60, 86400, false, true},
		{"presence.default_ttl", "duration", "3600", 60, 86400, false, true},
		{"presence.grokbot_ttl", "duration", "600", 60, 86400, false, true},
		{"presence.session_ttl", "duration", "900", 60, 86400, false, true},
		{"presence.launcher_ttl", "duration", "900", 60, 86400, false, true},
		{"agents.default_max_depth", "int", "32", 1, 64, false, true},
		{"agents.default_ttl", "duration", "86400", 60, 604800, false, true},
		{"agents.thread_budget", "int", "500", 1, 10000, false, true},
		{"rates.sends_per_minute", "int", "60", 1, 600, false, true},
		{"rates.threads_per_hour", "int", "20", 1, 200, false, true},
		{"provisioning.creates_per_hour", "int", "60", 1, 600, false, true},
		{"provisioning.max_peers", "int", "0", 0, 10000, true, false},
		{"rates.anonymous_burst", "int", "30", 1, 300, false, false},
		{"rates.anonymous_refill", "duration", "2", 1, 60, false, false},
		{"rates.avatar_changes_per_hour", "int", "10", 1, 100, false, false},
		{"agents.max_expiry", "duration", "604800", 86400, 604800, false, false},
		{"agents.text_cap", "int", "4096", 1024, 4096, false, false},
		{"files.read_token_ttl", "duration", "604800", 60, 604800, false, false},
	}
	if got := len(prefs.Definitions()); got != len(rows) {
		t.Fatalf("registry has %d keys; want %d", got, len(rows))
	}
	for _, r := range rows {
		t.Run(r.key, func(t *testing.T) {
			d, err := prefs.Lookup(r.key)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(d.Default)
			if err != nil {
				t.Fatal(err)
			}
			if string(d.Type) != r.kind || string(b) != r.def || d.Editable != r.editable {
				t.Fatalf("type/default/editable = %s/%s/%t", d.Type, b, d.Editable)
			}
			if err := prefs.Validate(d, d.Default); err != nil {
				t.Fatal(err)
			}
			if r.kind == "int" || r.kind == "duration" {
				if d.Min == nil || d.Max == nil || *d.Min != r.min || *d.Max != r.max {
					t.Fatalf("bounds differ: %+v", d)
				}
				for _, v := range []int64{r.min, r.max} {
					if err := prefs.Validate(d, prefs.Int(v)); err != nil {
						t.Fatalf("boundary %d: %v", v, err)
					}
				}
				for _, v := range []int64{-1, r.max + 1} {
					if err := prefs.Validate(d, prefs.Int(v)); err == nil {
						t.Fatalf("accepted out of range %d", v)
					}
				}
				if r.min > 1 {
					if err := prefs.Validate(d, prefs.Int(r.min-1)); err == nil {
						t.Fatal("accepted below minimum")
					}
				}
				if got := prefs.Validate(d, prefs.Int(0)) == nil; got != r.zero {
					t.Fatalf("zero valid=%t, want %t", got, r.zero)
				}
				if (d.DisabledValue != nil) != r.zero {
					t.Fatal("disabled_value declaration differs")
				}
				if r.zero && *d.DisabledValue != 0 {
					t.Fatal("disabled_value must be zero")
				}
			}
			if r.kind == "duration" && d.Unit != "seconds" {
				t.Fatal("duration is not seconds")
			}
			if err := prefs.Validate(d, prefs.Enum("https://invalid.example/private")); err == nil {
				t.Fatal("accepted arbitrary string")
			}
			if r.kind != "bool" {
				if err := prefs.Validate(d, prefs.Bool(false)); err == nil {
					t.Fatal("accepted wrong bool type")
				}
			}
			if r.kind == "bool" {
				for _, v := range []prefs.Value{prefs.Bool(false), prefs.Bool(true)} {
					if err := prefs.Validate(d, v); err != nil {
						t.Fatal(err)
					}
				}
				if prefs.Validate(d, prefs.Int(0)) == nil {
					t.Fatal("bool accepted integer")
				}
			}
		})
	}
}

func TestProvisioningMaxPeersZeroSemantics(t *testing.T) {
	d, err := prefs.Lookup("provisioning.max_peers")
	if err != nil {
		t.Fatal(err)
	}
	const want = "Zero disables provisioning under that policy, even with allowed kinds. Absent principal policy still permits no provisioning. Explicit principal policies remain authoritative."
	if d.Description != want || d.Help != want {
		t.Fatalf("zero provisioning semantics: description=%q help=%q; want %q", d.Description, d.Help, want)
	}
	if d.Default != prefs.Int(0) || d.DisabledValue == nil || *d.DisabledValue != 0 {
		t.Fatal("zero must remain the default and explicitly disable provisioning")
	}
	if err := prefs.Validate(d, prefs.Int(0)); err != nil {
		t.Fatalf("zero must remain valid: %v", err)
	}
	if d.Editable || d.Runtime != "bootstrap" || d.UnavailableReason == "" {
		t.Fatal("provisioning.max_peers must remain deferred and read-only")
	}
}

func TestRegistryMetadataOrder(t *testing.T) {
	defs := prefs.Definitions()
	keys := make([]string, len(defs))
	groups := map[string]bool{"retention_storage": true, "files": true, "notifications_presence": true, "agents_defaults": true}
	for i, d := range defs {
		keys[i] = d.Key
		if d.Owner == "" || d.Consumer == "" || d.Label == "" || d.Description == "" || d.Help == "" || !groups[d.Group] || d.SettableBy != "operator" || d.Runtime == "" {
			t.Fatalf("incomplete metadata: %+v", d)
		}
		if !d.Editable && d.UnavailableReason == "" {
			t.Fatalf("missing unavailable reason: %s", d.Key)
		}
		switch d.Type {
		case prefs.TypeBool, prefs.TypeInt, prefs.TypeDuration, prefs.TypeEnum:
		default:
			t.Fatalf("unsafe type %s", d.Type)
		}
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("unstable key order")
	}
	if !reflect.DeepEqual(defs, prefs.Definitions()) {
		t.Fatal("metadata changed between reads")
	}
	// Registry callers must not be able to change shared metadata through slices or pointers.
	for i := range defs {
		if defs[i].Min != nil {
			*defs[i].Min = -999
		}
		if len(defs[i].AllowedValues) > 0 {
			defs[i].AllowedValues[0] = "bad"
		}
		if len(defs[i].Danger) > 0 {
			defs[i].Danger[0] = "bad"
		}
	}
	d, _ := prefs.Lookup("events.refresh_interval")
	if *d.Min != 60 {
		t.Fatal("mutated registry minimum")
	}
	d, _ = prefs.Lookup("blobs.expiry")
	if d.AllowedValues[0] != "1h" {
		t.Fatal("mutated registry enum")
	}
	for _, key := range []string{"theme", "density", "blobs.storage_quota", "r2.storage_quota_bytes", "unknown"} {
		if _, err := prefs.Lookup(key); !errors.Is(err, prefs.ErrUnknownKey) {
			t.Fatalf("lookup %s: %v", key, err)
		}
	}
}

func TestBlobExpiryEnum(t *testing.T) {
	d, _ := prefs.Lookup("blobs.expiry")
	want := []string{"1h", "6h", "12h", "1d", "7d", "30d", "off"}
	if !reflect.DeepEqual(d.AllowedValues, want) {
		t.Fatalf("enum=%v", d.AllowedValues)
	}
	for _, v := range want {
		if err := prefs.Validate(d, prefs.Enum(v)); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []prefs.Value{prefs.Enum("0"), prefs.Enum("forever"), prefs.Int(0), prefs.Duration(86400)} {
		if prefs.Validate(d, v) == nil {
			t.Fatal("accepted invalid expiry")
		}
	}
}

func TestCompletePolicyTTLBound(t *testing.T) {
	for _, tc := range []struct {
		ttl, max int64
		ok       bool
	}{{3600, 3600, true}, {3601, 3600, false}, {86400, 604800, true}, {604800, 604800, true}, {60, 0, false}, {60, 604801, false}} {
		err := prefs.ValidatePolicy(map[string]prefs.Value{"agents.default_ttl": prefs.Duration(tc.ttl)}, tc.max)
		if (err == nil) != tc.ok {
			t.Fatalf("ttl=%d max=%d: %v", tc.ttl, tc.max, err)
		}
	}
	if err := prefs.ValidatePolicy(nil, 3600); err == nil {
		t.Fatal("failed to validate default TTL against configured cap")
	}
	if err := prefs.ValidatePolicy(map[string]prefs.Value{"unknown": prefs.Int(1)}, 604800); !errors.Is(err, prefs.ErrUnknownKey) {
		t.Fatalf("unknown: %v", err)
	}
	if err := prefs.ValidatePolicy(map[string]prefs.Value{"agents.max_expiry": prefs.Duration(86400), "agents.default_ttl": prefs.Duration(86401)}, 604800); err == nil {
		t.Fatal("ignored prospective MaxExpiry")
	}
}

func TestSnapshotGetAndImmutability(t *testing.T) {
	values := map[string]prefs.Effective{"events.refresh_interval": {Value: prefs.Duration(600), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL", Revision: 2}}
	defs := prefs.Definitions()
	s, err := prefs.NewSnapshot(12, `"prefs-12-test"`, defs, values)
	if err != nil {
		t.Fatal(err)
	}
	values["events.refresh_interval"] = prefs.Effective{Value: prefs.Duration(60), Source: prefs.SourceStored}
	defs[0].Label = "mutated"
	e, err := s.Get("events.refresh_interval")
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != prefs.Duration(600) || e.Source != prefs.SourceFlag || e.Origin != "VECNET_EVENTS_REFRESH_INTERVAL" || e.Revision != 12 {
		t.Fatalf("wrong effective: %+v", e)
	}
	for _, d := range prefs.Definitions() {
		e, err := s.Get(d.Key)
		if err != nil || e.Revision != 12 {
			t.Fatalf("%s revision: %+v / %v", d.Key, e, err)
		}
	}
	if _, err := s.Get("unknown"); !errors.Is(err, prefs.ErrUnknownKey) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := prefs.NewSnapshot(0, "", prefs.Definitions(), map[string]prefs.Effective{"unknown": {Value: prefs.Int(1)}}); !errors.Is(err, prefs.ErrUnknownKey) {
		t.Fatal("unknown snapshot key accepted")
	}
	for _, source := range []prefs.Source{"env", "saved", "edited", "inherited", ""} {
		_, err := prefs.NewSnapshot(0, "", prefs.Definitions(), map[string]prefs.Effective{"events.refresh_interval": {Value: prefs.Duration(60), Source: source}})
		if err == nil {
			t.Fatalf("accepted source %s", source)
		}
	}
}

func TestShippedMessageTextHardCap(t *testing.T) {
	d, err := prefs.Lookup("agents.text_cap")
	if err != nil {
		t.Fatal(err)
	}
	if err := prefs.Validate(d, prefs.Int(4097)); err == nil {
		t.Fatal("accepted text cap above the Message schema's 4096-byte hard cap")
	}
}

func TestGrokTTLRegistryMetadata(t *testing.T) {
	if prefs.RegistryVersion != "2" {
		t.Errorf("registry contract version = %s, want 2", prefs.RegistryVersion)
	}
	d, err := prefs.Lookup("presence.grokbot_ttl")
	if err != nil {
		t.Fatal(err)
	}
	if d.Default != prefs.Duration(600) || d.Type != prefs.TypeDuration || d.Unit != "seconds" ||
		d.Min == nil || *d.Min != 60 || d.Max == nil || *d.Max != 86400 ||
		d.Group != "notifications_presence" || d.SettableBy != "operator" || !d.Editable ||
		d.Runtime != "live" || d.UnavailableReason != "" || d.DisabledValue != nil ||
		!reflect.DeepEqual(d.Danger, []string{"presence_shortened"}) {
		t.Fatalf("Grok TTL metadata does not match the literal contract: %+v", d)
	}
	description := strings.ToLower(d.Description)
	if !strings.Contains(description, "inherit") || !strings.Contains(description, "presence.default_ttl") || !strings.Contains(description, "explicit") {
		t.Fatal("Grok TTL description must explain inheritance from explicit presence.default_ttl")
	}
}
