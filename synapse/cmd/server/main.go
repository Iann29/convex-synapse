// Synapse: open-source control plane for self-hosted Convex.
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
	"strings"
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
	// One-shot CLI subcommands live alongside the server. We parse a
	// dedicated FlagSet first; if it matches a subcommand flag, run
	// it and exit without touching the DB / Docker / HTTP server.
	if handled, err := runSubcommand(os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// stringSliceFlag is a flag.Value collector for repeatable --map
// arguments in the form key=value. Stored as a slice (preserving
// order for diagnostics); callers convert to a map.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// runSubcommand inspects argv looking for one of the one-shot CLI
// flags. Returns (handled=true, err) when the flag is set, telling
// main() to exit instead of starting the server. Returns
// (handled=false, nil) when no subcommand was requested — main()
// continues with the normal boot path.
//
// Today there's exactly one subcommand: --adopt-domains-from-caddy.
// Adding more is just another fs.Bool / fs.String + early-return
// branch here. If the surface grows past 2-3 we should split into
// a proper synapse-cli binary; until then this keeps the install
// footprint at one Go binary.
func runSubcommand(args []string, stdout *os.File) (bool, error) {
	fs := flag.NewFlagSet("synapse", flag.ContinueOnError)
	fs.SetOutput(stdout)

	adopt := fs.Bool("adopt-domains-from-caddy", false,
		"parse a Caddyfile and register the hostnames as Synapse deployment_domains, then exit")
	caddyfile := fs.String("caddyfile", "",
		"path to the Caddyfile to import (required with --adopt-domains-from-caddy)")
	apiURL := fs.String("api-url", "http://localhost:8080",
		"Synapse API base URL")
	token := fs.String("token", "", "admin/access token for the Synapse API")
	dryRun := fs.Bool("dry-run", false,
		"parse + print the import plan without making any API calls")
	defaultRole := fs.String("default-role", "api",
		`fallback role when the parser can't infer ("api" or "dashboard")`)
	var maps stringSliceFlag
	fs.Var(&maps, "map",
		"hostname=deployment-name override (repeatable). Example: --map=api.foo.com=fooprod")

	// Custom error handling: if the user passes a flag we don't
	// know we want to continue to run() (the server has its own env-
	// based config, no flags). flag.ContinueOnError + a manual error
	// check gets us that.
	if err := fs.Parse(args); err != nil {
		// Don't swallow help requests — flag.ErrHelp is what we get
		// when the user passes -h / --help; surface it as "handled
		// with no error" so we exit 0.
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		// Unknown flags here are not necessarily fatal — the server's
		// regular run() flow doesn't use any flags, so anything we
		// don't recognize must be a typo. Surface it.
		return true, err
	}

	if !*adopt {
		// No subcommand — fall through to the server boot path. We
		// still tolerate the user passing extra flags we ignored
		// because future-us might add unrelated subcommands.
		return false, nil
	}

	overrides := map[string]string{}
	for _, raw := range maps {
		k, v, ok := strings.Cut(raw, "=")
		if !ok || k == "" || v == "" {
			return true, fmt.Errorf("--map expects host=name (got %q)", raw)
		}
		overrides[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}

	flags := adoptDomainsFlags{
		Caddyfile:   *caddyfile,
		APIURL:      *apiURL,
		Token:       *token,
		DryRun:      *dryRun,
		DefaultRole: *defaultRole,
		Overrides:   overrides,
	}
	return true, adoptDomainsRun(flags, stdout)
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

	// Proxy resolver — built up-front so the domains handler can
	// invalidate cache entries when an active row gets added /
	// deleted / status-flipped. Always created (even when neither
	// proxy mode is enabled) so the api package gets a working
	// invalidator; the resolver itself only does work if some path
	// down below actually invokes it.
	proxyResolver := &proxy.Resolver{
		DB:            pool,
		UseNetworkDNS: cfg.HealthcheckViaNetwork,
		CacheTTL:      30 * time.Second,
		DashboardAddr: cfg.DashboardAddr,
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
		BaseDomain:            cfg.BaseDomain,
		HA: api.HAConfig{
			Enabled:             cfg.HAEnabled,
			BackendPostgresURL:  cfg.BackendPostgresURL,
			BackendS3Endpoint:   cfg.BackendS3Endpoint,
			BackendS3Region:     cfg.BackendS3Region,
			BackendS3AccessKey:  cfg.BackendS3AccessKey,
			BackendS3SecretKey:  cfg.BackendS3SecretKey,
			BackendBucketPrefix: cfg.BackendS3BucketPrefix,
		},
		Crypto:        secretBox,
		UpdaterSocket: cfg.UpdaterSocket,
		GitHubRepo:    cfg.GitHubRepo,
		PublicIP:      cfg.PublicIP,
		DomainCache:   proxyResolver,
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

	// Top-level routing decision tree:
	//
	//   1. If BaseDomain is set AND r.Host matches `<sub>.<base>` →
	//      route to the proxy (wildcard subdomain mode, v1.0+).
	//   2. Else if r.Host matches an active deployment_domains row →
	//      route to the proxy (per-deployment custom domain, v1.1+).
	//   3. Else if path starts with /d/ → route to the proxy (legacy
	//      path-based mode, v0.2+, controlled by SYNAPSE_PROXY_ENABLED).
	//   4. Else → route to the API.
	//
	// Mounting via http.NewServeMux works for #3+#4 but not #1/#2 (mux
	// doesn't dispatch on Host). When EITHER host-based mode is on we
	// wrap the mux in a Host-checking shim. The custom-domain branch
	// is always-on by virtue of operators being able to register
	// rows; we still honour ProxyEnabled to gate the path mode for
	// installs that want host routing only.
	var topHandler http.Handler = handler
	hostMode := cfg.BaseDomain != "" // wildcard mode flag
	if cfg.ProxyEnabled || hostMode {
		proxyH := proxy.Handler(proxyResolver, logger, cfg.BaseDomain)

		mux := http.NewServeMux()
		mux.Handle("/d/", proxyH)
		mux.Handle("/", handler)
		topHandler = mux

		// Host-based dispatch shim. Wraps the mux so the proxy
		// handler sees host-style requests (no /d/ prefix). The shim
		// runs unconditionally now — even without BaseDomain — so a
		// custom domain registered via the API immediately starts
		// routing without an operator restart.
		suffix := ""
		baseLower := ""
		if hostMode {
			suffix = "." + strings.ToLower(cfg.BaseDomain)
			baseLower = strings.ToLower(cfg.BaseDomain)
		}
		inner := topHandler
		topHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := strings.ToLower(r.Host)
			if i := strings.IndexByte(host, ':'); i >= 0 {
				host = host[:i]
			}
			// Wildcard subdomain match — keep the existing behaviour.
			if hostMode && strings.HasSuffix(host, suffix) && host != baseLower {
				proxyH.ServeHTTP(w, r)
				return
			}
			// Custom-domain match — cheap cached lookup. Skip the
			// special-cased internal hosts (synapse-api, localhost,
			// 127.x.x.x) so compose-network calls don't accidentally
			// trip a DB query on every request.
			if host != "" && host != baseLower &&
				!strings.HasPrefix(host, "127.") &&
				host != "localhost" && host != "synapse-api" {
				if _, _, derr := proxyResolver.ResolveDomain(r.Context(), host); derr == nil {
					proxyH.ServeHTTP(w, r)
					return
				}
			}
			inner.ServeHTTP(w, r)
		})
		if hostMode {
			logger.Info("custom domains enabled (wildcard)", "base_domain", cfg.BaseDomain)
		}
		logger.Info("custom domains enabled (per-deployment)")
		if cfg.ProxyEnabled {
			logger.Info("reverse proxy enabled", "mount", "/d/")
		}
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
