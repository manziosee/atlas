// Command atlas-node runs a single Atlas cluster member.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/manziosee/atlas/internal/api"
	"github.com/manziosee/atlas/internal/config"
	"github.com/manziosee/atlas/internal/dnsdiscovery"
	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/logging"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/transport"
	"github.com/manziosee/atlas/internal/version"
)

const shutdownTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "atlas-node:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log, logLevel, err := logging.NewDynamic(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	log = log.With("node", cfg.NodeID, "region", cfg.Region)
	metrics.BuildInfo.WithLabelValues(version.Version, version.Commit).Set(1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	registry, err := storage.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { _ = registry.Close() }()

	// --- gossip transport + membership ---------------------------------
	keyring, err := security.NewKeyring(cfg.GossipKeys...)
	if err != nil {
		return err
	}
	tr, err := transport.Listen(cfg.GossipBindAddr, keyring, log)
	if err != nil {
		return err
	}
	defer func() { _ = tr.Close() }()

	advertise, err := advertiseAddr(cfg)
	if err != nil {
		return err
	}
	ml := membership.New(membership.Config{
		NodeID:           cfg.NodeID,
		Region:           cfg.Region,
		AdvertiseAddr:    advertise,
		ProbeInterval:    cfg.ProbeInterval,
		ProbeTimeout:     cfg.ProbeTimeout,
		SuspicionTimeout: cfg.SuspicionTimeout,
		GossipInterval:   cfg.GossipInterval,
		Fanout:           cfg.GossipFanout,
		ReapAfter:        cfg.DeadNodeReap,
		MaxClockSkew:     cfg.MaxClockSkew,
		AllowedNodeIDs:   cfg.AllowedNodeIDs,
		Services:         registry,
	}, tr, log)
	var dnsServer *dnsdiscovery.Server
	if cfg.DNSAddr != "" {
		dnsServer, err = dnsdiscovery.New(registry, cfg.DNSZone, cfg.DNSTTL)
		if err != nil {
			return err
		}
	}

	broker := events.NewBroker(4096)
	var wg sync.WaitGroup
	wg.Go(func() { publishEvents(ctx, ml, registry, broker) })
	wg.Go(func() { ml.Run(ctx) })
	wg.Go(func() { joinLoop(ctx, ml, cfg.Seeds, log) })
	wg.Go(func() {
		runHealthChecks(ctx, registry, cfg.NodeID, cfg.HealthCheckInterval, cfg.HealthCheckTimeout, log)
	})
	wg.Go(func() { expireTTLServices(ctx, registry, cfg.NodeID, log) })
	wg.Go(func() { reapOrphanedServices(ctx, registry, ml, cfg.OrphanServiceReap, log) })
	// --- HTTP API ------------------------------------------------------
	limiter := security.NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst, cfg.TrustedProxies...)
	wg.Go(func() { limiter.Run(ctx) })
	auth, err := security.NewConfiguredTokenAuthenticator(cfg.APITokens, cfg.APICredentials)
	if err != nil {
		return fmt.Errorf("configure API credentials: %w", err)
	}

	apiDeps := api.Deps{
		Cluster:     ml,
		Registry:    registry,
		Auth:        auth,
		RateLimiter: limiter,
		Log:         log.With("component", "api"),
		Ready:       func() bool { return ctx.Err() == nil },
		Events:      broker,
		Replay:      flyReplay(cfg.NodeID),
	}
	wg.Go(func() { reloadConfigLoop(ctx, cfg, keyring, auth, limiter, ml, logLevel, log) })
	apiSrv := api.NewServer(cfg.HTTPAddr, api.NewHandler(apiDeps))
	var tlsConfig *tls.Config
	if cfg.TLS.Enabled {
		tlsConfig, err = security.ServerTLSConfig(cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.CAFile, cfg.TLS.RequireClientCert)
		if err != nil {
			return err
		}
		apiSrv.TLSConfig = tlsConfig
	}
	grpcSrv := api.NewGRPCServer(apiDeps, tlsConfig)
	grpcListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen for gRPC API: %w", err)
	}
	defer func() { _ = grpcListener.Close() }()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
	metricsSrv := api.NewServer(cfg.MetricsAddr, metricsMux)
	pprofSrv := &http.Server{
		Addr: cfg.PprofAddr, Handler: newPprofHandler(),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}

	errCh := make(chan error, 5)
	go func() { errCh <- serve(apiSrv, cfg.TLS.Enabled) }()
	go func() { errCh <- serve(metricsSrv, false) }()
	go func() { errCh <- serve(pprofSrv, false) }()
	go func() {
		err := grpcSrv.Serve(grpcListener)
		if errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
		errCh <- err
	}()
	if cfg.DNSAddr != "" {
		wg.Go(func() {
			if err := dnsServer.Serve(ctx, cfg.DNSAddr); err != nil {
				errCh <- err
			}
		})
	}

	log.Info("atlas node started",
		"version", version.String(),
		"env", cfg.Env,
		"gossip_bind", tr.LocalAddr().String(),
		"gossip_advertise", advertise,
		"http", cfg.HTTPAddr,
		"grpc", cfg.GRPCAddr,
		"metrics", cfg.MetricsAddr,
		"pprof", cfg.PprofAddr,
		"dns", cfg.DNSAddr,
		"tls", cfg.TLS.Enabled,
		"mtls", cfg.TLS.Enabled && cfg.TLS.RequireClientCert,
		"seeds", cfg.Seeds,
		"gossip_keys", len(cfg.GossipKeys),
	)

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err = <-errCh:
		log.Error("server failed", "err", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)
	_ = pprofSrv.Shutdown(shutdownCtx)
	grpcStopped := make(chan struct{})
	go func() { grpcSrv.GracefulStop(); close(grpcStopped) }()
	select {
	case <-grpcStopped:
	case <-shutdownCtx.Done():
		grpcSrv.Stop()
	}
	wg.Wait() // membership broadcasts "left" before the transport closes
	log.Info("atlas node stopped")
	return err
}

func newPprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}

func reloadConfigLoop(ctx context.Context, initial *config.Config, keyring *security.Keyring, auth *security.TokenAuthenticator, limiter *security.RateLimiter, members *membership.Memberlist, level *slog.LevelVar, log *slog.Logger) {
	reloads := make(chan os.Signal, 1)
	signal.Notify(reloads, syscall.SIGHUP)
	defer signal.Stop(reloads)
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloads:
			next, err := config.FromEnv()
			if err != nil {
				log.Error("configuration reload rejected", "err", err)
				continue
			}
			if err := applyRuntimeConfig(next, keyring, auth, limiter, members, level); err != nil {
				log.Error("runtime configuration reload rejected", "err", err)
				continue
			}
			if restartRequired(initial, next) {
				log.Warn("some configuration changes require a node restart")
			}
			log.Info("runtime configuration reloaded", "node_admission_entries", len(next.AllowedNodeIDs))
		}
	}
}

func applyRuntimeConfig(next *config.Config, keyring *security.Keyring, auth *security.TokenAuthenticator, limiter *security.RateLimiter, members *membership.Memberlist, level *slog.LevelVar) error {
	var nextLevel slog.Level
	if err := nextLevel.UnmarshalText([]byte(strings.ToUpper(next.LogLevel))); err != nil {
		return err
	}
	if len(next.APITokens) > 0 && len(next.APICredentials) > 0 {
		return errors.New("configure either API tokens or scoped credentials, not both")
	}
	if err := security.ValidateCredentials(next.APICredentials); err != nil {
		return err
	}
	if _, err := security.NewKeyring(next.GossipKeys...); err != nil {
		return err
	}
	if err := keyring.Replace(next.GossipKeys...); err != nil {
		return err
	}
	if err := auth.ReplaceConfigured(next.APITokens, next.APICredentials); err != nil {
		return err
	}
	limiter.Update(next.RateLimitRPS, next.RateLimitBurst, next.TrustedProxies...)
	members.SetAllowedNodeIDs(next.AllowedNodeIDs)
	level.Set(nextLevel)
	return nil
}

func restartRequired(old, next *config.Config) bool {
	return old.Env != next.Env || old.NodeID != next.NodeID || old.Region != next.Region || old.DatabasePath != next.DatabasePath ||
		old.GossipBindAddr != next.GossipBindAddr || old.GossipAdvertiseAddr != next.GossipAdvertiseAddr || !slices.Equal(old.Seeds, next.Seeds) ||
		old.ProbeInterval != next.ProbeInterval || old.ProbeTimeout != next.ProbeTimeout || old.SuspicionTimeout != next.SuspicionTimeout ||
		old.GossipInterval != next.GossipInterval || old.GossipFanout != next.GossipFanout || old.DeadNodeReap != next.DeadNodeReap ||
		old.MaxClockSkew != next.MaxClockSkew || old.HTTPAddr != next.HTTPAddr || old.GRPCAddr != next.GRPCAddr ||
		old.MetricsAddr != next.MetricsAddr || old.PprofAddr != next.PprofAddr || old.DNSAddr != next.DNSAddr || old.DNSZone != next.DNSZone || old.DNSTTL != next.DNSTTL ||
		old.TLS != next.TLS || old.HealthCheckInterval != next.HealthCheckInterval ||
		old.HealthCheckTimeout != next.HealthCheckTimeout || old.OrphanServiceReap != next.OrphanServiceReap || old.LogFormat != next.LogFormat
}

func serve(srv *http.Server, tls bool) error {
	var err error
	if tls {
		err = srv.ListenAndServeTLS("", "") // certificates come from TLSConfig.GetCertificate
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// flyReplay lets a Fly.io machine hand an owner-only request to the machine
// that owns the service. It is enabled only when node IDs are Fly machine IDs
// (the default, since a machine's hostname is its ID).
func flyReplay(nodeID string) func(string) string {
	if machine := os.Getenv("FLY_MACHINE_ID"); machine == "" || machine != nodeID {
		return nil
	}
	return func(owner string) string { return "instance=" + owner }
}

// advertiseAddr resolves the address peers should dial: explicit config,
// then Fly.io's private 6PN address, then interface auto-detection.
func advertiseAddr(cfg *config.Config) (string, error) {
	if cfg.GossipAdvertiseAddr != "" {
		return cfg.GossipAdvertiseAddr, nil
	}
	_, port, _ := net.SplitHostPort(cfg.GossipBindAddr)
	if ip := os.Getenv("FLY_PRIVATE_IP"); ip != "" {
		return net.JoinHostPort(ip, port), nil
	}
	return transport.AdvertiseAddr(cfg.GossipBindAddr)
}

// seedRefreshInterval is how often a connected node pulls state from one
// seed, which heals partitions in which both sides still have live peers.
const seedRefreshInterval = 30 * time.Second

// maxJoinBackoff caps retries while a node has no live peer, so an isolated
// node rejoins soon after its network returns.
const maxJoinBackoff = 10 * time.Second

// joinLoop contacts seeds for the life of the node. Without live peers it
// joins every seed with exponential backoff, so nodes may start in any order
// and an isolated node finds its way back. Once connected it periodically
// pulls from one random seed: after a partition each side has marked the
// other dead, and gossip alone never crosses back.
func joinLoop(ctx context.Context, ml *membership.Memberlist, seeds []string, log *slog.Logger) {
	if len(seeds) == 0 {
		log.Info("no seeds configured; waiting for peers to join us")
		return
	}
	backoff := 500 * time.Millisecond
	connected := false
	for {
		wait := seedRefreshInterval
		// A peer may have joined *us* (e.g. we are the only seed listed).
		if hasLivePeer(ml.Members(), ml.LocalNode().ID) {
			if !connected {
				log.Info("joined cluster", "members", len(ml.Members()))
				connected = true
			}
			backoff = 500 * time.Millisecond
			if err := ml.Rejoin(ctx, seeds); err != nil {
				log.Debug("seed refresh failed", "err", err)
			}
		} else {
			if connected {
				log.Warn("lost contact with every peer; contacting seeds")
				connected = false
			}
			if _, err := ml.Join(ctx, seeds); err != nil {
				log.Warn("join attempt failed", "err", err, "retry_in", backoff)
			}
			wait = backoff
			backoff = min(backoff*2, maxJoinBackoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func hasLivePeer(members []membership.Member, selfID string) bool {
	for _, member := range members {
		if member.ID != selfID && member.State == membership.StateAlive {
			return true
		}
	}
	return false
}

func runHealthChecks(ctx context.Context, registry *storage.Store, nodeID string, interval, timeout time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	check := func() {
		if err := registry.CheckOwnedHealth(ctx, nodeID, timeout); err != nil && ctx.Err() == nil {
			log.Warn("service health checks failed", "err", err)
		}
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}
