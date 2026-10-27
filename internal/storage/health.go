package storage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// maxConcurrentChecks bounds probes in flight so one slow service cannot
// delay the others and a large registry cannot exhaust sockets.
const maxConcurrentChecks = 16

// HealthChecker probes the checks of services owned by one node. Only the
// owning node publishes health changes, so replicas do not overwrite one
// another's observations from different network regions.
//
// A service's first result decides its health. After that, health changes
// only after UnhealthyThreshold consecutive failures (default 3) or
// HealthyThreshold consecutive successes (default 2), so one slow response
// does not flap it.
type HealthChecker struct {
	store           *Store
	nodeID          string
	defaultInterval time.Duration
	timeout         time.Duration
	http            *http.Client
	probe           func(ctx context.Context, check string) error

	mu    sync.Mutex
	state map[string]*checkState
}

type checkState struct {
	lastRun   time.Time
	successes int
	failures  int
	running   bool
}

// NewHealthChecker checks services every defaultInterval unless a service
// sets its own interval. Each probe is bounded by timeout.
func NewHealthChecker(store *Store, nodeID string, defaultInterval, timeout time.Duration) *HealthChecker {
	c := &HealthChecker{
		store: store, nodeID: nodeID, defaultInterval: defaultInterval, timeout: timeout,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		state: make(map[string]*checkState),
	}
	c.probe = c.run
	return c
}

// Run checks due services every second until ctx ends.
func (c *HealthChecker) Run(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := c.CheckDue(ctx, time.Now()); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// CheckDue probes every owned service whose interval has elapsed and waits
// for the probes to finish.
func (c *HealthChecker) CheckDue(ctx context.Context, now time.Time) error {
	services, err := c.store.Owned(ctx, c.nodeID)
	if err != nil {
		return fmt.Errorf("storage: list health checks: %w", err)
	}
	due := c.due(services, now)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, maxConcurrentChecks)
	)
	for _, service := range due {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := c.checkOne(ctx, service); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// due selects services whose interval elapsed, marks them running and drops
// state for services this node no longer owns.
func (c *HealthChecker) due(services []Service, now time.Time) []Service {
	c.mu.Lock()
	defer c.mu.Unlock()
	owned := make(map[string]bool, len(services))
	var due []Service
	for _, service := range services {
		owned[service.ID] = true
		state := c.state[service.ID]
		if state == nil {
			state = &checkState{}
			c.state[service.ID] = state
		}
		interval := c.defaultInterval
		if service.CheckIntervalSeconds > 0 {
			interval = time.Duration(service.CheckIntervalSeconds) * time.Second
		}
		if state.running || (!state.lastRun.IsZero() && now.Sub(state.lastRun) < interval) {
			continue
		}
		state.running, state.lastRun = true, now
		due = append(due, service)
	}
	for id := range c.state {
		if !owned[id] {
			delete(c.state, id)
		}
	}
	return due
}

func (c *HealthChecker) checkOne(ctx context.Context, service Service) error {
	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	probeErr := c.probe(checkCtx, service.HealthCheck)
	cancel()
	if err := ctx.Err(); err != nil {
		c.finish(service.ID)
		return err
	}
	next := c.record(service, probeErr == nil)
	err := c.store.SetHealth(ctx, service.ID, c.nodeID, next, time.Now())
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotOwner) {
		return fmt.Errorf("service %s: persist health: %w", service.ID, err)
	}
	return nil
}

// record counts the result and returns the health to store.
func (c *HealthChecker) record(service Service, passed bool) Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state[service.ID]
	if state == nil {
		state = &checkState{}
		c.state[service.ID] = state
	}
	state.running = false
	if passed {
		state.successes, state.failures = state.successes+1, 0
	} else {
		state.successes, state.failures = 0, state.failures+1
	}
	unhealthyAfter, healthyAfter := service.UnhealthyThreshold, service.HealthyThreshold
	if unhealthyAfter == 0 {
		unhealthyAfter = DefaultUnhealthyThreshold
	}
	if healthyAfter == 0 {
		healthyAfter = DefaultHealthyThreshold
	}
	switch {
	case service.Health == HealthUnknown && passed:
		return HealthHealthy
	case service.Health == HealthUnknown:
		return HealthUnhealthy
	case service.Health == HealthHealthy && state.failures >= unhealthyAfter:
		return HealthUnhealthy
	case service.Health == HealthUnhealthy && state.successes >= healthyAfter:
		return HealthHealthy
	default:
		return service.Health
	}
}

func (c *HealthChecker) finish(id string) {
	c.mu.Lock()
	if state := c.state[id]; state != nil {
		state.running = false
	}
	c.mu.Unlock()
}

// run executes one probe according to the check URL's scheme.
func (c *HealthChecker) run(ctx context.Context, check string) error {
	u, err := url.Parse(check)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, check, nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	case "tcp":
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return err
		}
		return conn.Close()
	case "grpc", "grpcs":
		return probeGRPC(ctx, u)
	default:
		return fmt.Errorf("unsupported health check scheme %q", u.Scheme)
	}
}

// probeGRPC calls the standard grpc.health.v1 Check RPC. grpcs:// verifies
// the server certificate against the system roots.
func probeGRPC(ctx context.Context, u *url.URL) error {
	creds := insecure.NewCredentials()
	if u.Scheme == "grpcs" {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
	}
	conn, err := grpc.NewClient(u.Host, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: strings.TrimPrefix(u.Path, "/")})
	if err != nil {
		return err
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("grpc health status %s", resp.GetStatus())
	}
	return nil
}
