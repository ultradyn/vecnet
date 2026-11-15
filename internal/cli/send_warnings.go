package cli

import (
	"encoding/json"
	"strings"
)

// sendWarnings reads optional receipt metadata independently of the send's
// required fields: an advisory must never turn a completed send into an error.
func (a *app) sendWarnings(raw json.RawMessage) {
	var receipt struct {
		Warnings json.RawMessage `json:"warnings"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return
	}
	var warnings []json.RawMessage
	if json.Unmarshal(receipt.Warnings, &warnings) != nil {
		return
	}
	for _, item := range warnings {
		var warning struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			GroupID   string `json:"group_id"`
			Recipient string `json:"recipient"`
		}
		if json.Unmarshal(item, &warning) != nil {
			continue
		}
		subject := warning.GroupID
		switch warning.Code {
		case "rapid_thread_creation":
		case "recipient_not_seen":
			subject = warning.Recipient
		default:
			continue
		}
		message := strings.TrimSpace(unbidi(flatten(warning.Message)))
		if message == "" {
			continue
		}
		subject = strings.TrimSpace(unbidi(flatten(subject)))
		if subject != "" {
			a.warn("%s: %s", subject, message)
		} else {
			a.warn("%s", message)
		}
	}
}
