package k8ssync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/pkg/atlas"
)

// Service annotations. Only AnnotationRegister is required.
const (
	AnnotationRegister = "atlas.io/register" // "true" opts the Service in
	AnnotationName     = "atlas.io/name"     // Atlas service name (default: the Service name)
	AnnotationPort     = "atlas.io/port"     // Service port name or number (default: the only port)
	AnnotationTags     = "atlas.io/tags"     // comma-separated tags
	AnnotationVersion  = "atlas.io/version"  // version
	AnnotationHealth   = "atlas.io/health"   // /path (HTTP), tcp or grpc; default: TTL kept by readiness
	AnnotationMode     = "atlas.io/mode"     // endpoints (one instance per pod, default) or service
)

// Registry is the Atlas API the controller uses; *atlas.Client implements it.
type Registry interface {
	Register(ctx context.Context, input atlas.RegisterRequest) (atlas.Service, error)
	Heartbeat(ctx context.Context, id string) (atlas.Service, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filter atlas.Filter) ([]atlas.Service, error)
}

// Config tunes the controller. Zero values take the defaults noted.
type Config struct {
	// Cluster names this Kubernetes cluster. Registrations carry the tag
	// "atlas-k8s:<Cluster>", and reconciliation only removes services with
	// it, so give each cluster its own name (default "kubernetes").
	Cluster string
	// Namespaces limits the watch; empty watches every namespace.
	Namespaces []string
	// ClusterDomain builds addresses in service mode (default cluster.local).
	ClusterDomain string
	// TTL for instances without atlas.io/health (default 15s). The
	// controller heartbeats every TTL/3 while the pod is ready.
	TTL time.Duration
	// ResyncInterval compares Atlas with the desired state (default 1m).
	ResyncInterval time.Duration
	// CallTimeout bounds each Atlas call (default 10s).
	CallTimeout time.Duration
	Logger      *slog.Logger
}

// Controller mirrors annotated Kubernetes Services into Atlas:
//
//   - a ready pod endpoint is registered and, without atlas.io/health, kept
//     healthy with TTL heartbeats;
//   - a pod that is not ready stays registered but gets no heartbeats, so it
//     turns unhealthy after one TTL;
//   - a terminating pod is drained, so lookups skip it while it shuts down;
//   - an endpoint, Service or annotation that goes away is deregistered.
//
// A single goroutine owns all state. Watch streams feed it; it reconciles
// the desired instances against what it has applied.
type Controller struct {
	cfg   Config
	kube  *Kube
	atlas Registry
	log   *slog.Logger

	services map[string]Service       // annotated Services by namespace/name
	slices   map[string]EndpointSlice // all EndpointSlices by namespace/name
	applied  map[string]instance      // by Atlas service ID
	warned   map[string]string        // last configuration warning per Service
}

type instance struct {
	spec  atlas.RegisterRequest
	ready bool
}

// New creates a controller.
func New(cfg Config, kube *Kube, registry Registry) *Controller {
	if cfg.Cluster == "" {
		cfg.Cluster = "kubernetes"
	}
	if cfg.ClusterDomain == "" {
		cfg.ClusterDomain = "cluster.local"
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Second
	}
	if cfg.ResyncInterval <= 0 {
		cfg.ResyncInterval = time.Minute
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Controller{
		cfg: cfg, kube: kube, atlas: registry, log: cfg.Logger,
		services: map[string]Service{}, slices: map[string]EndpointSlice{},
		applied: map[string]instance{}, warned: map[string]string{},
	}
}

// OwnerTag is the tag on every service this controller registers.
func (c *Controller) OwnerTag() string { return "atlas-k8s:" + c.cfg.Cluster }

type update struct {
	resource string // "services" or "slices"
	scope    string // namespace, or "" for all
	replace  bool
	items    []json.RawMessage
	kind     string // ADDED, MODIFIED, DELETED
	object   json.RawMessage
}

// Run watches Kubernetes until ctx ends.
func (c *Controller) Run(ctx context.Context) error {
	updates := make(chan update, 1024)
	scopes := c.cfg.Namespaces
	if len(scopes) == 0 {
		scopes = []string{""}
	}
	for _, ns := range scopes {
		svcPath, slicePath := "/api/v1/services", "/apis/discovery.k8s.io/v1/endpointslices"
		if ns != "" {
			svcPath = "/api/v1/namespaces/" + ns + "/services"
			slicePath = "/apis/discovery.k8s.io/v1/namespaces/" + ns + "/endpointslices"
		}
		go c.informer(ctx, "services", ns, svcPath, updates)
		go c.informer(ctx, "slices", ns, slicePath, updates)
	}

	heartbeat := time.NewTicker(c.cfg.TTL / 3)
	defer heartbeat.Stop()
	resync := time.NewTicker(c.cfg.ResyncInterval)
	defer resync.Stop()
	// Changes are batched so a burst of events (a rollout) reconciles once.
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	synced := map[string]bool{}
	ready := func() bool { return len(synced) == 2*len(scopes) }

	for {
		select {
		case <-ctx.Done():
			return nil
		case u := <-updates:
			c.apply(u)
			if u.replace {
				synced[u.resource+"/"+u.scope] = true
			}
			if ready() {
				debounce.Reset(300 * time.Millisecond)
			}
		case <-debounce.C:
			c.reconcile(ctx)
		case <-heartbeat.C:
			c.heartbeats(ctx)
		case <-resync.C:
			if ready() {
				c.resync(ctx)
			}
		}
	}
}

// informer lists then watches one resource, relisting when the watch
// expires, and sends everything to the controller loop.
func (c *Controller) informer(ctx context.Context, resource, scope, path string, out chan<- update) {
	backoff := time.Second
	send := func(u update) {
		select {
		case out <- u:
		case <-ctx.Done():
		}
	}
	for ctx.Err() == nil {
		items, version, err := c.kube.list(ctx, path)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Warn("list failed; retrying", "resource", resource, "err", err, "in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		send(update{resource: resource, scope: scope, replace: true, items: items})
		for ctx.Err() == nil {
			version, err = c.kube.watch(ctx, path, version, func(e watchEvent) {
				send(update{resource: resource, scope: scope, kind: e.Type, object: e.Object})
			})
			if err != nil {
				if ctx.Err() == nil && !errors.Is(err, errGone) {
					c.log.Warn("watch interrupted; relisting", "resource", resource, "err", err)
					sleep(ctx, time.Second)
				}
				break
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (c *Controller) apply(u update) {
	inScope := func(ns string) bool { return u.scope == "" || ns == u.scope }
	switch u.resource {
	case "services":
		if u.replace {
			for k, s := range c.services {
				if inScope(s.Metadata.Namespace) {
					delete(c.services, k)
				}
			}
			for _, raw := range u.items {
				c.putService(raw, "ADDED")
			}
			return
		}
		c.putService(u.object, u.kind)
	case "slices":
		if u.replace {
			for k, s := range c.slices {
				if inScope(s.Metadata.Namespace) {
					delete(c.slices, k)
				}
			}
			for _, raw := range u.items {
				c.putSlice(raw, "ADDED")
			}
			return
		}
		c.putSlice(u.object, u.kind)
	}
}

func (c *Controller) putService(raw json.RawMessage, kind string) {
	var s Service
	if json.Unmarshal(raw, &s) != nil || s.Metadata.Name == "" {
		return
	}
	k := key(s.Metadata.Namespace, s.Metadata.Name)
	if kind == "DELETED" || !strings.EqualFold(s.Metadata.Annotations[AnnotationRegister], "true") {
		delete(c.services, k)
		return
	}
	c.services[k] = s
}

func (c *Controller) putSlice(raw json.RawMessage, kind string) {
	var s EndpointSlice
	if json.Unmarshal(raw, &s) != nil || s.Metadata.Name == "" {
		return
	}
	k := key(s.Metadata.Namespace, s.Metadata.Name)
	if kind == "DELETED" {
		delete(c.slices, k)
		return
	}
	c.slices[k] = s
}

func (c *Controller) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.cfg.CallTimeout)
}

// desired computes the instances that should exist.
func (c *Controller) desired() map[string]instance {
	desired := map[string]instance{}
	byService := map[string][]EndpointSlice{}
	for _, slice := range c.slices {
		if name := sliceService(slice); name != "" {
			k := key(slice.Metadata.Namespace, name)
			byService[k] = append(byService[k], slice)
		}
	}
	keys := make([]string, 0, len(c.services))
	for k := range c.services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		svc := c.services[k]
		instances, err := c.instancesFor(svc, byService[k])
		if err != nil {
			if c.warned[k] != err.Error() {
				c.log.Warn("cannot register service", "service", k, "err", err)
				c.warned[k] = err.Error()
			}
			continue
		}
		delete(c.warned, k)
		for id, inst := range instances {
			desired[id] = inst
		}
	}
	return desired
}

func (c *Controller) instancesFor(svc Service, slices []EndpointSlice) (map[string]instance, error) {
	ann := svc.Metadata.Annotations
	port, err := servicePort(svc)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(ann[AnnotationName])
	if name == "" {
		name = svc.Metadata.Name
	}
	base := atlas.RegisterRequest{
		Name:    name,
		Version: strings.TrimSpace(ann[AnnotationVersion]),
		Tags:    []string{c.OwnerTag(), "k8s-namespace:" + svc.Metadata.Namespace, "k8s-service:" + svc.Metadata.Name},
	}
	for _, tag := range strings.Split(ann[AnnotationTags], ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			base.Tags = append(base.Tags, tag)
		}
	}
	health := strings.TrimSpace(ann[AnnotationHealth])
	if health != "" && health != "tcp" && health != "grpc" && !strings.HasPrefix(health, "/") {
		return nil, fmt.Errorf("%s must be /path, tcp or grpc, got %q", AnnotationHealth, health)
	}
	withHealth := func(spec atlas.RegisterRequest) atlas.RegisterRequest {
		switch health {
		case "":
			spec.TTLSeconds = int64(max(c.cfg.TTL/time.Second, 1))
		case "tcp":
			spec.HealthCheck = "tcp://" + spec.Address
		case "grpc":
			spec.HealthCheck = "grpc://" + spec.Address
		default:
			spec.HealthCheck = "http://" + spec.Address + health
		}
		return spec
	}

	out := map[string]instance{}
	switch mode := strings.TrimSpace(ann[AnnotationMode]); mode {
	case "service":
		spec := base
		spec.ID = instanceID(c.cfg.Cluster, svc.Metadata.Namespace, svc.Metadata.Name, "svc")
		spec.Address = net.JoinHostPort(svc.Metadata.Name+"."+svc.Metadata.Namespace+".svc."+c.cfg.ClusterDomain, strconv.Itoa(port.Port))
		out[spec.ID] = instance{spec: withHealth(spec), ready: true}
		return out, nil
	case "", "endpoints":
	default:
		return nil, fmt.Errorf("%s must be endpoints or service, got %q", AnnotationMode, mode)
	}

	for _, slice := range slices {
		if slice.AddressType != "IPv4" && slice.AddressType != "IPv6" {
			continue
		}
		target := 0
		for _, p := range slice.Ports {
			pname := ""
			if p.Name != nil {
				pname = *p.Name
			}
			if pname == port.Name && p.Port != nil {
				target = *p.Port
			}
		}
		if target == 0 {
			continue
		}
		for _, ep := range slice.Endpoints {
			if len(ep.Addresses) == 0 {
				continue
			}
			terminating := ep.terminating()
			if terminating && !ep.serving() {
				continue // gone from rotation and no longer serving: deregister
			}
			who := ep.Addresses[0]
			if ep.TargetRef != nil && ep.TargetRef.Name != "" {
				who = ep.TargetRef.Name
			}
			spec := base
			spec.Tags = append([]string(nil), base.Tags...)
			if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" && len(spec.Tags) < 16 {
				spec.Tags = append(spec.Tags, "k8s-pod:"+ep.TargetRef.Name)
			}
			spec.ID = instanceID(c.cfg.Cluster, svc.Metadata.Namespace, svc.Metadata.Name, who)
			spec.Address = net.JoinHostPort(ep.Addresses[0], strconv.Itoa(target))
			spec.Draining = terminating
			out[spec.ID] = instance{spec: withHealth(spec), ready: ep.ready() && !terminating}
		}
	}
	return out, nil
}

// servicePort picks the port named or numbered by atlas.io/port, or the only one.
func servicePort(svc Service) (ServicePort, error) {
	want := strings.TrimSpace(svc.Metadata.Annotations[AnnotationPort])
	var tcp []ServicePort
	for _, p := range svc.Spec.Ports {
		if p.Protocol == "" || p.Protocol == "TCP" {
			tcp = append(tcp, p)
		}
	}
	if want == "" {
		if len(tcp) == 1 {
			return tcp[0], nil
		}
		return ServicePort{}, fmt.Errorf("set %s to a port name or number (the Service has %d TCP ports)", AnnotationPort, len(tcp))
	}
	for _, p := range tcp {
		if p.Name == want || strconv.Itoa(p.Port) == want {
			return p, nil
		}
	}
	return ServicePort{}, fmt.Errorf("%s=%q matches no TCP port of the Service", AnnotationPort, want)
}

// instanceID builds a valid, stable Atlas ID (letters, digits, . _ -; at
// most 128 characters), hashing the tail when it would be too long.
func instanceID(cluster, namespace, service, who string) string {
	raw := "k8s-" + cluster + "-" + namespace + "-" + service + "-" + who
	var b strings.Builder
	for _, r := range raw {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	id := b.String()
	if len(id) > 128 {
		sum := sha256.Sum256([]byte(raw))
		id = id[:111] + "-" + hex.EncodeToString(sum[:8])
	}
	return id
}

func (c *Controller) reconcile(ctx context.Context) {
	desired := c.desired()
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		want := desired[id]
		have, known := c.applied[id]
		if known && reflect.DeepEqual(have.spec, want.spec) {
			if want.ready && !have.ready && want.spec.TTLSeconds > 0 {
				c.beat(ctx, want)
			}
			c.applied[id] = want
			continue
		}
		if err := c.register(ctx, want.spec); err != nil {
			c.log.Warn("register", "service_id", id, "err", err)
			continue
		}
		if !known {
			c.log.Info("registered", "service", want.spec.Name, "service_id", id, "address", want.spec.Address)
		} else if want.spec.Draining && !have.spec.Draining {
			c.log.Info("draining terminating pod", "service", want.spec.Name, "service_id", id)
		}
		c.applied[id] = want
		if want.ready && want.spec.TTLSeconds > 0 {
			c.beat(ctx, want)
		}
	}
	for id, have := range c.applied {
		if _, ok := desired[id]; ok {
			continue
		}
		callCtx, cancel := c.call(ctx)
		err := c.atlas.Delete(callCtx, id)
		cancel()
		if err != nil && !atlas.IsCode(err, "not_found") {
			c.log.Warn("deregister", "service_id", id, "err", err)
			continue
		}
		delete(c.applied, id)
		c.log.Info("deregistered", "service", have.spec.Name, "service_id", id)
	}
}

func (c *Controller) register(ctx context.Context, spec atlas.RegisterRequest) error {
	callCtx, cancel := c.call(ctx)
	defer cancel()
	_, err := c.atlas.Register(callCtx, spec)
	return err
}

func (c *Controller) heartbeats(ctx context.Context) {
	for _, inst := range c.applied {
		if inst.ready && !inst.spec.Draining && inst.spec.TTLSeconds > 0 {
			c.beat(ctx, inst)
		}
	}
}

func (c *Controller) beat(ctx context.Context, inst instance) {
	callCtx, cancel := c.call(ctx)
	_, err := c.atlas.Heartbeat(callCtx, inst.spec.ID)
	cancel()
	if atlas.IsCode(err, "not_found") {
		if err = c.register(ctx, inst.spec); err == nil {
			callCtx, cancel = c.call(ctx)
			_, err = c.atlas.Heartbeat(callCtx, inst.spec.ID)
			cancel()
		}
	}
	if err != nil {
		c.log.Warn("heartbeat", "service_id", inst.spec.ID, "err", err)
	}
}

// resync removes registrations with this controller's tag that are no longer
// desired (for example after a restart) and restores ones deleted elsewhere.
func (c *Controller) resync(ctx context.Context) {
	c.reconcile(ctx)
	callCtx, cancel := c.call(ctx)
	existing, err := c.atlas.List(callCtx, atlas.Filter{Tags: []string{c.OwnerTag()}})
	cancel()
	if err != nil {
		c.log.Warn("list registrations", "err", err)
		return
	}
	present := map[string]bool{}
	for _, s := range existing {
		present[s.ID] = true
		if _, ok := c.applied[s.ID]; ok {
			continue
		}
		callCtx, cancel := c.call(ctx)
		err := c.atlas.Delete(callCtx, s.ID)
		cancel()
		if err == nil || atlas.IsCode(err, "not_found") {
			c.log.Info("removed stale registration", "service", s.Name, "service_id", s.ID)
		}
	}
	for id := range c.applied {
		if !present[id] {
			delete(c.applied, id) // re-registered on the next reconcile
		}
	}
	if len(c.applied) < len(c.desired()) {
		c.reconcile(ctx)
	}
}
