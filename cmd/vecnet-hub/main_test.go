package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

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
				// Drain before Close so the transport reuses the connection rather
				// than racing replacement dials against the next request/shutdown.
				// A new connection with no headers can hold Shutdown past 2 s.
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
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

func TestPreferenceBootstrapFlagsAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
		want      int64
		origin    string
	}{
		{"default", "", nil, 500, ""}, {"env", "700", nil, 700, "VECNET_THREAD_BUDGET"}, {"flag", "700", []string{"--thread-budget", "800"}, 800, "--thread-budget"}, {"equal env", "500", nil, 500, "VECNET_THREAD_BUDGET"}, {"equal flag", "700", []string{"--thread-budget", "500"}, 500, "--thread-budget"}, {"flag overrides invalid env", "synthetic-invalid-environment", []string{"--thread-budget", "500"}, 500, "--thread-budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
			fs.String("events-refresh-interval", "30m", "")
			resolve := registerPreferenceFlags(fs, func(key string) string {
				if key == "VECNET_THREAD_BUDGET" {
					return tc.env
				}
				return ""
			})
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg, err := resolve()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			h, db, err := openHub(dir, "", false, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s, err := h.Preferences.LoadSnapshot(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			e, err := s.Get("agents.thread_budget")
			if err != nil {
				t.Fatal(err)
			}
			n, _ := e.Value.Int64()
			source := prefs.SourceFlag
			if tc.origin == "" {
				source = prefs.SourceDefault
			}
			if n != tc.want || e.Source != source || e.Origin != tc.origin {
				t.Fatalf("bootstrap = %+v", e)
			}
		})
	}
}

func TestPreferenceFlagDiagnosticsExcludeEnvironment(t *testing.T) {
	for _, name := range []string{"presence-ttl", "session-presence-ttl", "launcher-presence-ttl", "default-max-depth", "default-ttl", "thread-budget", "sends-per-minute", "threads-per-hour", "creates-per-hour"} {
		for _, arg := range []string{"--help", "--unknown-option"} {
			t.Run(name+"/"+arg, func(t *testing.T) {
				const rejected = "synthetic-invalid-environment-<>&\""
				env := "VECNET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
				getenv := func(key string) string {
					if key == env {
						return rejected
					}
					return ""
				}
				// Capture run's real flag diagnostics, including its default usage.
				file, err := os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				func() {
					previous := os.Stderr
					os.Stderr = file
					defer func() { os.Stderr = previous }()
					err = run(context.Background(), []string{arg}, io.Discard, getenv)
				}()
				if err == nil {
					t.Fatal("missing flag diagnostic")
				}
				output, readErr := os.ReadFile(file.Name())
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Contains(output, []byte("Usage of vecnet-hub:")) || !bytes.Contains(output, []byte("-"+name)) {
					t.Fatal("missing preference flag usage")
				}
				if bytes.Contains(output, []byte("synthetic-invalid-environment")) || strings.Contains(err.Error(), "synthetic-invalid-environment") {
					t.Fatal("rejected environment value entered flag diagnostics")
				}
			})
		}
	}
}
func TestPreferenceBootstrapRejectsInvalidBeforeOpeningStore(t *testing.T) {
	for _, arg := range []string{"--thread-budget=synthetic-secret", "--presence-ttl=59s", "--default-ttl=1.5s", "--default-max-depth=65", "--creates-per-hour=0", "--session-presence-ttl=-1m", "--launcher-presence-ttl=25h"} {
		dir := filepath.Join(t.TempDir(), "unopened")
		err := run(context.Background(), []string{"--listen", "127.0.0.1:0", "--data-dir", dir, arg}, io.Discard, noEnv)
		if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("unsafe or missing bootstrap rejection: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("opened store on invalid bootstrap: %v", err)
		}
	}
}

func TestPreferenceBootstrapAllEnabledFlags(t *testing.T) {
	for _, tc := range []struct {
		flag, key, raw string
		want           int64
	}{
		{"presence-ttl", "presence.default_ttl", "2h", 7200},
		{"session-presence-ttl", "presence.session_ttl", "30m", 1800},
		{"launcher-presence-ttl", "presence.launcher_ttl", "45m", 2700},
		{"default-max-depth", "agents.default_max_depth", "40", 40},
		{"default-ttl", "agents.default_ttl", "48h", 172800},
		{"thread-budget", "agents.thread_budget", "700", 700},
		{"sends-per-minute", "rates.sends_per_minute", "80", 80},
		{"threads-per-hour", "rates.threads_per_hour", "30", 30},
		{"creates-per-hour", "provisioning.creates_per_hour", "90", 90},
	} {
		for _, fromEnv := range []bool{false, true} {
			t.Run(tc.flag+map[bool]string{false: "/flag", true: "/env"}[fromEnv], func(t *testing.T) {
				env := "VECNET_" + strings.ToUpper(strings.ReplaceAll(tc.flag, "-", "_"))
				fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
				fs.String("events-refresh-interval", "30m", "")
				resolve := registerPreferenceFlags(fs, func(key string) string {
					if fromEnv && key == env {
						return tc.raw
					}
					return ""
				})
				args := []string{}
				origin := env
				if !fromEnv {
					args = []string{"--" + tc.flag, tc.raw}
					origin = "--" + tc.flag
				}
				if err := fs.Parse(args); err != nil {
					t.Fatal(err)
				}
				cfg, err := resolve()
				if err != nil {
					t.Fatal(err)
				}
				e := cfg.PreferenceBootstrap[tc.key]
				n, _ := e.Value.Int64()
				if n != tc.want || e.Source != prefs.SourceFlag || e.Origin != origin {
					t.Fatalf("bootstrap = %+v", e)
				}
			})
		}
	}
}
