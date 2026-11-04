// Command vecnet-hub runs the vecnet hub, or initialises its operator.
//
//	vecnet-hub [--listen 127.0.0.1:45752] [--data-dir /data] [--public-url URL] [--dev-ui-dir DIR] [--allow-private-egress] [--trust-fly-client-ip]
//	vecnet-hub init [--data-dir /data] --operator-name <name>
//	vecnet-hub identity show|rotate [--data-dir /data] [--yes]
//
// VECNET_LISTEN, VECNET_DATA_DIR, VECNET_PUBLIC_URL and VECNET_RELOCATED_TO
// replace the corresponding flag defaults; flags still win. --relocated-to
// serves the old hub without workers and answers hub_moved for ordinary routes.
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
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/logging"
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
	publicURL := fs.String("public-url", getenv("VECNET_PUBLIC_URL"), "externally visible origin ($VECNET_PUBLIC_URL; default http://<listen address>)")
	devUIDir := fs.String("dev-ui-dir", "", "serve the operator UI from this directory instead of the embedded copy (development)")
	allowPrivate := fs.Bool("allow-private-egress", getenv("VECNET_ALLOW_PRIVATE_EGRESS") == "1",
		"development only: let outbound URLs be http and reach private/loopback addresses ($VECNET_ALLOW_PRIVATE_EGRESS=1)")
	trustFly := fs.Bool("trust-fly-client-ip", getenv("VECNET_TRUST_FLY_CLIENT_IP") == "1",
		"key the per-client rate limit on the Fly-Client-IP header; only behind Fly's proxy ($VECNET_TRUST_FLY_CLIENT_IP=1)")
	relocatedTo := fs.String("relocated-to", getenv("VECNET_RELOCATED_TO"), "answer hub_moved without workers at the old hub ($VECNET_RELOCATED_TO)")
	restartRetry := fs.String("restart-retry-ms", envOr(getenv, "VECNET_RESTART_RETRY_MS", "5000"), "restart reconnect delay in milliseconds, 0..60000 ($VECNET_RESTART_RETRY_MS)")
	if err := fs.Parse(args); err != nil {
		return err
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
	retryMS, err := strconv.ParseInt(*restartRetry, 10, 64)
	if err != nil || retryMS < 0 || retryMS > 60000 {
		return errors.New("--restart-retry-ms must be an integer in 0..60000")
	}
	return serve(ctx, serveConfig{
		Listen: *listen, DataDir: *dataDir, PublicURL: *publicURL, DevUIDir: *devUIDir, RelocatedTo: *relocatedTo,
		AllowPrivateEgress: *allowPrivate, TrustFly: *trustFly, RestartRetry: time.Duration(retryMS) * time.Millisecond,
	}, stdout)
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
	return store.Open(filepath.Join(dataDir, "hub.db"))
}

// openHub opens <dataDir>/hub.db (creating dataDir) and builds the Hub.
func openHub(dataDir, publicURL string, allowPrivateEgress bool) (*hub.Hub, *store.DB, error) {
	db, err := openStore(dataDir)
	if err != nil {
		return nil, nil, err
	}
	cfg := hub.DefaultConfig()
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

// ShutdownGrace bounds restart announcement, drain and http.Server.Shutdown.
// It stays below fly.toml's kill_timeout so the store closes before the
// platform kills the process.
const ShutdownGrace = 10 * time.Second

type serveConfig struct {
	Listen, DataDir, PublicURL, DevUIDir string
	RelocatedTo                          string
	AllowPrivateEgress, TrustFly         bool
	RestartRetry                         time.Duration
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
	publicURL := cfg.PublicURL
	var target string
	if cfg.RelocatedTo != "" {
		var err error
		target, err = hubid.NormalizeURL(cfg.RelocatedTo)
		if err != nil {
			return fmt.Errorf("--relocated-to: %w", err)
		}
		if publicURL != "" {
			own, err := hubid.NormalizeURL(publicURL)
			if err != nil {
				return fmt.Errorf("--public-url: %w", err)
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
	if target != "" {
		own, err := hubid.NormalizeURL(publicURL)
		if err != nil {
			return fmt.Errorf("--public-url: %w", err)
		}
		if target == own {
			return errors.New("--relocated-to must differ from --public-url")
		}
	}
	h, db, err := openHub(cfg.DataDir, publicURL, cfg.AllowPrivateEgress)
	if err != nil {
		ln.Close()
		return err
	}
	defer db.Close()

	if cfg.AllowPrivateEgress {
		slog.Warn("vecnet-hub: private egress allowed (development only): outbound URLs may be http and reach private addresses")
	}
	stack := httpapi.NewStack(h)
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
	var handler http.Handler
	if target != "" {
		notice, err := h.EnsureMovingNotice(ctx, target)
		if err != nil {
			return err
		}
		// An offline rotation leaves stored notices signed by the old key.
		// Replace only an unverifiable notice, atomically updating the active
		// notice and its Events while preserving the operator's move fields.
		if err := hubid.VerifyNotice(h.IdentityKey().Pubkey(), notice); err != nil {
			notice, err = h.PostNotice(ctx, auth.Caller{Kind: "operator"}, hub.NoticeRequest{
				Kind: notice.Kind, Message: notice.Message, At: notice.At,
				ExpiresAt: notice.ExpiresAt, NewURL: notice.NewURL,
			})
			if err != nil {
				return err
			}
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
	deps.Drain.Announce(cfg.RestartRetry)
	deps.Drain.Wait(time.Second + 250*time.Millisecond)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Long-lived streams (SSE, long polls) outlasted the grace period.
		srv.Close()
		return err
	}
	return nil
}
