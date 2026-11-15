// Command profile-service serves user profiles over HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/distrocommerce/profile-service/internal/config"
	"github.com/distrocommerce/profile-service/internal/httpapi"
	"github.com/distrocommerce/profile-service/internal/store"
	"github.com/distrocommerce/profile-service/internal/telemetry"
)

// version is set at build time: -ldflags "-X main.version=<git sha>".
var version = "dev"

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second

	// drainDelay is how long the server keeps serving after SIGTERM. A
	// terminating pod is removed from the EndpointSlice right away, but Envoy
	// learns that through xDS a moment later and may still send requests here
	// meanwhile. /readyz fails during this time as a second signal (the
	// readiness probe runs only every 10 s, so it is not what removes the pod).
	drainDelay = 5 * time.Second
)

func main() {
	// JSON from the first line, even for config errors (LOG_LEVEL is not known yet).
	slog.SetDefault(telemetry.NewLogger(os.Stdout, slog.LevelInfo))
	if err := run(); err != nil {
		var logged loggedError
		if !errors.As(err, &logged) {
			slog.Error("exit", "error", err) // before telemetry: stdout only
		}
		os.Exit(1)
	}
}

// loggedError is an error that run has already logged, before the telemetry
// flush, so that it is exported too.
type loggedError struct{ error }

func (e loggedError) Unwrap() error { return e.error }

func run() (err error) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	stdoutLog := telemetry.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(stdoutLog)
	// OTel's own errors: stdout only, at warn whatever LOG_LEVEL is.
	telemetry.SetErrorLogger(os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Telemetry first: the pgx tracer and pool metrics use the global providers.
	providers, err := telemetry.Setup(ctx, version)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := providers.Shutdown(ctx); err != nil {
			stdoutLog.Warn("telemetry shutdown", "error", err) // the exporter is gone
		}
	}()
	// From here on records are exported over OTLP too (stdout only when the SDK is disabled).
	log := telemetry.NewExportingLogger(os.Stdout, cfg.LogLevel, providers.Logs, cfg.LogExportLevel)
	slog.SetDefault(log)
	// Registered after the telemetry shutdown, so it runs before the flush and
	// the error that ends the process is exported too.
	defer func() {
		if err != nil {
			log.Error("exit", "error", err)
			err = loggedError{err}
		}
	}()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	applied, err := st.Migrate(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.Info("migrations done", "applied", applied)

	api := httpapi.New(st, log, cfg.MaxBodyBytes)
	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           api.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "version", version)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	stop() // a second signal kills the process at once

	log.Info("shutting down", "drain_delay", drainDelay.String(), "timeout", cfg.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	api.StartDraining()
	select {
	case <-time.After(min(drainDelay, cfg.ShutdownTimeout/2)):
	case <-shutdownCtx.Done():
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("stopped")
	return nil
}
