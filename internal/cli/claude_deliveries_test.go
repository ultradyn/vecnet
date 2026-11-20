package cli

// Task 8 (claude delivery chain): the operator surface for native delivery
// dispositions. The tests seed the shared delivery ledger (the same SQLite
// kv the TS delivery host owns) with strict claim documents, then pin:
// bounded, redacted listings that never carry coordinates or inbox cursors,
// resolve refusals that leave state untouched, the inbox-only hold, and the
// pending cap state in `claude status`. The retry success path (admission on
// a live registered root) lives in claude_deliveries_linux_test.go.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

const (
	claudeDeliveryTestThread  = "thr_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	claudeDeliveryTestMessage = "msg_01E8YQZ5N8W3C4R6T7A9A2B4CD"
	claudeDeliveryTestGen     = "0102030405060708090a0b0c0d0e0f10"
)

// claudeDeliveryScopeKey is the addressed binding key of the witnessed
// profile in claudeStatusFixture (pinned hub claudeTestHubPin, peer_A).
func claudeDeliveryScopeKey() string {
	return claudeBindingKey(claudeScope{
		HubIdentity: claudeTestHubPin,
		Recipient:   "peer_A",
	})
}

// claudeDeliveryLegacyScopeKey is the slice-3 key the same recipient's claims
// were recorded under, when the hub identity was the endpoint.
func claudeDeliveryLegacyScopeKey() string {
	return claudeLegacyBindingKey(claudeScope{
		HubEndpoint: "https://hub.example.com",
		Recipient:   "peer_A",
	})
}

// claudeDeliveryTestDSN escapes one filesystem path into a sqlite file DSN.
func claudeDeliveryTestDSN(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// claudeDeliveryLedger is the shared delivery database the TS host owns,
// seeded with raw kv rows (ns claude) exactly as the adapter writes them.
func claudeDeliveryLedger(t *testing.T, home string, rows map[string]string) string {
	t.Helper()
	dir := filepath.Join(home, ".local", "state", "vecnet", "codex")
	if err := platform.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "delivery.sqlite")
	// Seed the inode privately before SQLite touches it, as the TS host does:
	// on Windows SQLite's own creator would inherit the parent's ACL and
	// owner, which the ledger's private-file check rightly refuses.
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		seed, err := platform.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+claudeDeliveryTestDSN(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS kv (ns TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(ns, key))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	for k, v := range rows {
		if _, err := db.Exec(`INSERT INTO kv (ns, key, value) VALUES ('claude', ?, ?)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := cliFixtureChmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeDeliveryClaimJSON is one strict, fully populated claim record for the
// addressed scope; over mutates single fields.
func claudeDeliveryClaimJSON(t *testing.T, over map[string]any) string {
	t.Helper()
	record := map[string]any{
		"v": 1, "deliveryId": "dly_00000000000000000000000001",
		"messageId": claudeDeliveryTestMessage, "threadId": claudeDeliveryTestThread,
		"msgId": "0f0e0d0c0b0a09080706050403020100", "scope": claudeDeliveryScopeKey(),
		"generation": claudeDeliveryTestGen, "cursor": "e1.1", "predecessor": "e1.0",
		"priority": "wake", "state": "released", "attemptId": "07060504030201000f0e0d0c0b0a0908",
		"disposition": "attempted", "dispositionReason": "write_completed_no_presentation_proof",
		"hubAckObserved": false, "createdAt": 5000, "updatedAt": 6000,
	}
	for k, v := range over {
		record[k] = v
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func claudeDeliveryClaimKey(id string) string { return "claude-claims:claim:" + id }

// claudeDeliveryKV reads every claude-claims kv row back from the ledger.
func claudeDeliveryKV(t *testing.T, home string) map[string]string {
	t.Helper()
	path := filepath.Join(home, ".local", "state", "vecnet", "codex", "delivery.sqlite")
	db, err := sql.Open("sqlite", "file:"+claudeDeliveryTestDSN(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT key, value FROM kv WHERE ns = 'claude'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	return out
}

func claudeDeliveriesJSON(t *testing.T, env map[string]string, args ...string) map[string]any {
	t.Helper()
	full := append([]string{"--net", "p", "claude"}, args...)
	code, out, errs := claudeStatusRun(t, env, full...)
	if code != 0 {
		t.Fatalf("claude %s exit %d: %s", strings.Join(args, " "), code, errs)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("claude %s output is not JSON: %q (%v)", strings.Join(args, " "), out, err)
	}
	return result
}

// TestClaudeDeliveriesListIsBoundedAndRedacted pins the listing surface: the
// addressed scope's dispositions only, opaque ids and states, no message
// bodies, no credentials, no scope coordinates and no inbox cursors.
func TestClaudeDeliveriesListIsBoundedAndRedacted(t *testing.T) {
	home, env := claudeStatusFixture(t)
	foreign := strings.Repeat("cd", 32)
	pending := claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": "dly_00000000000000000000000002", "state": "uncertain",
		"disposition": "uncertain", "dispositionReason": "write_failed_or_partial",
		"msgId": "0f0e0d0c0b0a090807060504030201ff", "cursor": "e1.2",
	})
	other := claudeDeliveryClaimJSON(t, map[string]any{"scope": foreign})
	corrupt := `{"v":1,"deliveryId":"dly_broken"}`
	progress := `{"v":1,"generation":"` + claudeDeliveryTestGen + `","cursor":"e1.2","blockedPendingLimit":true,"updatedAt":7000}`
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey("dly_00000000000000000000000001"): claudeDeliveryClaimJSON(t, nil),
		claudeDeliveryClaimKey("dly_00000000000000000000000002"): pending,
		claudeDeliveryClaimKey("dly_foreign"):                    other,
		claudeDeliveryClaimKey("dly_broken"):                     corrupt,
		"claude-claims:scope:" + claudeDeliveryScopeKey():        progress,
	})

	code, out, errs := claudeStatusRun(t, env, "--net", "p", "claude", "deliveries", "--json")
	if code != 0 {
		t.Fatalf("claude deliveries exit %d: %s", code, errs)
	}
	// Redaction: no coordinates, no home paths, no inbox cursors, no secrets.
	for _, leak := range []string{"peer_A", "hub.example.com", home, "e1.1", "e1.2", "secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("claude deliveries leaked %q: %s", leak, out)
		}
	}
	var result struct {
		Version             int              `json:"version"`
		Scope               string           `json:"scope"`
		Deliveries          []map[string]any `json:"deliveries"`
		PendingCount        int              `json:"pending_count"`
		BlockedPendingLimit bool             `json:"blocked_pending_limit"`
		TruncatedSettled    int              `json:"truncated_settled"`
		Corrupt             int              `json:"corrupt"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("claude deliveries output is not JSON: %q (%v)", out, err)
	}
	if result.Version != 1 || result.Scope != claudeDeliveryScopeKey() {
		t.Fatalf("claude deliveries addresses the wrong scope: %+v", result)
	}
	if len(result.Deliveries) != 2 || result.PendingCount != 1 || !result.BlockedPendingLimit || result.Corrupt != 1 {
		t.Fatalf("claude deliveries counts are wrong: %+v", result)
	}
	wantKeys := map[string]bool{"delivery_id": true, "message_id": true, "msg_id": true,
		"thread_id": true, "scope": true, "generation": true, "state": true,
		"disposition": true, "reason": true, "hub_ack_observed": true, "updated_at": true}
	rows := map[string]map[string]any{}
	for _, row := range result.Deliveries {
		id, _ := row["delivery_id"].(string)
		rows[id] = row
		for k := range row {
			if !wantKeys[k] {
				t.Fatalf("claude deliveries row carries unexpected field %q: %v", k, row)
			}
		}
	}
	settled := rows["dly_00000000000000000000000001"]
	if settled == nil || settled["state"] != "released" || settled["disposition"] != "attempted" ||
		settled["reason"] != "write_completed_no_presentation_proof" || settled["scope"] != claudeDeliveryScopeKey() {
		t.Fatalf("the exit-0 terminal row does not read its disposition: %v", settled)
	}
	if _, shown := settled["hub_ack_observed"]; shown {
		t.Fatalf("a false hub ack must stay hidden, it is never inferred: %v", settled)
	}
	pendingRow := rows["dly_00000000000000000000000002"]
	if pendingRow == nil || pendingRow["state"] != "uncertain" || pendingRow["disposition"] != "uncertain" {
		t.Fatalf("the pending row is missing its disposition: %v", pendingRow)
	}
	if rows["dly_foreign"] != nil || strings.Contains(out, foreign) {
		t.Fatalf("a foreign scope's rows must not be listed: %s", out)
	}
	if strings.Contains(out, "dly_broken") {
		t.Fatalf("a corrupt row must not be listed by id: %s", out)
	}

	// The human list reads the DISPOSITION field and stays just as redacted.
	code, text, errs := claudeStatusRun(t, env, "--net", "p", "claude", "deliveries")
	if code != 0 {
		t.Fatalf("claude deliveries text exit %d: %s", code, errs)
	}
	if !strings.Contains(text, "disposition=attempted") || !strings.Contains(text, "state=uncertain") {
		t.Fatalf("the operator list must read the disposition field: %s", text)
	}
	for _, leak := range []string{"peer_A", "hub.example.com", home} {
		if strings.Contains(text, leak) {
			t.Fatalf("claude deliveries text leaked %q: %s", leak, text)
		}
	}
}

// TestClaudeDeliveriesCapOldestSettled pins the bounded tombstone retention:
// at most 256 rows are visible, only settled rows are hidden, pending rows
// always stay, and the truncation is noted in the output.
func TestClaudeDeliveriesCapOldestSettled(t *testing.T) {
	home, env := claudeStatusFixture(t)
	rows := map[string]string{}
	ids := func(n int) string { return fmt.Sprintf("dly_%026d", n) }
	for i := 0; i < 300; i++ {
		rows[claudeDeliveryClaimKey(ids(i))] = claudeDeliveryClaimJSON(t, map[string]any{
			"deliveryId": ids(i), "updatedAt": 1000 + i,
			"msgId": fmt.Sprintf("%032x", i), "attemptId": fmt.Sprintf("%032x", 0x10000+i),
		})
	}
	rows[claudeDeliveryClaimKey(ids(301))] = claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": ids(301), "state": "uncertain", "disposition": "uncertain",
		"dispositionReason": "write_failed_or_partial", "updatedAt": 9999,
		"msgId": strings.Repeat("aa", 16),
	})
	claudeDeliveryLedger(t, home, rows)

	result := claudeDeliveriesJSON(t, env, "deliveries", "--json")
	list, _ := result["deliveries"].([]any)
	if len(list) != 256 {
		t.Fatalf("the visible list is not capped at 256: %d", len(list))
	}
	if truncated, _ := result["truncated_settled"].(float64); truncated != 45 {
		t.Fatalf("the truncation note is wrong: %v", result["truncated_settled"])
	}
	seen := map[string]bool{}
	for _, raw := range list {
		row, _ := raw.(map[string]any)
		seen[row["delivery_id"].(string)] = true
	}
	if !seen[ids(299)] {
		t.Fatalf("the newest settled row must stay visible")
	}
	if !seen[ids(301)] {
		t.Fatalf("a pending row must never be truncated")
	}
	if seen[ids(0)] || seen[ids(44)] {
		t.Fatalf("the oldest settled rows must be the hidden ones")
	}
	if pending, _ := result["pending_count"].(float64); pending != 1 {
		t.Fatalf("pending_count must count every unresolved row, truncated or not: %v", result["pending_count"])
	}

	code, text, errs := claudeStatusRun(t, env, "--net", "p", "claude", "deliveries")
	if code != 0 {
		t.Fatalf("claude deliveries text exit %d: %s", code, errs)
	}
	if !strings.Contains(text, "truncated: 45") {
		t.Fatalf("the text list must note the truncation: %s", text)
	}
}

// TestClaudeDeliveriesEmptyWithoutLedger pins the passive default: with no
// delivery activity recorded the listing is empty, not an error.
func TestClaudeDeliveriesEmptyWithoutLedger(t *testing.T) {
	_, env := claudeStatusFixture(t)
	result := claudeDeliveriesJSON(t, env, "deliveries", "--json")
	if list, _ := result["deliveries"].([]any); len(list) != 0 {
		t.Fatalf("an empty ledger must list nothing: %v", result)
	}
	if result["pending_count"] != float64(0) || result["blocked_pending_limit"] != false {
		t.Fatalf("an empty ledger must be uncapped: %v", result)
	}
}

// TestClaudeDeliveriesRefuseUnsafeLedger pins the fail-closed read: a ledger
// that is not an owner-only regular file is never parsed.
func TestClaudeDeliveriesRefuseUnsafeLedger(t *testing.T) {
	home, env := claudeStatusFixture(t)
	path := claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey("dly_00000000000000000000000001"): claudeDeliveryClaimJSON(t, nil),
	})
	// os.Chmod on unix; on Windows the equivalent DACL with a world-read ACE.
	if err := cliFixtureChmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := claudeStatusRun(t, env, "--net", "p", "claude", "deliveries")
	if code == 0 {
		t.Fatalf("a group-readable ledger must be refused: %s", errs)
	}
}

// TestClaudeResolveRefusals pins every resolve refusal: none of them changes
// the stored claim, and none accepts a raw path or free text.
func TestClaudeResolveRefusals(t *testing.T) {
	home, env := claudeStatusFixture(t)
	claim := claudeDeliveryClaimJSON(t, map[string]any{"state": "uncertain", "disposition": "uncertain",
		"dispositionReason": "restart_may_have_written"})
	other := claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": "dly_foreign", "scope": strings.Repeat("cd", 32)})
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey("dly_00000000000000000000000001"): claim,
		claudeDeliveryClaimKey("dly_foreign"):                    other,
	})
	id := "dly_00000000000000000000000001"
	wrongGen := strings.Repeat("f", 32)
	cases := map[string][]string{
		"unknown delivery":           {"dly_nope", "--expect-generation", claudeDeliveryTestGen, "--keep-inbox"},
		"foreign scope":              {"dly_foreign", "--expect-generation", claudeDeliveryTestGen, "--keep-inbox"},
		"wrong generation":           {id, "--expect-generation", wrongGen, "--keep-inbox"},
		"short generation":           {id, "--expect-generation", "01", "--keep-inbox"},
		"missing generation":         {id, "--keep-inbox"},
		"retry without confirmation": {id, "--expect-generation", claudeDeliveryTestGen, "--retry"},
		"both actions":               {id, "--expect-generation", claudeDeliveryTestGen, "--keep-inbox", "--retry"},
		"no action":                  {id, "--expect-generation", claudeDeliveryTestGen},
		"raw path positional":        {home + "/inbox", "--expect-generation", claudeDeliveryTestGen, "--keep-inbox"},
		"relative path positional":   {"some/inbox", "--expect-generation", claudeDeliveryTestGen, "--keep-inbox"},
		"free text positional":       {"just an id", "--expect-generation", claudeDeliveryTestGen, "--keep-inbox"},
		"no positional":              {"--expect-generation", claudeDeliveryTestGen, "--keep-inbox"},
		"retry with no registration": {id, "--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk"},
	}
	before := claudeDeliveryKV(t, home)
	for name, args := range cases {
		code, _, errs := claudeStatusRun(t, env, append([]string{"--net", "p", "claude", "resolve"}, args...)...)
		if code == 0 {
			t.Fatalf("%s: resolve succeeded: %v", name, args)
		}
		if errs == "" {
			t.Fatalf("%s: refusal without a reason", name)
		}
	}
	after := claudeDeliveryKV(t, home)
	if len(after) != len(before) {
		t.Fatalf("a refusal wrote rows: %d -> %d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("a refusal changed row %s: %s -> %s", k, v, after[k])
		}
	}
}

// TestClaudeResolveRetryRefusesHeldAndPending pins the retry fences: hold is
// inbox-only full stop (a hold-priority claim is never re-armed for native
// transport), a wake claim the operator already held stays held, and a claim
// still owing a disposition (reserved/attempting) cannot be re-armed under a
// live attempt. Every refusal leaves the stored row byte-identical.
func TestClaudeResolveRetryRefusesHeldAndPending(t *testing.T) {
	home, env := claudeStatusFixture(t)
	holdPriority := claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": "dly_000000000000000000000000A1", "priority": "hold",
		"state": "held", "disposition": "held", "dispositionReason": "inbox_only",
	})
	heldWake := claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": "dly_000000000000000000000000A2", "state": "held",
		"disposition": "held", "dispositionReason": "operator_keep_inbox",
	})
	reserved := claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": "dly_000000000000000000000000A3", "state": "reserved",
	})
	attempting := claudeDeliveryClaimJSON(t, map[string]any{
		"deliveryId": "dly_000000000000000000000000A4", "state": "attempting",
	})
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey("dly_000000000000000000000000A1"): holdPriority,
		claudeDeliveryClaimKey("dly_000000000000000000000000A2"): heldWake,
		claudeDeliveryClaimKey("dly_000000000000000000000000A3"): reserved,
		claudeDeliveryClaimKey("dly_000000000000000000000000A4"): attempting,
	})
	for name, want := range map[string][2]string{
		"hold priority":    {"dly_000000000000000000000000A1", "inbox-only"},
		"operator held":    {"dly_000000000000000000000000A2", "inbox-only"},
		"still reserved":   {"dly_000000000000000000000000A3", "resolvable state"},
		"still attempting": {"dly_000000000000000000000000A4", "resolvable state"},
	} {
		code, _, errs := claudeStatusRun(t, env, "--net", "p", "claude", "resolve", want[0],
			"--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk")
		if code == 0 {
			t.Fatalf("%s: retry re-armed a claim it must refuse", name)
		}
		if !strings.Contains(errs, want[1]) {
			t.Fatalf("%s: refusal must name the claim state, got: %s", name, errs)
		}
	}
	after := claudeDeliveryKV(t, home)
	for id, want := range map[string]string{
		claudeDeliveryClaimKey("dly_000000000000000000000000A1"): holdPriority,
		claudeDeliveryClaimKey("dly_000000000000000000000000A2"): heldWake,
		claudeDeliveryClaimKey("dly_000000000000000000000000A3"): reserved,
		claudeDeliveryClaimKey("dly_000000000000000000000000A4"): attempting,
	} {
		if after[id] != want {
			t.Fatalf("a retry refusal changed the stored claim %s: %s", id, after[id])
		}
	}
}

// TestClaudeResolveKeepInboxHolds pins the inbox-only resolution: the claim is
// held (never promoted again), stays readable, and nothing acknowledges it.
func TestClaudeResolveKeepInboxHolds(t *testing.T) {
	home, env := claudeStatusFixture(t)
	id := "dly_00000000000000000000000001"
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey(id): claudeDeliveryClaimJSON(t, map[string]any{
			"state": "uncertain", "disposition": "uncertain",
			"dispositionReason": "restart_may_have_written",
		}),
		"claude-claims:scope:" + claudeDeliveryScopeKey(): `{"v":1,"generation":"` + claudeDeliveryTestGen + `","cursor":"e1.1","blockedPendingLimit":false,"updatedAt":6000}`,
	})
	code, out, errs := claudeStatusRun(t, env, "--net", "p", "claude", "resolve", id,
		"--expect-generation", claudeDeliveryTestGen, "--keep-inbox")
	if code != 0 {
		t.Fatalf("keep-inbox resolve exit %d: %s %s", code, out, errs)
	}
	var held map[string]any
	if err := json.Unmarshal([]byte(claudeDeliveryKV(t, home)[claudeDeliveryClaimKey(id)]), &held); err != nil {
		t.Fatal(err)
	}
	if held["state"] != "held" || held["disposition"] != "held" || held["dispositionReason"] != "operator_keep_inbox" {
		t.Fatalf("keep-inbox did not hold the claim: %v", held)
	}
	if held["hubAckObserved"] == true {
		t.Fatalf("keep-inbox must never acknowledge: %v", held)
	}
	if held["generation"] != claudeDeliveryTestGen || held["cursor"] != "e1.1" || held["messageId"] != claudeDeliveryTestMessage {
		t.Fatalf("keep-inbox disturbed the claim's identity: %v", held)
	}
	if updated, _ := held["updatedAt"].(float64); updated <= 6000 {
		t.Fatalf("keep-inbox must re-stamp the record: %v", held)
	}
	kv := claudeDeliveryKV(t, home)
	if len(kv) != 2 {
		t.Fatalf("keep-inbox wrote or removed rows: %v", kv)
	}
	// The hold stops promotion durably: a later listing shows it held, still
	// readable, still pending-free (held is terminal, not pending).
	result := claudeDeliveriesJSON(t, env, "deliveries", "--json")
	if list, _ := result["deliveries"].([]any); len(list) != 1 || list[0].(map[string]any)["state"] != "held" {
		t.Fatalf("the held claim is no longer visible: %v", result)
	}
	if result["pending_count"] != float64(0) {
		t.Fatalf("a held claim is not pending work: %v", result)
	}
}

// TestClaudeStatusShowsPendingCapState pins the status additions: the pending
// count comes from the durable claims and the cap state from the scope's
// staging record, while the status itself stays passive.
func TestClaudeStatusShowsPendingCapState(t *testing.T) {
	home, env := claudeStatusFixture(t)
	pending := func(id, state string) string {
		return claudeDeliveryClaimJSON(t, map[string]any{
			"deliveryId": id, "state": state, "disposition": "uncertain",
			"dispositionReason": "write_failed_or_partial",
		})
	}
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey("dly_00000000000000000000000001"): pending("dly_00000000000000000000000001", "uncertain"),
		claudeDeliveryClaimKey("dly_00000000000000000000000002"): pending("dly_00000000000000000000000002", "reserved"),
		claudeDeliveryClaimKey("dly_00000000000000000000000003"): claudeDeliveryClaimJSON(t, nil),
		"claude-claims:scope:" + claudeDeliveryScopeKey():        `{"v":1,"generation":"` + claudeDeliveryTestGen + `","cursor":"e1.2","blockedPendingLimit":true,"updatedAt":7000}`,
	})
	code, out, errs := claudeStatusRun(t, env, "--net", "p", "claude", "status", "--json")
	if code != 0 {
		t.Fatalf("claude status exit %d: %s", code, errs)
	}
	var result struct {
		PendingCount        int    `json:"pending_count"`
		BlockedPendingLimit bool   `json:"blocked_pending_limit"`
		Owner               string `json:"owner"`
		Mode                string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.PendingCount != 2 || !result.BlockedPendingLimit {
		t.Fatalf("status does not show the pending cap state: %s", out)
	}
	if result.Owner != "none" || result.Mode != "legacy" {
		t.Fatalf("status stopped being passive: %s", out)
	}
	code, text, errs := claudeStatusRun(t, env, "--net", "p", "claude", "status")
	if code != 0 {
		t.Fatalf("claude status text exit %d: %s", code, errs)
	}
	if !strings.Contains(text, "pending_count: 2") || !strings.Contains(text, "blocked_pending_limit: true") {
		t.Fatalf("status text hides the cap state: %s", text)
	}
}

// TestClaudeDeliveriesListsLegacyKeyRows pins the slice-3 migration of the
// claim ledger: claims recorded under the legacy endpoint-derived key stay
// listed, counted and resolvable inbox-only, while --retry refuses because
// that scope can never be admitted again.
func TestClaudeDeliveriesListsLegacyKeyRows(t *testing.T) {
	home, env := claudeStatusFixture(t)
	legacyKey := claudeDeliveryLegacyScopeKey()
	if legacyKey == claudeDeliveryScopeKey() {
		t.Fatal("the legacy and pinned keys coincide")
	}
	legacyID, pinnedID := "dly_000000000000000000000000L1", "dly_000000000000000000000000P1"
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey(legacyID): claudeDeliveryClaimJSON(t, map[string]any{
			"deliveryId": legacyID, "scope": legacyKey, "state": "uncertain",
			"disposition": "uncertain", "dispositionReason": "write_failed_or_partial",
		}),
		claudeDeliveryClaimKey(pinnedID): claudeDeliveryClaimJSON(t, map[string]any{
			"deliveryId": pinnedID, "msgId": "0f0e0d0c0b0a090807060504030201ff",
		}),
	})

	result := claudeDeliveriesJSON(t, env, "deliveries", "--json")
	list, _ := result["deliveries"].([]any)
	seen := map[string]string{}
	for _, raw := range list {
		row, _ := raw.(map[string]any)
		seen[row["delivery_id"].(string)], _ = row["scope"].(string)
	}
	if len(seen) != 2 || seen[legacyID] != legacyKey || seen[pinnedID] != claudeDeliveryScopeKey() {
		t.Fatalf("the legacy-key claim is not listed beside the pinned one: %v", result)
	}
	if result["pending_count"] != float64(1) {
		t.Fatalf("deliveries pending_count must count the legacy-key claim: %v", result["pending_count"])
	}
	if status := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json"); status.PendingCount != 1 {
		t.Fatalf("status pending_count must count the legacy-key claim: %+v", status)
	}

	// --retry on a legacy-key claim is refused and changes nothing.
	before := claudeDeliveryKV(t, home)
	code, _, errs := claudeStatusRun(t, env, "--net", "p", "claude", "resolve", legacyID,
		"--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk")
	if code == 0 || !strings.Contains(errs, "legacy endpoint-keyed scope") {
		t.Fatalf("retry on a legacy-key claim was not refused: %d %s", code, errs)
	}
	if after := claudeDeliveryKV(t, home); after[claudeDeliveryClaimKey(legacyID)] != before[claudeDeliveryClaimKey(legacyID)] {
		t.Fatalf("the refused retry changed the claim: %s", after[claudeDeliveryClaimKey(legacyID)])
	}

	// The legacy reason wins over every state fence: a legacy-key claim that
	// is still reserved (not otherwise resolvable) refuses for being legacy.
	reservedID := "dly_000000000000000000000000L2"
	claudeDeliveryLedger(t, home, map[string]string{
		claudeDeliveryClaimKey(reservedID): claudeDeliveryClaimJSON(t, map[string]any{
			"deliveryId": reservedID, "scope": legacyKey, "state": "reserved",
			"msgId": "0f0e0d0c0b0a0908070605040302aaaa",
		}),
	})
	code, _, errs = claudeStatusRun(t, env, "--net", "p", "claude", "resolve", reservedID,
		"--expect-generation", claudeDeliveryTestGen, "--retry", "--confirm-duplicate-risk")
	if code == 0 || !strings.Contains(errs, "legacy endpoint-keyed scope") {
		t.Fatalf("a reserved legacy-key claim must refuse retry with the legacy reason: %d %s", code, errs)
	}

	// --keep-inbox holds it.
	code, out, errs := claudeStatusRun(t, env, "--net", "p", "claude", "resolve", legacyID,
		"--expect-generation", claudeDeliveryTestGen, "--keep-inbox")
	if code != 0 {
		t.Fatalf("keep-inbox on a legacy-key claim exit %d: %s %s", code, out, errs)
	}
	var held map[string]any
	if err := json.Unmarshal([]byte(claudeDeliveryKV(t, home)[claudeDeliveryClaimKey(legacyID)]), &held); err != nil {
		t.Fatal(err)
	}
	if held["state"] != "held" || held["scope"] != legacyKey {
		t.Fatalf("keep-inbox did not hold the legacy-key claim: %v", held)
	}
	if status := claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json"); status.PendingCount != 1 {
		t.Fatalf("only the reserved legacy-key claim should still count as pending: %+v", status)
	}
}
