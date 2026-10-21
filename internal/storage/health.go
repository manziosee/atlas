package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// CheckOwnedHealth probes HTTP checks registered by one node. Only the owning
// node publishes health changes, so replicas do not overwrite one another's
// observations from different network regions.
func (s *Store) CheckOwnedHealth(ctx context.Context, nodeID string, timeout time.Duration) error {
	services, err := s.Owned(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("storage: list health checks: %w", err)
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var errs []error
	for _, service := range services {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, service.HealthCheck, nil)
		if err != nil {
			cancel()
			errs = append(errs, fmt.Errorf("service %s: build health request: %w", service.ID, err))
			continue
		}
		response, requestErr := client.Do(req)
		cancel()
		health := HealthUnhealthy
		if requestErr == nil {
			health = HealthHealthy
			if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
				health = HealthUnhealthy
			}
			_ = response.Body.Close()
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.SetHealth(ctx, service.ID, nodeID, health, time.Now()); err != nil && !errors.Is(err, ErrNotFound) {
			errs = append(errs, fmt.Errorf("service %s: persist health: %w", service.ID, err))
		}
	}
	return errors.Join(errs...)
}
