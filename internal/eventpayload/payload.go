// Package eventpayload stores hub-private Delivery references and reconstructs
// the exact send-time wire bytes. Callers own transactions and row lifetimes;
// no connection pool, mutable directory state, or process cache is used here.
package eventpayload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	Inline        = "inline"
	DeliveryRefV1 = "delivery_ref_v1"
	maxKeys       = 200
)

// StoredPayload is storage metadata, never a public Event. Empty MsgID and
// SnapshotID represent NULL columns. BodySHA256 optionally carries the existing
// delivery row's hash (e.g. from a claim join) for a second integrity check.
type StoredPayload struct {
	Format     string
	Data       []byte
	MsgID      string
	SnapshotID string
	BodySHA256 string
}

// Queryer must be the caller's read snapshot or writer transaction. Hydration
// never acquires a connection behind this interface.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Transaction is implemented by *sql.Tx. EncodeDelivery may insert a snapshot,
// so its caller must commit it atomically with the referencing event.
type Transaction interface {
	Queryer
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

var ErrNotConvertible = errors.New("eventpayload: not_convertible")

// NotConvertibleError leaves the original event inline. Reasons are stable,
// content-free codes: invalid_shape, duplicate_key, identity_mismatch.
type NotConvertibleError struct{ Reason string }

func (e *NotConvertibleError) Error() string { return "eventpayload: not_convertible: " + e.Reason }
func (e *NotConvertibleError) Unwrap() error { return ErrNotConvertible }

// StorageError aborts the entire page. Errors deliberately exclude driver and
// parser messages: those can contain payload bytes or signed URLs.
type StorageError struct{ Reason string }

func (e *StorageError) Error() string { return "eventpayload: storage_error: " + e.Reason }
func storage(reason string) error     { return &StorageError{Reason: reason} }

// reference contains only immutable byte slices and their identities. JSON
// strings preserve exact prefix/suffix and attachment-array bytes on decode.
type reference struct {
	MsgID           string `json:"msg_id"`
	SnapshotID      string `json:"snapshot_id,omitempty"`
	BodySHA256      string `json:"body_sha256"`
	WirePrefix      string `json:"wire_prefix"`
	WireSuffix      string `json:"wire_suffix"`
	AttachmentsJSON string `json:"attachments_json,omitempty"`
	WireSHA256      string `json:"wire_sha256"`
}

type span struct{ start, end int }

func (s span) value(b []byte) []byte { return b[s.start:s.end] }

// object locates top-level values with the JSON decoder's byte offsets. It
// respects escapes/nesting and detects duplicate decoded keys (including escaped
// spellings). Values are never remarshal'ed; nested unknown fields survive.
func object(b []byte) (map[string]span, string) {
	if !json.Valid(b) {
		return nil, "invalid_shape"
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, "invalid_shape"
	}
	fields := make(map[string]span)
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return nil, "invalid_shape"
		}
		key, ok := tok.(string)
		if !ok {
			return nil, "invalid_shape"
		}
		if _, ok := fields[key]; ok {
			return nil, "duplicate_key"
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, "invalid_shape"
		}
		end := int(d.InputOffset())
		fields[key] = span{end - len(v), end}
	}
	return fields, ""
}
func splice(b []byte, s span, replacement []byte) []byte {
	out := make([]byte, 0, len(b)-(s.end-s.start)+len(replacement))
	out = append(out, b[:s.start]...)
	out = append(out, replacement...)
	return append(out, b[s.end:]...)
}
func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func message(b []byte, id string) (map[string]span, string) {
	f, reason := object(b)
	if reason != "" {
		return nil, reason
	}
	s, ok := f["message_id"]
	if !ok {
		return nil, "invalid_shape"
	}
	var have string
	if json.Unmarshal(s.value(b), &have) != nil || have == "" || have != id {
		return nil, "identity_mismatch"
	}
	if a, ok := f["attachments"]; ok && a.value(b)[0] != '[' {
		return nil, "invalid_shape"
	}
	return f, ""
}

// EncodeDelivery derives a reference by byte splicing, never by rebuilding a
// Delivery. A non-convertible shape returns the untouched inline payload and a
// typed error; callers may keep it inline. Database/integrity errors must abort.
func EncodeDelivery(ctx context.Context, tx Transaction, msgID string, wire []byte) (StoredPayload, error) {
	inline := StoredPayload{Format: Inline, Data: wire}
	fallback := func(reason string) (StoredPayload, error) { return inline, &NotConvertibleError{Reason: reason} }
	if !utf8.Valid(wire) {
		return fallback("invalid_shape")
	}
	fields, reason := object(wire)
	if reason != "" {
		return fallback(reason)
	}
	m, ok := fields["message"]
	if !ok {
		return fallback("invalid_shape")
	}
	embedded := m.value(wire)
	mf, reason := message(embedded, msgID)
	if reason != "" {
		return fallback(reason)
	}
	// Validate an already materialized delivery's expected bytes before conversion.
	if s, ok := fields["delivery_id"]; ok {
		var id string
		if json.Unmarshal(s.value(wire), &id) != nil || id == "" {
			return fallback("invalid_shape")
		}
		var hash string
		err := tx.QueryRowContext(ctx, `SELECT body_sha256 FROM deliveries WHERE dly_id=?`, id).Scan(&hash)
		if err == nil && hash != digest(wire) {
			return StoredPayload{}, storage("delivery_hash_mismatch")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return StoredPayload{}, storage("delivery_query")
		}
	}
	var canonical []byte
	err := tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE msg_id=?`, msgID).Scan(&canonical)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StoredPayload{}, storage("body_query")
	}
	base := embedded
	r := reference{MsgID: msgID, WirePrefix: string(wire[:m.start]), WireSuffix: string(wire[m.end:]), WireSHA256: digest(wire)}
	if err == nil {
		cf, why := message(canonical, msgID)
		if why != "" {
			return StoredPayload{}, storage("canonical_invalid")
		}
		ma, mok := mf["attachments"]
		ca, cok := cf["attachments"]
		if mok && cok && !bytes.Equal(ma.value(embedded), ca.value(canonical)) {
			r.AttachmentsJSON = string(ma.value(embedded))
			base = splice(embedded, ma, ca.value(canonical))
		}
	}
	r.BodySHA256 = digest(base)
	// Independent reconstruction proof before publishing any reference.
	composed, err := compose(r, base)
	if err != nil {
		return fallback("invalid_shape")
	}
	if !bytes.Equal(composed, wire) {
		return fallback("invalid_shape")
	}
	if !bytes.Equal(base, canonical) {
		// Deterministic identity; UNIQUE(msg_id,body_sha256) is authoritative. Full
		// bytes are checked even when a hash matches, never trusting hash equality.
		r.SnapshotID = "snap_" + digest([]byte(msgID+":"+r.BodySHA256))
		// Skip existing dedup keys before BEFORE INSERT guards run. An existing
		// snapshot may have a different id; never let REPLACE discard that identity.
		// The read below still checks its complete bytes, including hash collisions.
		_, err = tx.ExecContext(ctx, `INSERT INTO event_message_snapshots(snapshot_id,msg_id,body_sha256,body)
 SELECT ?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM event_message_snapshots WHERE msg_id=? AND body_sha256=?)
 ON CONFLICT(msg_id,body_sha256) DO NOTHING`, r.SnapshotID, msgID, r.BodySHA256, string(base), msgID, r.BodySHA256)
		if err != nil {
			return StoredPayload{}, storage("snapshot_insert")
		}
		var have []byte
		err = tx.QueryRowContext(ctx, `SELECT snapshot_id,body FROM event_message_snapshots WHERE msg_id=? AND body_sha256=?`, msgID, r.BodySHA256).Scan(&r.SnapshotID, &have)
		if err != nil {
			return StoredPayload{}, storage("snapshot_query")
		}
		if !bytes.Equal(have, base) {
			return StoredPayload{}, storage("snapshot_hash_collision")
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return StoredPayload{}, storage("reference_encode")
	}
	return StoredPayload{Format: DeliveryRefV1, Data: data, MsgID: msgID, SnapshotID: r.SnapshotID}, nil
}

func decode(p StoredPayload) (reference, error) {
	var r reference
	f, why := object(p.Data)
	if why != "" {
		return r, storage("reference_invalid")
	}
	allowed := map[string]bool{"msg_id": true, "snapshot_id": true, "body_sha256": true, "wire_prefix": true, "wire_suffix": true, "attachments_json": true, "wire_sha256": true}
	for key, s := range f {
		if !allowed[key] || s.value(p.Data)[0] != '"' {
			return r, storage("reference_invalid")
		}
	}
	for _, key := range []string{"msg_id", "body_sha256", "wire_prefix", "wire_suffix", "wire_sha256"} {
		if _, ok := f[key]; !ok {
			return r, storage("reference_invalid")
		}
	}
	if json.Unmarshal(p.Data, &r) != nil || r.MsgID == "" || r.MsgID != p.MsgID || r.SnapshotID != p.SnapshotID || !validHash(r.BodySHA256) || !validHash(r.WireSHA256) {
		return r, storage("reference_identity")
	}
	if p.BodySHA256 != "" && p.BodySHA256 != r.WireSHA256 {
		return r, storage("delivery_hash_mismatch")
	}
	if r.AttachmentsJSON != "" && (!json.Valid([]byte(r.AttachmentsJSON)) || bytes.TrimSpace([]byte(r.AttachmentsJSON))[0] != '[') {
		return r, storage("reference_attachments")
	}
	return r, nil
}
func compose(r reference, base []byte) ([]byte, error) {
	fields, reason := message(base, r.MsgID)
	if reason != "" {
		return nil, storage("body_invalid")
	}
	embedded := base
	if r.AttachmentsJSON != "" {
		a, ok := fields["attachments"]
		if !ok {
			return nil, storage("body_attachments")
		}
		embedded = splice(base, a, []byte(r.AttachmentsJSON))
	}
	wire := make([]byte, 0, len(r.WirePrefix)+len(embedded)+len(r.WireSuffix))
	wire = append(wire, r.WirePrefix...)
	wire = append(wire, embedded...)
	wire = append(wire, r.WireSuffix...)
	f, why := object(wire)
	s, ok := f["message"]
	if why != "" || !ok || !bytes.Equal(s.value(wire), embedded) || digest(wire) != r.WireSHA256 {
		return nil, storage("wire_hash_mismatch")
	}
	return wire, nil
}

// HydrateBatch preserves ordering and returns no partial page on failure. It
// groups recipient indices by immutable body key, then streams each body once
// to every recipient. There is no retained body cache (and thus no 8 MiB cache
// overflow), even for pages larger than 8 MiB. The necessary output bytes are
// owned by the caller. Each lookup binds at most 200 keys, independent of fanout.
// Callers must close event rows before calling and use one consistent snapshot.
func HydrateBatch(ctx context.Context, q Queryer, rows []StoredPayload) ([][]byte, error) {
	return hydrateBatch(ctx, q, rows, nil)
}

// workingBody owns the single scanned source buffer until composition finishes.
// Its observer accounts only retained source bytes, excluding returned wires and
// temporary parser/composition allocations. Keeping the buffer in this scope
// makes release deterministic on success and every error path.
type workingBody struct {
	bytes   []byte
	observe func(int)
}

func (b *workingBody) release() {
	b.bytes = nil
	if b.observe != nil {
		b.observe(0)
	}
}

func hydrateBatch(ctx context.Context, q Queryer, rows []StoredPayload, observe func(int)) ([][]byte, error) {
	out := make([][]byte, len(rows))
	refs := make([]reference, len(rows))
	groups := [2]map[string][]int{make(map[string][]int), make(map[string][]int)}
	keys := [2][]string{}
	for i, p := range rows {
		switch p.Format {
		case Inline:
			if p.MsgID != "" || p.SnapshotID != "" {
				return nil, storage("inline_identity")
			}
			out[i] = p.Data
		case DeliveryRefV1:
			r, err := decode(p)
			if err != nil {
				return nil, err
			}
			refs[i] = r
			kind, key := 0, r.MsgID
			if r.SnapshotID != "" {
				kind, key = 1, r.SnapshotID
			}
			if _, ok := groups[kind][key]; !ok {
				keys[kind] = append(keys[kind], key)
			}
			groups[kind][key] = append(groups[kind][key], i)
		default:
			return nil, storage("format_invalid")
		}
	}
	for kind, all := range keys {
		for start := 0; start < len(all); start += maxKeys {
			end := min(start+maxKeys, len(all))
			args := make([]any, end-start)
			for i, key := range all[start:end] {
				args[i] = key
			}
			selectSQL := "SELECT msg_id,msg_id,NULL,body FROM messages WHERE msg_id IN ("
			if kind == 1 {
				selectSQL = "SELECT snapshot_id,msg_id,body_sha256,body FROM event_message_snapshots WHERE snapshot_id IN ("
			}
			result, err := q.QueryContext(ctx, selectSQL+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
			if err != nil {
				return nil, storage("body_query")
			}
			err = func() error {
				defer result.Close()
				seen := 0
				for result.Next() {
					err := func() error {
						var key, id string
						var storedHash sql.NullString
						body := workingBody{observe: observe}
						defer body.release()
						if err := result.Scan(&key, &id, &storedHash, &body.bytes); err != nil {
							return storage("body_scan")
						}
						if body.observe != nil {
							body.observe(len(body.bytes))
						}
						hash := digest(body.bytes)
						if kind == 1 && (!storedHash.Valid || storedHash.String != hash) {
							return storage("snapshot_hash_mismatch")
						}
						for _, i := range groups[kind][key] {
							r := refs[i]
							if id != r.MsgID || hash != r.BodySHA256 {
								return storage("body_hash_mismatch")
							}
							wire, err := compose(r, body.bytes)
							if err != nil {
								return err
							}
							out[i] = wire
						}
						return nil
					}()
					if err != nil {
						return err
					}
					seen++
				}
				if result.Err() != nil {
					return storage("body_rows")
				}
				if seen != len(args) {
					return storage("body_missing")
				}
				return nil
			}()
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
