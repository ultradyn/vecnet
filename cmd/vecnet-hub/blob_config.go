package main

import (
	"errors"
	"flag"
	"strconv"

	"github.com/XertroV/vecnet/internal/hub"
)

// Environment input stays out of flag defaults and parse diagnostics.
func registerBlobFlags(fs *flag.FlagSet, getenv func(string) string) func(*hub.Config) error {
	values := map[string]*string{}
	for _, name := range []string{"blob-backend", "r2-account-id", "r2-bucket", "r2-endpoint", "r2-checksum-profile", "r2-storage-quota-bytes"} {
		values[name] = fs.String(name, "", "blob bootstrap configuration")
	}
	return func(cfg *hub.Config) error {
		explicit := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
		read := func(name, env, def string) string {
			if explicit[name] {
				return *values[name]
			}
			if v := getenv(env); v != "" {
				return v
			}
			return def
		}
		cfg.BlobBackend = read("blob-backend", "VECNET_BLOB_BACKEND", "disk")
		quota, err := strconv.ParseInt(read("r2-storage-quota-bytes", "VECNET_R2_STORAGE_QUOTA_BYTES", "5368709120"), 10, 64)
		if err != nil || quota <= 0 {
			return errors.New("invalid R2 storage quota")
		}
		cfg.R2 = hub.R2Config{AccountID: read("r2-account-id", "VECNET_R2_ACCOUNT_ID", ""), Bucket: read("r2-bucket", "VECNET_R2_BUCKET", ""), Endpoint: read("r2-endpoint", "VECNET_R2_ENDPOINT", ""), ChecksumProfile: read("r2-checksum-profile", "VECNET_R2_CHECKSUM_PROFILE", ""), StorageQuotaBytes: quota, AccessKeyID: getenv("VECNET_R2_ACCESS_KEY_ID"), SecretAccessKey: getenv("VECNET_R2_SECRET_ACCESS_KEY")}
		return hub.ValidateBlobConfig(*cfg)
	}
}
