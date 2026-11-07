package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type codexDeliveryProbe struct {
	Loaded   bool   `json:"loaded"`
	Previous string `json:"previous"`
}

type codexDeliveryRelease struct{ Binary, Digest, Entry, Node string }

var errCodexDelivery = errors.New("codex delivery unavailable or unsafe; using hooks fallback")

// A bounded helper is called by Task 3's background registration, not on the
// foreground hook budget. No hub connection or credential is needed here.
func (a *app) ensureCodexDelivery(ctx context.Context, hub, config, bindings string) (err error) {
	defer func() { a.persistCodexReason(hub, err) }()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	release, err := a.codexDeliveryRelease()
	if err != nil {
		return err
	}
	if err := a.checkCodexDeliveryNode(ctx, release); err != nil {
		return err
	}
	return a.ensureCodexDeliveryPlatform(ctx, hub, config, bindings, release)
}

func (a *app) probeCodexDelivery(ctx context.Context, home, thread, previous string) (codexDeliveryProbe, error) {
	result := codexDeliveryProbe{Previous: "unknown"}
	if !filepath.IsAbs(home) || !codexThreadID.MatchString(thread) || (previous != "" && !codexThreadID.MatchString(previous)) {
		return result, errCodexDelivery
	}
	release, err := a.codexDeliveryRelease()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.checkCodexDeliveryNode(ctx, release); err != nil {
		return result, err
	}
	args := []string{"--disable-warning=ExperimentalWarning", release.Entry, "probe", "--codex-home", home, "--thread", thread}
	if previous != "" {
		args = append(args, "--previous-thread", previous)
	}
	cmd := exec.CommandContext(ctx, release.Node, args...)
	cmd.Env = a.codexDeliveryEnv(release.Binary)
	cmd.WaitDelay = 100 * time.Millisecond
	var out deliveryOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return result, errCodexDelivery
	}
	// Reject missing fields and extra output rather than infer absence.
	var wire struct {
		Loaded   *bool  `json:"loaded"`
		Previous string `json:"previous"`
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if decoder.Decode(&wire) != nil || wire.Loaded == nil || (wire.Previous != "gone" && wire.Previous != "live" && wire.Previous != "unknown") {
		return result, errCodexDelivery
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return result, errCodexDelivery
	}
	return codexDeliveryProbe{Loaded: *wire.Loaded, Previous: wire.Previous}, nil
}

type deliveryOutput struct{ bytes.Buffer }

func (o *deliveryOutput) Write(p []byte) (int, error) {
	if o.Len()+len(p) > 4096 {
		return 0, errCodexDelivery
	}
	return o.Buffer.Write(p)
}

func (a *app) codexDeliveryEnv(binary string) []string {
	var env []string
	for _, key := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_DATA_HOME", "CODEX_HOME", "VECNET_CODEX_MAX_BINDINGS", "VECNET_CODEX_DELIVERY_ENTRY"} {
		if v := a.getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return append(env, "VECNET_BIN="+binary, "VECNET_CODEX_AUTO_DELIVERY=0")
}

func (a *app) codexDeliveryExecutable(name string) (string, error) {
	for _, dir := range filepath.SplitList(a.getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", errCodexDelivery
}

func (a *app) codexDeliveryRelease() (codexDeliveryRelease, error) {
	var r codexDeliveryRelease
	r.Binary = a.getenv("VECNET_BIN")
	if r.Binary == "" {
		r.Binary, _ = os.Executable()
	}
	if !filepath.IsAbs(r.Binary) {
		return r, codexInactiveReason("assets_missing")
	}
	var err error
	r.Binary, err = filepath.EvalSymlinks(r.Binary)
	if err != nil {
		return r, codexInactiveReason("assets_missing")
	}
	file, err := os.Open(r.Binary)
	if err != nil {
		return r, codexInactiveReason("assets_missing")
	}
	h := sha256.New()
	_, err = io.Copy(h, file)
	file.Close()
	if err != nil {
		return r, codexInactiveReason("assets_missing")
	}
	r.Digest = hex.EncodeToString(h.Sum(nil))
	var candidates []string
	if override := a.getenv("VECNET_CODEX_DELIVERY_ENTRY"); override != "" {
		candidates = []string{override}
	} else {
		// Prefer assets shipped beside npm's native/<platform>/vecnet over an
		// unrelated launcher release installed under HOME.
		candidates = append(candidates, filepath.Join(filepath.Dir(r.Binary), "../../connectors/launcher/src/codex/delivery-main.ts"))
		if home := a.getenv("HOME"); filepath.IsAbs(home) {
			candidates = append(candidates, filepath.Join(home, ".local/share/vecnet-launcher/current/launcher/src/codex/delivery-main.ts"))
		}
	}
	for _, path := range candidates {
		if !filepath.IsAbs(path) {
			continue
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil {
			if st, e := os.Stat(resolved); e == nil && st.Mode().IsRegular() {
				r.Entry = resolved
				break
			}
		}
	}
	if r.Entry == "" {
		return r, codexInactiveReason("assets_missing")
	}
	r.Node, err = a.codexDeliveryExecutable("node")
	if err != nil {
		return r, codexInactiveReason("node_missing")
	}
	return r, nil
}

func codexDeliveryHub(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errCodexDelivery
	}
	hub := normalizeHub(raw)
	sum := sha256.Sum256([]byte(hub))
	return hub, hex.EncodeToString(sum[:]), nil
}

func (a *app) cmdCodexDelivery(args []string) error { return a.cmdCodexDeliveryPlatform(args) }

// Installed delivery uses native TypeScript stripping and node:sqlite.
func (a *app) checkCodexDeliveryNode(ctx context.Context, r codexDeliveryRelease) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Node, "--version")
	cmd.Env = a.codexDeliveryEnv(r.Binary)
	cmd.WaitDelay = 100 * time.Millisecond
	var out deliveryOutput
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return codexInactiveReason("node_too_old")
	}
	version := strings.Split(strings.TrimPrefix(strings.TrimSpace(out.String()), "v"), ".")
	if len(version) != 3 {
		return codexInactiveReason("node_too_old")
	}
	major, e1 := strconv.Atoi(version[0])
	minor, e2 := strconv.Atoi(version[1])
	_, e3 := strconv.Atoi(version[2])
	if e1 != nil || e2 != nil || e3 != nil || major < 22 || (major == 22 && minor < 18) {
		return codexInactiveReason("node_too_old")
	}
	return nil
}
