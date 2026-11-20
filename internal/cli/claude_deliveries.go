package cli

// Task 8 (claude delivery chain): the operator surface for native delivery
// dispositions. The durable claim documents live in the canonical per-user
// kv the TS delivery host owns (the shared delivery database beside the
// elected bindings, ns "claude", keys claude-claims:claim:<deliveryId> and
// claude-claims:scope:<bindingKey>); this file reads them with a versioned
// strict Go decoder mirroring the TS schema. Every surface here is bounded
// and passive: opaque ids, cursors, states and dispositions only — no
// message bodies, no credentials, no snapshot scope coordinates (listings
// show the opaque binding-key scope, never profile/hub/caller/recipient),
// and no command accepts a raw path to any inbox. `claude resolve` mutates
// exactly one claim document: --keep-inbox quarantines it inbox-only (never
// an acknowledgement), --retry re-arms one fresh attempt after an explicit
// duplicate-risk confirmation and a fresh admission check. Nothing retires a
// claim tombstone from Go: retirement stays with the TS reconcile, gated on
// an independently confirmed hub ack, and listings bound what is visible.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/platform"

	_ "modernc.org/sqlite" // the shared delivery ledger is SQLite
)

const (
	// claudeClaimsNS is the kv namespace the TS delivery host writes claims
	// into (ledger.kv('claude')).
	claudeClaimsNS = "claude"
	// claudeClaimKeyPrefix and claudeScopeKeyPrefix mirror the TS adapter's
	// durable key layout.
	claudeClaimKeyPrefix = "claude-claims:claim:"
	claudeScopeKeyPrefix = "claude-claims:scope:"
	// claudeDeliveriesMax bounds the visible tombstone list; only the oldest
	// settled rows are ever hidden and the truncation is noted in the output.
	claudeDeliveriesMax = 256
	// claudeClaimMax bounds one kv value the decoder will ever parse.
	claudeClaimMax = 64 << 10
)

var (
	claudeClaimDeliveryID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	claudeClaimNativeID   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	claudeClaimCursor     = regexp.MustCompile(`^.{1,256}$`)

	claudeClaimPriorities   = map[string]bool{"wake": true, "wake_idle": true, "hold": true}
	claudeClaimStates       = map[string]bool{"reserved": true, "attempting": true, "held": true, "refused": true, "expired": true, "uncertain": true, "released": true}
	claudeClaimDispositions = map[string]bool{"attempted": true, "held": true, "refused": true, "uncertain": true, "released": true}
)

// claudeClaimPending mirrors the TS adapter's unresolved set: a claim in one
// of these states still owes a disposition and is never truncated from view.
func claudeClaimPending(state string) bool {
	return state == "reserved" || state == "attempting" || state == "uncertain"
}

// claudeClaimRecord is the Go mirror of the TS adapter's v1 claim document
// (connectors/launcher/src/delivery/claude-adapter.ts ClaudeClaimRecord).
// The decoder is strict — exactly the v1 field set, exactly the TS shapes —
// so a document any future schema version writes is refused, never guessed.
type claudeClaimRecord struct {
	V                 int     `json:"v"`
	DeliveryID        string  `json:"deliveryId"`
	MessageID         string  `json:"messageId"`
	ThreadID          string  `json:"threadId"`
	MsgID             string  `json:"msgId"`
	Scope             string  `json:"scope"`
	Generation        string  `json:"generation"`
	Cursor            string  `json:"cursor"`
	Predecessor       *string `json:"predecessor,omitempty"`
	Priority          string  `json:"priority"`
	State             string  `json:"state"`
	AttemptID         string  `json:"attemptId"`
	Disposition       string  `json:"disposition,omitempty"`
	DispositionReason string  `json:"dispositionReason,omitempty"`
	HubAckObserved    *bool   `json:"hubAckObserved,omitempty"`
	CreatedAt         float64 `json:"createdAt"`
	UpdatedAt         float64 `json:"updatedAt"`
}

// decodeClaudeClaim strictly decodes one claim document, mirroring the TS
// claimValid predicate field for field. Anything else is corrupt: skipped by
// listings (counted) and refused by resolve.
func decodeClaudeClaim(raw []byte) (claudeClaimRecord, error) {
	var doc claudeClaimRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || d.Decode(new(struct{})) != io.EOF {
		return claudeClaimRecord{}, usageErr("corrupt claude claim document")
	}
	if doc.V != 1 ||
		!claudeClaimDeliveryID.MatchString(doc.DeliveryID) ||
		!ids.Is(ids.Msg, doc.MessageID) || !ids.Is(ids.Thr, doc.ThreadID) ||
		!claudeClaimNativeID.MatchString(doc.MsgID) || !claudeClaimNativeID.MatchString(doc.AttemptID) ||
		!claudeStatusKey.MatchString(doc.Scope) || !claudeToken.MatchString(doc.Generation) ||
		!claudeClaimCursor.MatchString(doc.Cursor) ||
		!claudeClaimPriorities[doc.Priority] || !claudeClaimStates[doc.State] ||
		(doc.Disposition != "" && !claudeClaimDispositions[doc.Disposition]) {
		return claudeClaimRecord{}, usageErr("corrupt claude claim document")
	}
	return doc, nil
}

// claudeScopeProgress is the Go mirror of the TS adapter's per-scope staging
// record under claude-claims:scope:<bindingKey>: the durable cursor, the
// pending-cap flag and the fence generation. Versioned and strict like the
// claim decoder.
type claudeScopeProgress struct {
	V                   int     `json:"v"`
	Generation          string  `json:"generation"`
	Cursor              string  `json:"cursor"`
	BlockedPendingLimit bool    `json:"blockedPendingLimit"`
	UpdatedAt           float64 `json:"updatedAt"`
}

func decodeClaudeScopeProgress(raw []byte) (claudeScopeProgress, error) {
	var doc claudeScopeProgress
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || d.Decode(new(struct{})) != io.EOF || doc.V != 1 ||
		(doc.Generation != "" && !claudeToken.MatchString(doc.Generation)) || len(doc.Cursor) > 256 {
		return claudeScopeProgress{}, usageErr("corrupt claude delivery scope record")
	}
	return doc, nil
}

// claudeClaimsLedgerPath mirrors the TS host's codexRecoveryPath: the shared
// delivery database sits beside the elected bindings authority.
func claudeClaimsLedgerPath(getenv func(string) string) (string, error) {
	bindings, err := codexBindingsPath(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(bindings), "delivery.sqlite"), nil
}

var claudeLedgerDSNEscape = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// openClaudeClaimsLedger opens the TS host's delivery ledger with the same
// busy timeout the host uses (read-write: a WAL reader must attach the
// shared-memory files). A missing or never-initialized database is simply no
// recorded activity (nil, nil); anything not an owner-only regular file, or
// a schema the host never wrote, refuses. Windows has no POSIX mode bits
// (Stat synthesizes them from the read-only attribute), so there the ledger
// must pass the same owner-and-DACL private-file check the TS host applies
// to delivery.sqlite before SQLite opens it.
func openClaudeClaimsLedger(path string) (*sql.DB, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, usageErr("unsafe claude delivery ledger: " + filepath.Base(path) + " must be a regular file")
	}
	if runtime.GOOS == "windows" {
		if err := platform.CheckPath(path, false, true); err != nil {
			return nil, usageErr("unsafe claude delivery ledger: " + filepath.Base(path) + " must be a private file owned by the current user: " + err.Error())
		}
	} else if fi.Mode().Perm()&0o077 != 0 {
		return nil, usageErr("unsafe claude delivery ledger: " + filepath.Base(path) + " must be an owner-only file (mode 0600)")
	}
	db, err := sql.Open("sqlite", "file:"+claudeLedgerDSNEscape.Replace(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, usageErr("unreadable claude delivery ledger")
	}
	if version != 0 && version != 1 {
		db.Close()
		return nil, usageErr("unsupported claude delivery ledger schema")
	}
	if version == 0 {
		// The host creates the schema on first open; version 0 means it never
		// ran here, so no claim can exist.
		db.Close()
		return nil, nil
	}
	return db, nil
}

// claudeClaimScopeKeys is the set of claim scope keys one addressed scope
// owns: its pinned binding key and, when it differs, the legacy endpoint key
// slice-3 claims were recorded under. Legacy-key claims stay visible,
// counted and resolvable inbox-only; they are never admitted again.
func claudeClaimScopeKeys(scope claudeScope) []string {
	key, legacy := claudeBindingKey(scope), claudeLegacyBindingKey(scope)
	if key == legacy {
		return []string{key}
	}
	return []string{key, legacy}
}

// readClaudeClaims returns the decodable claim documents of the addressed
// scope keys, plus the count of claim rows the strict decoder refused
// (mirroring the host's own warn-and-skip of corrupt rows).
func readClaudeClaims(db *sql.DB, scopeKeys ...string) ([]claudeClaimRecord, int, error) {
	rows, err := db.Query(`SELECT value FROM kv WHERE ns = ? AND key LIKE 'claude-claims:claim:%'`, claudeClaimsNS)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var claims []claudeClaimRecord
	corrupt := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if len(raw) > claudeClaimMax {
			corrupt++
			continue
		}
		claim, err := decodeClaudeClaim([]byte(raw))
		if err != nil {
			corrupt++
			continue
		}
		if slices.Contains(scopeKeys, claim.Scope) {
			claims = append(claims, claim)
		}
	}
	return claims, corrupt, rows.Err()
}

// readClaudeScopeProgress reads the addressed scope's staging record; absent
// is (record, false, nil), a present-but-corrupt record refuses (fail closed:
// it may be hiding a true cap state).
func readClaudeScopeProgress(db *sql.DB, scopeKey string) (claudeScopeProgress, bool, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM kv WHERE ns = ? AND key = ?`, claudeClaimsNS, claudeScopeKeyPrefix+scopeKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return claudeScopeProgress{}, false, nil
	}
	if err != nil {
		return claudeScopeProgress{}, true, err
	}
	if len(raw) > claudeClaimMax {
		return claudeScopeProgress{}, true, usageErr("corrupt claude delivery scope record")
	}
	doc, err := decodeClaudeScopeProgress([]byte(raw))
	if err != nil {
		return claudeScopeProgress{}, true, err
	}
	return doc, true, nil
}

// claudeClaimsSummary is the passive status's view of the ledger: how many
// claims of the addressed scope are still unresolved, and whether its
// staging window is blocked on the pending cap.
type claudeClaimsSummary struct {
	Pending             int
	BlockedPendingLimit bool
}

// claudeClaimsSummary counts the unresolved claims under every given scope
// key; the cap state comes from the first (current) key's staging record.
func (a *app) claudeClaimsSummary(scopeKeys ...string) (claudeClaimsSummary, error) {
	path, err := claudeClaimsLedgerPath(a.getenv)
	if err != nil {
		return claudeClaimsSummary{}, err
	}
	db, err := openClaudeClaimsLedger(path)
	if err != nil {
		return claudeClaimsSummary{}, err
	}
	if db == nil {
		return claudeClaimsSummary{}, nil
	}
	defer db.Close()
	claims, _, err := readClaudeClaims(db, scopeKeys...)
	if err != nil {
		return claudeClaimsSummary{}, err
	}
	progress, present, err := readClaudeScopeProgress(db, scopeKeys[0])
	if err != nil {
		return claudeClaimsSummary{}, err
	}
	summary := claudeClaimsSummary{}
	for _, c := range claims {
		if claudeClaimPending(c.State) {
			summary.Pending++
		}
	}
	if present {
		summary.BlockedPendingLimit = progress.BlockedPendingLimit
	}
	return summary, nil
}

// claudeDeliveryRow is one bounded disposition row. Opaque ids, the opaque
// scope key, the fence generation, state, disposition and reason only —
// never a body, a credential, a cursor or a snapshot scope coordinate.
type claudeDeliveryRow struct {
	DeliveryID     string `json:"delivery_id"`
	MessageID      string `json:"message_id"`
	MsgID          string `json:"msg_id"`
	ThreadID       string `json:"thread_id"`
	Scope          string `json:"scope"`
	Generation     string `json:"generation"`
	State          string `json:"state"`
	Disposition    string `json:"disposition,omitempty"`
	Reason         string `json:"reason,omitempty"`
	HubAckObserved bool   `json:"hub_ack_observed,omitempty"`
	UpdatedAt      int64  `json:"updated_at"`
}

// claudeDeliveriesResult is the listings document: the addressed scope's
// visible rows, its pending count, its cap state, and how many of the oldest
// settled rows the 256-row bound hid (the tombstones themselves are kept).
type claudeDeliveriesResult struct {
	Version             int                 `json:"version"`
	Scope               string              `json:"scope"`
	Deliveries          []claudeDeliveryRow `json:"deliveries"`
	PendingCount        int                 `json:"pending_count"`
	BlockedPendingLimit bool                `json:"blocked_pending_limit"`
	TruncatedSettled    int                 `json:"truncated_settled"`
	Corrupt             int                 `json:"corrupt"`
}

// cmdClaudeDeliveries lists the addressed recipient's bounded dispositions.
// It reads only the durable claim documents, lists only the addressed scope
// (its pinned key plus the legacy endpoint key slice-3 claims carry), and
// never touches a hub or an inbox.
func (a *app) cmdClaudeDeliveries(args []string) error {
	o, err := parseFlags("claude deliveries", args, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("claude deliveries takes no positional arguments")
	}
	profile := a.namedProfile()
	if profile == "" {
		return usageErr("claude deliveries requires the addressed profile: --net PROFILE")
	}
	scope, err := a.claudeScopeForProfileAllowLegacy(profile)
	if err != nil {
		return err
	}
	if err := claudeOptOutScopeValid(scope); err != nil {
		return err
	}
	key := claudeBindingKey(scope)
	result := claudeDeliveriesResult{Version: 1, Scope: key, Deliveries: []claudeDeliveryRow{}}
	path, err := claudeClaimsLedgerPath(a.getenv)
	if err != nil {
		return err
	}
	db, err := openClaudeClaimsLedger(path)
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
		claims, corrupt, err := readClaudeClaims(db, claudeClaimScopeKeys(scope)...)
		if err != nil {
			return err
		}
		result.Corrupt = corrupt
		progress, present, err := readClaudeScopeProgress(db, key)
		if err != nil {
			return err
		}
		if present {
			result.BlockedPendingLimit = progress.BlockedPendingLimit
		}
		for _, c := range claims {
			if claudeClaimPending(c.State) {
				result.PendingCount++
			}
			row := claudeDeliveryRow{
				DeliveryID: c.DeliveryID, MessageID: c.MessageID, MsgID: c.MsgID,
				ThreadID: c.ThreadID, Scope: c.Scope, Generation: c.Generation,
				State: c.State, Disposition: c.Disposition, Reason: c.DispositionReason,
				UpdatedAt: int64(c.UpdatedAt),
			}
			if c.HubAckObserved != nil {
				row.HubAckObserved = *c.HubAckObserved
			}
			result.Deliveries = append(result.Deliveries, row)
		}
	}
	// Newest first; ties break on the delivery id so the view is stable.
	slices.SortFunc(result.Deliveries, func(x, y claudeDeliveryRow) int {
		if x.UpdatedAt != y.UpdatedAt {
			if y.UpdatedAt < x.UpdatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(x.DeliveryID, y.DeliveryID)
	})
	// Bounded tombstone retention: only the oldest settled rows may leave the
	// visible list, never a pending one, and the hiding is noted in output.
	// The walk runs from the oldest end so the oldest settled rows hide first.
	if len(result.Deliveries) > claudeDeliveriesMax {
		hide := len(result.Deliveries) - claudeDeliveriesMax
		drop := make([]bool, len(result.Deliveries))
		for i := len(result.Deliveries) - 1; i >= 0 && hide > 0; i-- {
			if !claudeClaimPending(result.Deliveries[i].State) {
				drop[i] = true
				hide--
			}
		}
		kept := result.Deliveries[:0]
		for i, row := range result.Deliveries {
			if drop[i] {
				result.TruncatedSettled++
				continue
			}
			kept = append(kept, row)
		}
		result.Deliveries = kept
	}
	if a.json {
		return a.emitValue(result)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "scope: %s\n", result.Scope)
	fmt.Fprintf(&out, "pending_count: %d\n", result.PendingCount)
	if result.BlockedPendingLimit {
		fmt.Fprintln(&out, "blocked_pending_limit: true")
	}
	if result.Corrupt > 0 {
		fmt.Fprintf(&out, "corrupt: %d unreadable claim row(s)\n", result.Corrupt)
	}
	for _, row := range result.Deliveries {
		when := time.UnixMilli(row.UpdatedAt).UTC().Format(time.RFC3339)
		fmt.Fprintf(&out, "delivery: %s state=%s disposition=%s reason=%s generation=%s message=%s thread=%s native=%s updated=%s\n",
			row.DeliveryID, row.State, row.Disposition, row.Reason, row.Generation,
			row.MessageID, row.ThreadID, row.MsgID, when)
	}
	if result.TruncatedSettled > 0 {
		fmt.Fprintf(&out, "truncated: %d oldest settled row(s) hidden (the claim tombstones are retained)\n", result.TruncatedSettled)
	}
	fmt.Fprint(a.stdout, out.String())
	return nil
}

// claudeResolveAdmission recomputes the native-delivery admission for one
// binding key under the claude-bindings lock, exactly as the deliver helper
// does at attempt time: a retry may only re-arm a claim whose scope is
// currently registered under its pinned hub identity, live, policy-enabled,
// still witnessed by the config at its own stored path, and still fenced by
// the same generation (build qualification is parked).
func (a *app) claudeResolveAdmission(key, generation string) error {
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 5*time.Second)
	defer cancel()
	unlock, err := a.lockLocalState(ctx, claudeLockName)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := a.readClaudeBindingsDocument()
	if err != nil {
		return err
	}
	settings, err := a.claudeSettings()
	if err != nil {
		return err
	}
	for _, b := range rows.Bindings {
		if claudeBindingKey(b.claudeScope) != key {
			continue
		}
		if b.Generation != generation {
			return usageErr("claude resolve: the binding generation changed before the retry; the claim stays fenced and unchanged")
		}
		// The canary opt-in is checked first: with build gating parked it
		// is the only production gate on a native write.
		if evidence := claudeAdmissionEvidence(b, rows.OptOuts, rows.Canary, settings.Disabled); !evidence.Canary {
			return usageErr("claude resolve: " + errClaudeCanaryNotOptIn.Error())
		} else if !evidence.Live {
			return usageErr("claude resolve: the registered claude root is not live")
		} else if !evidence.Policy {
			return usageErr("claude resolve: the recipient opted out of native delivery")
		} else if !evidence.VersionQualified {
			return usageErr("claude resolve: the registered build is not a qualified native target")
		}
		if err := a.claudeScopeWitnessAt(b.claudeScope); err != nil {
			return usageErr("claude resolve: the binding's stored config no longer witnesses its scope (config witness): " + err.Error())
		}
		if err := validateClaudeRoot(b.claudeRoot); err != nil {
			return usageErr("claude resolve: the registered root no longer validates: " + err.Error())
		}
		return nil
	}
	return usageErr("claude resolve: no registered binding matches this recipient scope")
}

// claudeClaimAttemptID mints a fresh opaque attempt identity, in the same
// shape the TS adapter mints per attempt.
func claudeClaimAttemptID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// cmdClaudeResolve settles one claim by delivery id. --keep-inbox stops
// native promotion and keeps the delivery readable in the inbox without
// acknowledging anything; --retry re-arms exactly one fresh native attempt
// and only after the explicit duplicate-risk confirmation plus a fresh
// admission check. Wrong generation, unknown deliveries and foreign scopes
// refuse with the stored state untouched.
func (a *app) cmdClaudeResolve(args []string) error {
	o, err := parseFlags("claude resolve", args, map[string]flagKind{
		"expect-generation": strFlag, "keep-inbox": boolFlag, "retry": boolFlag,
		"confirm-duplicate-risk": boolFlag,
	})
	if err != nil {
		return err
	}
	if len(o.pos) != 1 {
		return usageErr("claude resolve takes exactly one delivery id from `claude deliveries`")
	}
	delivery := o.pos[0]
	if !claudeClaimDeliveryID.MatchString(delivery) {
		return usageErr("claude resolve takes a delivery id from `claude deliveries`, never a path or free text")
	}
	if len(o.all("expect-generation")) != 1 || !claudeToken.MatchString(o.str("expect-generation")) {
		return usageErr("claude resolve requires the claim's generation from `claude deliveries`: --expect-generation G")
	}
	expect := o.str("expect-generation")
	keep, retry := o.has("keep-inbox"), o.has("retry")
	if keep == retry {
		return usageErr("claude resolve requires exactly one of --keep-inbox or --retry")
	}
	if retry && !o.has("confirm-duplicate-risk") {
		return usageErr("claude resolve --retry may repeat a native write that already left; pass --confirm-duplicate-risk to accept that risk")
	}
	profile := a.namedProfile()
	if profile == "" {
		return usageErr("claude resolve requires the addressed profile: --net PROFILE")
	}
	// An unpinned profile still reaches its legacy-key claims for
	// --keep-inbox; --retry needs the pinned key and refuses below.
	scope, err := a.claudeScopeForProfileAllowLegacy(profile)
	if err != nil {
		return err
	}
	if err := claudeOptOutScopeValid(scope); err != nil {
		return err
	}
	key := claudeBindingKey(scope)
	keys := claudeClaimScopeKeys(scope)
	path, err := claudeClaimsLedgerPath(a.getenv)
	if err != nil {
		return err
	}
	db, err := openClaudeClaimsLedger(path)
	if err != nil {
		return err
	}
	if db == nil {
		return usageErr("claude resolve: no claude delivery activity is recorded")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM kv WHERE ns = ? AND key = ?`,
		claudeClaimsNS, claudeClaimKeyPrefix+delivery).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return usageErr("claude resolve: no claim is stored for delivery " + delivery)
	}
	if err != nil {
		return err
	}
	if len(raw) > claudeClaimMax {
		return usageErr("claude resolve: the stored claim is unreadable and is never promoted")
	}
	claim, err := decodeClaudeClaim([]byte(raw))
	if err != nil {
		return usageErr("claude resolve: the stored claim is corrupt and is never promoted")
	}
	if !slices.Contains(keys, claim.Scope) {
		return usageErr("claude resolve: the claim belongs to another recipient scope")
	}
	if claim.Generation != expect {
		return usageErr("claude resolve: the claim generation moved; re-read `claude deliveries` — the stored state is unchanged")
	}
	if retry {
		// A claim recorded under the legacy endpoint key (or addressed
		// through a profile with no pinned hub identity) belongs to a scope
		// that can never be admitted again: inbox-only, whatever its state.
		if !claudeHubPinned(scope.HubIdentity) || claim.Scope != key {
			return usageErr("claude resolve --retry: the claim was recorded under the legacy endpoint-keyed scope, which is never admitted again; use --keep-inbox")
		}
		// Retry fences, checked before admission and before any mutation: a
		// hold-priority delivery is inbox-only full stop (never re-armed for
		// native transport however it settled), and a claim still owing a
		// disposition (reserved, attempting) or already held cannot be re-armed
		// under a live attempt. Only terminal claims are resolvable.
		if claim.Priority == "hold" || claim.State == "held" {
			return usageErr("claude resolve --retry: a held claim is inbox-only and is never re-armed for native transport")
		}
		switch claim.State {
		case "refused", "uncertain", "released", "expired":
		default:
			return usageErr("claude resolve --retry: the claim is not in a resolvable state (it still owes a native disposition); re-read `claude deliveries`")
		}
		if err := a.claudeResolveAdmission(key, expect); err != nil {
			return err
		}
	}
	claim.UpdatedAt = float64(time.Now().UnixMilli())
	state, human := "", ""
	switch {
	case keep:
		// Inbox-only: native promotion stops (the host only re-attempts
		// reserved claims), the delivery stays readable, nothing is acked.
		claim.State, claim.Disposition, claim.DispositionReason = "held", "held", "operator_keep_inbox"
		state, human = "held", "held inbox-only; it stays readable and is never acknowledged"
	case retry:
		// One fresh attempt: reserved again with a fresh attempt identity.
		// The host's own fences (admission at attempt time, generation, fresh
		// correlation id) stay in charge of the write itself.
		attempt, err := claudeClaimAttemptID()
		if err != nil {
			return err
		}
		claim.State, claim.Disposition, claim.DispositionReason, claim.AttemptID = "reserved", "", "", attempt
		state, human = "reserved", "re-armed for one fresh native attempt (the delivery host re-checks admission before any write)"
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kv (ns, key, value) VALUES (?, ?, ?)
		ON CONFLICT(ns, key) DO UPDATE SET value=excluded.value`,
		claudeClaimsNS, claudeClaimKeyPrefix+delivery, string(encoded)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if a.json {
		return a.emitValue(map[string]any{"ok": true, "delivery": delivery, "state": state})
	}
	return a.ok("claude resolve: " + delivery + " " + human)
}
