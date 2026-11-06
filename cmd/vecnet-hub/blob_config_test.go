package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
)

func r2TestEnv(key string) string {
	return map[string]string{"VECNET_BLOB_BACKEND": "r2", "VECNET_R2_CHECKSUM_PROFILE": "sha256", "VECNET_R2_ACCOUNT_ID": strings.Repeat("a", 32), "VECNET_R2_BUCKET": "test-bucket", "VECNET_R2_ACCESS_KEY_ID": "FAKEACCESSKEY", "VECNET_R2_SECRET_ACCESS_KEY": "fake-secret-material"}[key]
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
