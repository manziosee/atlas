// Command atlas-node runs a single Atlas cluster member.
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
	"sync"
	"syscall"
	"time"

	"github.com/manziosee/atlas/internal/api"
	"github.com/manziosee/atlas/internal/config"
	"github.com/manziosee/atlas/internal/logging"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/security"
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
	log, err := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	log = log.With("node", cfg.NodeID, "region", cfg.Region)
	metrics.BuildInfo.WithLabelValues(version.Version, version.Commit).Set(1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	}, tr, log)

	var wg sync.WaitGroup
	wg.Go(func() { ml.Run(ctx) })
	wg.Go(func() { joinLoop(ctx, ml, cfg.Seeds, log) })

	// --- HTTP API ------------------------------------------------------
	limiter := security.NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst)
	wg.Go(func() { limiter.Run(ctx) })

	apiSrv := api.NewServer(cfg.HTTPAddr, api.NewHandler(api.Deps{
		Cluster:     ml,
		Auth:        security.NewTokenAuthenticator(cfg.APITokens),
		RateLimiter: limiter,
		Log:         log.With("component", "api"),
		Ready:       func() bool { return ctx.Err() == nil },
	}))
	if cfg.TLS.Enabled {
		apiSrv.TLSConfig, err = security.ServerTLSConfig(cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.CAFile, cfg.TLS.RequireClientCert)
		if err != nil {
			return err
		}
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
	metricsSrv := api.NewServer(cfg.MetricsAddr, metricsMux)

	errCh := make(chan error, 2)
	go func() { errCh <- serve(apiSrv, cfg.TLS.Enabled) }()
	go func() { errCh <- serve(metricsSrv, false) }()

	log.Info("atlas node started",
		"version", version.String(),
		"env", cfg.Env,
		"gossip_bind", tr.LocalAddr().String(),
		"gossip_advertise", advertise,
		"http", cfg.HTTPAddr,
		"metrics", cfg.MetricsAddr,
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
	wg.Wait() // membership broadcasts "left" before the transport closes
	log.Info("atlas node stopped")
	return err
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

// joinLoop keeps contacting seeds with exponential backoff until at least
// one peer is known, so nodes may start in any order.
func joinLoop(ctx context.Context, ml *membership.Memberlist, seeds []string, log *slog.Logger) {
	if len(seeds) == 0 {
		log.Info("no seeds configured; waiting for peers to join us")
		return
	}
	backoff := 500 * time.Millisecond
	for {
		// A peer may have joined *us* (e.g. we are the only seed listed).
		if n := len(ml.Members()); n > 1 {
			log.Info("joined cluster", "members", n)
			return
		}
		if _, err := ml.Join(ctx, seeds); err != nil {
			log.Warn("join attempt failed", "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
