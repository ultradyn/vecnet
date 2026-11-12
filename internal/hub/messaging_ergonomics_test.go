package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// Wire-only decoding keeps behavior RED independent of new Go product fields.
type ergReceipt struct {
	MessageID  string `json:"message_id"`
	ThreadID   string `json:"thread_id"`
	Deliveries []struct {
		DeliveryID    string  `json:"delivery_id"`
		Recipient     string  `json:"recipient"`
		RecipientName string  `json:"recipient_name"`
		Via           *string `json:"via"`
		State         string  `json:"state"`
	} `json:"deliveries"`
}

func ergDecodeReceipt(t *testing.T, raw []byte) ergReceipt {
	t.Helper()
	var receipt ergReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestSendReceiptNameSnapshotSurvivesRenameReplay(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Mira")
	body := req("erg-snapshot", b)
	status, _, firstRaw := sendHTTP(t, env, secret, body)
	if status != http.StatusAccepted {
		t.Fatalf("initial send: %d %s", status, firstRaw)
	}
	first := ergDecodeReceipt(t, firstRaw)
	if err := env.Hub.RenamePeer(context.Background(), b, "Mira Renamed"); err != nil {
		t.Fatal(err)
	}
	status, _, replayRaw := sendHTTP(t, env, secret, body)
	if status != http.StatusOK || !bytes.Equal(firstRaw, replayRaw) {
		t.Fatalf("replay changed original receipt: status=%d first=%s replay=%s", status, firstRaw, replayRaw)
	}
	status, _, freshRaw := sendHTTP(t, env, secret, req("erg-fresh", b))
	fresh := ergDecodeReceipt(t, freshRaw)
	if status != http.StatusAccepted || len(first.Deliveries) != 1 || len(fresh.Deliveries) != 1 || first.Deliveries[0].Recipient != b || first.Deliveries[0].RecipientName != "Mira" || fresh.Deliveries[0].RecipientName != "Mira Renamed" {
		t.Errorf("names are not send-time snapshots: first=%s fresh=%s", firstRaw, freshRaw)
	}
	// A receipt from before the feature remains replayable without enrichment.
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(firstRaw, &legacy); err != nil {
		t.Fatal(err)
	}
	var deliveries []map[string]json.RawMessage
	if err := json.Unmarshal(legacy["deliveries"], &deliveries); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		delete(delivery, "recipient_name")
	}
	legacy["deliveries"], _ = json.Marshal(deliveries)
	legacyRaw, _ := json.Marshal(legacy)
	if _, err := env.Hub.DB.Exec(`UPDATE idempotency SET response=? WHERE sender=? AND key=?`, string(legacyRaw), a, "erg-snapshot"); err != nil {
		t.Fatal(err)
	}
	status, _, legacyReplay := sendHTTP(t, env, secret, body)
	if status != http.StatusOK || ergDecodeReceipt(t, legacyReplay).Deliveries[0].RecipientName != "" || count(t, env, `SELECT count(*) FROM messages`) != 2 {
		t.Fatal("legacy replay was enriched, rerouted or resent")
	}
}

func TestSendReceiptGroupOverlapNames(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Shared")
	c, _ := env.NewPeer("session", "Shared") // Fixed display names are not unique addresses.
	g1 := addGroup(t, env, a, a, b, c)
	g2 := addGroup(t, env, a, c)
	status, sent, raw := sendHTTP(t, env, secret, req("erg-groups", b, g1, g2))
	receipt := ergDecodeReceipt(t, raw)
	if status != http.StatusAccepted || len(receipt.Deliveries) != 2 {
		t.Fatalf("group receipt: %d %s", status, raw)
	}
	if receipt.Deliveries[0].Recipient != b || receipt.Deliveries[0].Via != nil {
		t.Fatal("direct-first delivery order/via changed")
	}
	seen := map[string]string{}
	for _, delivery := range receipt.Deliveries {
		seen[delivery.Recipient] = delivery.RecipientName
		if delivery.State != "queued" || delivery.DeliveryID == "" {
			t.Fatal("receipt lost delivery identity/state")
		}
		if delivery.Recipient == c && (delivery.Via == nil || *delivery.Via != g1) {
			t.Fatal("group delivery lost first via")
		}
	}
	if !reflect.DeepEqual(seen, map[string]string{b: "Shared", c: "Shared"}) {
		t.Errorf("receipt must name both distinct canonical peers: %s", raw)
	}
	for _, peer := range []string{b, c} {
		events := deliveryEvents(t, env, peer)
		if len(events) != 1 {
			t.Fatal("overlapping groups duplicated delivery")
		}
		_, _, message := decodeDelivery(t, events[0]) // Still canonical-schema-valid.
		if message.MessageID != sent.MessageID || !reflect.DeepEqual(message.Loop.SenderChain, []string{a}) || bytes.Contains(events[0], []byte(`"recipient_name"`)) {
			t.Fatal("receipt metadata leaked into canonical delivery or changed loop")
		}
	}
}

func TestSendNamesAreNotAddressesStill(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Shared")
	env.NewPeer("session", "Shared")
	for _, name := range []string{"Shared", "missing", "@Shared"} {
		status, _, raw := sendHTTP(t, env, secret, req("erg-name-"+name, name))
		var result struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusUnprocessableEntity || result.Error.Code != "name_not_an_address" {
			t.Fatalf("display name became an address: %d %s", status, raw)
		}
	}
	mustSend(t, env, secret, req("erg-id", b))
}
