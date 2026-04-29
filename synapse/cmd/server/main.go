// Synapse: open-source control plane for self-hosted Convex.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Iann29/synapse/internal/api"
	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/config"
	"github.com/Iann29/synapse/internal/db"
	dockerprov "github.com/Iann29/synapse/internal/docker"
)

// Version is overridden at build time via -ldflags.
var Version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	logger.Info("synapse starting", "version", Version, "addr", cfg.HTTPAddr)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.Migrate(cfg.DBURL, logger); err != nil {
		return err
	}

	pool, err := db.Connect(rootCtx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("postgres connected")

	jwtIssuer := auth.NewJWTIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)

	dockerClient, err := dockerprov.NewClient(cfg.DockerHost, cfg.BackendImage, cfg.DockerNetwork, logger)
	if err != nil {
		logger.Warn("docker unavailable; provisioning endpoints will fail", "err", err)
	}

	handler := api.NewRouter(api.RouterDeps{
		Logger:                logger,
		DB:                    pool,
		JWT:                   jwtIssuer,
		Docker:                dockerClient,
		PortRangeMin:          cfg.PortRangeMin,
		PortRangeMax:          cfg.PortRangeMax,
		HealthcheckViaNetwork: cfg.HealthcheckViaNetwork,
		AllowedOrigins:        cfg.AllowedOrigins,
		Version:               Version,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		logger.Info("shutdown requested")
	case err := <-errCh:
		return err
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		return err
	}
	logger.Info("synapse stopped")
	return nil
}
