package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHubURLFlagDiagnosticsExcludeEnvironment(t *testing.T) {
	for _, envKey := range []string{"VECNET_PUBLIC_URL", "VECNET_RELOCATED_TO"} {
		for _, tc := range []struct {
			name, raw string
			canaries  []string
		}{
			{"userinfo", "https://URLUSERCANARY:URLPASSWORDCANARY@hub.example/vecnet", []string{"URLUSERCANARY", "URLPASSWORDCANARY"}},
			{"query", "https://hub.example/vecnet?token=URLQUERYCANARY", []string{"URLQUERYCANARY"}},
			{"fragment", "https://hub.example/vecnet#URLFRAGMENTCANARY", []string{"URLFRAGMENTCANARY"}},
			{"malformed_authority", "https://[2001:db8::7URLAUTHORITYCANARY/vecnet", []string{"URLAUTHORITYCANARY"}},
			{"valid_path_prefix", "https://hub.example/URLPATHCANARY", []string{"URLPATHCANARY"}},
			{"valid_ipv6", "https://[2001:db8::7]/URLIPV6CANARY", []string{"URLIPV6CANARY"}},
		} {
			for _, diagnostic := range []struct{ name, arg string }{
				{"help", "--help"},
				{"unknown_flag", "--unknown-option"},
			} {
				t.Run(envKey+"/"+tc.name+"/"+diagnostic.name, func(t *testing.T) {
					dir := filepath.Join(t.TempDir(), "unopened")
					env := map[string]string{envKey: tc.raw}
					// No address can be bound if a regression reaches serve. The
					// parser result below must win over this invalid listen value.
					args := []string{"--listen", "url-diagnostics-must-not-listen", "--data-dir", dir, diagnostic.arg}
					file, err := os.CreateTemp(t.TempDir(), "stderr")
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					var stdout bytes.Buffer
					func() {
						previous := os.Stderr
						os.Stderr = file
						defer func() { os.Stderr = previous }()
						err = run(context.Background(), args, &stdout, func(key string) string { return env[key] })
					}()
					if err == nil {
						t.Fatal("missing parser diagnostic")
					}
					if diagnostic.name == "help" {
						if !errors.Is(err, flag.ErrHelp) {
							t.Errorf("help error = %v, want flag.ErrHelp", err)
						}
					} else if errors.Is(err, flag.ErrHelp) || err.Error() != "flag provided but not defined: -unknown-option" {
						t.Errorf("unknown flag error = %v, want ordinary unknown-option parser error", err)
					}
					stderr, readErr := os.ReadFile(file.Name())
					if readErr != nil {
						t.Fatal(readErr)
					}
					for _, want := range []string{"Usage of vecnet-hub:", "  -public-url string", "  -relocated-to string", "$VECNET_PUBLIC_URL", "$VECNET_RELOCATED_TO"} {
						if !strings.Contains(string(stderr), want) {
							t.Errorf("stderr lacks ordinary usage/options %q", want)
						}
					}
					if diagnostic.name == "unknown_flag" && !bytes.Contains(stderr, []byte("flag provided but not defined: -unknown-option")) {
						t.Error("stderr lacks unknown-option diagnostic")
					}
					for channel, output := range map[string]string{"stderr": string(stderr), "stdout": stdout.String(), "error": err.Error()} {
						for _, forbidden := range append([]string{tc.raw, strconv.Quote(tc.raw)}, tc.canaries...) {
							if strings.Contains(output, forbidden) {
								t.Errorf("%s exposes environment URL/default metadata %q", channel, forbidden)
							}
						}
					}
					if stdout.Len() != 0 {
						t.Errorf("parser diagnostic wrote supplied stdout: %q", stdout.String())
					}
					if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
						t.Errorf("parser diagnostic created data directory/store: %v", statErr)
					}
				})
			}
		}
	}
}

func TestHubURLFlagOverridesEnvironment(t *testing.T) {
	for _, setting := range []struct {
		envKey, flagName, pathURL, ipv6URL, safeURL string
	}{
		{"VECNET_PUBLIC_URL", "public-url", "https://hub.example/env-prefix", "https://[2001:db8::7]/env-prefix", "https://flag.example/selected-prefix"},
		{"VECNET_RELOCATED_TO", "relocated-to", "https://next.example/env-prefix", "https://[2001:db8::8]/env-prefix", "https://flag-next.example/selected-prefix"},
	} {
		for _, tc := range []struct {
			name, raw, selected string
			args                []string
		}{
			{"environment_path_prefix", setting.pathURL, setting.pathURL, nil},
			{"environment_ipv6", setting.ipv6URL, setting.ipv6URL, nil},
			{"safe_flag_over_invalid_environment", "https://hub.example?token=OVERRIDECANARY", setting.safeURL, []string{"--" + setting.flagName, setting.safeURL}},
			{"empty_flag_over_invalid_environment", "https://hub.example?token=OVERRIDECANARY", "", []string{"--" + setting.flagName + "="}},
			{"empty_flag_over_valid_environment", setting.pathURL, "", []string{"--" + setting.flagName, ""}},
		} {
			t.Run(setting.envKey+"/"+tc.name, func(t *testing.T) {
				env := map[string]string{setting.envKey: tc.raw}
				args := []string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}
				const ownURL = "https://old.example/vecnet"
				if setting.flagName == "relocated-to" {
					args = append(args, "--public-url", ownURL)
				}
				args = append(args, tc.args...)
				h := bootHub(t, args, func(key string) string { return env[key] })
				wantPublic, wantTarget := tc.selected, ""
				if setting.flagName == "relocated-to" {
					wantPublic, wantTarget = ownURL, tc.selected
				} else if wantPublic == "" {
					wantPublic = h.base
				}
				if !strings.Contains(h.out.String(), "(public url "+wantPublic+")\n") {
					t.Errorf("startup did not select public URL %q: %s", wantPublic, h.out.String())
				}
				if strings.Contains(h.out.String(), "OVERRIDECANARY") {
					t.Error("startup announced the overridden environment value")
				}
				// Only contact this boot's loopback listener. Never follow a
				// redirect or use a proxy to reach any synthetic URL above.
				transport := &http.Transport{DisableKeepAlives: true}
				defer transport.CloseIdleConnections()
				client := &http.Client{
					Transport: transport,
					Timeout:   5 * time.Second,
					CheckRedirect: func(*http.Request, []*http.Request) error {
						return http.ErrUseLastResponse
					},
				}
				resp, err := client.Get(h.base + "/v1/hub/identity")
				if err != nil {
					t.Fatal(err)
				}
				var identity struct {
					PublicURL string `json:"public_url"`
				}
				err = json.NewDecoder(resp.Body).Decode(&identity)
				resp.Body.Close()
				if err != nil || resp.StatusCode != http.StatusOK || identity.PublicURL != wantPublic {
					t.Fatalf("served identity = %d %+v %v, want public URL %q", resp.StatusCode, identity, err, wantPublic)
				}
				resp, err = client.Get(h.base + "/mcp")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if wantTarget != "" {
					var moved struct {
						Error struct {
							Code    string
							Details struct {
								NewURL string `json:"new_url"`
								Notice struct {
									NewURL string `json:"new_url"`
								}
							}
						}
					}
					decodeErr := json.Unmarshal(body, &moved)
					if decodeErr != nil || resp.StatusCode != http.StatusGone || moved.Error.Code != "hub_moved" || moved.Error.Details.NewURL != wantTarget || moved.Error.Details.Notice.NewURL != wantTarget {
						t.Errorf("relocation response = %d %s %v, want hub_moved to %q", resp.StatusCode, body, decodeErr, wantTarget)
					}
					if !strings.Contains(h.out.String(), "vecnet-hub relocated: answering hub_moved → "+wantTarget+"\n") {
						t.Error("startup did not announce the selected relocation target")
					}
				} else {
					if resp.StatusCode != http.StatusUnauthorized {
						t.Errorf("ordinary MCP response = %d %s, want unauthenticated 401", resp.StatusCode, body)
					}
					if strings.Contains(h.out.String(), "vecnet-hub relocated:") {
						t.Error("empty relocation target still announced relocation")
					}
				}
				if err := h.stop(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
