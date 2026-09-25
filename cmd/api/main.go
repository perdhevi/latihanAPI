package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/account"
	"github.com/perdhevi/latihanAPI/internal/authn/local"
	"github.com/perdhevi/latihanAPI/internal/config"
	"github.com/perdhevi/latihanAPI/internal/database"
	"github.com/perdhevi/latihanAPI/internal/httpapi"
	"github.com/perdhevi/latihanAPI/internal/idempotency"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/telemetry"
	"github.com/perdhevi/latihanAPI/internal/training"
)

// commands are one-off tasks run with the same binary, since the runtime image
// has no shell: api keygen, api healthcheck.
var commands = map[string]func(args []string) error{
	"keygen":      keygen,
	"healthcheck": func([]string) error { return healthcheck(os.Getenv("HTTP_ADDR")) },
}

func main() {
	if len(os.Args) > 1 {
		if command, ok := commands[os.Args[1]]; ok {
			if err := command(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, os.Args[1]+":", err)
				os.Exit(1)
			}
			return
		}
	}
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(telemetry.LogHandler{Handler: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})})
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTracing, err := telemetry.SetupTracing(ctx, version)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(flushCtx)
	}()
	metrics := telemetry.NewMetrics(version)
	pool, err := database.Open(ctx, cfg.DatabaseURL, database.Options{MaxConns: cfg.DBMaxConns, StatementTimeout: cfg.DBStatementTimeout, Tracer: telemetry.QueryTracer{}})
	if err != nil {
		return err
	}
	defer pool.Close()
	metrics.RegisterPool(pool)
	if warning := database.TransportWarning(cfg.DatabaseURL); warning != "" {
		logger.Warn(warning)
	}
	// Providers get the database through database/sql so the auth contract
	// stays free of driver dependencies.
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	authenticator, err := auth.New(ctx, cfg.AuthProvider, auth.Deps{Getenv: cfg.Env.Get, Logger: logger, HTTPClient: &http.Client{Timeout: 10 * time.Second}, DB: db})
	if err != nil {
		// An unreadable *_FILE secret explains an empty setting better than the provider can.
		return errors.Join(cfg.Env.Err(), err)
	}
	// Providers read settings through Env; report unreadable *_FILE secrets.
	if err := cfg.Env.Err(); err != nil {
		return err
	}
	logger.Info("authentication provider ready", "provider", cfg.AuthProvider)
	sessions := training.NewService(training.NewPostgresRepository(pool))
	profiles := profile.NewService(profile.NewPostgresRepository(pool))
	keys := idempotency.NewStore(pool)
	accounts := account.NewStore(pool)
	go purgeExpired(ctx, keys, accounts, cfg.AuditRetention, logger)
	server := newServer(cfg.HTTPAddr, httpapi.NewRouter(sessions, profiles, pool, authenticator, logger, httpapi.Options{
		PerIP: cfg.RateLimitIP, PerUser: cfg.RateLimitUser, Public: cfg.RateLimitAuth,
		MaxInFlight: cfg.MaxInFlight, TrustedProxies: cfg.TrustedProxies, RequireIfMatch: cfg.RequireIfMatch,
		Idempotency: keys, Metrics: metrics,
		Account: accounts, Export: cfg.RateLimitExport, TruncateClientIP: cfg.TruncateClientIP,
	}))
	admin := newAdminServer(cfg.AdminAddr, metrics, cfg.AdminPprof)
	errCh := make(chan error, 2)
	go func() { errCh <- server.ListenAndServe() }()
	go func() { errCh <- admin.ListenAndServe() }()
	logger.Info("server listening", "address", cfg.HTTPAddr, "admin_address", cfg.AdminAddr, "pprof", cfg.AdminPprof, "version", version)
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
	_ = admin.Shutdown(shutdownCtx)
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

// purgeExpired deletes data past its retention every hour until ctx ends at
// shutdown: idempotency keys (24 hours, with their stored responses) and audit
// events (auditRetention).
func purgeExpired(ctx context.Context, keys *idempotency.Store, accounts *account.Store, auditRetention time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := keys.Purge(ctx); err != nil {
				logger.WarnContext(ctx, "purging idempotency keys failed", "error", err)
			} else if n > 0 {
				logger.InfoContext(ctx, "purged idempotency keys", "count", n)
			}
			if n, err := accounts.PurgeAudit(ctx, auditRetention); err != nil {
				logger.WarnContext(ctx, "purging audit events failed", "error", err)
			} else if n > 0 {
				logger.InfoContext(ctx, "purged audit events", "count", n)
			}
		}
	}
}

// newAdminServer serves operational endpoints on a separate listener that is
// never published to the internet: /metrics always, /debug/pprof only when
// enabled, because profiles expose internals and a CPU profile costs CPU.
func newAdminServer(addr string, metrics *telemetry.Metrics, withPprof bool) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	if withPprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// A CPU profile streams for up to 30 seconds.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// healthcheck asks this container's own server whether it is ready. The
// runtime image has no shell or wget, so the container healthcheck runs the
// binary itself: api healthcheck.
func healthcheck(addr string) error {
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", port)+"/ready", nil) //nolint:gosec // G704: always loopback; only the port comes from HTTP_ADDR
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s", resp.Status)
	}
	return nil
}
