package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

// Registry operations shared by the HTTP and gRPC APIs. They return storage
// errors or the error types below; each transport maps them to its own codes.

// ServiceRegistry is the subset of storage.Store the API needs.
type ServiceRegistry interface {
	Upsert(context.Context, storage.Service) (storage.Service, storage.Outcome, error)
	Update(ctx context.Context, id, nodeID string, ifRevision uint64, fn func(*storage.Service) error) (storage.Service, storage.Outcome, error)
	Get(context.Context, string) (storage.Service, error)
	List(context.Context, storage.Filter) ([]storage.Service, error)
	ListPage(ctx context.Context, filter storage.Filter, cursor string, limit int) (storage.Page, error)
	Delete(ctx context.Context, id, nodeID string) error
	Tombstone(context.Context, string) (storage.Service, error)
	Heartbeat(ctx context.Context, id, nodeID string, at time.Time) (storage.Service, error)
}

const (
	defaultPageSize = 100
	maxTagFilters   = 16
)

var (
	errRegistryUnavailable = errors.New("service registry unavailable")
	serviceNamePattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

// validationError is a client input problem; its message is safe to return.
type validationError struct{ msg string }

func (e validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return validationError{msg: fmt.Sprintf(format, args...)}
}

// ownerAliveError refuses a delete that only the live owner (or an admin) may make.
type ownerAliveError struct{ owner string }

func (e ownerAliveError) Error() string { return "the owning node " + e.owner + " is alive" }

// serviceSpec is the client-owned part of a registration.
type serviceSpec struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
	TTLSeconds  int64    `json:"ttl_seconds,omitempty"`
}

func specOf(s storage.Service) serviceSpec {
	return serviceSpec{ID: s.ID, Name: s.Name, Address: s.Address, Version: s.Version, Tags: s.Tags, HealthCheck: s.HealthCheck, TTLSeconds: s.TTLSeconds}
}

func (spec serviceSpec) applyTo(s *storage.Service) {
	s.Name, s.Address, s.Version, s.Tags = spec.Name, spec.Address, spec.Version, spec.Tags
	s.HealthCheck, s.TTLSeconds = spec.HealthCheck, spec.TTLSeconds
}

// register creates or idempotently re-registers a service owned by this node.
// idempotencyKey, when set, derives the ID from the caller's credential so a
// retry maps to the same record on any node.
func (d Deps) register(ctx context.Context, spec serviceSpec, idempotencyKey string) (storage.Service, storage.Outcome, error) {
	if d.Registry == nil {
		return storage.Service{}, 0, errRegistryUnavailable
	}
	if idempotencyKey != "" {
		if len(idempotencyKey) > 256 {
			return storage.Service{}, 0, invalid("idempotency key must be at most 256 characters")
		}
		derived := idempotentID(security.AuthenticatedTokenID(ctx), idempotencyKey)
		if spec.ID != "" && spec.ID != derived {
			return storage.Service{}, 0, invalid("send either an id or an idempotency key, not both")
		}
		spec.ID = derived
	}
	if spec.ID == "" {
		var idBytes [16]byte
		if _, err := rand.Read(idBytes[:]); err != nil {
			return storage.Service{}, 0, fmt.Errorf("allocate service ID: %w", err)
		}
		spec.ID = hex.EncodeToString(idBytes[:])
	}
	if err := validateSpec(spec); err != nil {
		return storage.Service{}, 0, err
	}
	node := d.Cluster.LocalNode()
	record := storage.Service{NodeID: node.ID, Region: node.Region, Health: storage.HealthUnknown, ID: spec.ID}
	spec.applyTo(&record)
	service, outcome, err := d.Registry.Upsert(ctx, record)
	var notOwner *storage.NotOwnerError
	if errors.As(err, &notOwner) && !d.ownerAlive(notOwner.Owner) {
		// The previous owner left the cluster, so this node adopts the ID.
		if _, err = d.Registry.Tombstone(ctx, spec.ID); err == nil || errors.Is(err, storage.ErrNotFound) {
			service, outcome, err = d.Registry.Upsert(ctx, record)
		}
	}
	return service, outcome, err
}

func idempotentID(tokenID, key string) string {
	sum := sha256.Sum256([]byte("atlas/idempotency\x00" + tokenID + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}

// update edits a service owned by this node. fn changes a copy; the result is
// validated before it is stored.
func (d Deps) update(ctx context.Context, id string, ifRevision uint64, fn func(*serviceSpec)) (storage.Service, error) {
	if d.Registry == nil {
		return storage.Service{}, errRegistryUnavailable
	}
	service, _, err := d.Registry.Update(ctx, id, d.Cluster.LocalNode().ID, ifRevision, func(s *storage.Service) error {
		spec := specOf(*s)
		fn(&spec)
		if err := validateSpec(spec); err != nil {
			return err
		}
		spec.applyTo(s)
		return nil
	})
	return service, err
}

func (d Deps) heartbeatService(ctx context.Context, id string) (storage.Service, error) {
	if d.Registry == nil {
		return storage.Service{}, errRegistryUnavailable
	}
	return d.Registry.Heartbeat(ctx, id, d.Cluster.LocalNode().ID, time.Now())
}

// deregister removes a service through its owner. Another node may remove it
// when the owner has left the cluster, or when the caller is an admin.
func (d Deps) deregister(ctx context.Context, id string) error {
	if d.Registry == nil {
		return errRegistryUnavailable
	}
	err := d.Registry.Delete(ctx, id, d.Cluster.LocalNode().ID)
	var notOwner *storage.NotOwnerError
	if !errors.As(err, &notOwner) {
		return err
	}
	if d.ownerAlive(notOwner.Owner) && !security.Granted(ctx, security.ScopeAdmin) {
		return ownerAliveError{owner: notOwner.Owner}
	}
	_, err = d.Registry.Tombstone(ctx, id)
	return err
}

func (d Deps) listPage(ctx context.Context, filter storage.Filter, cursor string, limit int) (storage.Page, error) {
	if d.Registry == nil {
		return storage.Page{}, errRegistryUnavailable
	}
	if limit == 0 {
		limit = defaultPageSize
	}
	if limit < 1 || limit > storage.MaxPageSize {
		return storage.Page{}, invalid("limit must be between 1 and %d", storage.MaxPageSize)
	}
	if err := validateFilter(filter); err != nil {
		return storage.Page{}, err
	}
	page, err := d.Registry.ListPage(ctx, filter, cursor, limit)
	if errors.Is(err, storage.ErrInvalidCursor) {
		return page, invalid("cursor is invalid; restart the listing without it")
	}
	return page, err
}

func validateFilter(filter storage.Filter) error {
	if filter.Health != "" && filter.Health != storage.HealthHealthy && filter.Health != storage.HealthUnhealthy && filter.Health != storage.HealthUnknown {
		return invalid("health must be healthy, unhealthy, or unknown")
	}
	if len(filter.Tags) > maxTagFilters {
		return invalid("at most %d tag filters are allowed", maxTagFilters)
	}
	for _, value := range append([]string{filter.Name, filter.Region, filter.Version, filter.NodeID}, filter.Tags...) {
		if len(value) > 128 {
			return invalid("filter values must be at most 128 characters")
		}
	}
	return nil
}

func (d Deps) lookup(ctx context.Context, name, region, key string, tags []string) (storage.Service, error) {
	if d.Registry == nil || d.Selector == nil {
		return storage.Service{}, errRegistryUnavailable
	}
	if name == "" || len(name) > 128 || len(region) > 64 || len(key) > 256 || len(tags) > maxTagFilters {
		return storage.Service{}, invalid("lookup parameters are missing or exceed limits")
	}
	services, err := d.Registry.List(ctx, storage.Filter{Name: name, Tags: tags})
	if err != nil {
		return storage.Service{}, err
	}
	return d.Selector.SelectWithLatency(services, region, key, d.nodeLatency)
}

func (d Deps) nodeLatency(nodeID string) time.Duration {
	if reader, ok := d.Cluster.(interface{ Latency(string) time.Duration }); ok {
		return reader.Latency(nodeID)
	}
	return 0
}

// ownerAlive reports whether nodeID is this node or a live or suspect member.
func (d Deps) ownerAlive(nodeID string) bool {
	if nodeID == d.Cluster.LocalNode().ID {
		return true
	}
	for _, member := range d.Cluster.Members() {
		if member.ID == nodeID {
			return member.State == membership.StateAlive || member.State == membership.StateSuspect
		}
	}
	return false
}

// ownerOf extracts the owning node from a not-owner or owner-alive error.
func ownerOf(err error) (string, bool) {
	var notOwner *storage.NotOwnerError
	if errors.As(err, &notOwner) {
		return notOwner.Owner, true
	}
	var alive ownerAliveError
	if errors.As(err, &alive) {
		return alive.owner, true
	}
	return "", false
}

func validateSpec(in serviceSpec) error {
	if in.ID != "" && !serviceNamePattern.MatchString(in.ID) {
		return invalid("id must be 1-128 letters, digits, dots, underscores, or hyphens")
	}
	if !serviceNamePattern.MatchString(in.Name) {
		return invalid("name must be 1-128 letters, digits, dots, underscores, or hyphens")
	}
	if host, port, err := net.SplitHostPort(in.Address); err != nil || host == "" {
		return invalid("address must be host:port")
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return invalid("address port must be between 1 and 65535")
	}
	if len(in.Version) > 128 {
		return invalid("version must be at most 128 characters")
	}
	if len(in.Tags) > 16 {
		return invalid("at most 16 tags are allowed")
	}
	for _, tag := range in.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 64 {
			return invalid("tags must contain 1-64 non-whitespace characters")
		}
	}
	if in.TTLSeconds < 0 || in.TTLSeconds > storage.MaxTTLSeconds {
		return invalid("ttl_seconds must be between 1 and %d", storage.MaxTTLSeconds)
	}
	if in.TTLSeconds > 0 && in.HealthCheck != "" {
		return invalid("use either health_check or ttl_seconds, not both")
	}
	if in.HealthCheck != "" {
		u, err := url.ParseRequestURI(in.HealthCheck)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Host != in.Address {
			return invalid("health_check must be an http or https URL on the service address without user information")
		}
	}
	return nil
}
