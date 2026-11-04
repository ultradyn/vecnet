package envelope

import (
	"encoding/json"
	"strings"
)

// The operator CLI label (PROTOCOL §2.2 step 11, §13.7). A `vecnet` run that
// sends with the operator credential names the terminal it came from in the
// ClientHeader request header; the hub records a sanitized copy, for the
// operator credential only, under the hub-owned metadata key
// ClientMetadataKey. It is provenance for display, not authority: the
// operator credential is the authority.
const (
	ClientHeader          = "X-Vecnet-Client"
	ClientMetadataKey     = "vecnet.client"
	ClientKindOperatorCLI = "operator-cli"

	// MaxClientHeader bounds the header the hub will parse.
	MaxClientHeader = 512
	// MaxClientHost caps the host label (a DNS label is at most 63 bytes;
	// a full name is longer, but a label this long says enough).
	MaxClientHost = 64
)

// OperatorClient is the value of metadata["vecnet.client"] and, without
// Label, of the X-Vecnet-Client header. AgentSuspected marks a send whose
// run looked like an agent's (§13.7): one that named the operator profile
// explicitly with --net from an agent harness or without a terminal.
type OperatorClient struct {
	Kind           string `json:"kind"`
	Host           string `json:"host"`
	SSH            bool   `json:"ssh"`
	AgentSuspected bool   `json:"agent_suspected,omitempty"`
	Label          string `json:"label,omitempty"`
}

// CleanClientHost flattens an untrusted host name to [A-Za-z0-9._-], at most
// MaxClientHost bytes; nothing left is "unknown".
func CleanClientHost(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= MaxClientHost {
			break
		}
		if r < 0x80 && (r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// Clean returns c with every field sanitized and Label recomputed from
// them; ok is false unless Kind is operator-cli.
func (c OperatorClient) Clean() (OperatorClient, bool) {
	if c.Kind != ClientKindOperatorCLI {
		return OperatorClient{}, false
	}
	out := OperatorClient{Kind: c.Kind, Host: CleanClientHost(c.Host), SSH: c.SSH, AgentSuspected: c.AgentSuspected}
	out.Label = "Operator on " + out.Host + " CLI"
	if out.SSH {
		out.Label += " via ssh"
	}
	if out.AgentSuspected {
		out.Label += " (agent suspected)"
	}
	return out, true
}

// ParseOperatorClient decodes and cleans an X-Vecnet-Client header value.
func ParseOperatorClient(header string) (OperatorClient, bool) {
	if header == "" || len(header) > MaxClientHeader {
		return OperatorClient{}, false
	}
	var c OperatorClient
	if json.Unmarshal([]byte(header), &c) != nil {
		return OperatorClient{}, false
	}
	return c.Clean()
}

// Header encodes c (without its label) for the X-Vecnet-Client header.
func (c OperatorClient) Header() string {
	c.Label = ""
	b, _ := json.Marshal(c)
	return string(b)
}
