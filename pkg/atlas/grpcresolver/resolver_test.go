package grpcresolver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/manziosee/atlas/internal/api"
	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/pkg/atlas"
	"github.com/manziosee/atlas/pkg/atlas/grpcresolver"
)

type oneNode struct{}

func (oneNode) LocalNode() membership.Member {
	return membership.Member{ID: "n1", Addr: "10.0.0.1:7946", State: membership.StateAlive}
}
func (n oneNode) Members() []membership.Member { return []membership.Member{n.LocalNode()} }

func liveClient(t *testing.T) *atlas.Client {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	broker := events.NewBroker(256)
	store.OnChange(func(change storage.Change) {
		if event, ok := events.FromServiceChange(change); ok {
			broker.Publish(event)
		}
	})
	token := "resolver-test-token-resolver-test-token"
	server := httptest.NewServer(api.NewHandler(api.Deps{
		Cluster: oneNode{}, Registry: store, Events: broker,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	client, err := atlas.NewClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// backend is a gRPC server that counts health checks it answers.
func backend(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		calls.Add(1)
		return handler(ctx, req)
	}))
	healthpb.RegisterHealthServer(server, health.NewServer())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), &calls
}

func TestResolverBalancesAcrossInstancesAndFollowsDrains(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	addrA, callsA := backend(t)
	addrB, callsB := backend(t)
	for id, addr := range map[string]string{"echo-a": addrA, "echo-b": addrB} {
		if _, err := client.Register(ctx, atlas.RegisterRequest{ID: id, Name: "echo", Address: addr}); err != nil {
			t.Fatal(err)
		}
	}

	conn, err := grpc.NewClient("atlas:///echo",
		grpc.WithResolvers(grpcresolver.NewBuilder(client)),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	healthClient := healthpb.NewHealthClient(conn)
	check := func() {
		t.Helper()
		if _, err := healthClient.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		check()
	}
	if callsA.Load() == 0 || callsB.Load() == 0 {
		t.Fatalf("calls a=%d b=%d, want round-robin across both", callsA.Load(), callsB.Load())
	}

	if _, err := client.Drain(ctx, "echo-a"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		before := callsA.Load()
		for range 5 {
			check()
		}
		if callsA.Load() == before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drained instance still receives calls")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestResolverRejectsBadTargets(t *testing.T) {
	client := liveClient(t)
	conn, err := grpc.NewClient("atlas:///a/b",
		grpc.WithResolvers(grpcresolver.NewBuilder(client)),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err == nil {
		defer func() { _ = conn.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	}
	if err == nil {
		t.Fatal("a nested target path was accepted")
	}
}
