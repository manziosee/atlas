package storage

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func healthOf(ctx context.Context, t *testing.T, store *Store, id string) Health {
	t.Helper()
	service, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return service.Health
}

func TestHealthThresholdsPreventFlapping(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	service, err := store.Register(ctx, Service{
		ID: "web-1", Name: "web", NodeID: "node-a", Address: server.Listener.Addr().String(),
		HealthCheck: server.URL + "/healthz",
	})
	if err != nil {
		t.Fatal(err)
	}
	other := NewHealthChecker(store, "node-b", time.Second, time.Second)
	if err := other.CheckDue(ctx, time.Now()); err != nil || healthOf(ctx, t, store, service.ID) != HealthUnknown {
		t.Fatalf("a non-owner checked the service: %v", err)
	}

	checker := NewHealthChecker(store, "node-a", time.Second, time.Second)
	now := time.Now()
	step := func(want Health, why string) {
		t.Helper()
		if err := checker.CheckDue(ctx, now); err != nil {
			t.Fatal(err)
		}
		if got := healthOf(ctx, t, store, service.ID); got != want {
			t.Fatalf("%s: health = %s, want %s", why, got, want)
		}
		now = now.Add(time.Second)
	}
	step(HealthHealthy, "first success decides")
	failing.Store(true)
	step(HealthHealthy, "one failure is tolerated")
	step(HealthHealthy, "two failures are tolerated")
	step(HealthUnhealthy, "third consecutive failure")
	failing.Store(false)
	step(HealthUnhealthy, "one success is not enough to recover")
	step(HealthHealthy, "second consecutive success recovers")

	if _, _, err := store.Update(ctx, service.ID, "node-a", 0, func(s *Service) error { s.UnhealthyThreshold = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	step(HealthUnhealthy, "a per-service threshold of 1 reacts at once")
}

func TestHealthCheckIntervalPerService(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	for _, s := range []Service{
		{ID: "fast", Name: "a", NodeID: "n", Address: "10.0.0.1:80", HealthCheck: "tcp://10.0.0.1:80"},
		{ID: "slow", Name: "b", NodeID: "n", Address: "10.0.0.2:80", HealthCheck: "tcp://10.0.0.2:80", CheckIntervalSeconds: 10},
	} {
		if _, err := store.Register(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	checker := NewHealthChecker(store, "n", time.Second, time.Second)
	probed := map[string]int{}
	checker.probe = func(_ context.Context, check string) error {
		probed[check]++
		return nil
	}
	start := time.Now()
	for i := range 11 {
		if err := checker.CheckDue(ctx, start.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if probed["tcp://10.0.0.1:80"] != 11 || probed["tcp://10.0.0.2:80"] != 2 {
		t.Fatalf("probes = %v, want 11 for the default interval and 2 for a 10s interval", probed)
	}
}

func TestTCPAndGRPCProbes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	checker := NewHealthChecker(nil, "n", time.Second, time.Second)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := checker.run(ctx, "tcp://"+addr); err != nil {
		t.Fatalf("tcp probe of an open port: %v", err)
	}
	_ = listener.Close()
	if err := checker.run(ctx, "tcp://"+addr); err == nil {
		t.Fatal("tcp probe of a closed port passed")
	}

	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	status := health.NewServer()
	healthpb.RegisterHealthServer(server, status)
	go func() { _ = server.Serve(grpcListener) }()
	defer server.Stop()
	target := "grpc://" + grpcListener.Addr().String()
	if err := checker.run(ctx, target); err != nil {
		t.Fatalf("grpc probe of a serving server: %v", err)
	}
	status.SetServingStatus("payments.v1.API", healthpb.HealthCheckResponse_NOT_SERVING)
	if err := checker.run(ctx, target+"/payments.v1.API"); err == nil {
		t.Fatal("grpc probe of a NOT_SERVING service passed")
	}
	status.SetServingStatus("payments.v1.API", healthpb.HealthCheckResponse_SERVING)
	if err := checker.run(ctx, target+"/payments.v1.API"); err != nil {
		t.Fatalf("grpc probe of a SERVING service: %v", err)
	}
}

func TestValidateHealthCheckAndRoutable(t *testing.T) {
	const addr = "10.0.0.5:9000"
	for _, check := range []string{"http://" + addr + "/health", "https://" + addr, "tcp://" + addr, "grpc://" + addr, "grpcs://" + addr + "/pkg.Svc"} {
		if err := ValidateHealthCheck(check, addr); err != nil {
			t.Errorf("%s rejected: %v", check, err)
		}
	}
	for _, check := range []string{"udp://" + addr, "tcp://" + addr + "/x", "grpc://" + addr + "/a/b", "http://10.0.0.6:9000/", "http://u:p@" + addr, "http://" + addr + "/?q=1"} {
		if err := ValidateHealthCheck(check, addr); err == nil {
			t.Errorf("%s accepted", check)
		}
	}
	thresholdWithoutCheck := Service{ID: "x", Name: "x", NodeID: "n", Address: addr, Health: HealthUnknown, UnhealthyThreshold: 2}
	if err := thresholdWithoutCheck.Validate(); err == nil {
		t.Error("threshold without a health check accepted")
	}

	base := Service{Health: HealthHealthy}
	drained := base
	drained.Draining = true
	sick := base
	sick.Health = HealthUnhealthy
	if !base.Routable() || drained.Routable() || sick.Routable() || !(Service{Health: HealthUnknown}).Routable() {
		t.Fatal("Routable() disagrees with drain and health rules")
	}
}

func TestDrainingIsAReplicatedSpecChange(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	service, err := store.Register(ctx, Service{ID: "d", Name: "d", NodeID: "n", Address: "10.0.0.1:1", Health: HealthHealthy})
	if err != nil {
		t.Fatal(err)
	}
	drained, outcome, err := store.Update(ctx, service.ID, "n", 0, func(s *Service) error { s.Draining = true; return nil })
	if err != nil || outcome != Updated || !drained.Draining || drained.Health != HealthHealthy {
		t.Fatalf("drain = %+v %v %v, want draining with health kept", drained, outcome, err)
	}
	snapshot, err := store.ReplicationSnapshot(ctx)
	if err != nil || len(snapshot) != 1 || !snapshot[0].Draining {
		t.Fatalf("replicated snapshot = %+v %v", snapshot, err)
	}
	if err := store.Merge(ctx, Service{ID: "other", Name: "o", NodeID: "m", Address: "10.0.0.2:1", Health: HealthHealthy, Revision: 1, Draining: true, CheckIntervalSeconds: 0}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, "other"); err != nil || !got.Draining {
		t.Fatalf("merged draining record = %+v %v", got, err)
	}
	if _, err := store.Heartbeat(ctx, service.ID, "n", time.Now()); !errors.Is(err, ErrNoTTL) {
		t.Fatalf("unexpected heartbeat result: %v", err)
	}
}
