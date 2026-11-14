package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDisplayProofDoesNotReleaseClaims(t *testing.T) {
	s := hookState{Shown: []string{"reservation"}, Claims: []claimRef{{ID: "pending", Claim: "nonce", Generation: "generation", Accepted: true}}, Replay: []replayRef{{ID: "unread"}}}
	s.recordDisplay("printed")
	if len(s.Claims) != 1 || !s.Claims[0].Accepted || len(s.Replay) != 1 || len(s.Shown) != 1 || s.has("reservation") == false || !s.has("pending") || !s.has("printed") {
		t.Fatalf("proof mutated reservations or lost ownership: %+v", s)
	}
	for i := 0; i < shownWindow+1; i++ {
		s.recordDisplay(string(rune(i + 1)))
	}
	if len(s.Displayed) != shownWindow || !s.has("pending") {
		t.Fatal("proof window evicted unresolved claim")
	}
	s.bind(hookIdentity{Hub: "http://old", Caller: "peer_A", Cred: "old"})
	s.bind(hookIdentity{Hub: "http://new", Caller: "peer_A", Cred: "new"})
	if len(s.Displayed) != 0 {
		t.Fatal("foreign identity retained display proof")
	}
}

func TestDisplayCommitFailureCannotRetryOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	id := hookIdentity{Hub: "http://hub", Caller: "peer_A", Cred: "credential"}
	f := stateFile{path}
	if err := f.prepareDisplay(t.Context(), &id, true, nil); err != nil {
		t.Fatal(err)
	}
	called := 0
	err := f.displayFor(t.Context(), &id, "printed", false, func() error {
		called++
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Mkdir(path, 0700) // fail the state rename after successful output
	})
	if !errors.Is(err, errWatchOutput) || called != 1 {
		t.Fatalf("post-output failure was retryable: %v %d", err, called)
	}
	err = f.displayFor(t.Context(), &id, "printed", false, func() error { called++; return nil })
	if !errors.Is(err, errDisplayDeferred) || called != 1 {
		t.Fatalf("uncertain proof retried stdout: %v %d", err, called)
	}
}

func TestStrictStateDisplayPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte(`{"Hub":"http://hub","Caller":"peer_A","Shown":[],"Displayed":["prior"],"future":{"keep":true},"Replay":[{"id":"unread","after":"opaque","future_entry":{"keep":true}}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := codexUpdateState(context.Background(), path, false, func(s *hookState) error { s.recordDisplay("printed"); return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := validateCodexStateJSON(got); err != nil {
		t.Fatalf("update created ambiguous state: %s, %v", got, err)
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["future"]) != `{"keep":true}` {
		t.Fatalf("future field lost: %s", got)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(doc["replay"], &rows); err != nil || len(rows) != 1 || string(rows[0]["future_entry"]) != `{"keep":true}` {
		t.Fatalf("unread metadata lost: %s", got)
	}
	if !slices.Contains(readHookState(t, path).Displayed, "printed") {
		t.Fatal("missing display proof")
	}
}

func TestStrictStateMetadataIdentityEncoding(t *testing.T) {
	for _, raw := range []string{
		`{"hub":"http://hub","caller":"peer_A","shown":[],"replay":[{"ID":"dly_\u0061","After":"opaque","future":true}],"claims":[{"ID":"claim_\u0061","Claim":"nonce_\u0061","Generation":"gen_\u0061","future":true}]}`,
		`{"hub":"http://hub","caller":"peer_A","ſhown":[],"diſplayed":["old"],"replay":[{"id":"unread","after":"opaque","future":true}],"claimſ":[{"ID":"pending","Claim":"nonce","future":true}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := codexUpdateState(t.Context(), path, false, func(s *hookState) error { s.recordDisplay("printed"); return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateCodexStateJSON(got); err != nil {
				t.Fatalf("ambiguous output state: %s: %v", got, err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"replay", "claims"} {
				var refs []map[string]json.RawMessage
				if err := json.Unmarshal(doc[key], &refs); err != nil || len(refs) != 1 || string(refs[0]["future"]) != "true" {
					t.Fatalf("lost metadata in %s: %s", key, got)
				}
			}
		})
	}
}
