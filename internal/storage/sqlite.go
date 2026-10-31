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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("storage: service not found")
var ErrRegistryTooLarge = errors.New("storage: registry exceeds replication limit")
var ErrNotOwner = errors.New("storage: service belongs to another node")
var ErrRevisionMismatch = errors.New("storage: service revision does not match")
var ErrNoTTL = errors.New("storage: service has no TTL")

// NotOwnerError reports the node that owns a service. It matches ErrNotOwner.
type NotOwnerError struct{ Owner string }

func (e *NotOwnerError) Error() string        { return "storage: service belongs to node " + e.Owner }
func (e *NotOwnerError) Is(target error) bool { return target == ErrNotOwner }

const MaxReplicatedServices = 100_000

// MaxTTLSeconds bounds heartbeat TTLs to one day.
const MaxTTLSeconds = 86_400

// Health-check tuning bounds. Zero values select the node defaults.
const (
	MaxCheckIntervalSeconds   = 3600
	MaxCheckThreshold         = 10
	DefaultUnhealthyThreshold = 3
	DefaultHealthyThreshold   = 2
)

type Health string

const (
	HealthUnknown   Health = "unknown"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// Service is a registered service instance. Revision is a per-instance
// monotonic version used when replicas merge updates from gossip.
// CheckedAt and HeartbeatAt are the owner's local bookkeeping; they are never
// replicated or included in the registry digest.
type Service struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NodeID      string   `json:"node_id"`
	Address     string   `json:"address"`
	Region      string   `json:"region"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
	Health      Health   `json:"health"`
	Revision    uint64   `json:"revision"`
	Deleted     bool     `json:"deleted,omitempty"`
	TTLSeconds  int64    `json:"ttl_seconds,omitempty"`
	// CheckIntervalSeconds overrides ATLAS_HEALTH_CHECK_INTERVAL for this
	// service; the thresholds are consecutive results needed to change state.
	CheckIntervalSeconds int64 `json:"check_interval_seconds,omitempty"`
	UnhealthyThreshold   int   `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold     int   `json:"healthy_threshold,omitempty"`
	// Draining keeps the record but removes it from lookups, DNS and client
	// load balancing, so a deploy can stop sending traffic before shutdown.
	Draining    bool      `json:"draining,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	HeartbeatAt time.Time `json:"heartbeat_at,omitzero"`
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
	if service.TTLSeconds < 0 || service.TTLSeconds > MaxTTLSeconds {
		return errors.New("storage: TTL must be between 1 second and 1 day")
	}
	if service.TTLSeconds > 0 && service.HealthCheck != "" {
		return errors.New("storage: a service uses either an HTTP health check or a TTL heartbeat, not both")
	}
	if len(service.HealthCheck) > 2048 {
		return errors.New("storage: health-check URL is too long")
	}
	if service.HealthCheck != "" {
		if err := ValidateHealthCheck(service.HealthCheck, service.Address); err != nil {
			return err
		}
	}
	if service.CheckIntervalSeconds < 0 || service.CheckIntervalSeconds > MaxCheckIntervalSeconds {
		return errors.New("storage: check interval must be between 1 second and 1 hour")
	}
	if service.UnhealthyThreshold < 0 || service.UnhealthyThreshold > MaxCheckThreshold ||
		service.HealthyThreshold < 0 || service.HealthyThreshold > MaxCheckThreshold {
		return errors.New("storage: health thresholds must be between 1 and 10")
	}
	if service.HealthCheck == "" && (service.CheckIntervalSeconds != 0 || service.UnhealthyThreshold != 0 || service.HealthyThreshold != 0) {
		return errors.New("storage: check interval and thresholds require a health check")
	}
	return nil
}

// Routable reports whether lookups, DNS and client load balancers may send
// traffic to the service. Unknown health is routable as a fallback.
func (service Service) Routable() bool {
	return !service.Deleted && !service.Draining && service.Health != HealthUnhealthy
}

// ValidateHealthCheck accepts http(s)://, tcp:// and grpc(s):// checks whose
// host and port are the service address. A gRPC check may name the service to
// ask about in its path, for example grpc://10.0.0.5:9000/payments.v1.API.
func ValidateHealthCheck(check, address string) error {
	u, err := url.Parse(check)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Host != address || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("storage: health check must be a URL on the service address without credentials, query or fragment")
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	case "tcp":
		if u.Path != "" {
			return errors.New("storage: a tcp health check has no path")
		}
		return nil
	case "grpc", "grpcs":
		if strings.Count(strings.TrimPrefix(u.Path, "/"), "/") > 0 {
			return errors.New("storage: a grpc health check path is a single service name")
		}
		return nil
	default:
		return errors.New("storage: health check scheme must be http, https, tcp, grpc or grpcs")
	}
}

// Filter selects active services. Every set field must match, and a service
// must carry every listed tag.
type Filter struct {
	Name    string
	Region  string
	Health  Health
	Version string
	NodeID  string
	Tags    []string
}

// Store persists service records in SQLite. One connection serializes local
// writes; WAL lets backup readers inspect the database without blocking them.
type Store struct {
	db          *sql.DB
	path        string
	mu          sync.Mutex
	sampleMu    sync.Mutex
	sampleAfter string

	listenersMu sync.RWMutex
	listeners   []func(Change)

	historyOff atomic.Bool
}

// Change describes one committed write. Before is nil for a new record.
type Change struct {
	Before *Service
	After  Service
}

// OnChange registers fn for every committed write, including replicated
// merges. fn runs while the store's write lock is held, so it must not block
// or call back into the store.
func (s *Store) OnChange(fn func(Change)) {
	s.listenersMu.Lock()
	s.listeners = append(s.listeners, fn)
	s.listenersMu.Unlock()
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

const schemaVersion = 4

// migrations[n] upgrades a database from schema version n-1 to n.
var migrations = [...]string{
	1: `
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
`,
	2: `
ALTER TABLE services ADD COLUMN ttl_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN heartbeat_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS services_owner ON services(node_id, deleted);
`,
	3: `
ALTER TABLE services ADD COLUMN check_interval_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN unhealthy_threshold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN healthy_threshold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN draining INTEGER NOT NULL DEFAULT 0;
`,
	4: `
CREATE TABLE IF NOT EXISTS service_history (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  service_id TEXT NOT NULL,
  name TEXT NOT NULL,
  revision INTEGER NOT NULL,
  change TEXT NOT NULL,
  at INTEGER NOT NULL,
  record TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS service_history_service ON service_history(service_id, seq);
CREATE INDEX IF NOT EXISTS service_history_name ON service_history(name, seq);
CREATE INDEX IF NOT EXISTS service_history_at ON service_history(at);
`,
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("storage: read SQLite schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("storage: database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	for next := version + 1; next <= schemaVersion; next++ {
		if err := s.migrateTo(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateTo(ctx context.Context, version int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: start SQLite migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, migrations[version]); err != nil {
		return fmt.Errorf("storage: migrate SQLite schema to version %d: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("storage: set SQLite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit SQLite migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Outcome reports what Upsert or Update did.
type Outcome int

const (
	Created Outcome = iota + 1
	Updated
	Unchanged
)

// Register creates a service instance or advances its revision when the same
// instance ID is registered again by its owning node.
func (s *Store) Register(ctx context.Context, service Service) (Service, error) {
	stored, _, err := s.Upsert(ctx, service)
	return stored, err
}

// Upsert registers service under its ID. Re-registering identical content is
// a no-op, which makes client retries safe. Changing the address, health check
// or TTL resets health to the supplied value; other edits keep the observed
// health. Any node may reuse the ID of a deleted service.
func (s *Store) Upsert(ctx context.Context, service Service) (Service, Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if service.Health == "" {
		service.Health = HealthUnknown
	}
	if service.Health != HealthUnknown && service.Health != HealthHealthy && service.Health != HealthUnhealthy {
		return Service{}, 0, fmt.Errorf("storage: invalid health %q", service.Health)
	}
	current, err := scanService(s.db.QueryRowContext(ctx, serviceSelect+` WHERE id = ?`, service.ID))
	var before *Service
	outcome := Created
	switch {
	case err == nil:
		before = &current
		if !current.Deleted {
			if current.NodeID != service.NodeID {
				return Service{}, 0, &NotOwnerError{Owner: current.NodeID}
			}
			if sameSpec(current, service) {
				return current, Unchanged, nil
			}
			outcome = Updated
			if !resetsHealth(current, service) {
				service.Health, service.CheckedAt = current.Health, current.CheckedAt
			}
		}
		service.Revision = current.Revision + 1
	case errors.Is(err, sql.ErrNoRows):
		service.Revision = 1
	default:
		return Service{}, 0, err
	}
	now := time.Now().UTC()
	service.Deleted = false
	service.UpdatedAt = now
	if service.TTLSeconds > 0 {
		service.HeartbeatAt = now
	}
	if err := service.Validate(); err != nil {
		return Service{}, 0, err
	}
	stored, err := s.write(ctx, before, service)
	return stored, outcome, err
}

// Update applies fn to a copy of the active service owned by nodeID and stores
// the result. A non-zero ifRevision must equal the stored revision.
func (s *Store) Update(ctx context.Context, id, nodeID string, ifRevision uint64, fn func(*Service) error) (Service, Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(ctx, id)
	if err != nil {
		return Service{}, 0, err
	}
	if current.NodeID != nodeID {
		return Service{}, 0, &NotOwnerError{Owner: current.NodeID}
	}
	if ifRevision != 0 && current.Revision != ifRevision {
		return Service{}, 0, ErrRevisionMismatch
	}
	next := current
	next.Tags = slices.Clone(current.Tags)
	if err := fn(&next); err != nil {
		return Service{}, 0, err
	}
	next.ID, next.NodeID, next.Region = current.ID, current.NodeID, current.Region
	if sameSpec(current, next) {
		return current, Unchanged, nil
	}
	now := time.Now().UTC()
	if resetsHealth(current, next) {
		next.Health, next.CheckedAt = HealthUnknown, time.Time{}
		if next.TTLSeconds > 0 {
			next.HeartbeatAt = now
		}
	}
	next.Revision = current.Revision + 1
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return Service{}, 0, err
	}
	stored, err := s.write(ctx, &current, next)
	return stored, Updated, err
}

// sameSpec compares the client-supplied part of two registrations.
func sameSpec(a, b Service) bool {
	return a.Name == b.Name && a.Address == b.Address && a.Region == b.Region && a.Version == b.Version &&
		slices.Equal(a.Tags, b.Tags) && a.HealthCheck == b.HealthCheck && a.TTLSeconds == b.TTLSeconds &&
		a.CheckIntervalSeconds == b.CheckIntervalSeconds && a.UnhealthyThreshold == b.UnhealthyThreshold &&
		a.HealthyThreshold == b.HealthyThreshold && a.Draining == b.Draining
}

func resetsHealth(before, after Service) bool {
	return before.Address != after.Address || before.HealthCheck != after.HealthCheck || before.TTLSeconds != after.TTLSeconds
}

// Delete tombstones a service owned by nodeID.
func (s *Store) Delete(ctx context.Context, id, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if service.NodeID != nodeID {
		return &NotOwnerError{Owner: service.NodeID}
	}
	_, err = s.tombstone(ctx, service)
	return err
}

// tombstone marks service deleted with a higher revision. Callers hold s.mu.
func (s *Store) tombstone(ctx context.Context, service Service) (Service, error) {
	before := service
	service.Revision++
	service.Deleted = true
	service.UpdatedAt = time.Now().UTC()
	return s.write(ctx, &before, service)
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
		return &NotOwnerError{Owner: service.NodeID}
	}
	if service.Health == health {
		return s.updateCheckedAt(ctx, id, checkedAt)
	}
	before := service
	service.Health = health
	service.Revision++
	service.UpdatedAt = checkedAt.UTC()
	service.CheckedAt = checkedAt.UTC()
	_, err = s.write(ctx, &before, service)
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
	query, args := filterQuery(filter)
	return s.query(ctx, query+` ORDER BY name, region, node_id, id`, args...)
}

func filterQuery(filter Filter) (string, []any) {
	query := serviceSelect + ` WHERE deleted = 0`
	args := make([]any, 0, 5+len(filter.Tags))
	for _, field := range []struct{ column, value string }{
		{"name", filter.Name}, {"region", filter.Region}, {"health", string(filter.Health)},
		{"version", filter.Version}, {"node_id", filter.NodeID},
	} {
		if field.value != "" {
			query += ` AND ` + field.column + ` = ?`
			args = append(args, field.value)
		}
	}
	for _, tag := range filter.Tags {
		query += ` AND EXISTS (SELECT 1 FROM json_each(services.tags) WHERE json_each.value = ?)`
		args = append(args, tag)
	}
	return query, args
}

func (s *Store) query(ctx context.Context, query string, args ...any) ([]Service, error) {
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
	return s.query(ctx, serviceSelect+` WHERE node_id = ? AND deleted = 0 AND health_check <> '' ORDER BY id`, nodeID)
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
		service.CheckedAt, service.HeartbeatAt = time.Time{}, time.Time{}
		encoded, err := json.Marshal(service)
		if err != nil {
			return digest, err
		}
		if uint64(len(encoded)) > uint64(^uint32(0)) {
			return digest, ErrRegistryTooLarge
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(encoded))) // #nosec G115 -- length is bounded by the uint32 guard above.
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
		service.CheckedAt, service.HeartbeatAt = time.Time{}, time.Time{}
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
		service.CheckedAt, service.HeartbeatAt = time.Time{}, time.Time{}
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
		if incoming.Revision > 1<<63-1 {
			return errors.New("storage: replicated service revision is too large")
		}
		incoming.UpdatedAt = time.Unix(0, int64(incoming.Revision)).UTC()
	}
	incoming.CheckedAt, incoming.HeartbeatAt = time.Time{}, time.Time{}
	if err := incoming.Validate(); err != nil {
		return err
	}
	current, err := scanService(s.db.QueryRowContext(ctx, serviceSelect+` WHERE id = ?`, incoming.ID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var before *Service
	if err == nil {
		if !wins(incoming, current) {
			return nil
		}
		before = &current
	}
	_, err = s.write(ctx, before, incoming)
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
	service.CheckedAt, service.HeartbeatAt = time.Time{}, time.Time{}
	return service
}

// write persists service and notifies change listeners. Callers hold s.mu.
// The record and its history entry commit in one transaction, and listeners
// (events, replication) run only after the commit. So a write is either fully
// stored, history included, with its notification sent, or not stored at all.
func (s *Store) write(ctx context.Context, before *Service, service Service) (Service, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Service{}, fmt.Errorf("storage: start write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := s.put(ctx, tx, service)
	if err != nil {
		return Service{}, err
	}
	change := Change{Before: before, After: stored}
	if err := s.recordHistory(ctx, tx, change); err != nil {
		return Service{}, err
	}
	if err := tx.Commit(); err != nil {
		return Service{}, fmt.Errorf("storage: commit write: %w", err)
	}
	s.listenersMu.RLock()
	for _, listener := range s.listeners {
		listener(change)
	}
	s.listenersMu.RUnlock()
	return stored, nil
}

// execer runs a statement on the database or inside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) put(ctx context.Context, ex execer, service Service) (Service, error) {
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
	_, err = ex.ExecContext(ctx, `
INSERT INTO services (id,name,node_id,address,region,version,tags,health_check,health,revision,deleted,updated_at,checked_at,ttl_seconds,heartbeat_at,
 check_interval_seconds,unhealthy_threshold,healthy_threshold,draining)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,node_id=excluded.node_id,address=excluded.address,region=excluded.region,
 version=excluded.version,tags=excluded.tags,health_check=excluded.health_check,health=excluded.health,
 revision=excluded.revision,deleted=excluded.deleted,updated_at=excluded.updated_at,checked_at=excluded.checked_at,
 ttl_seconds=excluded.ttl_seconds,check_interval_seconds=excluded.check_interval_seconds,
 unhealthy_threshold=excluded.unhealthy_threshold,healthy_threshold=excluded.healthy_threshold,draining=excluded.draining,
 heartbeat_at=CASE WHEN excluded.heartbeat_at = 0 THEN services.heartbeat_at ELSE excluded.heartbeat_at END`,
		service.ID, service.Name, service.NodeID, service.Address, service.Region, service.Version,
		string(tags), service.HealthCheck, service.Health, service.Revision, deleted,
		service.UpdatedAt.UnixNano(), timeNanos(service.CheckedAt), service.TTLSeconds, timeNanos(service.HeartbeatAt),
		service.CheckIntervalSeconds, service.UnhealthyThreshold, service.HealthyThreshold, boolInt(service.Draining))
	if err != nil {
		return Service{}, fmt.Errorf("storage: save service: %w", err)
	}
	return service, nil
}

func (s *Store) updateCheckedAt(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE services SET checked_at = ? WHERE id = ?`, timeNanos(at), id)
	return err
}

const serviceSelect = `SELECT id,name,node_id,address,region,version,tags,health_check,health,revision,deleted,updated_at,checked_at,ttl_seconds,heartbeat_at,
 check_interval_seconds,unhealthy_threshold,healthy_threshold,draining FROM services`

type scanner interface{ Scan(...any) error }

func scanService(row scanner) (Service, error) {
	var service Service
	var tags string
	var deleted, draining int
	var updatedAt, checkedAt, heartbeatAt int64
	if err := row.Scan(&service.ID, &service.Name, &service.NodeID, &service.Address, &service.Region,
		&service.Version, &tags, &service.HealthCheck, &service.Health, &service.Revision,
		&deleted, &updatedAt, &checkedAt, &service.TTLSeconds, &heartbeatAt,
		&service.CheckIntervalSeconds, &service.UnhealthyThreshold, &service.HealthyThreshold, &draining); err != nil {
		return Service{}, err
	}
	if err := json.Unmarshal([]byte(tags), &service.Tags); err != nil {
		return Service{}, fmt.Errorf("storage: decode tags: %w", err)
	}
	service.Deleted = deleted != 0
	service.Draining = draining != 0
	service.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if checkedAt != 0 {
		service.CheckedAt = time.Unix(0, checkedAt).UTC()
	}
	if heartbeatAt != 0 {
		service.HeartbeatAt = time.Unix(0, heartbeatAt).UTC()
	}
	return service, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
