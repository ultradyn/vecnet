package prefs_test

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/prefs"
)

func TestValueStrictJSON(t *testing.T) {
	for _, raw := range []string{`1.5`, `1.0`, `1e3`, `-1`, `null`, `9223372036854775808`, `true false`, `[]`, `{}`, `"free text"`, `"https://invalid.example/private"`} {
		var v prefs.Value
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			t.Fatalf("accepted invalid scalar %s", raw)
		}
	}
	for _, raw := range []string{`true`, `false`, `0`, `9223372036854775807`, `"off"`, `"1d"`} {
		var v prefs.Value
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(v)
		if err != nil || string(b) != raw {
			t.Fatalf("scalar round trip %s: %s / %v", raw, b, err)
		}
	}
	if _, err := json.Marshal(prefs.Value{}); err == nil {
		t.Fatal("uninitialized scalar encoded")
	}
	if _, err := json.Marshal(prefs.Int(-1)); err == nil {
		t.Fatal("negative scalar encoded")
	}
	if _, err := json.Marshal(prefs.Enum("arbitrary")); err == nil {
		t.Fatal("arbitrary enum encoded")
	}
}

func TestPatchStrictJSON(t *testing.T) {
	invalid := []string{
		`{}`, `null`, `[]`,
		`{"set":null}`, `{"reset":null}`, `{"confirm":null}`, `{"apply_existing":null}`,
		`{"set":{"events.refresh_interval":60},"unknown":true}`,
		`{"set":{"events.refresh_interval":60},"Set":{}}`,
		`{"set":{"events.refresh_interval":60},"set":{"events.refresh_interval":120}}`,
		`{"set":{"events.refresh_interval":60,"events.refresh_interval":120}}`,
		`{"set":{"events.refresh_interval":60,"events.\u0072efresh_interval":120}}`,
		`{"set":{"unknown":true}}`, `{"reset":["unknown"]}`,
		`{"reset":["events.refresh_interval","events.refresh_interval"]}`,
		`{"set":{"events.refresh_interval":60},"reset":["events.refresh_interval"]}`,
		`{"set":{"events.refresh_interval":1.5}}`, `{"set":{"events.refresh_interval":1.0}}`,
		`{"set":{"events.refresh_interval":1e3}}`, `{"set":{"events.refresh_interval":-1}}`,
		`{"set":{"events.refresh_interval":null}}`, `{"set":{"events.refresh_interval":9223372036854775808}}`,
		`{"set":{"events.refresh_interval":"600"}}`, `{"set":{"events.refresh_interval":true}}`,
		`{"set":{"blobs.expiry":0}}`, `{"set":{"retention.archive_mode":0}}`,
		`{"set":{"events.refresh_interval":60},"confirm":true}`,
		`{"set":{"events.refresh_interval":60},"confirm":[null]}`,
		`{"set":{"events.refresh_interval":60},"reset":[null]}`,
		`{"set":{"events.refresh_interval":60},"apply_existing":"true"}`,
		`{"set":{"events.refresh_interval":60},"reset":[],"reset":[]}`,
	}
	for i, raw := range invalid {
		var p prefs.Patch
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			t.Errorf("invalid patch %d accepted", i)
		}
	}
	valid := []string{
		`{"set":{"events.refresh_interval":600},"reset":[],"confirm":[],"apply_existing":false}`,
		`{"reset":["events.refresh_interval"]}`,
		`{"set":{"retention.archive_mode":false,"retention.history":0,"blobs.expiry":"off"}}`,
	}
	for _, raw := range valid {
		var p prefs.Patch
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var again prefs.Patch
		if err := json.Unmarshal(b, &again); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p, again) {
			t.Fatal("patch round trip changed values")
		}
	}
	var p prefs.Patch
	if err := json.Unmarshal([]byte(valid[0]), &p); err != nil {
		t.Fatal(err)
	}
	if p.Set["events.refresh_interval"] != prefs.Duration(600) {
		t.Fatal("duration not tagged after wire decode")
	}
	// A failed decode must not partially replace an existing valid patch.
	before, _ := json.Marshal(p)
	if err := json.Unmarshal([]byte(`{"set":{"unknown":1}}`), &p); err == nil {
		t.Fatal("unknown key accepted")
	}
	after, _ := json.Marshal(p)
	if string(before) != string(after) {
		t.Fatal("failed decode mutated receiver")
	}
	if err := json.Unmarshal([]byte(`{"set":{"unknown":1}}`), &p); !errors.Is(err, prefs.ErrUnknownKey) {
		t.Fatalf("unknown error=%v", err)
	}
	for _, raw := range []string{`{"set":{"events.refresh_interval":"rejected-sensitive-value"}}`, `{"set":{"secret-unknown-key":1}}`} {
		err := json.Unmarshal([]byte(raw), &p)
		if err == nil || strings.Contains(err.Error(), "rejected-sensitive-value") || strings.Contains(err.Error(), "secret-unknown-key") {
			t.Fatal("raw rejected request disclosed")
		}
	}
}

func TestPreferenceWireFixtures(t *testing.T) {
	raw, err := os.ReadFile("../opapi/testdata/preferences.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Snapshot opapi.Preferences            `json:"snapshot"`
		Patch    opapi.PreferencesPatch       `json:"patch"`
		Preview  opapi.PreferencesPreview     `json:"preview"`
		Details  opapi.PreferenceErrorDetails `json:"details"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("wire fixture changed: %s", out)
	}
	e, err := fixture.Snapshot.Get("events.refresh_interval")
	if err != nil {
		t.Fatal(err)
	}
	if e.Revision != 12 || e.Source != prefs.SourceFlag || e.Value != prefs.Duration(1800) {
		t.Fatal("fixture effective metadata lost")
	}
	if fixture.Snapshot.ETag != "" {
		t.Fatal("ETag must remain an HTTP header")
	}
	if fixture.Preview.Impact.HighWater == nil || *fixture.Preview.Impact.HighWater != 42 || fixture.Preview.Impact.NotBefore != "2026-11-06T02:00:00.000Z" {
		t.Fatal("preview boundaries lost")
	}
}

func TestSnapshotWireDescriptorNames(t *testing.T) {
	s, err := prefs.NewSnapshot(3, `"prefs-3-test"`, prefs.Definitions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Revision    uint64                       `json:"revision"`
		Preferences []map[string]json.RawMessage `json:"preferences"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Revision != 3 || len(wire.Preferences) != 21 {
		t.Fatal("incomplete snapshot")
	}
	for _, d := range wire.Preferences {
		for _, key := range []string{"key", "type", "value", "source", "origin", "default", "unit", "label", "description", "group", "settable_by", "editable", "runtime", "danger"} {
			if _, ok := d[key]; !ok {
				t.Fatalf("missing descriptor field %s", key)
			}
		}
		for _, key := range []string{"owner", "consumer", "help", "etag", "revision"} {
			if _, ok := d[key]; ok {
				t.Fatalf("unexpected descriptor field %s", key)
			}
		}
		if string(d["danger"]) == "null" {
			t.Fatal("null danger list")
		}
	}
	var decoded prefs.Snapshot
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	e, _ := decoded.Get("retention.history")
	if e.Revision != 3 || e.Value != prefs.Duration(0) || e.Source != prefs.SourceDefault {
		t.Fatal("zero/default lost")
	}
	b, err = json.Marshal(prefs.ErrorDetails{})
	if err != nil || string(b) != "{}" {
		t.Fatal("error details are not an empty object")
	}
}

func TestPatchErrorCodesAndLimits(t *testing.T) {
	for _, tc := range []struct{ raw, code, field string }{
		{`{"set":{"events.refresh_interval":"rejected-sensitive-value"}}`, prefs.CodeInvalidValue, "events.refresh_interval"},
		{`{"set":{"events.refresh_interval":1.5}}`, prefs.CodeInvalidValue, "events.refresh_interval"},
		{`{"set":{"retention.archive_mode":42}}`, prefs.CodeInvalidValue, "retention.archive_mode"},
		{`{"set":{"blobs.expiry":"invalid-expiry"}}`, prefs.CodeInvalidValue, "blobs.expiry"},
		{`{"set":{"events.refresh_interval":59}}`, prefs.CodeInvalidValue, "events.refresh_interval"},
		{`{"set":{"unknown-key":60}}`, prefs.CodeUnknownKey, ""},
		{`{"set":{"events.refresh_interval":60},"unknown":true}`, prefs.CodeInvalidBody, ""},
	} {
		var p prefs.Patch
		err := json.Unmarshal([]byte(tc.raw), &p)
		var pe *prefs.Error
		if !errors.As(err, &pe) || pe.Code != tc.code || pe.Details.Field != tc.field {
			t.Fatalf("code/field: got %v; want %s/%s", err, tc.code, tc.field)
		}
	}
	var p prefs.Patch
	raw := `{"set":{"events.refresh_interval":60},"confirm":["` + strings.Repeat("a", 16384) + `"]}`
	var pe *prefs.Error
	if err := json.Unmarshal([]byte(raw), &p); !errors.As(err, &pe) || pe.Code != prefs.CodePayloadTooLarge {
		t.Fatal("oversize patch accepted")
	}
	resets := make([]string, 65)
	for i := range resets {
		resets[i] = "events.refresh_interval"
	}
	rawBytes, err := json.Marshal(map[string][]string{"reset": resets})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawBytes, &p); !errors.Is(err, prefs.ErrInvalidBody) {
		t.Fatal("65 operations accepted")
	}
}

func TestSourceVocabulary(t *testing.T) {
	for _, raw := range []string{`"env"`, `"saved"`, `"edited"`, `"inherited"`, `null`, `42`} {
		var source prefs.Source
		if err := json.Unmarshal([]byte(raw), &source); err == nil {
			t.Fatal("noncanonical wire source accepted")
		}
	}
	for _, source := range []prefs.Source{prefs.SourceDefault, prefs.SourceFlag, prefs.SourceStored} {
		b, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var decoded prefs.Source
		if err := json.Unmarshal(b, &decoded); err != nil || decoded != source {
			t.Fatal("canonical source did not round trip")
		}
	}
}

func TestSnapshotCopiesDescriptorsAndRejectsUnsafeOrigin(t *testing.T) {
	s, err := prefs.NewSnapshot(12, "", prefs.Definitions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := s.Descriptors()
	for i := range descriptors {
		if descriptors[i].Min != nil {
			*descriptors[i].Min = 0
		}
		if len(descriptors[i].AllowedValues) > 0 {
			descriptors[i].AllowedValues[0] = "corrupted"
		}
		if len(descriptors[i].Danger) > 0 {
			descriptors[i].Danger[0] = "corrupted"
		}
	}
	for _, d := range s.Descriptors() {
		if d.Min != nil && *d.Min == 0 && d.Key != "provisioning.max_peers" {
			t.Fatal("snapshot min mutated")
		}
		if len(d.AllowedValues) > 0 && d.AllowedValues[0] != "1h" {
			t.Fatal("snapshot allowed_values mutated")
		}
		if len(d.Danger) > 0 && d.Danger[0] == "corrupted" {
			t.Fatal("snapshot dangers mutated")
		}
	}
	for _, origin := range []string{"https://invalid.example/private", "--events-refresh-interval=value", "VECNET_EVENTS_REFRESH_INTERVAL=value", "Bearer credential"} {
		_, err := prefs.NewSnapshot(0, "", prefs.Definitions(), map[string]prefs.Effective{"events.refresh_interval": {Value: prefs.Duration(60), Source: prefs.SourceFlag, Origin: origin}})
		if err == nil || strings.Contains(err.Error(), origin) {
			t.Fatal("unsafe provenance accepted or disclosed")
		}
	}
}
