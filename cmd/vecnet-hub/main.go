// Command vecnet-hub runs the vecnet hub, or initialises its operator.
//
//	vecnet-hub [--listen 127.0.0.1:45752] [--data-dir /data] [--public-url URL] [--dev-ui-dir DIR]
//	vecnet-hub init [--data-dir /data] --operator-name <name>
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "vecnet-hub:", err)
		os.Exit(1)
	}
}

// run parses flags and dispatches to the server or the init subcommand.
func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("vecnet-hub", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:45752", "address to listen on")
	dataDir := fs.String("data-dir", "/data", "directory holding hub.db and blobs/")
	publicURL := fs.String("public-url", "", "externally visible origin (default http://<listen>)")
	devUIDir := fs.String("dev-ui-dir", "", "serve the operator UI from this directory instead of the embedded copy (development)")
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
	if *publicURL == "" {
		*publicURL = "http://" + *listen
	}
	return serve(*listen, *dataDir, *publicURL, *devUIDir)
}

// openHub opens <dataDir>/hub.db (creating dataDir) and builds the Hub.
func openHub(dataDir, publicURL string) (*hub.Hub, *store.DB, error) {
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
	h, db, err := openHub(dataDir, "")
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

// serve runs the HTTP server, the push worker and the hub's tickers until
// SIGINT or SIGTERM. devUIDir "" serves the embedded operator UI.
func serve(listen, dataDir, publicURL, devUIDir string) error {
	h, db, err := openHub(dataDir, publicURL)
	if err != nil {
		return err
	}
	defer db.Close()

	worker := &push.Worker{Hub: h}
	srv := &http.Server{
		Addr:              listen,
		Handler:           httpapi.New(httpapi.Deps{Hub: h, Kick: worker.Kick, VerifyRoute: worker.Verify, UIDir: devUIDir}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("vecnet-hub listening", "addr", listen, "public_url", publicURL)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
