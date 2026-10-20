package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("storage: service not found")
var ErrRegistryTooLarge = errors.New("storage: registry exceeds replication limit")

const MaxReplicatedServices = 100_000

type Health string

const (
	HealthUnknown   Health = "unknown"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// Service is a registered service instance. Revision is a per-instance
// monotonic version used when replicas merge updates from gossip.
type Service struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	NodeID      string    `json:"node_id"`
	Address     string    `json:"address"`
	Region      string    `json:"region"`
	Version     string    `json:"version,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	HealthCheck string    `json:"health_check,omitempty"`
	Health      Health    `json:"health"`
	Revision    uint64    `json:"revision"`
	Deleted     bool      `json:"deleted,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
}

func (service Service) Validate() error {
	if service.ID == "" || len(service.ID) > 128 || service.NodeID == "" || len(service.NodeID) > 128 {
		return errors.New("storage: invalid service or node ID")
	}
	if len(service.Name) == 0 || len(service.Name) > 128 {
		return errors.New("storage: invalid service name")
	}
	for i, r := range service.Name {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '.' || r == '_' || r == '-')
		if !valid {
			return errors.New("storage: invalid service name")
		}
	}
	if host, port, err := net.SplitHostPort(service.Address); err != nil || host == "" {
		return errors.New("storage: invalid service address")
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("storage: invalid service port")
	}
	if len(service.Region) > 64 || len(service.Version) > 128 || len(service.Tags) > 16 {
		return errors.New("storage: service metadata exceeds limits")
	}
	for _, tag := range service.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 64 {
			return errors.New("storage: invalid service tag")
		}
	}
	if service.Health != HealthUnknown && service.Health != HealthHealthy && service.Health != HealthUnhealthy {
		return fmt.Errorf("storage: invalid health %q", service.Health)
	}
	if len(service.HealthCheck) > 2048 {
		return errors.New("storage: health-check URL is too long")
	}
	if service.HealthCheck != "" {
		u, err := url.ParseRequestURI(service.HealthCheck)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Host != service.Address {
			return errors.New("storage: invalid health-check URL")
		}
	}
	return nil
}

type Filter struct {
	Name   string
	Region string
	Health Health
}

// Store persists service records in SQLite. One connection serializes local
// writes; WAL lets backup readers inspect the database without blocking them.
type Store struct {
	db          *sql.DB
	path        string
	mu          sync.Mutex
	sampleMu    sync.Mutex
	sampleAfter string
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("storage: database path is required")
	}
	if path != ":memory:" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("storage: resolve database path: %w", err)
		}
		path = absolute
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("storage: create database directory: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: connect SQLite: %w", err)
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("storage: configure SQLite: %w", err)
		}
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("storage: read SQLite schema version: %w", err)
	}
	if version > 1 {
		return fmt.Errorf("storage: database schema version %d is newer than supported version 1", version)
	}
	if version == 1 {
		return nil
	}
	const schema = `
CREATE TABLE IF NOT EXISTS services (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  node_id TEXT NOT NULL,
  address TEXT NOT NULL,
  region TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  tags TEXT NOT NULL DEFAULT '[]',
  health_check TEXT NOT NULL DEFAULT '',
  health TEXT NOT NULL DEFAULT 'unknown',
  revision INTEGER NOT NULL,
  deleted INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  checked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS services_lookup ON services(name, region, health, deleted);
`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: start SQLite migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("storage: migrate SQLite schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		return fmt.Errorf("storage: set SQLite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit SQLite migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Register creates a service instance or advances its revision when the same
// instance ID is registered again by its owning node.
func (s *Store) Register(ctx context.Context, service Service) (Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if service.Health == "" {
		service.Health = HealthUnknown
	}
	if service.Health != HealthUnknown && service.Health != HealthHealthy && service.Health != HealthUnhealthy {
		return Service{}, fmt.Errorf("storage: invalid health %q", service.Health)
	}
	current, err := scanService(s.db.QueryRowContext(ctx, serviceSelect+` WHERE id = ?`, service.ID))
	if err == nil {
		if current.NodeID != service.NodeID {
			return Service{}, errors.New("storage: service belongs to another node")
		}
		service.Revision = current.Revision + 1
	} else if errors.Is(err, sql.ErrNoRows) {
		service.Revision = 1
	} else {
		return Service{}, err
	}
	service.Deleted = false
	service.UpdatedAt = time.Now().UTC()
	if err := service.Validate(); err != nil {
		return Service{}, err
	}
	return s.put(ctx, service)
}

func (s *Store) Delete(ctx context.Context, id, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if service.NodeID != nodeID {
		return errors.New("storage: service belongs to another node")
	}
	service.Revision++
	service.Deleted = true
	service.UpdatedAt = time.Now().UTC()
	_, err = s.put(ctx, service)
	return err
}

func (s *Store) SetHealth(ctx context.Context, id, nodeID string, health Health, checkedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if health != HealthHealthy && health != HealthUnhealthy && health != HealthUnknown {
		return fmt.Errorf("storage: invalid health %q", health)
	}
	service, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if service.NodeID != nodeID || service.Deleted {
		return errors.New("storage: service is not owned by this node")
	}
	if service.Health == health {
		return s.updateCheckedAt(ctx, id, checkedAt)
	}
	service.Health = health
	service.Revision++
	service.UpdatedAt = checkedAt.UTC()
	service.CheckedAt = checkedAt.UTC()
	_, err = s.put(ctx, service)
	return err
}

func (s *Store) Get(ctx context.Context, id string) (Service, error) {
	service, err := scanService(s.db.QueryRowContext(ctx, serviceSelect+` WHERE id = ? AND deleted = 0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, ErrNotFound
	}
	return service, err
}

func (s *Store) List(ctx context.Context, filter Filter) ([]Service, error) {
	query := serviceSelect + ` WHERE deleted = 0`
	args := make([]any, 0, 3)
	if filter.Name != "" {
		query += ` AND name = ?`
		args = append(args, filter.Name)
	}
	if filter.Region != "" {
		query += ` AND region = ?`
		args = append(args, filter.Region)
	}
	if filter.Health != "" {
		query += ` AND health = ?`
		args = append(args, string(filter.Health))
	}
	query += ` ORDER BY name, region, node_id, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	services := make([]Service, 0)
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		services = append(services, service)
	}
	return services, rows.Err()
}

// Owned returns active registrations owned by nodeID that have HTTP checks.
func (s *Store) Owned(ctx context.Context, nodeID string) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx, serviceSelect+` WHERE node_id = ? AND deleted = 0 AND health_check <> '' ORDER BY id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	services := make([]Service, 0)
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		services = append(services, service)
	}
	return services, rows.Err()
}

// Snapshot returns a rotating page of records and deletion tombstones for
// anti-entropy. It uses an indexed cursor rather than sorting the full table
// randomly on every gossip packet.
func (s *Store) Snapshot(ctx context.Context, limit int) ([]Service, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()
	services, err := s.snapshotPage(ctx, ` WHERE id > ? ORDER BY id LIMIT ?`, s.sampleAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: snapshot service records: %w", err)
	}
	if len(services) < limit && s.sampleAfter != "" {
		wrapped, err := s.snapshotPage(ctx, ` WHERE id <= ? ORDER BY id LIMIT ?`, s.sampleAfter, limit-len(services))
		if err != nil {
			return nil, fmt.Errorf("storage: wrap service snapshot: %w", err)
		}
		services = append(services, wrapped...)
	}
	if len(services) > 0 {
		s.sampleAfter = services[len(services)-1].ID
	}
	return services, nil
}

// RegistryDigest returns a stable digest of the replicated registry state.
// CheckedAt is deliberately excluded because it is local probe bookkeeping.
func (s *Store) RegistryDigest(ctx context.Context) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	rows, err := s.db.QueryContext(ctx, serviceSelect+` ORDER BY id`)
	if err != nil {
		return digest, err
	}
	defer rows.Close()
	h := sha256.New()
	var count uint64
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return digest, err
		}
		if count >= MaxReplicatedServices {
			return digest, ErrRegistryTooLarge
		}
		service.CheckedAt = time.Time{}
		encoded, err := json.Marshal(service)
		if err != nil {
			return digest, err
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(encoded)))
		_, _ = h.Write(length[:])
		_, _ = h.Write(encoded)
		count++
	}
	if err := rows.Err(); err != nil {
		return digest, err
	}
	_, _ = h.Write([]byte{'A', 'T', 'R', 'G', 1})
	var countBytes [8]byte
	binary.BigEndian.PutUint64(countBytes[:], count)
	_, _ = h.Write(countBytes[:])
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

// ReplicationSnapshot returns the bounded full registry, including tombstones.
func (s *Store) ReplicationSnapshot(ctx context.Context) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx, serviceSelect+` ORDER BY id LIMIT ?`, MaxReplicatedServices+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	services := make([]Service, 0)
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		service.CheckedAt = time.Time{}
		services = append(services, service)
		if len(services) > MaxReplicatedServices {
			return nil, ErrRegistryTooLarge
		}
	}
	return services, rows.Err()
}

func (s *Store) snapshotPage(ctx context.Context, suffix string, cursor string, limit int) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx, serviceSelect+suffix, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	services := make([]Service, 0, limit)
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		service.CheckedAt = time.Time{}
		services = append(services, service)
	}
	return services, rows.Err()
}

// Merge applies the maximum replicated record under a deterministic total
// order. This makes merge associative, commutative and idempotent.
func (s *Store) Merge(ctx context.Context, incoming Service) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if incoming.ID == "" || incoming.Revision == 0 {
		return errors.New("storage: replicated service must have an id and revision")
	}
	if incoming.UpdatedAt.IsZero() {
		// Older peers may omit the timestamp. Use a stable fallback rather than
		// letting each receiver assign its own wall-clock time.
		incoming.UpdatedAt = time.Unix(0, int64(incoming.Revision)).UTC()
	}
	incoming.CheckedAt = time.Time{}
	if err := incoming.Validate(); err != nil {
		return err
	}
	current, err := scanService(s.db.QueryRowContext(ctx, serviceSelect+` WHERE id = ?`, incoming.ID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && !wins(incoming, current) {
		return nil
	}
	_, err = s.put(ctx, incoming)
	return err
}

func wins(incoming, current Service) bool {
	if incoming.Revision != current.Revision {
		return incoming.Revision > current.Revision
	}
	if !incoming.UpdatedAt.Equal(current.UpdatedAt) {
		return incoming.UpdatedAt.After(current.UpdatedAt)
	}
	if incoming.NodeID != current.NodeID {
		return incoming.NodeID > current.NodeID
	}
	if incoming.Deleted != current.Deleted {
		return incoming.Deleted
	}
	left, _ := json.Marshal(canonicalReplicatedService(incoming))
	right, _ := json.Marshal(canonicalReplicatedService(current))
	return bytes.Compare(left, right) > 0
}

func canonicalReplicatedService(service Service) Service {
	service.UpdatedAt = service.UpdatedAt.UTC().Round(0)
	service.CheckedAt = time.Time{}
	return service
}

func (s *Store) put(ctx context.Context, service Service) (Service, error) {
	tags, err := json.Marshal(service.Tags)
	if err != nil {
		return Service{}, fmt.Errorf("storage: encode tags: %w", err)
	}
	if service.UpdatedAt.IsZero() {
		service.UpdatedAt = time.Now().UTC()
	}
	deleted := 0
	if service.Deleted {
		deleted = 1
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO services (id,name,node_id,address,region,version,tags,health_check,health,revision,deleted,updated_at,checked_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,node_id=excluded.node_id,address=excluded.address,region=excluded.region,
 version=excluded.version,tags=excluded.tags,health_check=excluded.health_check,health=excluded.health,
 revision=excluded.revision,deleted=excluded.deleted,updated_at=excluded.updated_at,checked_at=excluded.checked_at`,
		service.ID, service.Name, service.NodeID, service.Address, service.Region, service.Version,
		string(tags), service.HealthCheck, service.Health, service.Revision, deleted,
		service.UpdatedAt.UnixNano(), timeNanos(service.CheckedAt))
	if err != nil {
		return Service{}, fmt.Errorf("storage: save service: %w", err)
	}
	return service, nil
}

func (s *Store) updateCheckedAt(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE services SET checked_at = ? WHERE id = ?`, timeNanos(at), id)
	return err
}

const serviceSelect = `SELECT id,name,node_id,address,region,version,tags,health_check,health,revision,deleted,updated_at,checked_at FROM services`

type scanner interface{ Scan(...any) error }

func scanService(row scanner) (Service, error) {
	var service Service
	var tags string
	var deleted int
	var updatedAt, checkedAt int64
	if err := row.Scan(&service.ID, &service.Name, &service.NodeID, &service.Address, &service.Region,
		&service.Version, &tags, &service.HealthCheck, &service.Health, &service.Revision,
		&deleted, &updatedAt, &checkedAt); err != nil {
		return Service{}, err
	}
	if err := json.Unmarshal([]byte(tags), &service.Tags); err != nil {
		return Service{}, fmt.Errorf("storage: decode tags: %w", err)
	}
	service.Deleted = deleted != 0
	service.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if checkedAt != 0 {
		service.CheckedAt = time.Unix(0, checkedAt).UTC()
	}
	return service, nil
}

func timeNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
