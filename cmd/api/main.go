package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/authn/local"
	"github.com/perdhevi/latihanAPI/internal/config"
	"github.com/perdhevi/latihanAPI/internal/database"
	"github.com/perdhevi/latihanAPI/internal/httpapi"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		if err := keygen(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "keygen:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL, database.Options{MaxConns: cfg.DBMaxConns, StatementTimeout: cfg.DBStatementTimeout})
	if err != nil {
		return err
	}
	defer pool.Close()
	// Providers get the database through database/sql so the auth contract
	// stays free of driver dependencies.
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	authenticator, err := auth.New(ctx, cfg.AuthProvider, auth.Deps{Getenv: os.Getenv, Logger: logger, HTTPClient: &http.Client{Timeout: 10 * time.Second}, DB: db})
	if err != nil {
		return err
	}
	logger.Info("authentication provider ready", "provider", cfg.AuthProvider)
	sessions := training.NewService(training.NewPostgresRepository(pool))
	profiles := profile.NewService(profile.NewPostgresRepository(pool))
	server := newServer(cfg.HTTPAddr, httpapi.NewRouter(sessions, profiles, pool, authenticator, logger, httpapi.Options{
		PerIP: cfg.RateLimitIP, PerUser: cfg.RateLimitUser, Public: cfg.RateLimitAuth,
		MaxInFlight: cfg.MaxInFlight, TrustedProxies: cfg.TrustedProxies,
	}))
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	logger.Info("server listening", "address", cfg.HTTPAddr)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	logger.Info("server stopped")
	return nil
}

// keygen creates the jwt provider's Ed25519 signing key. It never overwrites
// an existing key, so it is safe to run on every deployment.
func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := flags.String("out", "", "path of the PEM file to create")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("usage: api keygen -out /keys/signing.pem")
	}
	created, err := local.GenerateKeyFile(*out)
	if err != nil {
		return err
	}
	if created {
		fmt.Println("created signing key", *out)
	} else {
		fmt.Println("signing key", *out, "already exists; left unchanged")
	}
	return nil
}

// newServer bounds how long and how much a client may take to send a request.
// Go adds 4 KiB of slack to MaxHeaderBytes, so headers are refused above about
// 20 KiB with 431 Request Header Fields Too Large.
func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		MaxHeaderBytes:    16 << 10,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
