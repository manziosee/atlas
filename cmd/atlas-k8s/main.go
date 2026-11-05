// Command atlas-k8s registers annotated Kubernetes Services in Atlas.
// Annotate a Service with atlas.io/register: "true" and every ready pod
// behind it becomes an Atlas instance; terminating pods are drained and
// deleted pods removed. See internal/k8ssync and docs/KUBERNETES.md.
//
// Settings come from the environment:
//
//	ATLAS_ADDR                Atlas node API URL (default http://atlas:8080)
//	ATLAS_TOKEN / _FILE       API token with write scope (the file is read at start)
//	ATLAS_CA_FILE             CA bundle for HTTPS to Atlas
//	ATLAS_CLIENT_CERT / _KEY  client certificate for mTLS to Atlas
//	ATLAS_K8S_CLUSTER         name for this cluster's owner tag (default kubernetes)
//	ATLAS_K8S_NAMESPACES      comma-separated namespaces to watch (default all)
//	ATLAS_K8S_CLUSTER_DOMAIN  cluster DNS domain for service mode (default cluster.local)
//	ATLAS_K8S_TTL             TTL for pods without atlas.io/health (default 15s)
//	ATLAS_K8S_API_URL         API server; unset uses the in-cluster service account
//	ATLAS_K8S_TOKEN           bearer token for ATLAS_K8S_API_URL
//	ATLAS_K8S_CA_FILE         CA for ATLAS_K8S_API_URL
//	ATLAS_K8S_HEALTH_ADDR     liveness endpoint (default :8081, "" disables)
//	ATLAS_LOG_LEVEL           debug, info, warn or error (default info)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/manziosee/atlas/internal/k8ssync"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/version"
	"github.com/manziosee/atlas/pkg/atlas"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("atlas-k8s", version.String())
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "atlas-k8s:", err)
		os.Exit(1)
	}
}

func run() error {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(envOr("ATLAS_LOG_LEVEL", "info"))); err != nil {
		return fmt.Errorf("ATLAS_LOG_LEVEL: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ttl, err := time.ParseDuration(envOr("ATLAS_K8S_TTL", "15s"))
	if err != nil || ttl < 3*time.Second {
		return errors.New("ATLAS_K8S_TTL must be a duration of at least 3s")
	}
	token := os.Getenv("ATLAS_TOKEN")
	if file := os.Getenv("ATLAS_TOKEN_FILE"); file != "" {
		raw, err := os.ReadFile(file) // #nosec G304 G703 -- operator-supplied secret path.
		if err != nil {
			return fmt.Errorf("ATLAS_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		return errors.New("set ATLAS_TOKEN or ATLAS_TOKEN_FILE to a token with the write scope")
	}

	tlsCfg, err := security.ClientTLSConfig(os.Getenv("ATLAS_CA_FILE"), os.Getenv("ATLAS_CLIENT_CERT"), os.Getenv("ATLAS_CLIENT_KEY"))
	if err != nil {
		return err
	}
	addr := envOr("ATLAS_ADDR", "http://atlas:8080")
	client, err := atlas.NewClient(addr, token, &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}})
	if err != nil {
		return err
	}

	kubeCfg, err := k8ssync.InCluster(k8ssync.KubeConfig{
		URL: os.Getenv("ATLAS_K8S_API_URL"), Token: os.Getenv("ATLAS_K8S_TOKEN"), CAFile: os.Getenv("ATLAS_K8S_CA_FILE"),
	})
	if err != nil {
		return err
	}
	kube, err := k8ssync.NewKube(kubeCfg)
	if err != nil {
		return err
	}

	var namespaces []string
	for _, ns := range strings.Split(os.Getenv("ATLAS_K8S_NAMESPACES"), ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			namespaces = append(namespaces, ns)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if healthAddr := envOr("ATLAS_K8S_HEALTH_ADDR", ":8081"); healthAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		server := &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("health endpoint", "err", err)
			}
		}()
		go func() {
			<-ctx.Done()
			_ = server.Close()
		}()
	}

	controller := k8ssync.New(k8ssync.Config{
		Cluster:       envOr("ATLAS_K8S_CLUSTER", "kubernetes"),
		Namespaces:    namespaces,
		ClusterDomain: envOr("ATLAS_K8S_CLUSTER_DOMAIN", "cluster.local"),
		TTL:           ttl,
		Logger:        log,
	}, kube, client)
	log.Info("atlas kubernetes controller started", "version", version.Version, "atlas", addr, "kubernetes", kubeCfg.URL,
		"owner_tag", controller.OwnerTag(), "namespaces", namespaces)
	return controller.Run(ctx)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
