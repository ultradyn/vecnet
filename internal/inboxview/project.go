// Package inboxview renders compact inbox views without changing canonical events.
package inboxview

import (
	"encoding/json"
	"fmt"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Project copies events for presentation. Unless include is true, it removes
// only delivery.message.loop.sender_chain. Unknown fields and numeric tokens
// remain intact; canonical input events and their Data buffers are never changed.
func Project(events []envelope.Event, include bool) ([]envelope.Event, error) {
	out := make([]envelope.Event, len(events))
	copy(out, events)
	if include {
		return out, nil
	}
	for i, event := range events {
		if event.Type != "delivery" {
			continue
		}
		data, err := withoutSenderChain(event.Data)
		if err != nil {
			return nil, fmt.Errorf("inbox view: delivery %d: %w", i, err)
		}
		out[i].Data = data
	}
	return out, nil
}

func withoutSenderChain(raw json.RawMessage) (json.RawMessage, error) {
	var data, message, loop map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if len(data["message"]) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data["message"], &message); err != nil {
		return nil, err
	}
	if len(message["loop"]) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(message["loop"], &loop); err != nil {
		return nil, err
	}
	if _, present := loop["sender_chain"]; !present {
		return raw, nil
	}
	delete(loop, "sender_chain")
	var err error
	if message["loop"], err = json.Marshal(loop); err != nil {
		return nil, err
	}
	if data["message"], err = json.Marshal(message); err != nil {
		return nil, err
	}
	return json.Marshal(data)
}
