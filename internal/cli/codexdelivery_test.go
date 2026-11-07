package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"
)

func TestCodexDeliveryMissingAssets(t *testing.T) {
	d := t.TempDir()
	env := map[string]string{"HOME": d, "XDG_CONFIG_HOME": filepath.Join(d, "config"), "CODEX_HOME": filepath.Join(d, "codex"), "XDG_DATA_HOME": filepath.Join(d, "data"), "XDG_RUNTIME_DIR": filepath.Join(d, "run"), "VECNET_CODEX_DELIVERY_ENTRY": filepath.Join(d, "absent.ts")}
	a := &app{ctx: context.Background(), getenv: func(k string) string { return env[k] }, stdout: io.Discard, stderr: io.Discard}
	if err := a.ensureCodexDelivery(context.Background(), "https://hub.invalid", filepath.Join(d, "v.toml"), filepath.Join(d, "bindings.json")); err == nil {
		t.Fatal("missing delivery assets must fail closed")
	}
	if _, err := a.probeCodexDelivery(context.Background(), env["CODEX_HOME"], bindT1, ""); err == nil {
		t.Fatal("missing probe assets must fail closed")
	}
}
