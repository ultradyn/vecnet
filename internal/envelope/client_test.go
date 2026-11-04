package envelope

import (
	"strings"
	"testing"
)

func TestParseOperatorClient(t *testing.T) {
	cases := []struct {
		name   string
		header string
		ok     bool
		want   OperatorClient
	}{
		{"plain", `{"kind":"operator-cli","host":"xsm"}`, true,
			OperatorClient{Kind: "operator-cli", Host: "xsm", Label: "Operator on xsm CLI"}},
		{"ssh", `{"kind":"operator-cli","host":"x-tall","ssh":true}`, true,
			OperatorClient{Kind: "operator-cli", Host: "x-tall", SSH: true, Label: "Operator on x-tall CLI via ssh"}},
		{"hostile host is flattened", `{"kind":"operator-cli","host":"evil\u001b[31m\nhost <b>"}`, true,
			OperatorClient{Kind: "operator-cli", Host: "evil31mhostb", Label: "Operator on evil31mhostb CLI"}},
		{"empty host", `{"kind":"operator-cli","host":"\n"}`, true,
			OperatorClient{Kind: "operator-cli", Host: "unknown", Label: "Operator on unknown CLI"}},
		{"agent suspected", `{"kind":"operator-cli","host":"box","agent_suspected":true}`, true,
			OperatorClient{Kind: "operator-cli", Host: "box", AgentSuspected: true, Label: "Operator on box CLI (agent suspected)"}},
		{"agent suspected over ssh", `{"kind":"operator-cli","host":"box","ssh":true,"agent_suspected":true}`, true,
			OperatorClient{Kind: "operator-cli", Host: "box", SSH: true, AgentSuspected: true, Label: "Operator on box CLI via ssh (agent suspected)"}},
		{"unknown fields dropped", `{"kind":"operator-cli","host":"box","ssh_from":"10.0.0.5","extra":"<x>"}`, true,
			OperatorClient{Kind: "operator-cli", Host: "box", Label: "Operator on box CLI"}},
		{"label is recomputed", `{"kind":"operator-cli","host":"box","label":"the CEO"}`, true,
			OperatorClient{Kind: "operator-cli", Host: "box", Label: "Operator on box CLI"}},
		{"unknown kind", `{"kind":"web","host":"box"}`, false, OperatorClient{}},
		{"not json", `operator-cli`, false, OperatorClient{}},
		{"empty", ``, false, OperatorClient{}},
		{"oversize", `{"kind":"operator-cli","host":"` + strings.Repeat("a", 600) + `"}`, false, OperatorClient{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseOperatorClient(tc.header)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ParseOperatorClient(%q) = %+v, %v; want %+v, %v", tc.header, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCleanClientHostCaps(t *testing.T) {
	got := CleanClientHost(strings.Repeat("h", 200))
	if len(got) != MaxClientHost {
		t.Fatalf("len = %d, want %d", len(got), MaxClientHost)
	}
	if got := CleanClientHost("Box.Local_1-2"); got != "Box.Local_1-2" {
		t.Fatalf("CleanClientHost kept %q", got)
	}
}
