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
			Code    string `json:"code"`
			Message string `json:"message"`
			GroupID string `json:"group_id"`
		}
		if json.Unmarshal(item, &warning) != nil || warning.Code != "rapid_thread_creation" {
			continue
		}
		message := strings.TrimSpace(unbidi(flatten(warning.Message)))
		if message == "" {
			continue
		}
		group := strings.TrimSpace(unbidi(flatten(warning.GroupID)))
		if group != "" {
			a.warn("%s: %s", group, message)
		} else {
			a.warn("%s", message)
		}
	}
}
