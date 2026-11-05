package dockeragent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/pkg/atlas"
)

// Container labels the agent reads. Only LabelName is required.
const (
	LabelName    = "atlas.name"    // service name; its presence opts the container in
	LabelPort    = "atlas.port"    // container port (default: the only exposed port)
	LabelAddress = "atlas.address" // explicit host:port, overriding discovery
	LabelNetwork = "atlas.network" // network whose IP to use when attached to several
	LabelTags    = "atlas.tags"    // comma-separated tags
	LabelVersion = "atlas.version" // version (default: the image tag)
	LabelHealth  = "atlas.health"  // /path (HTTP), tcp, or a full check URL
	LabelEnable  = "atlas.enable"  // "false" opts a labeled container out
)

// Engine is the Docker API the agent uses; *Docker implements it.
type Engine interface {
	Labeled(ctx context.Context, label string) ([]string, error)
	Inspect(ctx context.Context, id string) (Container, error)
	Events(ctx context.Context, opened func(), fn func(Event)) error
}

// Registry is the Atlas API the agent uses; *atlas.Client implements it.
type Registry interface {
	Register(ctx context.Context, input atlas.RegisterRequest) (atlas.Service, error)
	Heartbeat(ctx context.Context, id string) (atlas.Service, error)
	Drain(ctx context.Context, id string) (atlas.Service, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filter atlas.Filter) ([]atlas.Service, error)
}

// Config tunes the agent. Zero values take the defaults noted on each field.
type Config struct {
	// AgentID scopes ownership: every registration carries the tag
	// "atlas-agent:<AgentID>", and reconciliation only removes services with
	// that tag. Use one ID per Docker host (default "docker").
	AgentID string
	// HostAddress, when set, registers HostAddress:<published port> instead of
	// the container's own IP, for clients on other hosts.
	HostAddress string
	// TTL for containers without an atlas.health label (default 15s). The
	// agent heartbeats every TTL/3 while Docker reports the container healthy.
	TTL time.Duration
	// ResyncInterval bounds how long a missed event can go unnoticed
	// (default 1m).
	ResyncInterval time.Duration
	// CallTimeout bounds each Docker or Atlas call (default 10s).
	CallTimeout time.Duration
	Logger      *slog.Logger
}

// Agent registers labeled containers and keeps them in step with Docker:
//
//   - start, unpause or a health change: register (or refresh) the instance;
//   - stop, kill or pause: drain it, so lookups skip it while it shuts down;
//   - die or destroy: deregister it;
//   - containers without atlas.health use a TTL that the agent heartbeats
//     only while the container is running and, if it has a Docker
//     HEALTHCHECK, healthy; with atlas.health the Atlas node checks it.
//
// On start and every ResyncInterval the agent compares Docker with Atlas and
// removes registrations carrying its tag whose container is gone. Stopping
// the agent leaves registrations in place: TTL instances expire on their own,
// and a restarted agent reconciles the rest.
type Agent struct {
	cfg     Config
	docker  Engine
	atlas   Registry
	log     *slog.Logger
	tracked map[string]*instance // by container ID
}

type instance struct {
	spec     atlas.RegisterRequest
	ready    bool // Docker considers it running and healthy
	draining bool
	stopping bool // drained by stop or kill; only a new start undrains it
}

// New creates an agent.
func New(cfg Config, docker Engine, registry Registry) *Agent {
	if cfg.AgentID == "" {
		cfg.AgentID = "docker"
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
	return &Agent{cfg: cfg, docker: docker, atlas: registry, log: cfg.Logger, tracked: map[string]*instance{}}
}

// OwnerTag is the tag on every service this agent registers.
func (a *Agent) OwnerTag() string { return "atlas-agent:" + a.cfg.AgentID }

// ServiceID is the Atlas service ID for a container.
func ServiceID(containerID string) string {
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}
	return "docker-" + containerID
}

// Run watches Docker until ctx ends. A single goroutine owns all state, so
// events, heartbeats and reconciliation never race.
func (a *Agent) Run(ctx context.Context) error {
	events := make(chan Event, 256)
	opened := make(chan struct{}, 1)
	streamDone := make(chan error, 1)
	startStream := func() {
		go func() {
			streamDone <- a.docker.Events(ctx,
				func() { opened <- struct{}{} },
				func(e Event) {
					select {
					case events <- e:
					case <-ctx.Done():
					}
				})
		}()
	}
	startStream()

	heartbeat := time.NewTicker(a.cfg.TTL / 3)
	defer heartbeat.Stop()
	resync := time.NewTicker(a.cfg.ResyncInterval)
	defer resync.Stop()
	var retry <-chan time.Time
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-opened:
			backoff = time.Second
			a.log.Info("watching docker events")
			a.reconcile(ctx)
		case event := <-events:
			a.handle(ctx, event)
		case <-heartbeat.C:
			a.heartbeats(ctx)
		case <-resync.C:
			a.reconcile(ctx)
		case err := <-streamDone:
			if ctx.Err() != nil {
				return nil
			}
			a.log.Warn("docker event stream ended; reconnecting", "err", err, "in", backoff)
			retry = time.After(backoff)
			backoff = min(backoff*2, 30*time.Second)
		case <-retry:
			retry = nil
			startStream()
		}
	}
}

func (a *Agent) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, a.cfg.CallTimeout)
}

func (a *Agent) handle(ctx context.Context, event Event) {
	if event.Type != "" && event.Type != "container" {
		return
	}
	id := event.Actor.ID
	_, known := a.tracked[id]
	if !known && event.Actor.Attributes[LabelName] == "" {
		return
	}
	action, _, _ := strings.Cut(event.Action, ":")
	switch action {
	case "start", "restart", "unpause":
		a.sync(ctx, id, true)
	case "health_status", "update":
		a.sync(ctx, id, false)
	case "kill":
		if stopSignal(event.Actor.Attributes["signal"]) {
			a.drain(ctx, id, true)
		}
	case "stop":
		a.drain(ctx, id, true)
	case "pause":
		a.drain(ctx, id, false)
	case "die", "destroy", "oom":
		a.remove(ctx, id)
	}
}

// stopSignal reports whether a kill event stops the container rather than,
// say, asking it to reload (SIGHUP) or rotate logs (SIGUSR1).
func stopSignal(signal string) bool {
	switch strings.TrimPrefix(strings.ToUpper(signal), "SIG") {
	case "", "TERM", "15", "KILL", "9", "INT", "2", "QUIT", "3":
		return true
	}
	return false
}

// reconcile brings Atlas in line with Docker after startup, a reconnect or a
// missed event.
func (a *Agent) reconcile(ctx context.Context) {
	callCtx, cancel := a.call(ctx)
	ids, err := a.docker.Labeled(callCtx, LabelName)
	cancel()
	if err != nil {
		a.log.Warn("list containers", "err", err)
		return
	}
	running := map[string]bool{}
	for _, id := range ids {
		running[id] = true
		a.sync(ctx, id, false)
	}
	for id := range a.tracked {
		if !running[id] {
			a.remove(ctx, id)
		}
	}

	callCtx, cancel = a.call(ctx)
	services, err := a.atlas.List(callCtx, atlas.Filter{Tags: []string{a.OwnerTag()}})
	cancel()
	if err != nil {
		a.log.Warn("list agent services", "err", err)
		return
	}
	mine := map[string]bool{}
	for _, inst := range a.tracked {
		mine[inst.spec.ID] = true
	}
	for _, service := range services {
		if mine[service.ID] {
			continue
		}
		callCtx, cancel = a.call(ctx)
		err := a.atlas.Delete(callCtx, service.ID)
		cancel()
		if err != nil && !atlas.IsCode(err, "not_found") {
			a.log.Warn("remove stale registration", "service_id", service.ID, "err", err)
			continue
		}
		a.log.Info("removed stale registration", "service", service.Name, "service_id", service.ID)
	}
}

// sync inspects a container and registers it, or removes it when it is gone,
// stopped, opted out or misconfigured. started is true for a (re)start, which
// ends a drain begun by stop or kill.
func (a *Agent) sync(ctx context.Context, id string, started bool) {
	callCtx, cancel := a.call(ctx)
	c, err := a.docker.Inspect(callCtx, id)
	cancel()
	if errors.Is(err, ErrNotFound) {
		a.remove(ctx, id)
		return
	}
	if err != nil {
		a.log.Warn("inspect container", "container", short(id), "err", err)
		return
	}
	labels := c.Config.Labels
	if labels[LabelName] == "" || strings.EqualFold(labels[LabelEnable], "false") {
		a.remove(ctx, id)
		return
	}
	if c.State.Status != "running" && c.State.Status != "paused" {
		a.remove(ctx, id)
		return
	}
	spec, err := a.specFor(c)
	if err != nil {
		a.log.Warn("cannot register container", "container", containerName(c), "err", err)
		a.remove(ctx, id)
		return
	}
	previous, known := a.tracked[id]
	stopping := known && previous.stopping && !started
	draining := c.State.Status == "paused" || stopping
	spec.Draining = draining
	inst := &instance{
		spec:     spec,
		ready:    !draining && (c.State.Health == nil || c.State.Health.Status == "healthy"),
		draining: draining,
		stopping: stopping,
	}
	if err := a.register(ctx, inst); err != nil {
		a.log.Warn("register container", "container", containerName(c), "service", spec.Name, "err", err)
		return
	}
	if !known || previous.spec.Address != spec.Address {
		a.log.Info("registered container", "container", containerName(c), "service", spec.Name, "service_id", spec.ID, "address", spec.Address)
	}
	a.tracked[id] = inst
	if inst.ready && spec.TTLSeconds > 0 {
		a.beat(ctx, inst)
	}
}

func (a *Agent) register(ctx context.Context, inst *instance) error {
	callCtx, cancel := a.call(ctx)
	defer cancel()
	_, err := a.atlas.Register(callCtx, inst.spec)
	return err
}

func (a *Agent) drain(ctx context.Context, id string, stopping bool) {
	inst, ok := a.tracked[id]
	if !ok {
		return
	}
	inst.stopping = inst.stopping || stopping
	if inst.draining {
		return
	}
	inst.draining, inst.ready = true, false
	inst.spec.Draining = true
	callCtx, cancel := a.call(ctx)
	_, err := a.atlas.Drain(callCtx, inst.spec.ID)
	cancel()
	if err != nil && !atlas.IsCode(err, "not_found") {
		a.log.Warn("drain", "service_id", inst.spec.ID, "err", err)
		return
	}
	a.log.Info("draining container", "service", inst.spec.Name, "service_id", inst.spec.ID)
}

func (a *Agent) remove(ctx context.Context, id string) {
	inst, ok := a.tracked[id]
	if !ok {
		return
	}
	callCtx, cancel := a.call(ctx)
	err := a.atlas.Delete(callCtx, inst.spec.ID)
	cancel()
	if err != nil && !atlas.IsCode(err, "not_found") {
		// Keep tracking it; the next reconcile retries.
		a.log.Warn("deregister", "service_id", inst.spec.ID, "err", err)
		return
	}
	delete(a.tracked, id)
	a.log.Info("deregistered container", "service", inst.spec.Name, "service_id", inst.spec.ID)
}

func (a *Agent) heartbeats(ctx context.Context) {
	for _, inst := range a.tracked {
		if inst.ready && !inst.draining && inst.spec.TTLSeconds > 0 {
			a.beat(ctx, inst)
		}
	}
}

// beat refreshes a TTL instance, registering it again if it expired (for
// example while the container was unhealthy or Atlas was unreachable).
func (a *Agent) beat(ctx context.Context, inst *instance) {
	callCtx, cancel := a.call(ctx)
	_, err := a.atlas.Heartbeat(callCtx, inst.spec.ID)
	cancel()
	if atlas.IsCode(err, "not_found") {
		if err = a.register(ctx, inst); err == nil {
			callCtx, cancel = a.call(ctx)
			_, err = a.atlas.Heartbeat(callCtx, inst.spec.ID)
			cancel()
		}
	}
	if err != nil {
		a.log.Warn("heartbeat", "service_id", inst.spec.ID, "err", err)
	}
}

// specFor turns a container's labels and network settings into a
// registration.
func (a *Agent) specFor(c Container) (atlas.RegisterRequest, error) {
	labels := c.Config.Labels
	address, err := a.address(c)
	if err != nil {
		return atlas.RegisterRequest{}, err
	}
	spec := atlas.RegisterRequest{
		ID:      ServiceID(c.ID),
		Name:    strings.TrimSpace(labels[LabelName]),
		Address: address,
		Version: strings.TrimSpace(labels[LabelVersion]),
		Tags:    []string{a.OwnerTag()},
	}
	if spec.Version == "" {
		spec.Version = imageTag(c.Config.Image)
	}
	for _, tag := range strings.Split(labels[LabelTags], ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			spec.Tags = append(spec.Tags, tag)
		}
	}
	switch health := strings.TrimSpace(labels[LabelHealth]); {
	case health == "":
		spec.TTLSeconds = int64(max(a.cfg.TTL/time.Second, 1))
	case strings.EqualFold(health, "tcp"):
		spec.HealthCheck = "tcp://" + address
	case strings.HasPrefix(health, "/"):
		spec.HealthCheck = "http://" + address + health
	case strings.Contains(health, "://"):
		spec.HealthCheck = health
	default:
		return atlas.RegisterRequest{}, fmt.Errorf("%s must be a /path, tcp, or a URL, got %q", LabelHealth, health)
	}
	return spec, nil
}

func (a *Agent) address(c Container) (string, error) {
	labels := c.Config.Labels
	port, err := containerPort(c)
	if explicit := strings.TrimSpace(labels[LabelAddress]); explicit != "" {
		if _, _, splitErr := net.SplitHostPort(explicit); splitErr == nil {
			return explicit, nil
		}
		if err != nil {
			return "", fmt.Errorf("%s %q has no port and %w", LabelAddress, explicit, err)
		}
		return net.JoinHostPort(explicit, port), nil
	}
	if err != nil {
		return "", err
	}
	if c.HostConfig.NetworkMode == "host" {
		host := a.cfg.HostAddress
		if host == "" {
			host = "127.0.0.1"
		}
		return net.JoinHostPort(host, port), nil
	}
	if a.cfg.HostAddress != "" {
		for _, binding := range c.NetworkSettings.Ports[port+"/tcp"] {
			if binding.HostPort != "" {
				return net.JoinHostPort(a.cfg.HostAddress, binding.HostPort), nil
			}
		}
		return "", fmt.Errorf("port %s is not published; publish it (-p) or set %s", port, LabelAddress)
	}
	ip, err := containerIP(c)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, port), nil
}

func containerPort(c Container) (string, error) {
	if port := strings.TrimSpace(c.Config.Labels[LabelPort]); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("%s must be a port number, got %q", LabelPort, port)
		}
		return port, nil
	}
	var tcp []string
	for exposed := range c.Config.ExposedPorts {
		if port, proto, _ := strings.Cut(exposed, "/"); proto == "" || proto == "tcp" {
			tcp = append(tcp, port)
		}
	}
	if len(tcp) == 1 {
		return tcp[0], nil
	}
	return "", fmt.Errorf("set the %s label (the image exposes %d TCP ports)", LabelPort, len(tcp))
}

func containerIP(c Container) (string, error) {
	networks := c.NetworkSettings.Networks
	if name := strings.TrimSpace(c.Config.Labels[LabelNetwork]); name != "" {
		network, ok := networks[name]
		if !ok || network.IPAddress == "" {
			return "", fmt.Errorf("container has no IP on network %q", name)
		}
		return network.IPAddress, nil
	}
	var names []string
	for name, network := range networks {
		if network.IPAddress != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return "", errors.New("container has no IP address; set " + LabelAddress)
	case 1:
		return networks[names[0]].IPAddress, nil
	default:
		return "", fmt.Errorf("container is on several networks (%s); set %s", strings.Join(names, ", "), LabelNetwork)
	}
}

// imageTag returns the tag of an image reference, ignoring "latest" and
// digests, which say nothing about the version.
func imageTag(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return ""
	}
	tag := image[colon+1:]
	if tag == "latest" {
		return ""
	}
	return tag
}

func containerName(c Container) string {
	if name := strings.TrimPrefix(c.Name, "/"); name != "" {
		return name
	}
	return short(c.ID)
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
