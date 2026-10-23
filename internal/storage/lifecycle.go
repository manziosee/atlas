package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TTLDeregisterFactor is how many missed TTL periods remove a service. After
// one period without a heartbeat it is unhealthy; after this many it is gone.
const TTLDeregisterFactor = 3

// MaxPageSize bounds one ListPage call.
const MaxPageSize = 1000

var ErrInvalidCursor = errors.New("storage: invalid page cursor")

// Page is one ordered slice of a filtered listing. NextCursor is empty on the
// last page.
type Page struct {
	Services   []Service
	NextCursor string
}

// pageKey is the listing sort key; cursors encode the last row's key.
type pageKey struct {
	Name   string `json:"n"`
	Region string `json:"r"`
	NodeID string `json:"o"`
	ID     string `json:"i"`
}

// ListPage returns up to limit services after cursor in the same order as
// List. Keyset pagination stays stable while records are added or removed.
func (s *Store) ListPage(ctx context.Context, filter Filter, cursor string, limit int) (Page, error) {
	if limit < 1 || limit > MaxPageSize {
		return Page{}, fmt.Errorf("storage: page size must be between 1 and %d", MaxPageSize)
	}
	query, args := filterQuery(filter)
	if cursor != "" {
		key, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		query += ` AND (name, region, node_id, id) > (?, ?, ?, ?)`
		args = append(args, key.Name, key.Region, key.NodeID, key.ID)
	}
	query += ` ORDER BY name, region, node_id, id LIMIT ?`
	args = append(args, limit+1)
	services, err := s.query(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	page := Page{Services: services}
	if len(services) > limit {
		page.Services = services[:limit]
		last := page.Services[limit-1]
		page.NextCursor = encodeCursor(pageKey{Name: last.Name, Region: last.Region, NodeID: last.NodeID, ID: last.ID})
	}
	return page, nil
}

func encodeCursor(key pageKey) string {
	encoded, _ := json.Marshal(key)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(cursor string) (pageKey, error) {
	var key pageKey
	if len(cursor) > 1024 {
		return key, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(raw, &key) != nil || key.ID == "" {
		return pageKey{}, ErrInvalidCursor
	}
	return key, nil
}

// Heartbeat records a TTL heartbeat for a service owned by nodeID and marks it
// healthy. Heartbeat timestamps are local to the owner and not replicated; only
// the resulting health transition is.
func (s *Store) Heartbeat(ctx context.Context, id, nodeID string, at time.Time) (Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, err := s.Get(ctx, id)
	if err != nil {
		return Service{}, err
	}
	if service.NodeID != nodeID {
		return Service{}, &NotOwnerError{Owner: service.NodeID}
	}
	if service.TTLSeconds == 0 {
		return Service{}, ErrNoTTL
	}
	at = at.UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE services SET heartbeat_at = ? WHERE id = ?`, at.UnixNano(), id); err != nil {
		return Service{}, fmt.Errorf("storage: record heartbeat: %w", err)
	}
	service.HeartbeatAt = at
	if service.Health == HealthHealthy {
		return service, nil
	}
	before := service
	service.Health = HealthHealthy
	service.Revision++
	service.UpdatedAt = at
	service.CheckedAt = at
	return s.write(ctx, &before, service)
}

// ExpireTTL marks TTL services owned by nodeID unhealthy after one missed TTL
// period and deletes them after TTLDeregisterFactor periods.
func (s *Store) ExpireTTL(ctx context.Context, nodeID string, now time.Time) (unhealthy, deregistered int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	services, err := s.query(ctx, serviceSelect+` WHERE node_id = ? AND deleted = 0 AND ttl_seconds > 0 ORDER BY id`, nodeID)
	if err != nil {
		return 0, 0, fmt.Errorf("storage: list TTL services: %w", err)
	}
	now = now.UTC()
	for _, service := range services {
		if service.HeartbeatAt.IsZero() {
			// Learned through replication (for example after the owner lost its
			// disk): start the clock now rather than expiring it immediately.
			if _, err := s.db.ExecContext(ctx, `UPDATE services SET heartbeat_at = ? WHERE id = ?`, now.UnixNano(), service.ID); err != nil {
				return unhealthy, deregistered, fmt.Errorf("storage: start TTL clock: %w", err)
			}
			continue
		}
		ttl := time.Duration(service.TTLSeconds) * time.Second
		silent := now.Sub(service.HeartbeatAt)
		switch {
		case silent > TTLDeregisterFactor*ttl:
			if _, err := s.tombstone(ctx, service); err != nil {
				return unhealthy, deregistered, err
			}
			deregistered++
		case silent > ttl && service.Health != HealthUnhealthy:
			before := service
			service.Health = HealthUnhealthy
			service.Revision++
			service.UpdatedAt = now
			service.CheckedAt = now
			if _, err := s.write(ctx, &before, service); err != nil {
				return unhealthy, deregistered, err
			}
			unhealthy++
		}
	}
	return unhealthy, deregistered, nil
}

// Tombstone deletes a service regardless of its owner. Use it for operator
// removal and for services whose owner has left the cluster.
func (s *Store) Tombstone(ctx context.Context, id string) (Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, err := s.Get(ctx, id)
	if err != nil {
		return Service{}, err
	}
	return s.tombstone(ctx, service)
}

// TombstoneOwnedBy deletes every active service owned by nodeID.
func (s *Store) TombstoneOwnedBy(ctx context.Context, nodeID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	services, err := s.query(ctx, serviceSelect+` WHERE node_id = ? AND deleted = 0 ORDER BY id`, nodeID)
	if err != nil {
		return 0, err
	}
	for i, service := range services {
		if _, err := s.tombstone(ctx, service); err != nil {
			return i, err
		}
	}
	return len(services), nil
}

// OwnerNodes returns the distinct owners of active services.
func (s *Store) OwnerNodes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT node_id FROM services WHERE deleted = 0 ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}
