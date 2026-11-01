// Command vecnet-hub runs the vecnet hub, or initialises its operator.
//
//	vecnet-hub [--listen 127.0.0.1:45752] [--data-dir /data] [--public-url URL] [--dev-ui-dir DIR] [--allow-private-egress]
//	vecnet-hub init [--data-dir /data] --operator-name <name>
//
// VECNET_LISTEN, VECNET_DATA_DIR and VECNET_PUBLIC_URL, when set, replace
// the defaults of --listen, --data-dir and --public-url; flags still win.
// VECNET_ALLOW_PRIVATE_EGRESS=1 is the default of --allow-private-egress,
// a development-only override that lets push and notify URLs be http and
// reach private or loopback addresses (PROTOCOL §14.7).
package main

import (
	"context"
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
	"sync"
	"syscall"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/push"
	"github.com/XertroV/vecnet/internal/store"
)

func main() {
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
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown subcommand %q", fs.Arg(0))
	}
	return serve(ctx, *listen, *dataDir, *publicURL, *devUIDir, *allowPrivate, stdout)
}

// openHub opens <dataDir>/hub.db (creating dataDir) and builds the Hub.
func openHub(dataDir, publicURL string, allowPrivateEgress bool) (*hub.Hub, *store.DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := store.Open(filepath.Join(dataDir, "hub.db"))
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

// ShutdownGrace bounds http.Server.Shutdown; it must stay below fly.toml's
// kill_timeout so the store closes before the platform kills the process.
const ShutdownGrace = 10 * time.Second

// serve runs the HTTP server, the push worker and the hub's tickers until
// ctx is cancelled. It binds listen first, so ":0" picks a free port, and
// writes "vecnet-hub listening on http://<addr> (public url <url>)" to
// stdout once accepting. publicURL "" means http://<bound address>.
// devUIDir "" serves the embedded operator UI. allowPrivateEgress is the
// development-only egress override (hub.Config.AllowPrivateEgress).
func serve(ctx context.Context, listen, dataDir, publicURL, devUIDir string, allowPrivateEgress bool, stdout io.Writer) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	if publicURL == "" {
		publicURL = "http://" + addr
	}
	h, db, err := openHub(dataDir, publicURL, allowPrivateEgress)
	if err != nil {
		ln.Close()
		return err
	}
	defer db.Close()

	if allowPrivateEgress {
		slog.Warn("vecnet-hub: private egress allowed (development only): outbound URLs may be http and reach private addresses")
	}
	worker := &push.Worker{Hub: h}
	// Shutdown does not cancel request contexts, so long-lived SSE streams
	// and long-polls watch stop instead; closing it on shutdown lets them
	// end at once rather than hold the whole grace period.
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }
	defer closeStop()
	srv := &http.Server{
		Handler:           httpapi.New(httpapi.Deps{Hub: h, Kick: worker.Kick, VerifyRoute: worker.Verify, UIDir: devUIDir, Stop: stop}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	srv.RegisterOnShutdown(closeStop)

	// Background work stops after the HTTP server, before the store closes.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	defer func() {
		bgCancel()
		bg.Wait()
	}()
	bg.Go(func() { worker.Run(bgCtx) })
	bg.Go(func() { h.RunTickers(bgCtx, hub.DefaultTickInterval) })

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	slog.Info("vecnet-hub listening", "addr", addr, "public_url", publicURL)
	fmt.Fprintf(stdout, "vecnet-hub listening on http://%s (public url %s)\n", addr, publicURL)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// The grace period stays under Fly's kill_timeout (fly.toml).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Long-lived streams (SSE, long polls) outlasted the grace period.
		srv.Close()
		return err
	}
	return nil
}
