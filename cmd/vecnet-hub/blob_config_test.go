package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
)

func r2TestEnv(key string) string {
	return map[string]string{"VECNET_BLOB_BACKEND": "r2", "VECNET_R2_CHECKSUM_PROFILE": "sha256", "VECNET_R2_ACCOUNT_ID": strings.Repeat("a", 32), "VECNET_R2_BUCKET": "test-bucket", "VECNET_R2_ACCESS_KEY_ID": "FAKEACCESSKEY", "VECNET_R2_SECRET_ACCESS_KEY": "fake-secret-material"}[key]
}

type blobConfigTransport func(*http.Request) (*http.Response, error)

func (f blobConfigTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBlobExpiryBootstrapProductionPreferences(t *testing.T) {
	for _, tc := range []struct {
		name, env, want, origin string
		args                    []string
	}{
		{name: "default", want: "1d"},
		{name: "environment", env: "6h", want: "6h", origin: "VECNET_BLOBS_EXPIRY"},
		{name: "explicit default environment", env: "1d", want: "1d", origin: "VECNET_BLOBS_EXPIRY"},
		{name: "flag wins", env: "6h", args: []string{"--blobs-expiry=12h"}, want: "12h", origin: "--blobs-expiry"},
		{name: "explicit default flag", env: "6h", args: []string{"--blobs-expiry=1d"}, want: "1d", origin: "--blobs-expiry"},
		{name: "flag overrides invalid environment", env: "invalid", args: []string{"--blobs-expiry=7d"}, want: "7d", origin: "--blobs-expiry"},
		{name: "bootstrap keep", env: "off", want: "off", origin: "VECNET_BLOBS_EXPIRY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "VECNET_BLOBS_EXPIRY" {
					return tc.env
				}
				return r2TestEnv(key)
			}
			fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
			resolvePrefs := registerPreferenceFlags(fs, getenv)
			resolveBlobs := registerBlobFlags(fs, getenv)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg, err := resolvePrefs()
			if err != nil {
				t.Fatal(err)
			}
			if err := resolveBlobs(&cfg); err != nil {
				t.Fatal(err)
			}
			cfg.R2.Client = &http.Client{Transport: blobConfigTransport(func(*http.Request) (*http.Response, error) {
				t.Error("constructor or preference preview contacted provider")
				return nil, fmt.Errorf("provider calls forbidden")
			})}
			h, db, err := openHub(t.TempDir(), "", false, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			check := func(h *hub.Hub) {
				t.Helper()
				s := h.Preferences.Current()
				e, err := s.Get("blobs.expiry")
				if err != nil {
					t.Fatal(err)
				}
				value, _ := e.Value.Enum()
				source := prefs.SourceDefault
				if tc.origin != "" {
					source = prefs.SourceFlag
				}
				if value != tc.want || e.Source != source || e.Origin != tc.origin || e.Revision != 0 {
					t.Fatal("bootstrap lost expiry value or provenance")
				}
				for _, d := range s.Descriptors() {
					if d.Key == "blobs.expiry" && (!d.Editable || d.Runtime != "live" || d.UnavailableReason != "") {
						t.Fatal("production R2 expiry consumer unavailable")
					}
				}
			}
			check(h)
			// A reconstructed production hub accepts the same bootstrap and policy.
			again, err := hub.New(db, h.Cfg, h.Now)
			if err != nil {
				t.Fatal(err)
			}
			check(again)
		})
	}
}

func TestBlobExpiryBootstrapRejectsInvalidValuesBeforeOpeningStore(t *testing.T) {
	for _, value := range []string{"0", "24h", "2d", "forever", "", "rejected-expiry-canary"} {
		for _, surface := range []string{"flag", "environment"} {
			if surface == "environment" && value == "" {
				continue // getenv's empty string means absent.
			}
			t.Run(surface+"/"+value, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "unopened")
				args := []string{"--listen=invalid-listener", "--data-dir=" + dir}
				if surface == "flag" {
					args = append(args, "--blobs-expiry="+value)
				}
				err := run(context.Background(), args, io.Discard, func(key string) string {
					if surface == "environment" && key == "VECNET_BLOBS_EXPIRY" {
						return value
					}
					return r2TestEnv(key)
				})
				if err == nil || !strings.Contains(err.Error(), prefs.CodeInvalidValue) || strings.Contains(err.Error(), "rejected-expiry-canary") {
					t.Fatal("invalid expiry accepted or echoed")
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("invalid expiry opened store")
				}
			})
		}
	}
}
func TestR2ConfigRejectsBeforeListenerAndStore(t *testing.T) {
	for _, args := range [][]string{{"--blob-backend=oops"}, {"--r2-storage-quota-bytes=0"}, {"--r2-storage-quota-bytes=-1"}, {"--r2-storage-quota-bytes=unlimited"}, {"--r2-storage-quota-bytes=9223372036854775808"}, {"--r2-checksum-profile=md5"}, {"--r2-endpoint=https://evil.invalid"}, {"--r2-endpoint=https://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.r2.cloudflarestorage.com/"}, {"--r2-account-id="}, {"--r2-bucket="}} {
		t.Run(strings.Split(args[0], "=")[0]+"/"+strings.Split(args[0], "=")[1], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "unopened")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// Invalid listen would be the error if startup bound before validation.
			flags := append([]string{"--listen=invalid-listener", "--data-dir=" + dir}, args...)
			err := run(ctx, flags, io.Discard, r2TestEnv)
			if err == nil || strings.Contains(err.Error(), "missing port") || strings.Contains(err.Error(), "fake-secret-material") {
				t.Fatal("invalid R2 config reached listener or leaked input")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid R2 config opened store")
			}
		})
	}
	for _, missing := range []string{"VECNET_R2_ACCESS_KEY_ID", "VECNET_R2_SECRET_ACCESS_KEY", "VECNET_R2_CHECKSUM_PROFILE"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := run(ctx, []string{"--listen=invalid-listener"}, io.Discard, func(k string) string {
			if k == missing {
				return ""
			}
			return r2TestEnv(k)
		})
		if err == nil || !strings.Contains(err.Error(), "invalid R2") {
			t.Fatal("missing secret accepted")
		}
	}
}
func TestR2ConfigDefaultsPrecedenceAndSecretSafeDiagnostics(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	resolve := registerBlobFlags(fs, r2TestEnv)
	if err := fs.Parse([]string{"--r2-storage-quota-bytes=99"}); err != nil {
		t.Fatal(err)
	}
	cfg := hub.DefaultConfig()
	if err := resolve(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BlobBackend != "r2" || cfg.R2.StorageQuotaBytes != 99 || cfg.R2.ChecksumProfile != "sha256" {
		t.Fatal("wrong resolved R2 configuration")
	}
	for _, render := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), func() string { raw, _ := json.Marshal(cfg); return string(raw) }()} {
		if strings.Contains(render, "FAKEACCESSKEY") || strings.Contains(render, "fake-secret-material") {
			t.Fatal("configuration diagnostic exposed credential")
		}
	}
	fs = flag.NewFlagSet("default", flag.ContinueOnError)
	resolve = registerBlobFlags(fs, noEnv)
	cfg = hub.DefaultConfig()
	if err := resolve(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BlobBackend != "disk" || cfg.R2.StorageQuotaBytes != 5<<30 {
		t.Fatal("disk/default storage changed")
	}
}
