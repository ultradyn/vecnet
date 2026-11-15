// Command vecnet-hub runs the vecnet hub, or initialises its operator.
//
//	vecnet-hub [--listen 127.0.0.1:45752] [--data-dir /data] [--public-url URL] [--dev-ui-dir DIR] [--allow-private-egress] [--trust-fly-client-ip]
//	vecnet-hub init [--data-dir /data] --operator-name <name>
//	vecnet-hub identity show|rotate [--data-dir /data] [--yes]
//
// VECNET_LISTEN, VECNET_DATA_DIR, VECNET_PUBLIC_URL and VECNET_RELOCATED_TO
// replace the corresponding flag defaults; flags still win. --relocated-to
// serves the old hub without workers and answers hub_moved for ordinary routes.
// VECNET_RESTART_RETRY_MS defaults --restart-retry-ms (5000; 0..60000), the
// reconnect hint streams get when the hub restarts.
// VECNET_MCP_ALLOWED_ORIGINS defaults --mcp-allowed-origins: comma-separated
// extra exact origins, empty for https://chatgpt.com, none for public origin only.
// VECNET_EVENTS_REFRESH_INTERVAL defaults --events-refresh-interval (30m; 1m..24h).
// VECNET_OAUTH_REDIRECTS defaults --oauth-redirects: comma-separated redirect
// patterns appended to the built-in OAuth redirects.
// VECNET_ALLOW_PRIVATE_EGRESS=1 is the default of --allow-private-egress,
// a development-only override that lets push and notify URLs be http and
// reach private or loopback addresses (PROTOCOL §14.7).
// VECNET_TRUST_FLY_CLIENT_IP=1 is the default of --trust-fly-client-ip: the
// per-client limiter keys on Fly's Fly-Client-IP header, which is only safe
// behind Fly's proxy (PROTOCOL §20.2; fly.toml sets it).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/logging"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

func main() {
	slog.SetDefault(slog.New(logging.NewHandler(os.Stderr, slog.LevelInfo)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Getenv)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vecnet-hub:", err)
		os.Exit(1)
	}
}

// envOr returns getenv(key), or def when it is unset or empty.
func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// run parses flags and dispatches to the server or the init subcommand. The
// server runs until ctx is cancelled; getenv supplies the VECNET_* flag
// defaults.
func run(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("vecnet-hub", flag.ContinueOnError)
	listen := fs.String("listen", envOr(getenv, "VECNET_LISTEN", "127.0.0.1:45752"), "address to listen on ($VECNET_LISTEN)")
	dataDir := fs.String("data-dir", envOr(getenv, "VECNET_DATA_DIR", "/data"), "directory holding hub.db and blobs/ ($VECNET_DATA_DIR)")
	publicURL := fs.String("public-url", "", "externally visible origin ($VECNET_PUBLIC_URL; default http://<listen address>)")
	mcpOrigins := fs.String("mcp-allowed-origins", getenv("VECNET_MCP_ALLOWED_ORIGINS"), "extra /mcp origins, comma-separated; empty defaults to https://chatgpt.com, none allows public origin only ($VECNET_MCP_ALLOWED_ORIGINS)")
	mcpLegacy := fs.String("mcp-legacy-sessions", envOr(getenv, "VECNET_MCP_LEGACY_SESSIONS", "on"), "legacy MCP sessions: on, off-for-oauth, off ($VECNET_MCP_LEGACY_SESSIONS)")
	oauthRedirects := fs.String("oauth-redirects", getenv("VECNET_OAUTH_REDIRECTS"), "extra OAuth redirect patterns, comma-separated, appended to defaults ($VECNET_OAUTH_REDIRECTS)")
	references := fs.Bool("delivery-references", false, "enable reference writes (rollback requires a dual reader)")
	maintenance := fs.Bool("storage-maintenance", false, "enable bounded PASSIVE checkpoint on slow ticks")
	reclaimPages := fs.Int("storage-reclaim-pages", 0, "incremental reclaim pages per slow tick, 0 disables reclaim, maximum 128")
	maintenanceTarget := fs.Duration("storage-maintenance-target", 50*time.Millisecond, "cooperative maintenance slice time budget")
	backfill := fs.Bool("storage-backfill", false, "enable bounded legacy Delivery backfill on slow ticks")
	defaults := hub.DefaultBackfillLimits()
	backfillRows := fs.Int("storage-backfill-rows", defaults.Rows, "maximum events per backfill slice")
	backfillBytes := fs.Int64("storage-backfill-bytes", defaults.Bytes, "source JSON byte budget per backfill slice")
	backfillTarget := fs.Duration("storage-backfill-target", defaults.Target, "target writer time per backfill slice")
	devUIDir := fs.String("dev-ui-dir", "", "serve the operator UI from this directory instead of the embedded copy (development)")
	allowPrivate := fs.Bool("allow-private-egress", getenv("VECNET_ALLOW_PRIVATE_EGRESS") == "1",
		"development only: let outbound URLs be http and reach private/loopback addresses ($VECNET_ALLOW_PRIVATE_EGRESS=1)")
	trustFly := fs.Bool("trust-fly-client-ip", getenv("VECNET_TRUST_FLY_CLIENT_IP") == "1",
		"key the per-client rate limit on the Fly-Client-IP header; only behind Fly's proxy ($VECNET_TRUST_FLY_CLIENT_IP=1)")
	relocatedTo := fs.String("relocated-to", "", "answer hub_moved without workers at the old hub ($VECNET_RELOCATED_TO)")
	restartRetry := fs.String("restart-retry-ms", envOr(getenv, "VECNET_RESTART_RETRY_MS", "5000"), "restart reconnect delay in milliseconds, 0..60000 ($VECNET_RESTART_RETRY_MS)")
	resolvePreferences := registerPreferenceFlags(fs, getenv)
	resolveBlobs := registerBlobFlags(fs, getenv)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// URL environment values must stay out of flag metadata: parser
	// diagnostics run before URL validation. Explicit empty flags still
	// override the environment.
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if !explicit["public-url"] {
		*publicURL = getenv("VECNET_PUBLIC_URL")
	}
	if !explicit["relocated-to"] {
		*relocatedTo = getenv("VECNET_RELOCATED_TO")
	}
	if fs.Arg(0) == "init" {
		ifs := flag.NewFlagSet("vecnet-hub init", flag.ContinueOnError)
		dir := ifs.String("data-dir", *dataDir, "directory holding hub.db")
		name := ifs.String("operator-name", "", "display name of the operator (required)")
		if err := ifs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("init: --operator-name is required")
		}
		return runInit(*dir, *name, stdout)
	}
	if fs.Arg(0) == "identity" {
		return runIdentity(ctx, fs.Args()[1:], *dataDir, stdout)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown subcommand %q", fs.Arg(0))
	}
	storage := storageOptions{references: *references, backfill: *backfill, limits: hub.BackfillLimits{Rows: *backfillRows, Bytes: *backfillBytes, Target: *backfillTarget}, maintenance: *maintenance, maintenanceLimits: store.MaintenanceOptions{ReclaimPages: *reclaimPages, Target: *maintenanceTarget}}
	if err := store.ValidateMaintenanceOptions(storage.maintenanceLimits); err != nil {
		return err
	}
	if err := hub.ValidateBackfillLimits(storage.limits); err != nil {
		return err
	}
	retryMS, err := strconv.ParseInt(*restartRetry, 10, 64)
	if err != nil || retryMS < 0 || retryMS > 60000 {
		return errors.New("--restart-retry-ms must be an integer in 0..60000")
	}
	// --events-refresh-interval is registered and resolved with the other
	// preference flags: its environment value never enters flag metadata, so
	// usage and parser errors cannot echo it.
	preferenceConfig, err := resolvePreferences()
	if err != nil {
		var pe *prefs.Error
		if errors.As(err, &pe) && pe.Details.Field == "events.refresh_interval" {
			return fmt.Errorf("events refresh interval must be a whole-second Go duration between 1m and 24h: %w", err)
		}
		return err
	}
	if err := resolveBlobs(&preferenceConfig); err != nil {
		return err
	}
	origins, err := mcpserver.ParseAllowedOrigins(*mcpOrigins)
	if err != nil {
		return err
	}
	legacy, err := mcpserver.ParseLegacySessions(*mcpLegacy)
	if err != nil {
		return err
	}
	redirects, err := oauth.ParseAllowedRedirects(*oauthRedirects)
	if err != nil {
		return err
	}
	return serve(ctx, serveConfig{
		Listen: *listen, DataDir: *dataDir, PublicURL: *publicURL, DevUIDir: *devUIDir, RelocatedTo: *relocatedTo,
		AllowPrivateEgress: *allowPrivate, TrustFly: *trustFly, RestartRetry: time.Duration(retryMS) * time.Millisecond,
		MCPOrigins: origins, OAuthRedirects: redirects, RefreshInterval: preferenceConfig.EventsRefreshInterval, MCPLegacySessions: legacy,
		Bootstrap: &preferenceConfig, Storage: storage,
	}, stdout)
}

// storageOptions are the DB-compaction switches: reference writes and the
// bounded online backfill. They are deployment flags, not preferences.
type storageOptions struct {
	references        bool
	backfill          bool
	maintenance       bool
	maintenanceLimits store.MaintenanceOptions
	limits            hub.BackfillLimits
}

// runIdentity reads identity metadata or rotates the database-backed key offline.
// Refuse an unconfirmed rotation before opening the store, so even a new data
// directory is untouched.
func runIdentity(ctx context.Context, args []string, defaultDir string, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "show" && args[0] != "rotate") {
		return errors.New("usage: vecnet-hub identity show|rotate [--data-dir D] [--yes]")
	}
	fs := flag.NewFlagSet("vecnet-hub identity "+args[0], flag.ContinueOnError)
	dir := fs.String("data-dir", defaultDir, "directory holding hub.db")
	var yes bool
	if args[0] == "rotate" {
		fs.BoolVar(&yes, "yes", false, "confirm offline identity rotation (stop the hub first)")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("identity: unexpected arguments")
	}
	if args[0] == "rotate" && !yes {
		return errors.New("identity rotate: --yes is required; stop the hub before rotating")
	}
	// Do not construct a Hub here: startup recovery would reset a live hub's
	// connected peers and in-flight dispatch claims, even for identity show.
	db, err := openStore(*dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if args[0] == "rotate" {
		oldFP, newFP, err := hub.RotateIdentity(ctx, db, time.Now())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "old %s\nnew %s\n", oldFP, newFP)
		return err
	}
	doc, err := readIdentityMetadata(ctx, db)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(stdout, "pubkey %s\nfingerprint %s\ncreated_at %s\n", doc.Pubkey, doc.Fingerprint, doc.KeyCreatedAt); err != nil {
		return err
	}
	if doc.Previous != nil {
		_, err = fmt.Fprintf(stdout, "previous %s\n", doc.Previous.Pubkey)
	}
	return err
}

// readIdentityMetadata reads one consistent metadata snapshot without running
// serving-hub recovery or creating a blob store. The URL is not needed by show.
func readIdentityMetadata(ctx context.Context, db *store.DB) (envelope.HubIdentity, error) {
	var doc envelope.HubIdentity
	var seed, previous string
	if err := db.QueryRowContext(ctx, `SELECT seed.value, created.value,
		COALESCE((SELECT value FROM meta WHERE key='hub_identity_prev'), '')
		FROM meta seed JOIN meta created ON created.key='hub_identity_created_at'
		WHERE seed.key='hub_identity_seed'`).Scan(&seed, &doc.KeyCreatedAt, &previous); err != nil {
		return doc, fmt.Errorf("read identity: %w", err)
	}
	key, err := hubid.KeyFromSeedHex(seed)
	if err != nil {
		return doc, err
	}
	doc.Pubkey = key.Pubkey()
	doc.Fingerprint, err = hubid.Fingerprint(doc.Pubkey)
	if err != nil {
		return doc, err
	}
	if _, err := envelope.ParseTime(doc.KeyCreatedAt); err != nil {
		return doc, fmt.Errorf("identity creation time: %w", err)
	}
	if previous != "" {
		var prev envelope.HubKeyPrevious
		if err := json.Unmarshal([]byte(previous), &prev); err != nil {
			return doc, fmt.Errorf("previous identity: %w", err)
		}
		if err := hubid.VerifyEndorsement(prev, doc.Pubkey); err != nil {
			return doc, fmt.Errorf("previous identity: %w", err)
		}
		doc.Previous = &prev
	}
	return doc, nil
}

func openStore(dataDir string) (*store.DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := privateDataDir(dataDir); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dataDir, "hub.db"))
}

// privateDataDir makes dir private before hub.db is opened: the database
// holds plaintext signing keys (hub_key, route and notify whsec_ keys)
// protected only by the 0700 data directory (PROTOCOL §12, Secrets at
// rest). MkdirAll keeps an existing directory's mode, such as a volume
// mount's 0755, so group and other bits are cleared here; a directory this
// process cannot chmod is refused. Windows has no such mode bits.
func privateDataDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data dir %s is not a directory", dir)
	}
	perm := info.Mode().Perm()
	if runtime.GOOS == "windows" || perm&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, perm&^0o077); err != nil {
		return fmt.Errorf("data dir %s is %v, readable beyond its owner, and cannot be made private (%w); chmod 700 it", dir, perm, err)
	}
	slog.Warn("vecnet-hub: data dir was readable beyond its owner; made it private", "dir", dir, "was", perm.String(), "now", (perm &^ 0o077).String())
	return nil
}

// openHub opens <dataDir>/hub.db (creating dataDir) and builds the Hub.
func openHub(dataDir, publicURL string, allowPrivateEgress bool, bootstrap ...hub.Config) (*hub.Hub, *store.DB, error) {
	db, err := openStore(dataDir)
	if err != nil {
		return nil, nil, err
	}
	cfg := hub.DefaultConfig()
	if len(bootstrap) > 0 {
		cfg = bootstrap[0]
	}
	cfg.DataDir = dataDir
	cfg.PublicURL = publicURL
	cfg.AllowPrivateEgress = allowPrivateEgress
	h, err := hub.New(db, cfg, nil)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return h, db, nil
}

// runInit creates the operator once and prints its peer id and secret; a
// later run prints "already initialised" and the existing peer id, never a
// secret.
func runInit(dataDir, name string, w io.Writer) error {
	h, db, err := openHub(dataDir, "", false)
	if err != nil {
		return err
	}
	defer db.Close()
	peer, secret, created, err := h.EnsureOperator(context.Background(), name)
	if err != nil {
		return err
	}
	if !created {
		_, err = fmt.Fprintf(w, "already initialised: operator peer %s (rotate the credential to get a new secret)\n", peer)
		return err
	}
	_, err = fmt.Fprintf(w, "operator peer:   %s\noperator secret: %s\n\nThe secret is shown once. Store it now.\n", peer, secret)
	return err
}

// hubConfigURL runs the operator's public or relocation URL through
// CheckHubURL. Userinfo, a query, a fragment, a bad scheme and a string that
// does not parse refuse startup. The message names the rule and no part of
// raw. Plain http is operator configuration and is allowed here; the CLI's
// LAN rule is what stops a client sending a bearer in the clear.
func hubConfigURL(flagName, raw string) error {
	if raw == "" {
		return nil
	}
	err := hubid.CheckHubURL(raw)
	if err == nil || errors.Is(err, hubid.ErrPlainHTTP) {
		return nil
	}
	return errors.New(flagName + ": " + err.Error())
}

// ShutdownGrace bounds restart announcement, drain and http.Server.Shutdown.
// It stays below fly.toml's kill_timeout so the store closes before the
// platform kills the process.
const ShutdownGrace = 10 * time.Second

type serveConfig struct {
	Listen, DataDir, PublicURL, DevUIDir string
	RelocatedTo                          string
	AllowPrivateEgress, TrustFly         bool
	RestartRetry                         time.Duration
	MCPOrigins, OAuthRedirects           []string
	RefreshInterval                      time.Duration
	MCPLegacySessions                    string
	// Bootstrap is the hub.Config resolved from the preference flags; nil
	// means hub.DefaultConfig(). RefreshInterval still wins for Events, and
	// Storage sets the reference-write and backfill switches.
	Bootstrap *hub.Config
	Storage   storageOptions
}

// serve runs until ctx is cancelled. Relocated mode serves a signed move
// without workers; ordinary mode runs the push and Events workers and tickers.
// It binds listen first, so ":0" picks a free port, and
// writes "vecnet-hub listening on http://<addr> (public url <url>)" to
// stdout once accepting. publicURL "" means http://<bound address>.
// devUIDir "" serves the embedded operator UI. allowPrivateEgress is the
// development-only egress override (hub.Config.AllowPrivateEgress).
// trustFly keys the per-client limiter on Fly-Client-IP (Deps.TrustFlyClientIP).
func serve(ctx context.Context, cfg serveConfig, stdout io.Writer) error {
	if cfg.Bootstrap != nil {
		if err := hub.ValidateBlobConfig(*cfg.Bootstrap); err != nil {
			return err
		}
	}
	publicURL := cfg.PublicURL
	if err := hubConfigURL("--public-url", publicURL); err != nil {
		return err
	}
	if err := hubConfigURL("--relocated-to", cfg.RelocatedTo); err != nil {
		return err
	}
	var target string
	if cfg.RelocatedTo != "" {
		var err error
		target, err = hubid.NormalizeURL(cfg.RelocatedTo)
		if err != nil {
			return errors.New("--relocated-to: " + hubid.ErrHubForm.Error())
		}
		if publicURL != "" {
			own, err := hubid.NormalizeURL(publicURL)
			if err != nil {
				return errors.New("--public-url: " + hubid.ErrHubForm.Error())
			}
			if target == own {
				return errors.New("--relocated-to must differ from --public-url")
			}
		}
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	addr := ln.Addr().String()
	if publicURL == "" {
		publicURL = "http://" + addr
	}
	if err := hubConfigURL("--public-url", publicURL); err != nil {
		return err
	}
	if target != "" {
		own, err := hubid.NormalizeURL(publicURL)
		if err != nil {
			return errors.New("--public-url: " + hubid.ErrHubForm.Error())
		}
		if target == own {
			return errors.New("--relocated-to must differ from --public-url")
		}
	}
	// Preference flags resolve into the bootstrap Config before hub.New, so
	// the preference service records their flag/environment provenance.
	bootstrap := hub.DefaultConfig()
	if cfg.Bootstrap != nil {
		bootstrap = *cfg.Bootstrap
	}
	bootstrap.EventsRefreshInterval = cfg.RefreshInterval
	bootstrap.DeliveryReferences = cfg.Storage.references
	bootstrap.BackfillEnabled = cfg.Storage.backfill
	bootstrap.BackfillLimits = cfg.Storage.limits
	bootstrap.StorageMaintenanceEnabled = cfg.Storage.maintenance
	bootstrap.StorageMaintenance = cfg.Storage.maintenanceLimits
	h, db, err := openHub(cfg.DataDir, publicURL, cfg.AllowPrivateEgress, bootstrap)
	if err != nil {
		ln.Close()
		return err
	}
	defer db.Close()

	if cfg.AllowPrivateEgress {
		slog.Warn("vecnet-hub: private egress allowed (development only): outbound URLs may be http and reach private addresses")
	}
	stack := httpapi.NewStack(h)
	stack.OAuth.AllowedRedirects = cfg.OAuthRedirects
	// Shutdown does not cancel request contexts, so long-lived SSE streams
	// and long-polls watch stop instead; closing it on shutdown lets them
	// end at once rather than hold the whole grace period.
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }
	defer closeStop()
	deps := stack.Deps()
	deps.UIDir, deps.Stop = cfg.DevUIDir, stop
	deps.Drain = httpapi.NewDrain()
	deps.TrustFlyClientIP = cfg.TrustFly
	deps.MCPAllowedOrigins = cfg.MCPOrigins
	deps.MCPLegacySessions = cfg.MCPLegacySessions
	var handler http.Handler
	if target != "" {
		// EnsureMovingNotice also re-signs, as the hub, a stored notice that
		// an offline rotation left unverifiable.
		notice, err := h.EnsureMovingNotice(ctx, target)
		if err != nil {
			return err
		}
		handler = httpapi.Relocated(deps, notice)
	} else {
		if _, err := h.ClearOnBoot(ctx); err != nil {
			return err
		}
		handler = httpapi.New(deps)
	}
	srv := &http.Server{Handler: handler}
	httpapi.ConfigureServer(srv)
	srv.RegisterOnShutdown(closeStop)

	// Background work stops after the HTTP server, before the store closes.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	defer func() {
		bgCancel()
		bg.Wait()
	}()
	if target == "" {
		bg.Go(func() { stack.Run(bgCtx) })
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	slog.Info("vecnet-hub listening", "addr", addr, "public_url", publicURL)
	if target != "" {
		fmt.Fprintf(stdout, "vecnet-hub relocated: answering hub_moved → %s\n", target)
	}
	fmt.Fprintf(stdout, "vecnet-hub listening on http://%s (public url %s)\n", addr, publicURL)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// The grace period stays under Fly's kill_timeout (fly.toml).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
	defer cancel()
	// Announce first, so event and activity streams end with hub_restarting
	// carrying the configured hint (and held long-polls answer with it), and
	// give them a moment to flush. Then close stop, which ends every other
	// long-lived response (MCP streams), before Shutdown closes listeners
	// and waits for handlers.
	deps.Drain.Announce(cfg.RestartRetry)
	deps.Drain.Wait(time.Second + 250*time.Millisecond)
	closeStop()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Long-lived streams (SSE, long polls) outlasted the grace period.
		srv.Close()
		return err
	}
	return nil
}

// registerPreferenceFlags captures explicit visits separately from values, so
// configuring a built-in value still carries flag/environment provenance.
// String flags defer scalar parsing until resolution to avoid echoing rejected
// values in flag package errors. getenv is injected; empty means absent under
// the existing run contract (func(string) string cannot distinguish unset).
func registerPreferenceFlags(fs *flag.FlagSet, getenv func(string) string) func() (hub.Config, error) {
	type binding struct {
		key, flag, env string
	}
	bindings := []binding{
		{key: "retention.ack_events", flag: "retention-ack-events"},
		{key: "retention.history", flag: "retention-history"},
		{key: "retention.archive_mode", flag: "retention-archive-mode"},
		{key: "blobs.expiry", flag: "blobs-expiry"},
		{key: "events.refresh_interval", flag: "events-refresh-interval"},
		{key: "presence.default_ttl", flag: "presence-ttl"},
		{key: "presence.send_warning_after", flag: "send-warning-after"},
		{key: "presence.session_ttl", flag: "session-presence-ttl"},
		{key: "presence.launcher_ttl", flag: "launcher-presence-ttl"},
		{key: "agents.default_max_depth", flag: "default-max-depth"},
		{key: "agents.default_ttl", flag: "default-ttl"},
		{key: "agents.thread_budget", flag: "thread-budget"},
		{key: "rates.sends_per_minute", flag: "sends-per-minute"},
		{key: "rates.threads_per_hour", flag: "threads-per-hour"},
		{key: "provisioning.creates_per_hour", flag: "creates-per-hour"},
	}
	for i := range bindings {
		b := &bindings[i]
		b.env = "VECNET_" + strings.ToUpper(strings.ReplaceAll(b.flag, "-", "_"))
		d, _ := prefs.Lookup(b.key)
		n, _ := d.Default.Int64()
		def := strconv.FormatInt(n, 10)
		if d.Type == prefs.TypeBool {
			def = "false"
		}
		if d.Type == prefs.TypeDuration {
			def = (time.Duration(n) * time.Second).String()
		} else if d.Type == prefs.TypeEnum {
			def, _ = d.Default.Enum()
		}
		// Usage can run before validation (help or any parser error). Only safe
		// built-in defaults may enter flag metadata; resolve the environment below.
		fs.String(b.flag, def, d.Description+" ($"+b.env+")")
	}
	return func() (hub.Config, error) {
		cfg := hub.DefaultConfig()
		cfg.PreferenceBootstrap = map[string]prefs.Effective{}
		visited := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
		for _, b := range bindings {
			origin := ""
			raw := fs.Lookup(b.flag).Value.String()
			if environment := getenv(b.env); environment != "" {
				origin = b.env
				raw = environment
			}
			if visited[b.flag] {
				origin = "--" + b.flag
				raw = fs.Lookup(b.flag).Value.String()
			}
			d, _ := prefs.Lookup(b.key)
			var n int64
			var err error
			if d.Type == prefs.TypeDuration {
				var duration time.Duration
				duration, err = time.ParseDuration(raw)
				if err != nil || duration%time.Second != 0 {
					return cfg, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: b.key, Constraint: "whole_seconds"}}
				}
				n = int64(duration / time.Second)
			} else if d.Type == prefs.TypeBool {
				if raw == "true" {
					n = 1
				} else if raw != "false" {
					return cfg, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: b.key, Constraint: "boolean"}}
				}
			} else if d.Type != prefs.TypeEnum {
				n, err = strconv.ParseInt(raw, 10, 64)
			}
			if err != nil {
				return cfg, &prefs.Error{Code: prefs.CodeInvalidValue, Details: prefs.ErrorDetails{Field: b.key, Constraint: "integer"}}
			}
			value := prefs.Int(n)
			if d.Type == prefs.TypeBool {
				value = prefs.Bool(n == 1)
			}
			if d.Type == prefs.TypeDuration {
				value = prefs.Duration(n)
			} else if d.Type == prefs.TypeEnum {
				value = prefs.Enum(raw)
			}
			if err := prefs.Validate(d, value); err != nil {
				return cfg, err
			}
			if origin != "" {
				cfg.PreferenceBootstrap[b.key] = prefs.Effective{Value: value, Source: prefs.SourceFlag, Origin: origin}
			}
			switch b.key {
			case "retention.ack_events":
				cfg.AckedEventRetention = time.Duration(n) * time.Second
			case "retention.history":
				cfg.MessageHistoryRetention = time.Duration(n) * time.Second
			case "retention.archive_mode":
				cfg.ArchiveMode = n == 1
			case "events.refresh_interval":
				cfg.EventsRefreshInterval = time.Duration(n) * time.Second
			case "presence.default_ttl":
				cfg.PresenceTTL = time.Duration(n) * time.Second
			case "presence.session_ttl":
				cfg.SessionPresenceTTL = time.Duration(n) * time.Second
			case "presence.launcher_ttl":
				cfg.LauncherPresenceTTL = time.Duration(n) * time.Second
			case "agents.default_max_depth":
				cfg.DefaultMaxDepth = int(n)
			case "agents.default_ttl":
				cfg.DefaultTTL = time.Duration(n) * time.Second
			case "agents.thread_budget":
				cfg.ThreadBudget = int(n)
			case "rates.sends_per_minute":
				cfg.SendsPerMin = int(n)
			case "rates.threads_per_hour":
				cfg.ThreadsPerHour = int(n)
			case "provisioning.creates_per_hour":
				cfg.CreatesPerHour = int(n)
			}
		}
		return cfg, nil
	}
}
