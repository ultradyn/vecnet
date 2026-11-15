package opapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/envelope"
)

// DisplayState overlays the human read receipt only on transport acceptance.
func DisplayState(state string, readAt *string) string {
	if readAt != nil && (state == "accepted" || state == "pushed") {
		return "read"
	}
	return state
}

// Excerpt returns a single line of at most 200 UTF-8 bytes.
func Excerpt(m envelope.Message) string {
	text := m.Content.Text
	switch {
	case m.Kind == "approval_request" && m.ApprovalRequest != nil:
		text = "Approval: " + m.ApprovalRequest.Question
	case m.Kind == "approval_response" && m.ApprovalResponse != nil:
		text = "Decision: " + m.ApprovalResponse.Decision
	case m.Kind == "job_status" && m.JobStatus != nil:
		text = "Job " + m.JobStatus.State + " · " + text
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		end := 200
		for !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
	}
	return text
}

// Tags maps a decoded event payload to the snapshots it invalidates. Invalid
// or unknown events are inert, including a missing thread id on thread events.
func Tags(ev envelope.Event) []string {
	empty := []string{}
	switch ev.Type {
	case "delivery", "receipt", "thread_updated", "presence", "directory_updated", "approval_expired", "operator_view", "reaction", "hub_notice":
	default:
		return empty
	}
	raw := bytes.TrimSpace(ev.Data)
	if len(raw) == 0 || raw[0] != '{' {
		return empty
	}
	if ev.Type == "hub_notice" {
		if envelope.Validate("hubNoticeEvent", raw) != nil {
			return empty
		}
		return []string{"notices"}
	}
	if ev.Type == "operator_view" {
		// Match the web client's exact field names; struct decoding also accepts
		// alternate casing, which can hide missing or malformed wire fields.
		var data map[string]json.RawMessage
		if json.Unmarshal(raw, &data) != nil {
			return empty
		}
		var reason *string
		if json.Unmarshal(data["reason"], &reason) != nil || reason == nil {
			return empty
		}
		var thread *string
		if json.Unmarshal(data["thread_id"], &thread) != nil {
			return empty
		}
		tags := []string{"inbox", "threads", "peers"}
		if *reason == "subscription" {
			tags = append(tags, "subs")
		}
		if thread != nil && *thread != "" {
			tags = append(tags, "thread:"+*thread)
		}
		return tags
	}
	var data struct {
		ThreadID string `json:"thread_id"`
		PeerID   string `json:"peer_id"`
		Message  struct {
			ThreadID string `json:"thread_id"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return empty
	}
	if ev.Type == "presence" || ev.Type == "directory_updated" {
		if data.PeerID == "" {
			return empty
		}
		return []string{"peers"}
	}
	thread := data.ThreadID
	if ev.Type == "delivery" {
		thread = data.Message.ThreadID
	}
	if thread == "" {
		return empty
	}
	switch ev.Type {
	case "delivery":
		return []string{"inbox", "threads", "thread:" + thread}
	case "receipt", "approval_expired":
		return []string{"inbox", "thread:" + thread}
	case "thread_updated":
		return []string{"threads", "thread:" + thread}
	case "reaction":
		return []string{"thread:" + thread}
	}
	return empty
}
