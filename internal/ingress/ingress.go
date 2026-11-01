// Package ingress maps a provider's webhook payload onto a hub.SourceMessage
// (PROTOCOL §10): verification, parsing and the per-kind summary. The HTTP
// surface is internal/httpapi; the thread, dedupe and send logic is
// hub.IngestSource.
package ingress

import (
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/hub"
)

// threadTitleMax is $defs/sendRequest's maxLength for thread_title. It
// counts characters.
const threadTitleMax = 200

// GitHubMessage maps a verified GitHub delivery (body as received) onto the
// SourceMessage for srcID. A body that is not a JSON object is ErrBadPayload.
func GitHubMessage(srcID, delivery, event string, body []byte) (hub.SourceMessage, error) {
	e, err := ParseGitHub(event, body)
	if err != nil {
		return hub.SourceMessage{}, err
	}
	e.Delivery = delivery
	origin := e.OriginThread()
	title := origin
	if e.Title != "" {
		title += ": " + e.Title
	}
	return hub.SourceMessage{
		SrcID:              srcID,
		ProviderDeliveryID: delivery,
		OriginThreadID:     origin,
		OriginEventID:      delivery,
		EventName:          e.Name(),
		Title:              cutRunes(title, threadTitleMax),
		Summary:            SummaryGitHub(e),
		Raw:                body,
		MediaType:          "application/json",
	}, nil
}

// cutRunes shortens s to at most n characters.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	end, count := 0, 0
	for i := range s {
		if count == n {
			end = i
			break
		}
		count++
	}
	return s[:end]
}
