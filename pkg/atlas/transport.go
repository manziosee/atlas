package atlas

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Transport is an http.RoundTripper that load-balances requests to Atlas
// services. A request to http://payments.atlas/charge is sent to a routable
// instance of "payments", chosen round-robin; requests to other hosts pass
// through to the base transport unchanged.
//
//	httpClient := &http.Client{Transport: atlas.NewTransport(client, nil, atlas.InstanceOptions{Region: "eu"})}
//	resp, err := httpClient.Get("http://payments.atlas/health")
//
// When an instance refuses the connection, it is skipped for EjectFor and a
// request that can be replayed (no body, or one with GetBody) is retried once
// on another instance.
type Transport struct {
	// Suffix marks hosts to resolve through Atlas. The default is ".atlas".
	Suffix string
	// EjectFor is how long a failing instance is skipped. Default 30s.
	EjectFor time.Duration

	client *Client
	base   http.RoundTripper
	opts   InstanceOptions

	mu       sync.Mutex
	watchers map[string]*InstanceWatcher
}

// NewTransport routes "<service>.atlas" hosts through client. A nil base uses
// http.DefaultTransport.
func NewTransport(client *Client, base http.RoundTripper, opts InstanceOptions) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{Suffix: ".atlas", EjectFor: 30 * time.Second, client: client, base: base, opts: opts, watchers: make(map[string]*InstanceWatcher)}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !strings.HasSuffix(host, t.Suffix) || len(host) == len(t.Suffix) {
		return t.base.RoundTrip(req)
	}
	watcher, err := t.watcher(req.Context(), strings.TrimSuffix(host, t.Suffix))
	if err != nil {
		return nil, err
	}
	attempts := 1
	if replayable(req) {
		attempts = 2
	}
	var lastErr error
	for range attempts {
		instance, err := watcher.Pick()
		if err != nil {
			return nil, err
		}
		out, err := rewrite(req, instance.Address)
		if err != nil {
			return nil, err
		}
		resp, err := t.base.RoundTrip(out)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if req.Context().Err() != nil {
			return nil, err
		}
		watcher.Eject(instance.ID, t.EjectFor)
	}
	return nil, lastErr
}

// Close stops every service watcher the transport started.
func (t *Transport) Close() {
	t.mu.Lock()
	watchers := t.watchers
	t.watchers = make(map[string]*InstanceWatcher)
	t.mu.Unlock()
	for _, w := range watchers {
		w.Close()
	}
}

func (t *Transport) watcher(ctx context.Context, name string) (*InstanceWatcher, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w, ok := t.watchers[name]; ok {
		return w, nil
	}
	w, err := t.client.WatchInstances(ctx, name, t.opts)
	if err != nil {
		return nil, err
	}
	t.watchers[name] = w
	return w, nil
}

// rewrite clones req to target address, rewinding a replayable body.
func rewrite(req *http.Request, address string) (*http.Request, error) {
	out := req.Clone(req.Context())
	out.URL.Host = address
	out.Host = ""
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		out.Body = body
	}
	return out, nil
}

func replayable(req *http.Request) bool {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	// Other methods are retried only when the caller marked them idempotent.
	return req.Header.Get("Idempotency-Key") != ""
}
