package cli

import (
	"context"
	"encoding/json"
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

func TestStrictStateDisplayPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte(`{"hub":"http://hub","caller":"peer_A","shown":[],"future":{"keep":true},"replay":[{"ID":"unread","After":"opaque","future_entry":{"keep":true}}]}`)
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
