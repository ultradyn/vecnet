package mcpserver

import "testing"

func TestTimestampNormalizationReplacesOnlyTokens(t *testing.T) {
	before := []byte(" { \"attachments\" : [ {\"expires_at\" : \"2030-01-02T13:04:05+10:00\", \"name\":\"a\", \"name\":\"b\", \"size\":5.0} ], \"metadata\":{\"expires_at\":\"unchanged\"} } ")
	want := " { \"attachments\" : [ {\"expires_at\" : \"2030-01-02T03:04:05.000Z\", \"name\":\"a\", \"name\":\"b\", \"size\":5.0} ], \"metadata\":{\"expires_at\":\"unchanged\"} } "
	if got := string(normalizeToolTimestamps("vecnet_send", before)); got != want {
		t.Errorf("surrounding bytes changed:\n got %s\nwant %s", got, want)
	}
}
