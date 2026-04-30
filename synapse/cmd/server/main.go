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
	"github.com/Iann29/synapse/internal/crypto"
	"github.com/Iann29/synapse/internal/db"
	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/health"
	"github.com/Iann29/synapse/internal/provisioner"
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

	// Storage-secrets crypto. Optional — only HA-enabled clusters need
	// it. When unset the handler refuses ha:true with ha_misconfigured;
	// non-HA flows are unaffected.
	var secretBox *crypto.SecretBox
	if cfg.HAEnabled {
		secretBox, err = crypto.NewFromEnv()
		if err != nil {
			logger.Error("HA enabled but SYNAPSE_STORAGE_KEY is missing or malformed",
				"err", err)
			return err
		}
		logger.Info("HA mode enabled; storage secrets envelope active")
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
		PublicURL:             cfg.PublicURL,
		ProxyEnabled:          cfg.ProxyEnabled,
		HA: api.HAConfig{
			Enabled:             cfg.HAEnabled,
			BackendPostgresURL:  cfg.BackendPostgresURL,
			BackendS3Endpoint:   cfg.BackendS3Endpoint,
			BackendS3Region:     cfg.BackendS3Region,
			BackendS3AccessKey:  cfg.BackendS3AccessKey,
			BackendS3SecretKey:  cfg.BackendS3SecretKey,
			BackendBucketPrefix: cfg.BackendS3BucketPrefix,
		},
		Crypto: secretBox,
	})

	// Provisioning worker — dequeues 'provision' jobs inserted by the
	// /create_deployment handler and drives Docker.Provision to completion.
	// Survives process restarts (jobs persisted as rows) and shards across
	// nodes via SELECT FOR UPDATE SKIP LOCKED.
	if dockerClient != nil {
		hostName, _ := os.Hostname()
		nodeID := hostName
		if nodeID == "" {
			nodeID = "synapse"
		}
		pworker := &provisioner.Worker{
			DB:     pool,
			Docker: dockerClient,
			Config: provisioner.Config{
				PollInterval:          time.Second,
				JobTimeout:            5 * time.Minute,
				NodeID:                nodeID,
				HealthcheckViaNetwork: cfg.HealthcheckViaNetwork,
			},
			Logger: logger,
			Crypto: secretBox, // nil when HA is off — single-replica jobs don't read it
		}
		go pworker.Run(rootCtx)
	}

	// Health worker — periodic reconciler that flips deployment rows to
	// 'stopped' / 'failed' when the underlying Docker container has gone
	// missing. Skipped if no Docker daemon was reachable at startup; the
	// API still works for read-only / metadata operations in that case.
	if dockerClient != nil {
		worker := &health.Worker{
			DB:        pool,
			Docker:    dockerClient,
			Restarter: dockerClient,
			Config: health.Config{
				Interval:      30 * time.Second,
				StatusTimeout: 5 * time.Second,
				AutoRestart:   cfg.HealthAutoRestart,
			},
			Logger: logger,
		}
		go worker.Run(rootCtx)
		if cfg.HealthAutoRestart {
			logger.Info("health worker auto-restart enabled")
		}
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
//
// Multi-node coordination: 3 nodes booting at the same time would each issue
// the same UPDATE — idempotent, but noisy. Wrap in an advisory lock so only
// one node runs it; followers see acquired=false and move on.
func sweepOrphanedProvisioning(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	acquired, err := db.WithTryAdvisoryLock(ctx, pool, db.LockOrphanSweep,
		func(ctx context.Context) error {
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
		})
	if err != nil {
		return err
	}
	if !acquired {
		logger.Debug("orphan sweep: another node holds the lock; skipping")
	}
	return nil
}
