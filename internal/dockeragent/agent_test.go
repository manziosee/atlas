package dockeragent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/api"
	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/pkg/atlas"
)

// fakeDocker serves the slice of the Docker Engine API the agent uses.
type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]Container
	events     chan Event
}

func newFakeDocker(t *testing.T) (*fakeDocker, *Docker) {
	t.Helper()
	fake := &fakeDocker{containers: map[string]Container{}, events: make(chan Event, 16)}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := NewDocker("tcp://" + strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return fake, client
}

func (f *fakeDocker) set(c Container) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[c.ID] = c
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, apiVersion)
	switch {
	case path == "/events":
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-f.events:
				_ = json.NewEncoder(w).Encode(event)
				w.(http.Flusher).Flush()
			}
		}
	case path == "/containers/json":
		f.mu.Lock()
		defer f.mu.Unlock()
		var list []map[string]string
		for id, c := range f.containers {
			if c.Config.Labels[LabelName] != "" && (c.State.Status == "running" || c.State.Status == "paused") {
				list = append(list, map[string]string{"Id": id})
			}
		}
		_ = json.NewEncoder(w).Encode(list)
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.containers[strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.NotFound(w, r)
	}
}

// container builds an inspect result from the fields tests care about.
func container(t *testing.T, id, status string, labels map[string]string, networks map[string]string) Container {
	t.Helper()
	nets := map[string]any{}
	for name, ip := range networks {
		nets[name] = map[string]string{"IPAddress": ip}
	}
	raw, _ := json.Marshal(map[string]any{
		"Id": id, "Name": "/" + id,
		"Config":          map[string]any{"Image": "shop/payments:1.4.2", "Labels": labels, "ExposedPorts": map[string]any{"8080/tcp": map[string]any{}}},
		"State":           map[string]any{"Status": status},
		"NetworkSettings": map[string]any{"Networks": nets, "Ports": map[string]any{"8080/tcp": []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "49153"}}}},
	})
	var c Container
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

type singleNode struct{}

func (singleNode) LocalNode() membership.Member {
	return membership.Member{ID: "node-1", Addr: "10.0.0.1:7946", State: membership.StateAlive}
}
func (n singleNode) Members() []membership.Member { return []membership.Member{n.LocalNode()} }
func (singleNode) Evict(string) (membership.Member, error) {
	return membership.Member{}, membership.ErrUnknownMember
}

func newAtlas(t *testing.T) *atlas.Client {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	token := "agent-test-token-agent-test-token"
	server := httptest.NewServer(api.NewHandler(api.Deps{
		Cluster: singleNode{}, Registry: store, Events: events.NewBroker(16),
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	client, err := atlas.NewClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func get(ctx context.Context, client *atlas.Client, id string) (atlas.Service, bool) {
	service, err := client.Get(ctx, id)
	return service, err == nil
}

func TestAgentFollowsContainerLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fake, docker := newFakeDocker(t)
	client := newAtlas(t)

	const id = "0123456789abcdef0123"
	fake.set(container(t, id, "running", map[string]string{LabelName: "payments", LabelTags: "team-a, blue"}, map[string]string{"shop": "172.18.0.5"}))
	fake.set(container(t, "unlabeled00000", "running", map[string]string{"other": "x"}, map[string]string{"shop": "172.18.0.6"}))
	// A registration this agent made for a container that is gone, and one it
	// does not own.
	if _, err := client.Register(ctx, atlas.RegisterRequest{ID: "docker-gone", Name: "old", Address: "172.18.0.9:80", Tags: []string{"atlas-agent:test"}, TTLSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Register(ctx, atlas.RegisterRequest{ID: "manual", Name: "old", Address: "172.18.0.9:80", TTLSeconds: 30}); err != nil {
		t.Fatal(err)
	}

	agent := New(Config{AgentID: "test", TTL: 3 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, docker, client)
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	serviceID := ServiceID(id)
	eventually(t, "registration", func() bool {
		s, ok := get(ctx, client, serviceID)
		return ok && s.Health == atlas.HealthHealthy
	})
	s, _ := get(ctx, client, serviceID)
	if s.Name != "payments" || s.Address != "172.18.0.5:8080" || s.Version != "1.4.2" || s.TTLSeconds != 3 || strings.Join(s.Tags, ",") != "atlas-agent:test,blue,team-a" && strings.Join(s.Tags, ",") != "atlas-agent:test,team-a,blue" {
		t.Fatalf("registered %+v", s)
	}
	eventually(t, "stale registration removal", func() bool { _, ok := get(ctx, client, "docker-gone"); return !ok })
	if _, ok := get(ctx, client, "manual"); !ok {
		t.Fatal("agent removed a service it does not own")
	}
	if _, ok := get(ctx, client, ServiceID("unlabeled00000")); ok {
		t.Fatal("unlabeled container was registered")
	}

	event := func(action string, attrs map[string]string) {
		e := Event{Type: "container", Action: action}
		e.Actor.ID = id
		e.Actor.Attributes = map[string]string{LabelName: "payments"}
		for k, v := range attrs {
			e.Actor.Attributes[k] = v
		}
		fake.events <- e
	}

	// A reload signal is not a stop.
	event("kill", map[string]string{"signal": "1"})
	event("kill", map[string]string{"signal": "15"})
	eventually(t, "drain on stop", func() bool { s, ok := get(ctx, client, serviceID); return ok && s.Draining })
	// A health event while shutting down must not put it back in rotation.
	event("health_status: healthy", nil)
	time.Sleep(100 * time.Millisecond)
	if s, _ := get(ctx, client, serviceID); !s.Draining {
		t.Fatal("health event undrained a stopping container")
	}

	event("die", nil)
	eventually(t, "deregistration on die", func() bool { _, ok := get(ctx, client, serviceID); return !ok })

	// docker start brings it back, undrained.
	event("start", nil)
	eventually(t, "re-registration on start", func() bool { s, ok := get(ctx, client, serviceID); return ok && !s.Draining })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestAgentWaitsForDockerHealth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fake, docker := newFakeDocker(t)
	client := newAtlas(t)

	const id = "feedfacefeedface"
	c := container(t, id, "running", map[string]string{LabelName: "api"}, map[string]string{"bridge": "172.17.0.2"})
	c.State.Health = &struct {
		Status string `json:"Status"`
	}{Status: "starting"}
	fake.set(c)

	agent := New(Config{AgentID: "test", TTL: 3 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, docker, client)
	go func() { _ = agent.Run(ctx) }()

	serviceID := ServiceID(id)
	eventually(t, "registration", func() bool { _, ok := get(ctx, client, serviceID); return ok })
	if s, _ := get(ctx, client, serviceID); s.Health != atlas.HealthUnknown {
		t.Fatalf("container still starting but health = %s", s.Health)
	}

	c.State.Health = &struct {
		Status string `json:"Status"`
	}{Status: "healthy"}
	fake.set(c)
	e := Event{Type: "container", Action: "health_status: healthy"}
	e.Actor.ID = id
	e.Actor.Attributes = map[string]string{LabelName: "api"}
	fake.events <- e
	eventually(t, "healthy after docker health check", func() bool {
		s, ok := get(ctx, client, serviceID)
		return ok && s.Health == atlas.HealthHealthy
	})
}

func TestSpecForLabels(t *testing.T) {
	agent := New(Config{AgentID: "h1", TTL: 15 * time.Second}, nil, nil)
	cases := []struct {
		name     string
		labels   map[string]string
		networks map[string]string
		host     string
		mutate   func(*Container)
		address  string
		health   string
		err      string
	}{
		{name: "defaults", labels: map[string]string{LabelName: "a"}, networks: map[string]string{"n": "10.0.0.2"}, address: "10.0.0.2:8080"},
		{name: "explicit port", labels: map[string]string{LabelName: "a", LabelPort: "9000"}, networks: map[string]string{"n": "10.0.0.2"}, address: "10.0.0.2:9000"},
		{name: "http health", labels: map[string]string{LabelName: "a", LabelHealth: "/healthz"}, networks: map[string]string{"n": "10.0.0.2"}, address: "10.0.0.2:8080", health: "http://10.0.0.2:8080/healthz"},
		{name: "tcp health", labels: map[string]string{LabelName: "a", LabelHealth: "tcp"}, networks: map[string]string{"n": "10.0.0.2"}, address: "10.0.0.2:8080", health: "tcp://10.0.0.2:8080"},
		{name: "bad health", labels: map[string]string{LabelName: "a", LabelHealth: "ping"}, networks: map[string]string{"n": "10.0.0.2"}, err: LabelHealth},
		{name: "published port", labels: map[string]string{LabelName: "a"}, networks: map[string]string{"n": "10.0.0.2"}, host: "192.168.1.10", address: "192.168.1.10:49153"},
		{name: "explicit address", labels: map[string]string{LabelName: "a", LabelAddress: "payments.internal"}, address: "payments.internal:8080"},
		{name: "several networks", labels: map[string]string{LabelName: "a"}, networks: map[string]string{"x": "10.0.0.2", "y": "10.1.0.2"}, err: LabelNetwork},
		{name: "chosen network", labels: map[string]string{LabelName: "a", LabelNetwork: "y"}, networks: map[string]string{"x": "10.0.0.2", "y": "10.1.0.2"}, address: "10.1.0.2:8080"},
		{name: "no port", labels: map[string]string{LabelName: "a"}, networks: map[string]string{"n": "10.0.0.2"}, mutate: func(c *Container) { c.Config.ExposedPorts = nil }, err: LabelPort},
		{name: "host network", labels: map[string]string{LabelName: "a"}, mutate: func(c *Container) { c.HostConfig.NetworkMode = "host" }, address: "127.0.0.1:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := container(t, "c1", "running", tc.labels, tc.networks)
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			agent.cfg.HostAddress = tc.host
			spec, err := agent.specFor(c)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want mention of %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if spec.Address != tc.address || spec.HealthCheck != tc.health {
				t.Fatalf("address %q health %q", spec.Address, spec.HealthCheck)
			}
			if (tc.health == "") != (spec.TTLSeconds == 15) {
				t.Fatalf("ttl = %d with health %q", spec.TTLSeconds, spec.HealthCheck)
			}
		})
	}
}

func TestImageTagAndStopSignal(t *testing.T) {
	for image, want := range map[string]string{
		"nginx": "", "nginx:latest": "", "nginx:1.27": "1.27", "registry:5000/app": "", "registry:5000/app:v2": "v2", "app@sha256:abc": "",
	} {
		if got := imageTag(image); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", image, got, want)
		}
	}
	for signal, want := range map[string]bool{"15": true, "SIGTERM": true, "9": true, "": true, "1": false, "SIGHUP": false, "SIGUSR1": false} {
		if stopSignal(signal) != want {
			t.Errorf("stopSignal(%q) != %v", signal, want)
		}
	}
}
