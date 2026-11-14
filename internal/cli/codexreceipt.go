package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Match the existing engine's ownerOf/client-key encoding. Never infer a receipt
// from RPC acceptance: only the exact-thread receipt/history path supplies this
// key and its complete delivery set to settle.
func codexReceiptKey(b codexBinding, ids []string) string {
	owner := struct {
		Profile    string `json:"profile"`
		As         string `json:"as"`
		Hub        string `json:"hub"`
		Caller     string `json:"caller"`
		CodexHome  string `json:"codexHome"`
		ThreadID   string `json:"threadId"`
		ConfigPath string `json:"configPath"`
		Generation string `json:"generation"`
	}{b.Profile, b.As, b.Hub, b.Caller, b.CodexHome, b.ThreadID, b.ConfigPath, b.Generation}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(owner)
	// JSON.stringify emits these Unicode separators literally.
	raw := strings.TrimSuffix(buf.String(), "\n")
	var js strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if strings.HasPrefix(raw[i:], `\u2028`) || strings.HasPrefix(raw[i:], `\u2029`) {
				if raw[i+5] == '8' {
					js.WriteRune('\u2028')
				} else {
					js.WriteRune('\u2029')
				}
				i += 5
				continue
			}
			js.WriteByte(raw[i])
			i++
			js.WriteByte(raw[i])
			continue
		}
		js.WriteByte(raw[i])
	}
	ordered := slices.Clone(ids)
	slices.Sort(ordered)
	sum := sha256.Sum256([]byte(js.String() + "\n" + strings.Join(ordered, ",")))
	return fmt.Sprintf("vn.%x", sum[:12])
}

// Caller holds the authority lock. Matching receipt publication and claim
// retirement commit together under the state lock, before deleting recovery
// evidence. A lost successful response can safely repeat this exact proof.
func (a *app) codexRecordReceipt(b codexBinding, nonce, key, deliveries string, requireAccepted bool) error {
	ids := strings.Split(deliveries, ",")
	if len(ids) == 0 || len(ids) > appServerClaimLimit {
		return usageErr("invalid receipt delivery set")
	}
	set := map[string]bool{}
	for _, id := range ids {
		if !hookID.MatchString(id) || set[id] {
			return usageErr("invalid receipt delivery set")
		}
		set[id] = true
	}
	if key != codexReceiptKey(b, ids) {
		return usageErr("receipt key does not match exact owner and deliveries")
	}
	path, err := a.codexOwnerStatePath(b)
	if err != nil {
		return err
	}
	return codexUpdateState(a.ctx, path, false, func(s *hookState) error {
		if s.Hub != b.Hub || s.Caller != b.Caller || s.As != b.As {
			return usageErr("receipt state identity changed")
		}
		matched := 0
		for _, r := range s.Claims {
			if r.Generation == b.Generation && r.Claim == nonce {
				if requireAccepted && !r.Accepted {
					return usageErr("receipt has no pending or accepted send evidence")
				}
				if !set[r.ID] {
					return usageErr("receipt omits claimed delivery")
				}
				matched++
			} else if set[r.ID] {
				return usageErr("receipt delivery belongs to another claim")
			}
		}
		if matched == 0 {
			for _, id := range ids {
				if !slices.Contains(s.Displayed, id) {
					return usageErr("receipt claim missing")
				}
			}
			return nil
		}
		if matched != len(ids) {
			return usageErr("receipt delivery set differs from claim")
		}
		for _, id := range ids {
			s.recordDisplay(id)
		}
		s.Claims = slices.DeleteFunc(s.Claims, func(r claimRef) bool { return r.Generation == b.Generation && r.Claim == nonce })
		s.Replay = slices.DeleteFunc(s.Replay, func(r replayRef) bool { return set[r.ID] })
		return nil
	})
}
