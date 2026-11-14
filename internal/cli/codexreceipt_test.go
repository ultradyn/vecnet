package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// Golden keys use the engine's JSON.stringify(ownerOf(binding)) encoding.
func TestCodexReceiptKeyMatchesEngineEncoding(t *testing.T) {
	b := codexBinding{Profile: "p", Hub: "http://hub", Caller: "peer_A", CodexHome: "/home/codex", ThreadID: bindT1, ConfigPath: "/config/v.toml", Generation: strings.Repeat("a", 32)}
	if got := codexReceiptKey(b, []string{"dly_z", "dly_a"}); got != "vn.31f906d922203a52bf592cf7" {
		t.Fatalf("engine key mismatch: %s", got)
	}
	b.ConfigPath = "/config/<>&\u2028\\u2028.toml"
	if got := codexReceiptKey(b, []string{"dly_a", "dly_z"}); got != "vn.5c55cab4c375b10f144e0f4b" {
		t.Fatalf("Unicode/literal escape engine key mismatch: %s", got)
	}
}

func receiptOwner(t *testing.T, f *bindFixture) codexBinding {
	t.Helper()
	file, err := readCodexAuthority(f.path)
	if err != nil || len(file.Bindings) != 1 {
		t.Fatalf("authority: %+v %v", file, err)
	}
	return file.Bindings[0]
}

func TestCodexReceiptPublishesOnlyCompleteExactBatch(t *testing.T) {
	for _, evidence := range []string{"ambiguous", "accepted"} {
		t.Run(evidence, func(t *testing.T) {
			f := newBindFixture(t)
			f.bind("p", bindT1)
			path := f.seedGuardClaim("send1")
			b := receiptOwner(t, f)
			flags := f.guardFlags("send1")
			f.ok(append([]string{"guard"}, flags...)...)
			if evidence == "accepted" {
				f.ok(append([]string{"settle", "--outcome", "accepted"}, flags...)...)
			}
			if len(readHookState(t, path).Displayed) != 0 {
				t.Fatal("RPC acceptance created display proof")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []struct{ key, ids string }{
				{codexReceiptKey(b, []string{"dly_other"}), "dly_other"},
				{codexReceiptKey(b, []string{"dly_claim", "dly_other"}), "dly_claim,dly_other"},
				{codexReceiptKey(b, []string{"dly_claim"}), "dly_claim,dly_claim"},
				{"vn.wrong", "dly_claim"},
			} {
				args := append([]string{"settle", "--outcome", "accepted", "--receipt-key", bad.key, "--delivery-ids", bad.ids}, flags...)
				code, _, _ := f.run(args...)
				after, _ := os.ReadFile(path)
				if code == 0 || !bytes.Equal(before, after) {
					t.Fatal("mismatched receipt changed claim/proof")
				}
			}
			args := append([]string{"settle", "--outcome", "accepted", "--receipt-key", codexReceiptKey(b, []string{"dly_claim"}), "--delivery-ids", "dly_claim"}, flags...)
			f.ok(args...)
			state := readHookState(t, path)
			if len(state.Claims) != 0 || !slices.Contains(state.Displayed, "dly_claim") {
				t.Fatalf("missing exact proof: %+v", state)
			}
			f.ok(args...) // committed response loss is idempotent
		})
	}
}

func TestCodexReceiptWithoutSendEvidenceRefuses(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	path := f.seedGuardClaim("send1")
	b := receiptOwner(t, f)
	before, _ := os.ReadFile(path)
	args := append([]string{"settle", "--outcome", "accepted", "--receipt-key", codexReceiptKey(b, []string{"dly_claim"}), "--delivery-ids", "dly_claim"}, f.guardFlags("send1")...)
	code, _, _ := f.run(args...)
	after, _ := os.ReadFile(path)
	if code == 0 || !bytes.Equal(before, after) {
		t.Fatal("unsent reservation turned into display proof")
	}
}

func TestCodexReceiptPreservesUnrelatedRecovery(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	path := f.seedGuardClaim("send1")
	b := receiptOwner(t, f)
	if err := codexUpdateState(t.Context(), path, false, func(s *hookState) error {
		s.Claims = append(s.Claims, claimRef{ID: "dly_unrelated", Claim: "other", Generation: b.Generation, Accepted: true})
		s.Replay = []replayRef{{ID: "dly_unread", After: "opaque"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.ok(append([]string{"guard"}, f.guardFlags("send1")...)...)
	args := append([]string{"settle", "--outcome", "accepted", "--receipt-key", codexReceiptKey(b, []string{"dly_claim"}), "--delivery-ids", "dly_claim"}, f.guardFlags("send1")...)
	f.ok(args...)
	s := readHookState(t, path)
	if len(s.Claims) != 1 || s.Claims[0].ID != "dly_unrelated" || !s.Claims[0].Accepted || len(s.Replay) != 1 || s.Replay[0].After != "opaque" {
		t.Fatalf("receipt discarded unrelated recovery: %+v", s)
	}
	// Proof survives strict serialization and can be read without hidden state.
	raw, _ := os.ReadFile(path)
	if !json.Valid(raw) {
		t.Fatal("invalid persisted proof")
	}
}
