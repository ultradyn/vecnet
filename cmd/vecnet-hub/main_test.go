package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestBackfillLimitsRejectBeforeOpeningStore(t *testing.T) {
	for _, flags := range [][]string{{"--storage-backfill-rows=-1"}, {"--storage-backfill-bytes=-1"}, {"--storage-backfill-target=-1ms"}, {"--storage-backfill-bytes=9223372036854775807"}} {
		dir := filepath.Join(t.TempDir(), "unopened")
		args := append([]string{"--data-dir", dir, "--listen", "127.0.0.1:0"}, flags...)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := run(ctx, args, io.Discard, noEnv); err == nil || !strings.Contains(err.Error(), "invalid_limits") {
			t.Fatalf("invalid backfill limits accepted: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid limits opened store")
		}
	}
}

func TestInitPrintsSecretOnce(t *testing.T) {
	dir := t.TempDir()
	var first, second bytes.Buffer
	if err := runInit(dir, "Max", &first); err != nil {
		t.Fatalf("first runInit: %v", err)
	}
	if !strings.Contains(first.String(), "vn_operator_") {
		t.Fatalf("first output lacks the operator secret:\n%s", first.String())
	}
	peer := regexp.MustCompile(`peer_[0-9A-HJKMNP-TV-Z]{26}`).FindString(first.String())
	if peer == "" {
		t.Fatalf("first output lacks the operator peer id:\n%s", first.String())
	}
	if err := runInit(dir, "Max", &second); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	if !strings.Contains(second.String(), "already initialised") {
		t.Fatalf("second output lacks \"already initialised\":\n%s", second.String())
	}
	if strings.Contains(second.String(), "vn_operator_") {
		t.Fatalf("second output repeats a secret:\n%s", second.String())
	}
	if !strings.Contains(second.String(), peer) {
		t.Fatalf("second output does not name the existing peer %s:\n%s", peer, second.String())
	}
}

func TestMCPAllowedOriginsStartup(t *testing.T) {
	for _, useFlag := range []bool{false, true} {
		t.Run(map[bool]string{false: "env", true: "flag"}[useFlag], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "unopened")
			args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
			getenv := noEnv
			if useFlag {
				args = append(args, "--mcp-allowed-origins", "https://chatgpt.com/path")
			} else {
				getenv = func(key string) string {
					if key == "VECNET_MCP_ALLOWED_ORIGINS" {
						return "https://chatgpt.com/path"
					}
					return ""
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := run(ctx, args, io.Discard, getenv)
			if err == nil || !strings.Contains(err.Error(), "MCP allowed origins") {
				t.Fatalf("startup error = %v, want invalid MCP allowed origins", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid config opened data directory: %v", err)
			}
		})
	}
}

func TestMCPAllowedOriginsFlagAndEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		flags     []string
		origin    string
		want      int
	}{
		{"default", "", nil, "https://chatgpt.com", 401},
		{"none", "none", nil, "https://chatgpt.com", 403},
		{"custom env", "https://client.example", nil, "https://client.example", 401},
		{"flag wins", "https://client.example", []string{"--mcp-allowed-origins", "none"}, "https://client.example", 403},
		{"empty flag resets default", "none", []string{"--mcp-allowed-origins", ""}, "https://chatgpt.com", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}, tc.flags...)
			h := bootHub(t, args, func(key string) string {
				if key == "VECNET_MCP_ALLOWED_ORIGINS" {
					return tc.env
				}
				return ""
			})
			req, err := http.NewRequest(http.MethodPost, h.base+"/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", tc.origin)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == 401 && !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
				t.Fatal("missing discovery challenge")
			}
		})
	}
}

func TestMCPLegacySessionsStartup(t *testing.T) {
	for _, useFlag := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "unopened")
		args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
		getenv := noEnv
		if useFlag {
			args = append(args, "--mcp-legacy-sessions", "invalid")
		} else {
			getenv = func(k string) string {
				if k == "VECNET_MCP_LEGACY_SESSIONS" {
					return "invalid"
				}
				return ""
			}
		}
		err := run(context.Background(), args, io.Discard, getenv)
		if err == nil || !strings.Contains(err.Error(), "MCP legacy sessions") {
			t.Fatalf("invalid config error: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("invalid config opened data directory: %v", err)
		}
	}
}

func TestOAuthRedirectsStartup(t *testing.T) {
	for _, flagValue := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "unopened")
		args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
		getenv := func(key string) string {
			if key == "VECNET_OAUTH_REDIRECTS" {
				return "http://public.example/callback?PRIVATE_VALUE=secret"
			}
			return ""
		}
		if flagValue {
			args = append(args, "--oauth-redirects", "https://*.example/callback")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := run(ctx, args, io.Discard, getenv)
		if err == nil || !strings.Contains(err.Error(), "OAuth redirects") || strings.Contains(err.Error(), "PRIVATE_VALUE") {
			t.Fatalf("startup error = %v, want redacted invalid OAuth redirects", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("invalid config opened data directory: %v", err)
		}
	}
}

func TestOAuthRedirectsFlagAndEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		flags     []string
		want      int
	}{
		{"env", "https://connector.example/callback", nil, 201},
		{"flag wins", "http://invalid.example/cb", []string{"--oauth-redirects", "https://connector.example/callback"}, 201},
		{"empty flag keeps defaults", "https://connector.example/callback", []string{"--oauth-redirects", ""}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}, tc.flags...)
			h := bootHub(t, args, func(key string) string {
				if key == "VECNET_OAUTH_REDIRECTS" {
					return tc.env
				}
				return ""
			})
			for _, uri := range []string{"https://connector.example/callback", "https://chatgpt.com/connector_platform_oauth_redirect", "https://www.cursor.com/agents/mcp/oauth/callback"} {
				body, _ := json.Marshal(map[string]any{"redirect_uris": []string{uri}})
				resp, err := http.Post(h.base+"/oauth/register", "application/json", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal("OAuth registration response did not drain")
				}
				want := http.StatusCreated
				if uri == "https://connector.example/callback" {
					want = tc.want
				}
				if resp.StatusCode != want {
					t.Fatalf("register %s: %d, want %d", uri, resp.StatusCode, want)
				}
			}
		})
	}
}

func TestEventsRefreshIntervalValidation(t *testing.T) {
	for _, source := range []string{"env", "flag"} {
		for _, value := range []string{"invalid", "0", "-1m", "59s", "24h1s"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "unopened")
				args := []string{"--listen", "127.0.0.1:0", "--data-dir", dir}
				getenv := noEnv
				if source == "flag" {
					args = append(args, "--events-refresh-interval", value)
				} else {
					getenv = func(key string) string {
						if key == "VECNET_EVENTS_REFRESH_INTERVAL" {
							return value
						}
						return ""
					}
				}
				err := run(context.Background(), args, io.Discard, getenv)
				if err == nil || !strings.Contains(err.Error(), "events refresh interval") {
					t.Fatalf("startup error = %v", err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("invalid interval opened data directory: %v", err)
				}
			})
		}
	}
}

func TestEventsRefreshIntervalFlagAndEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		flags     []string
		want      time.Duration
	}{
		{"default", "", nil, 30 * time.Minute},
		{"env minimum", "1m", nil, time.Minute},
		{"env maximum", "24h", nil, 24*time.Hour - events.RefreshMargin},
		{"flag", "", []string{"--events-refresh-interval", "7m"}, 7 * time.Minute},
		{"flag wins over invalid env", "invalid", []string{"--events-refresh-interval", "8m"}, 8 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			if err := runInit(dir, "Max", &out); err != nil {
				t.Fatal(err)
			}
			bearer := regexp.MustCompile(`vn_operator_\S+`).FindString(out.String())
			args := append([]string{"--listen", "127.0.0.1:0", "--data-dir", dir, "--allow-private-egress"}, tc.flags...)
			h := bootHub(t, args, func(key string) string {
				if key == "VECNET_EVENTS_REFRESH_INTERVAL" {
					return tc.env
				}
				return ""
			})
			cb := testhub.NewCallback(t)
			subscribeSmoke(t, h.base, bearer, cb.URL)
			resp, raw := hubDo(t, http.MethodGet, h.base+"/v1/admin/subscriptions", bearer, nil)
			var result []struct {
				RefreshBefore string `json:"refresh_before"`
			}
			if resp.StatusCode != 200 || json.Unmarshal(raw, &result) != nil || len(result) != 1 {
				t.Fatalf("subscriptions: %d %s", resp.StatusCode, raw)
			}
			deadline, err := time.Parse(time.RFC3339Nano, result[0].RefreshBefore)
			if err != nil {
				t.Fatal(err)
			}
			if delta := time.Until(deadline) - tc.want; delta < -5*time.Second || delta > 5*time.Second {
				t.Fatalf("refresh interval differs from %s by %s", tc.want, delta)
			}
		})
	}
}
