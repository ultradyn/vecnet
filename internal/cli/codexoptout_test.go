package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexUnbindPersistsOptOut(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	f.ok("--net", "p", "unbind")
	raw, err := os.ReadFile(f.path + ".optout")
	if err != nil {
		t.Fatal("unbind must persist opt-out independently of authority:", err)
	}
	var data struct {
		Version  int                                    `json:"version"`
		Disabled []struct{ Config, Profile, As string } `json:"disabled"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.Version != 1 || len(data.Disabled) != 1 || data.Disabled[0].Profile != "p" {
		t.Fatalf("opt-out = %s, %v", raw, err)
	}
	// The strict authority schema is deliberately unchanged.
	if _, err := readCodexAuthority(f.path); err != nil {
		t.Fatal("opt-out invalidated ownership authority:", err)
	}
	f.bind("p", bindT1)
	raw, err = os.ReadFile(f.path + ".optout")
	if err != nil || json.Unmarshal(raw, &data) != nil || len(data.Disabled) != 0 {
		t.Fatalf("explicit bind did not clear opt-out: %s, %v", raw, err)
	}
}

func TestCodexOptOutAddressedOnly(t *testing.T) {
	f := newBindFixture(t)
	f.ok("--net", "p", "unbind")
	f.ok("--net", "q", "unbind")
	f.bind("p", bindT1)
	file, err := readCodexOptOuts(f.path)
	if err != nil || file.disabled(f.env["VECNET_CONFIG"], "p", "") || !file.disabled(f.env["VECNET_CONFIG"], "q", "") || file.disabled(f.env["VECNET_CONFIG"]+"other", "q", "") || file.disabled(f.env["VECNET_CONFIG"], "q", "peer_other") {
		t.Fatalf("wrong opt-out scope: %+v, %v", file, err)
	}
}

func TestCodexOptOutFencesSavedEnrollment(t *testing.T) {
	for _, duringRuntime := range []bool{false, true} {
		t.Run(map[bool]string{false: "already-unbound", true: "unbind-during-runtime"}[duringRuntime], func(t *testing.T) {
			f := newBindFixture(t)
			f.now = time.Now().UTC()
			if err := os.Chmod(f.env["HOME"], 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			f.env["CODEX_THREAD_ID"] = bindT1
			f.bind("p", bindT1)
			var calls atomic.Int32
			a := &app{ctx: t.Context(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string { return f.env[k] }}
			r, err := a.prepareLocalEnrollment("p")
			if err != nil {
				t.Fatal(err)
			}
			commitEnrollmentFixture(t, a, r, "peer_A")
			a.localRuntime = func(context.Context) error {
				calls.Add(1)
				if duringRuntime {
					f.ok("--net", "p", "unbind")
				}
				return nil
			}
			if !duringRuntime {
				f.ok("--net", "p", "unbind")
			}
			got := a.reconcileLocalEnrollment(*r)
			if !strings.Contains(got.Reason, "disabled by unbind") {
				t.Fatalf("saved intent bypassed opt-out: %+v", got)
			}
			if !duringRuntime && calls.Load() != 0 {
				t.Fatal("disabled enrollment activated runtime")
			}
			if len(f.entries()) != 0 {
				t.Fatal("repair rebound opted-out profile")
			}
			if err := a.retryLocalEnrollments(); err != nil {
				t.Fatalf("disabled enrollment must stop retrying: %v", err)
			}
			if len(f.entries()) != 0 {
				t.Fatal("second retry rebound opted-out profile")
			}
			file, err := readCodexOptOuts(f.path)
			if err != nil || !file.disabled(f.env["VECNET_CONFIG"], "p", "") {
				t.Fatalf("retry cleared opt-out: %+v %v", file, err)
			}
		})
	}
}

func TestCodexOptOutCorruptionRefusesReacquisition(t *testing.T) {
	f := newBindFixture(t)
	f.ok("--net", "p", "unbind")
	before := f.raw()
	for _, raw := range []string{`{}`, `{"version":1}`, `{"version":1,"disabled":null}`, `{"version":1,"disabled":[],"Disabled":[]}`, `{"version":1,"disabled":[],"future":true}`} {
		if err := os.WriteFile(f.path+".optout", []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		code, _, _ := f.run("--net", "p", "bind", "--thread", bindT1)
		if code == 0 || !bytes.Equal(before, f.raw()) {
			t.Fatalf("unsafe opt-out admitted bind: %s", raw)
		}
	}
}
