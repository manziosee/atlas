package atlas

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoInstances means a watched service has no instance that may receive
// traffic: none is registered, or every one is unhealthy or draining.
var ErrNoInstances = errors.New("atlas: no routable instances")

// InstanceOptions narrows which instances a watcher returns.
type InstanceOptions struct {
	// Region prefers instances in this region; others are used only when the
	// region has none.
	Region string
	// Tags requires every listed tag.
	Tags []string
	// Refresh re-reads the full list on this interval in addition to reacting
	// to events, which bounds staleness if the event stream drops. Zero means
	// 30 seconds.
	Refresh time.Duration
}

// InstanceWatcher keeps the routable instances of one service up to date.
// It reads the list once, follows registry events for that service, and
// re-reads periodically. Use Pick for client-side round-robin balancing.
// It is safe for concurrent use.
type InstanceWatcher struct {
	client *Client
	name   string
	opts   InstanceOptions

	mu        sync.RWMutex
	instances []Service
	listeners []func([]Service)
	ejected   map[string]time.Time

	next    atomic.Uint64
	refresh chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

// WatchInstances starts watching service name. It returns after the first
// read so Instances is populated; an error means the first read failed.
// Call Close to stop the watcher.
func (c *Client) WatchInstances(ctx context.Context, name string, opts InstanceOptions) (*InstanceWatcher, error) {
	if opts.Refresh <= 0 {
		opts.Refresh = 30 * time.Second
	}
	w := &InstanceWatcher{
		client: c, name: name, opts: opts,
		ejected: make(map[string]time.Time), refresh: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if err := w.reload(ctx); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel = cancel
	go w.run(runCtx)
	return w, nil
}

// Instances returns the instances that may receive traffic, in a stable
// order: healthy ones (or unknown-health ones if none are healthy), never
// draining or unhealthy ones, preferring the configured region.
func (w *InstanceWatcher) Instances() []Service {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return slices.Clone(w.instances)
}

// Pick returns the next instance in round-robin order, skipping instances
// recently reported by Eject while others remain.
func (w *InstanceWatcher) Pick() (Service, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.instances) == 0 {
		return Service{}, ErrNoInstances
	}
	now := time.Now()
	start := w.next.Add(1)
	for i := range len(w.instances) {
		candidate := w.instances[(start+uint64(i))%uint64(len(w.instances))] // #nosec G115 -- len is never negative.
		if until, bad := w.ejected[candidate.ID]; !bad || now.After(until) {
			return candidate, nil
		}
	}
	return w.instances[start%uint64(len(w.instances))], nil // every instance ejected: try anyway
}

// Eject skips an instance in Pick for d, for example after a connection
// failure, until the registry reports its health.
func (w *InstanceWatcher) Eject(id string, d time.Duration) {
	w.mu.Lock()
	w.ejected[id] = time.Now().Add(d)
	w.mu.Unlock()
}

// OnChange calls fn with the new instance list after every change, and once
// immediately with the current list.
func (w *InstanceWatcher) OnChange(fn func([]Service)) {
	w.mu.Lock()
	w.listeners = append(w.listeners, fn)
	current := slices.Clone(w.instances)
	w.mu.Unlock()
	fn(current)
}

// Refresh asks the watcher to re-read the list soon.
func (w *InstanceWatcher) Refresh() {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
}

// Close stops the watcher and waits for it to exit.
func (w *InstanceWatcher) Close() {
	w.cancel()
	<-w.done
}

func (w *InstanceWatcher) run(ctx context.Context) {
	defer close(w.done)
	go func() {
		// Events only signal that something changed; the list is always
		// re-read, so a missed or reordered event cannot leave it wrong.
		_ = w.client.WatchWithOptions(ctx, WatchOptions{Kinds: []string{"service"}, Service: w.name, Reconnect: true}, func(Event) error {
			w.Refresh()
			return nil
		})
	}()
	ticker := time.NewTicker(w.opts.Refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.refresh:
			// Coalesce a burst of events into one read.
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = w.reload(readCtx) // keep the last good list on failure
		cancel()
	}
}

func (w *InstanceWatcher) reload(ctx context.Context) error {
	all, err := w.client.List(ctx, Filter{Name: w.name, Tags: w.opts.Tags})
	if err != nil {
		return err
	}
	routable := selectRoutable(all, w.opts.Region)
	w.mu.Lock()
	changed := !slices.EqualFunc(routable, w.instances, func(a, b Service) bool {
		return a.ID == b.ID && a.Address == b.Address && a.Revision == b.Revision
	})
	w.instances = routable
	for id := range w.ejected {
		if !slices.ContainsFunc(routable, func(s Service) bool { return s.ID == id }) {
			delete(w.ejected, id)
		}
	}
	listeners := slices.Clone(w.listeners)
	w.mu.Unlock()
	if changed {
		for _, fn := range listeners {
			fn(slices.Clone(routable))
		}
	}
	return nil
}

// selectRoutable applies the server's routing rules: no draining or unhealthy
// instances, healthy before unknown, and the preferred region when it has any.
func selectRoutable(all []Service, region string) []Service {
	pick := func(health Health) []Service {
		var out []Service
		for _, s := range all {
			if s.Health == health && !s.Draining {
				out = append(out, s)
			}
		}
		return out
	}
	candidates := pick(HealthHealthy)
	if len(candidates) == 0 {
		candidates = pick(HealthUnknown)
	}
	if region != "" {
		var local []Service
		for _, s := range candidates {
			if s.Region == region {
				local = append(local, s)
			}
		}
		if len(local) > 0 {
			candidates = local
		}
	}
	slices.SortFunc(candidates, func(a, b Service) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return candidates
}
