package eventpayload_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventpayload"
)

const snapshotMessageID = "msg_00000000000000000000000001"

func snapshotMessage(id string) string {
	return fmt.Sprintf(`{"schema_version":1,"message_id":%q,"thread_id":"thr_00000000000000000000000001","in_reply_to":null,"origin":null,"kind":"message","sender":"hub","recipients":["peer_00000000000000000000000001"],"priority":"hold","created_at":"2026-11-05T00:00:00.000Z","content":{"text":"synthetic-original"},"attachments":[],"metadata":{},"loop":{"depth":0,"max_depth":8,"expires_at":"2026-11-06T00:00:00.000Z","sender_chain":[]}}`, id)
}

func snapshotDelivery(body string) string {
	return `{"schema_version":1,"delivery_id":"dly_00000000000000000000000001","recipient":"peer_00000000000000000000000001","via":null,"attempt":1,"ingress":"http_send","thread":{"thread_id":"thr_00000000000000000000000001","title":null,"participants":[],"group":null,"message_count":1,"budget_remaining":null,"history_access":"from_join"},"message":` + body + `}`
}

func TestUnsupportedSnapshotMessagePreservesInline(t *testing.T) {
	body := snapshotMessage(snapshotMessageID)
	if envelope.Validate("delivery", []byte(snapshotDelivery(body))) != nil {
		t.Fatal("baseline Delivery must satisfy the public schema")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal("invalid fixture")
	}
	cases := make(map[string]string)
	for key := range fields {
		changed := make(map[string]json.RawMessage, len(fields))
		for k, v := range fields {
			if k != key {
				changed[k] = v
			}
		}
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal("invalid fixture")
		}
		cases["missing_"+key] = string(raw)
	}
	for key, value := range map[string]string{
		"schema_version": `2`, "content": `{"text":null}`, "sender": `null`,
		"recipients": `[]`, "loop": `{}`, "metadata": `null`, "attachments": `[{}]`,
	} {
		changed := make(map[string]json.RawMessage, len(fields))
		for k, v := range fields {
			changed[k] = v
		}
		changed[key] = json.RawMessage(value)
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal("invalid fixture")
		}
		cases["unsupported_"+key] = string(raw)
	}
	db := openDB(t)
	for name, embedded := range cases {
		t.Run(name, func(t *testing.T) {
			wire := []byte(snapshotDelivery(embedded))
			if envelope.Validate("delivery", wire) == nil {
				t.Fatal("regression fixture must violate Message schema")
			}
			err := db.Tx(context.Background(), func(tx *sql.Tx) error {
				var n int
				if err := tx.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 0 {
					t.Fatal("canonical Message must be missing")
				}
				p, err := eventpayload.EncodeDelivery(context.Background(), tx, snapshotMessageID, wire)
				var nc *eventpayload.NotConvertibleError
				if !errors.Is(err, eventpayload.ErrNotConvertible) || !errors.As(err, &nc) || nc.Reason != "invalid_shape" {
					t.Fatal("unsupported Message did not yield invalid_shape")
				}
				if p.Format != eventpayload.Inline || !bytes.Equal(p.Data, wire) || p.MsgID != "" || p.SnapshotID != "" {
					t.Fatal("fallback changed inline bytes or reference identity")
				}
				// Commit the fallback: rollback must not hide an accidental insert.
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM event_message_snapshots`).Scan(&n); err != nil || n != 0 {
				t.Fatal("unsupported Message inserted a snapshot")
			}
		})
	}
}

func TestValidLegacySnapshotPreservesUnknownBytes(t *testing.T) {
	db := openDB(t)
	body := snapshotMessage(snapshotMessageID)
	if envelope.Validate("message", []byte(body)) != nil {
		t.Fatal("baseline Message must satisfy public schema")
	}
	// Historical Messages may omit sender_info and contain unknown nested fields.
	body = `{ "legacy" : [1.50,1e3,{"message":"escaped \\"}], ` + body[1:]
	body = string(bytes.Replace([]byte(body), []byte(`"content":{"text":"synthetic-original"}`), []byte(`"content" : { "legacy":2.00, "text":"synthetic-original" }`), 1))
	wire := snapshotDelivery(body)
	first := encode(t, db, snapshotMessageID, wire)
	second := encode(t, db, snapshotMessageID, wire)
	if first.Format != eventpayload.DeliveryRefV1 || first.SnapshotID == "" || first.SnapshotID != second.SnapshotID {
		t.Fatal("valid legacy snapshot was not deduplicated")
	}
	roundtrip(t, db, first, wire)
	var stored string
	if err := db.QueryRow(`SELECT body FROM event_message_snapshots WHERE snapshot_id=?`, first.SnapshotID).Scan(&stored); err != nil || stored != body {
		t.Fatal("legacy snapshot bytes changed")
	}
}
