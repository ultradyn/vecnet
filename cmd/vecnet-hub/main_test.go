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
				resp.Body.Close()
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
