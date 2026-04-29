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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/api"
	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/config"
	"github.com/Iann29/synapse/internal/db"
	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/health"
	"github.com/Iann29/synapse/internal/proxy"
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

	// Sweep orphaned 'provisioning' rows. If the previous Synapse process
	// crashed (or was SIGKILL'd) mid-provision, the goroutine that would
	// have flipped status to 'running'/'failed' is gone, leaving the row
	// stuck forever. 10 minutes is well past our 5-minute provision deadline,
	// so anything older is unambiguously dead.
	if err := sweepOrphanedProvisioning(rootCtx, pool, logger); err != nil {
		logger.Error("orphan sweep failed", "err", err)
	}

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

	// Health worker — periodic reconciler that flips deployment rows to
	// 'stopped' / 'failed' when the underlying Docker container has gone
	// missing. Skipped if no Docker daemon was reachable at startup; the
	// API still works for read-only / metadata operations in that case.
	if dockerClient != nil {
		worker := &health.Worker{
			DB:     pool,
			Docker: dockerClient,
			Config: health.Config{Interval: 30 * time.Second, StatusTimeout: 5 * time.Second},
			Logger: logger,
		}
		go worker.Run(rootCtx)
	}

	// Reverse-proxy mux: route /d/{name}/* to the proxy package, everything
	// else to the API router. Mounting via http.NewServeMux keeps this
	// composable without surgery on chi's tree.
	var topHandler http.Handler = handler
	if cfg.ProxyEnabled {
		mux := http.NewServeMux()
		resolver := &proxy.Resolver{
			DB:            pool,
			UseNetworkDNS: cfg.HealthcheckViaNetwork,
			CacheTTL:      30 * time.Second,
		}
		mux.Handle("/d/", proxy.Handler(resolver, logger))
		mux.Handle("/", handler)
		topHandler = mux
		logger.Info("reverse proxy enabled", "mount", "/d/")
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           topHandler,
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

// sweepOrphanedProvisioning bumps any deployment row that's been stuck in
// 'provisioning' for more than 10 minutes to 'failed'. This recovers from
// crashes where the goroutine driving Provision dies before it can update
// the row. Single SQL UPDATE — no Docker calls; the operator (or a future
// reconciler) can decide whether the underlying container is salvageable.
func sweepOrphanedProvisioning(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	tag, err := pool.Exec(ctx, `
		UPDATE deployments
		   SET status = 'failed',
		       last_deploy_at = now()
		 WHERE status = 'provisioning'
		   AND created_at < now() - interval '10 minutes'
	`)
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		logger.Warn("swept orphaned provisioning deployments", "count", n)
	}
	return nil
}
