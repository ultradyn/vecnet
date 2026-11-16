package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

func TestHookLegacyReservationBlocksAckUntilProof(t *testing.T) {
	for _, reservationInPage := range []bool{false, true} {
		t.Run(fmt.Sprint(reservationInPage), func(t *testing.T) {
			var acks atomic.Int32
			env, path := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/ack" {
					acks.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				events := []envelope.Event{hookEvent(2, "later delivery")}
				if reservationInPage {
					events = append([]envelope.Event{hookEvent(1, "unprinted reservation")}, events...)
				}
				hookPage(w, events, "e1.2")
			})
			seedHook(t, path, "e1.0", "dly_1")
			out, _ := runHook(t, env, "--event", "user-prompt", "--auto-ack")
			s := readHookState(t, path)
			if acks.Load() != 0 {
				t.Fatalf("acknowledged unproved legacy reservation: %s %+v", out, s)
			}
			if s.PollCursor != "e1.0" {
				t.Fatalf("advanced past unproved reservation: %+v", s)
			}
			cfg, err := loadConfig(env["VECNET_CONFIG"])
			if err != nil {
				t.Fatal(err)
			}
			if err := (stateFile{path}).update(t.Context(), func(s *hookState) error {
				s.bind(hookIdentity{Hub: normalizeHub(cfg.Profiles["p"].Hub), Caller: "peer_test", Cred: hookCredential("vn_peer_test")})
				s.recordDisplay("dly_1")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			out, _ = runHook(t, env, "--event", "user-prompt", "--auto-ack")
			if acks.Load() != 1 || strings.Contains(out, "unprinted reservation") {
				t.Fatalf("validated proof did not unblock ack without duplication: ack=%d output=%s", acks.Load(), out)
			}
		})
	}
}

func TestHookFirstDisplayBootstrapsProofBeforeAck(t *testing.T) {
	var acks atomic.Int32
	var path string
	env, state := hookFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ack" {
			s := readHookState(t, path)
			if s.Caller != "peer_test" || s.Hub == "" || !slices.Contains(s.Displayed, "dly_1") {
				t.Errorf("ack before bound positive proof: %+v", s)
			}
			acks.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		hookPage(w, []envelope.Event{hookEvent(1, "first legitimate display")}, "e1.1")
	})
	path = state
	seedHook(t, path, "e1.0")
	out, _ := runHook(t, env, "--event", "user-prompt", "--auto-ack")
	if acks.Load() != 1 || !strings.Contains(out, "first legitimate display") {
		t.Fatalf("first display bootstrap failed: %s ack=%d", out, acks.Load())
	}
}

func TestWatchPartialIdentityEvidenceDefersUnchanged(t *testing.T) {
	for _, identity := range []string{`"caller":"peer_other",`, `"caller":"peer_test",`, `"hub":%q,`, ``} {
		for _, evidence := range []string{`"displayed":["dly_1"]`, `"claims":[{"ID":"dly_1","Claim":"pending","Accepted":true}]`, `"replay":[{"ID":"dly_1","After":"opaque"}]`} {
			t.Run(identity+evidence, func(t *testing.T) {
				f := newDisplayFixture(t)
				scope := identity
				if strings.Contains(scope, "%q") {
					scope = fmt.Sprintf(scope, f.hub)
				}
				raw := []byte(`{` + scope + `"shown":[],` + evidence + `}`)
				if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				out := new(hookWatchOutput)
				stream, stop := f.watch(t, out, true)
				stream.frames <- []envelope.Event{f.delivery()}
				waitDisplayBlocked(t, f, stream, out)
				stop()
				after, err := os.ReadFile(f.path)
				if err != nil || string(after) != string(raw) {
					t.Fatalf("partial identity evidence mutated: %s -> %s: %v", raw, after, err)
				}
			})
		}
	}
}

func TestMonitorEmptyLegacyStateBootstrapsFirstDisplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := cliFixtureWriteFile(path, []byte(`{"shown":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	f := stateFile{path}
	id := hookIdentity{Hub: "http://hub", Caller: "peer_test", Cred: "credential"}
	if err := f.prepareDisplay(t.Context(), &id, false, nil); err != nil {
		t.Fatal(err)
	}
	printed := 0
	if err := f.displayFor(t.Context(), &id, "dly_first", false, func() error { printed++; return nil }); err != nil {
		t.Fatal(err)
	}
	s := readHookState(t, path)
	if printed != 1 || s.Hub != id.Hub || s.Caller != id.Caller || !slices.Contains(s.Displayed, "dly_first") {
		t.Fatalf("first display bootstrap failed: %+v %d", s, printed)
	}
	raw, _ := os.ReadFile(path)
	if !json.Valid(raw) {
		t.Fatal("invalid bootstrapped state")
	}
}
