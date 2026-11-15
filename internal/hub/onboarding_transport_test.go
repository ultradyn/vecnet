package hub

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/onboarding"
)

// Frozen from d0eadfb51f3f0fbe2eecb0c64bb93b89606b76b3 before T3.
// All material is synthetic; never update this encoder to follow production.
type historicalCanonicalV1 struct {
	ConnectorName   string `json:"connector_name"`
	EnrolTTLSeconds int    `json:"enrol_ttl_seconds"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	NameOpen        bool   `json:"name_open"`
	PrincipalID     string `json:"principal_id"`
	RequestID       string `json:"request_id"`
	SkipMCP         bool   `json:"skip_mcp"`
	SpecVersion     string `json:"spec_version"`
	WebhookKey      string `json:"webhook_key"`
	WebhookURL      string `json:"webhook_url"`
}

func historicalDigestV1(key []byte, n onboarding.Normalized) (string, string, error) {
	if len(key) < 32 {
		return "", "", errors.New("hub: meta.hub_key is not loaded")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("vecnet onboarding.v1 request"))
	derived := mac.Sum(nil)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	body := historicalCanonicalV1{
		ConnectorName: n.ConnectorName, EnrolTTLSeconds: n.EnrolTTLSeconds, Kind: n.Kind,
		Name: n.Name, NameOpen: n.NameOpen, PrincipalID: n.PrincipalID, RequestID: n.RequestID,
		SkipMCP: n.SkipMCP, SpecVersion: n.SpecVersion, WebhookKey: n.WebhookKey, WebhookURL: n.WebhookURL,
	}
	if err := enc.Encode(body); err != nil {
		return "", "", err
	}
	mac = hmac.New(sha256.New, derived)
	mac.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	return string(bytes.TrimRight(buf.Bytes(), "\n")), hex.EncodeToString(mac.Sum(nil)), nil
}

func historicalGrokInput() onboarding.Normalized {
	return onboarding.Normalized{Kind: "grokbot", SpecVersion: "2026-11-06", RequestID: "synthetic-historical-v1", Name: "Synthetic <Grok> & oracle", ConnectorName: "vecnet-synthetic-oracle", WebhookKey: "crsr_SYNTHETIC<oracle>&only", WebhookURL: "https://api2.cursor.sh/automations/webhook/00000000-0000-0000-0000-000000000001"}
}

func TestTransportHistoricalOracleCapture(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	n := historicalGrokInput()
	canonical, digest, err := historicalDigestV1(key, n)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != historicalCanonicalJSON || digest != historicalMAC {
		t.Fatal("frozen historical oracle changed")
	}
	h := &Hub{Blobs: &blob.Store{Key: key}}
	got, err := h.onboardDigest(n)
	if err != nil || got != digest {
		t.Fatalf("production encoder digest %s, frozen %s, error %v", got, digest, err)
	}
	t.Logf("SYNTHETIC_CANONICAL=%s", canonical)
	t.Logf("SYNTHETIC_DIGEST=%s", digest)
}

// Literal values captured by authorized t3-01 before any encoder edit.
const historicalCanonicalJSON = `{"connector_name":"vecnet-synthetic-oracle","enrol_ttl_seconds":0,"kind":"grokbot","name":"Synthetic <Grok> & oracle","name_open":false,"principal_id":"","request_id":"synthetic-historical-v1","skip_mcp":false,"spec_version":"2026-11-06","webhook_key":"crsr_SYNTHETIC<oracle>&only","webhook_url":"https://api2.cursor.sh/automations/webhook/00000000-0000-0000-0000-000000000001"}`
const historicalMAC = "a9097e2d76c5c9efd7825afe4ec2ca644420d33fe02e25b4b2b0b45f7f65b64b"

func TestTransportHistoricalMACCompatibility(t *testing.T) {
	h := &Hub{Blobs: &blob.Store{Key: []byte("0123456789abcdef0123456789abcdef")}}
	for _, selected := range []string{"", "delivery_v1", "recipient_drain_v1"} {
		n := historicalGrokInput()
		n.GrokbotTransport = selected

		canonical, err := onboardCanonicalBytes(n)
		if err != nil {
			t.Fatal(err)
		}
		want := historicalCanonicalJSON
		if selected == "recipient_drain_v1" {
			want = want[:len(want)-1] + `,"grokbot_transport":"recipient_drain_v1"}`
		}
		if string(canonical) != want {
			t.Errorf("canonical bytes changed for %q", selected)
		}
		got, err := h.onboardDigest(n)
		if err != nil {
			t.Fatal(err)
		}
		if selected == "recipient_drain_v1" {
			if got == historicalMAC {
				t.Error("opt-in ignored by MAC")
			}
		} else if got != historicalMAC {
			t.Errorf("historical legacy MAC changed: %s", got)
		}
	}
}

func TestTransportNonGrokCanonicalOmission(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	h := &Hub{Blobs: &blob.Store{Key: key}}
	for _, kind := range []string{"session", "dot"} {
		n := onboarding.Normalized{Kind: kind, SpecVersion: "2026-11-06", RequestID: "synthetic-non-grok", Name: "Synthetic"}
		oldBytes, oldMAC, err := historicalDigestV1(key, n)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := onboardCanonicalBytes(n)
		if err != nil || string(canonical) != oldBytes {
			t.Fatal("non-Grok bytes changed")
		}
		got, err := h.onboardDigest(n)
		if err != nil || got != oldMAC {
			t.Fatal("non-Grok MAC changed")
		}
	}
}

func TestTransportReceiptSecretScanAndMarshalSafe(t *testing.T) {
	// Canary in the new stored string must be scanned just like every old field.
	for _, secret := range []string{"crsr_SYNTHETIC_scan", "https://synthetic.invalid/secret"} {
		if !receiptStringsHold(onboardRecord{GrokbotTransport: secret}, secret, secret) {
			t.Fatal("transport string escaped receipt secret scan")
		}
	}
	r := onboarding.SubmitResult{Kind: "grokbot", GrokbotTransport: "recipient_drain_v1", HumanOutput: "SYNTHETIC-private-output"}
	safe, err := r.MarshalSafe()
	if err != nil || !bytes.Contains(safe, []byte(`"grokbot_transport":"recipient_drain_v1"`)) || bytes.Contains(safe, []byte("SYNTHETIC-private-output")) || bytes.Contains(safe, []byte("human_output")) {
		t.Fatal("MarshalSafe lost enum or retained human output")
	}
}

func TestTransportInvalidNormalizedDigestFailsClosed(t *testing.T) {
	h := &Hub{Blobs: &blob.Store{Key: []byte("0123456789abcdef0123456789abcdef")}}
	for _, c := range []struct{ kind, transport string }{{"grokbot", "unknown-secret-canary"}, {"session", "recipient_drain_v1"}} {
		n := historicalGrokInput()
		n.Kind = c.kind
		n.GrokbotTransport = c.transport
		got, err := h.onboardDigest(n)
		if err == nil || got != "" || bytes.Contains([]byte(err.Error()), []byte("unknown-secret-canary")) {
			t.Error("invalid normalized transport became legacy digest proof")
		}
	}
}
