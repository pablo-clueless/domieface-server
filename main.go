// Command domieface-server serves the social API described in HANDOFF.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"domieface/com/internal/api"
	"domieface/com/internal/auth"
	"domieface/com/internal/buildinfo"
	"domieface/com/internal/config"
	"domieface/com/internal/janitor"
	"domieface/com/internal/keepalive"
	"domieface/com/internal/store/postgres"
	"domieface/com/internal/uploads"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Printed plainly rather than through slog. This is the first thing a
		// human reads in a failed deploy log, and structured logging would
		// escape its newlines and collapse the whole thing onto one line.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		slog.Error("server exited with an error", "error", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config) error {
	setupLogging(cfg)

	// Cancelled on SIGINT or SIGTERM; every background goroutine watches it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return err
	}

	presigner, err := uploads.New(ctx, cfg.Storage)
	if err != nil {
		return err
	}

	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL)
	sessions := auth.NewService(st.Tokens(), issuer, cfg.RefreshTokenTTL)
	server := api.New(cfg, st, sessions, issuer, presigner)

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: server.Handler(),
		// Timeouts are set explicitly: http.Server's zero values mean "no
		// limit", which lets a single slow client hold a connection open
		// indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	var background sync.WaitGroup

	background.Add(1)
	go func() {
		defer background.Done()
		janitor.New(st, presigner, cfg.UploadGCAfter, time.Hour).Run(ctx)
	}()

	if cfg.SelfPing.Enabled {
		background.Add(1)
		go func() {
			defer background.Done()
			keepalive.New(cfg.SelfPing).Run(ctx)
		}()
	}

	for _, limiter := range server.Limiters() {
		background.Add(1)
		go func() {
			defer background.Done()
			limiter.StartReaper(5*time.Minute, 30*time.Minute, ctx.Done())
		}()
	}

	serveErr := make(chan error, 1)
	go func() {
		attrs := []any{
			"addr", cfg.Addr,
			"version", buildinfo.Version(),
			"environment", cfg.Environment,
			"bucket", cfg.Storage.Bucket,
		}
		if cfg.DocsEnabled {
			attrs = append(attrs, "docs", api.DocsPath, "openapi", api.OpenAPIPath)
		}
		slog.Info("listening", attrs...)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining connections")
	}

	// Stop accepting new connections and give in-flight requests time to finish
	// before the process exits.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	shutdownErr := httpServer.Shutdown(shutdownCtx)
	background.Wait()

	if shutdownErr != nil {
		return shutdownErr
	}
	slog.Info("shutdown complete")
	return <-serveErr
}

func setupLogging(cfg *config.Config) {
	level := slog.LevelDebug
	if cfg.IsProduction() {
		level = slog.LevelInfo
	}

	var handler slog.Handler
	if cfg.IsProduction() {
		// Structured JSON in production so logs are queryable; text locally so
		// they are readable.
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	slog.SetDefault(slog.New(handler))
}
