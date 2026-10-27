package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
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
// Owner-only writes that reach another node are forwarded to the owner when
// its API address is known (see forward.go).

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
	ID                   string   `json:"id,omitempty"`
	Name                 string   `json:"name"`
	Address              string   `json:"address"`
	Version              string   `json:"version,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	HealthCheck          string   `json:"health_check,omitempty"`
	TTLSeconds           int64    `json:"ttl_seconds,omitempty"`
	CheckIntervalSeconds int64    `json:"check_interval_seconds,omitempty"`
	UnhealthyThreshold   int      `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold     int      `json:"healthy_threshold,omitempty"`
	Draining             bool     `json:"draining,omitempty"`
}

// servicePatch changes the non-nil fields of a service. It is also the wire
// format when a non-owner forwards an update to the owner, so it must not
// encode absent fields.
type servicePatch struct {
	Name                 *string   `json:"name,omitempty"`
	Address              *string   `json:"address,omitempty"`
	Version              *string   `json:"version,omitempty"`
	Tags                 *[]string `json:"tags,omitempty"`
	HealthCheck          *string   `json:"health_check,omitempty"`
	TTLSeconds           *int64    `json:"ttl_seconds,omitempty"`
	CheckIntervalSeconds *int64    `json:"check_interval_seconds,omitempty"`
	UnhealthyThreshold   *int      `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold     *int      `json:"healthy_threshold,omitempty"`
	Draining             *bool     `json:"draining,omitempty"`
}

func (p servicePatch) apply(s *serviceSpec) {
	set(&s.Name, p.Name)
	set(&s.Address, p.Address)
	set(&s.Version, p.Version)
	set(&s.Tags, p.Tags)
	set(&s.HealthCheck, p.HealthCheck)
	set(&s.TTLSeconds, p.TTLSeconds)
	set(&s.CheckIntervalSeconds, p.CheckIntervalSeconds)
	set(&s.UnhealthyThreshold, p.UnhealthyThreshold)
	set(&s.HealthyThreshold, p.HealthyThreshold)
	set(&s.Draining, p.Draining)
}

func set[T any](dst *T, value *T) {
	if value != nil {
		*dst = *value
	}
}

// fullPatch replaces every client-owned field with spec's values.
func fullPatch(spec serviceSpec) servicePatch {
	tags := spec.Tags
	if tags == nil {
		tags = []string{} // encodes as [], not null, when forwarded
	}
	return servicePatch{
		Name: &spec.Name, Address: &spec.Address, Version: &spec.Version, Tags: &tags,
		HealthCheck: &spec.HealthCheck, TTLSeconds: &spec.TTLSeconds,
		CheckIntervalSeconds: &spec.CheckIntervalSeconds, UnhealthyThreshold: &spec.UnhealthyThreshold,
		HealthyThreshold: &spec.HealthyThreshold, Draining: &spec.Draining,
	}
}

func specOf(s storage.Service) serviceSpec {
	return serviceSpec{
		ID: s.ID, Name: s.Name, Address: s.Address, Version: s.Version, Tags: s.Tags,
		HealthCheck: s.HealthCheck, TTLSeconds: s.TTLSeconds, CheckIntervalSeconds: s.CheckIntervalSeconds,
		UnhealthyThreshold: s.UnhealthyThreshold, HealthyThreshold: s.HealthyThreshold, Draining: s.Draining,
	}
}

func (spec serviceSpec) applyTo(s *storage.Service) {
	s.Name, s.Address, s.Version, s.Tags = spec.Name, spec.Address, spec.Version, spec.Tags
	s.HealthCheck, s.TTLSeconds, s.CheckIntervalSeconds = spec.HealthCheck, spec.TTLSeconds, spec.CheckIntervalSeconds
	s.UnhealthyThreshold, s.HealthyThreshold, s.Draining = spec.UnhealthyThreshold, spec.HealthyThreshold, spec.Draining
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
	if !errors.As(err, &notOwner) {
		return service, outcome, err
	}
	if !d.ownerAlive(notOwner.Owner) {
		// The previous owner left the cluster, so this node adopts the ID.
		if _, err = d.Registry.Tombstone(ctx, spec.ID); err == nil || errors.Is(err, storage.ErrNotFound) {
			service, outcome, err = d.Registry.Upsert(ctx, record)
		}
		return service, outcome, err
	}
	if forwarded, status, ok, fwdErr := d.forwardService(ctx, notOwner.Owner, http.MethodPost, "/v1/services", spec, 0); ok {
		if fwdErr != nil {
			return storage.Service{}, 0, fwdErr
		}
		outcome = storage.Updated
		if status == http.StatusCreated {
			outcome = storage.Created
		}
		return forwarded, outcome, nil
	}
	return service, outcome, err
}

func idempotentID(tokenID, key string) string {
	sum := sha256.Sum256([]byte("atlas/idempotency\x00" + tokenID + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}

// update applies patch to a service. The owner applies it locally; any other
// node forwards the same patch to the owner.
func (d Deps) update(ctx context.Context, id string, ifRevision uint64, patch servicePatch) (storage.Service, error) {
	if d.Registry == nil {
		return storage.Service{}, errRegistryUnavailable
	}
	service, _, err := d.Registry.Update(ctx, id, d.Cluster.LocalNode().ID, ifRevision, func(s *storage.Service) error {
		spec := specOf(*s)
		patch.apply(&spec)
		if err := validateSpec(spec); err != nil {
			return err
		}
		spec.applyTo(s)
		return nil
	})
	var notOwner *storage.NotOwnerError
	if errors.As(err, &notOwner) {
		if forwarded, _, ok, fwdErr := d.forwardService(ctx, notOwner.Owner, http.MethodPatch, "/v1/services/"+url.PathEscape(id), patch, ifRevision); ok {
			return forwarded, fwdErr
		}
	}
	return service, err
}

func (d Deps) heartbeatService(ctx context.Context, id string) (storage.Service, error) {
	if d.Registry == nil {
		return storage.Service{}, errRegistryUnavailable
	}
	service, err := d.Registry.Heartbeat(ctx, id, d.Cluster.LocalNode().ID, time.Now())
	var notOwner *storage.NotOwnerError
	if errors.As(err, &notOwner) {
		if forwarded, _, ok, fwdErr := d.forwardService(ctx, notOwner.Owner, http.MethodPost, "/v1/services/"+url.PathEscape(id)+"/heartbeat", nil, 0); ok {
			return forwarded, fwdErr
		}
	}
	return service, err
}

// deregister removes a service through its owner, forwarding when another
// node owns it. Without a reachable owner, any node may remove it once the
// owner has left the cluster, and an admin may remove it at any time.
func (d Deps) deregister(ctx context.Context, id string) error {
	if d.Registry == nil {
		return errRegistryUnavailable
	}
	err := d.Registry.Delete(ctx, id, d.Cluster.LocalNode().ID)
	var notOwner *storage.NotOwnerError
	if !errors.As(err, &notOwner) {
		return err
	}
	if d.ownerAlive(notOwner.Owner) {
		if _, _, ok, fwdErr := d.forwardService(ctx, notOwner.Owner, http.MethodDelete, "/v1/services/"+url.PathEscape(id), nil, 0); ok {
			return fwdErr
		}
		if !security.Granted(ctx, security.ScopeAdmin) {
			return ownerAliveError{owner: notOwner.Owner}
		}
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
	_, alive := d.member(nodeID)
	return alive
}

// member returns nodeID's record and whether it is this node or a live or
// suspect peer.
func (d Deps) member(nodeID string) (membership.Member, bool) {
	if self := d.Cluster.LocalNode(); nodeID == self.ID {
		return self, true
	}
	for _, member := range d.Cluster.Members() {
		if member.ID == nodeID {
			return member, member.State == membership.StateAlive || member.State == membership.StateSuspect
		}
	}
	return membership.Member{}, false
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
	if in.HealthCheck != "" && storage.ValidateHealthCheck(in.HealthCheck, in.Address) != nil {
		return invalid("health_check must be an http, https, tcp, grpc or grpcs URL on the service address, without credentials, query or fragment")
	}
	if in.CheckIntervalSeconds < 0 || in.CheckIntervalSeconds > storage.MaxCheckIntervalSeconds {
		return invalid("check_interval_seconds must be between 1 and %d", storage.MaxCheckIntervalSeconds)
	}
	for _, threshold := range []int{in.UnhealthyThreshold, in.HealthyThreshold} {
		if threshold < 0 || threshold > storage.MaxCheckThreshold {
			return invalid("unhealthy_threshold and healthy_threshold must be between 1 and %d", storage.MaxCheckThreshold)
		}
	}
	if in.HealthCheck == "" && (in.CheckIntervalSeconds != 0 || in.UnhealthyThreshold != 0 || in.HealthyThreshold != 0) {
		return invalid("check_interval_seconds and thresholds need a health_check")
	}
	return nil
}
