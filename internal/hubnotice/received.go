// Package hubnotice validates received notice URLs without changing signed values.
package hubnotice

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

// ErrInvalidNotice is a protocol refusal that contains no received fields.
var ErrInvalidNotice = errors.New("invalid hub notice")

func validURL(raw string, allowHTTP bool) bool {
	err := hubid.CheckHubURL(raw)
	return err == nil || (allowHTTP && errors.Is(err, hubid.ErrPlainHTTP))
}

// ValidateNotice checks URLs only, including NewURL on cleared or nonmoving
// notices. Authentication and lifecycle policy remain the caller's responsibility.
func ValidateNotice(n envelope.HubNotice, allowHTTP bool) error {
	if !validURL(n.Hub, allowHTTP) || (n.NewURL != "" && !validURL(n.NewURL, allowHTTP)) {
		return ErrInvalidNotice
	}
	return nil
}

// decodeNotice also refuses null notice objects and null optional URL fields,
// which decoding directly into string fields would otherwise erase.
func decodeNotice(raw json.RawMessage, allowHTTP bool) (envelope.HubNotice, error) {
	var n envelope.HubNotice
	var fields struct {
		Hub    json.RawMessage `json:"hub"`
		NewURL json.RawMessage `json:"new_url"`
	}
	if json.Unmarshal(raw, &n) != nil || json.Unmarshal(raw, &fields) != nil ||
		bytes.Equal(bytes.TrimSpace(fields.Hub), []byte("null")) ||
		bytes.Equal(bytes.TrimSpace(fields.NewURL), []byte("null")) {
		return envelope.HubNotice{}, ErrInvalidNotice
	}
	if err := ValidateNotice(n, allowHTTP); err != nil {
		return envelope.HubNotice{}, err
	}
	return n, nil
}

// ValidateEvent refuses malformed hub_notice payloads and leaves other event
// types alone. It never rebuilds or modifies the original Event.Data.
func ValidateEvent(event envelope.Event, allowHTTP bool) error {
	if event.Type != "hub_notice" {
		return nil
	}
	var payload struct {
		Change string          `json:"change"`
		Notice json.RawMessage `json:"notice"`
	}
	if json.Unmarshal(event.Data, &payload) != nil {
		return ErrInvalidNotice
	}
	_, err := decodeNotice(payload.Notice, allowHTTP)
	return err
}

// ValidateBatch checks the entire page before a caller can apply any events.
func ValidateBatch(events []envelope.Event, allowHTTP bool) error {
	for _, event := range events {
		if err := ValidateEvent(event, allowHTTP); err != nil {
			return err
		}
	}
	return nil
}

// ProjectRelocation replaces received error prose and keeps only validated
// recovery fields. Notice message prose is retained as part of the signed notice;
// URL validation neither authenticates nor filters that prose.
func ProjectRelocation(details map[string]any, allowHTTP bool) (string, map[string]any) {
	var safe map[string]any
	if value, present := details["new_url"]; present {
		target, ok := value.(string)
		if !ok || !validURL(target, allowHTTP) {
			return "invalid hub relocation", nil
		}
		safe = map[string]any{"new_url": target}
	}
	if value, present := details["notice"]; present {
		raw, err := json.Marshal(value)
		if err != nil {
			return "invalid hub relocation", nil
		}
		n, err := decodeNotice(raw, allowHTTP)
		if err != nil {
			return "invalid hub relocation", nil
		}
		if safe == nil {
			safe = make(map[string]any)
		}
		safe["notice"] = n
	}
	return "hub moved", safe
}
