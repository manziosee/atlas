// Command atlas-agent registers Docker containers in Atlas from their labels.
// Add atlas.name=<service> to a container and the agent registers it, keeps
// it healthy, drains it on stop and removes it when it exits. Applications
// need no changes.
//
// Settings come from the environment:
//
//	ATLAS_ADDR                Atlas node API URL (default http://127.0.0.1:8080)
//	ATLAS_TOKEN               API token with write scope
//	ATLAS_CA_FILE             CA bundle for HTTPS
//	ATLAS_CLIENT_CERT         client certificate for mTLS
//	ATLAS_CLIENT_KEY          client key for mTLS
//	DOCKER_HOST               Docker daemon (default unix:///var/run/docker.sock)
//	ATLAS_AGENT_ID            owner tag for this Docker host (default: hostname)
//	ATLAS_AGENT_HOST_ADDRESS  register <this>:<published port> instead of container IPs
//	ATLAS_AGENT_TTL           TTL for containers without atlas.health (default 15s)
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
	"syscall"
	"time"

	"github.com/manziosee/atlas/internal/dockeragent"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/version"
	"github.com/manziosee/atlas/pkg/atlas"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("atlas-agent", version.String())
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "atlas-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(envOr("ATLAS_LOG_LEVEL", "info"))); err != nil {
		return fmt.Errorf("ATLAS_LOG_LEVEL: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ttl, err := time.ParseDuration(envOr("ATLAS_AGENT_TTL", "15s"))
	if err != nil || ttl < 3*time.Second {
		return errors.New("ATLAS_AGENT_TTL must be a duration of at least 3s")
	}
	agentID := os.Getenv("ATLAS_AGENT_ID")
	if agentID == "" {
		if agentID, err = os.Hostname(); err != nil || agentID == "" {
			agentID = "docker"
		}
	}

	tlsCfg, err := security.ClientTLSConfig(os.Getenv("ATLAS_CA_FILE"), os.Getenv("ATLAS_CLIENT_CERT"), os.Getenv("ATLAS_CLIENT_KEY"))
	if err != nil {
		return err
	}
	client, err := atlas.NewClient(envOr("ATLAS_ADDR", "http://127.0.0.1:8080"), os.Getenv("ATLAS_TOKEN"),
		&http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}})
	if err != nil {
		return err
	}
	docker, err := dockeragent.NewDocker(os.Getenv("DOCKER_HOST"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	agent := dockeragent.New(dockeragent.Config{
		AgentID:     agentID,
		HostAddress: os.Getenv("ATLAS_AGENT_HOST_ADDRESS"),
		TTL:         ttl,
		Logger:      log,
	}, docker, client)
	log.Info("atlas agent started", "version", version.Version, "atlas", envOr("ATLAS_ADDR", "http://127.0.0.1:8080"), "owner_tag", agent.OwnerTag())
	return agent.Run(ctx)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
