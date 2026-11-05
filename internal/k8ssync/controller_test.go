package k8ssync

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

// fakeKube serves list and watch for Services and EndpointSlices.
type fakeKube struct {
	mu      sync.Mutex
	objects map[string][]any // resource -> items
	watches map[string]chan watchEvent
}

func newFakeKube(t *testing.T) (*fakeKube, *Kube) {
	t.Helper()
	f := &fakeKube{objects: map[string][]any{}, watches: map[string]chan watchEvent{
		"services": make(chan watchEvent, 16), "endpointslices": make(chan watchEvent, 16),
	}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	kube, err := NewKube(KubeConfig{URL: server.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return f, kube
}

func (f *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer t" {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	resource := "services"
	if strings.HasSuffix(r.URL.Path, "/endpointslices") {
		resource = "endpointslices"
	}
	if r.URL.Query().Get("watch") == "1" {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-f.watches[resource]:
				_ = json.NewEncoder(w).Encode(e)
				w.(http.Flusher).Flush()
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"resourceVersion": "1"}, "items": f.objects[resource]})
}

func (f *fakeKube) push(resource, kind string, object any) {
	raw, _ := json.Marshal(object)
	f.watches[resource] <- watchEvent{Type: kind, Object: raw}
}

func service(ns, name string, annotations map[string]string, ports ...map[string]any) map[string]any {
	if len(ports) == 0 {
		ports = []map[string]any{{"name": "http", "port": 80, "protocol": "TCP"}}
	}
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": ns, "annotations": annotations, "resourceVersion": "2"},
		"spec":     map[string]any{"type": "ClusterIP", "clusterIP": "10.96.0.10", "ports": ports},
	}
}

type ep struct {
	pod                         string
	ip                          string
	ready, serving, terminating bool
}

func slice(ns, svc string, port int, endpoints ...ep) map[string]any {
	var items []map[string]any
	for _, e := range endpoints {
		items = append(items, map[string]any{
			"addresses":  []string{e.ip},
			"conditions": map[string]bool{"ready": e.ready, "serving": e.serving, "terminating": e.terminating},
			"targetRef":  map[string]string{"kind": "Pod", "name": e.pod},
		})
	}
	return map[string]any{
		"metadata":    map[string]any{"name": svc + "-abc", "namespace": ns, "labels": map[string]string{"kubernetes.io/service-name": svc}, "resourceVersion": "3"},
		"addressType": "IPv4",
		"ports":       []map[string]any{{"name": "http", "port": port, "protocol": "TCP"}},
		"endpoints":   items,
	}
}

type singleNode struct{}

func (singleNode) LocalNode() membership.Member {
	return membership.Member{ID: "node-1", Region: "eu", State: membership.StateAlive}
}
func (n singleNode) Members() []membership.Member { return []membership.Member{n.LocalNode()} }

func newAtlas(t *testing.T) *atlas.Client {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	token := "k8s-test-token-k8s-test-token-k8s"
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
	deadline := time.Now().Add(6 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestControllerMirrorsServiceEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	kube, client := newFakeKube(t)
	registry := newAtlas(t)

	on := map[string]string{AnnotationRegister: "true", AnnotationName: "payments", AnnotationTags: "team-a", AnnotationVersion: "2.1"}
	kube.objects["services"] = []any{service("shop", "payments-svc", on), service("shop", "ignored", nil)}
	kube.objects["endpointslices"] = []any{
		slice("shop", "payments-svc", 8080, ep{"p1", "10.1.0.1", true, true, false}, ep{"p2", "10.1.0.2", true, true, false}, ep{"p3", "10.1.0.3", false, false, false}),
		slice("shop", "ignored", 9000, ep{"x1", "10.1.0.9", true, true, false}),
	}
	// Left behind by an earlier run of this controller, and one it doesn't own.
	for _, r := range []atlas.RegisterRequest{
		{ID: "k8s-test-shop-gone-p0", Name: "payments", Address: "10.1.0.250:8080", TTLSeconds: 30, Tags: []string{"atlas-k8s:test"}},
		{ID: "manual", Name: "payments", Address: "10.1.0.251:8080", TTLSeconds: 30},
	} {
		if _, err := registry.Register(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	c := New(Config{Cluster: "test", TTL: 3 * time.Second, ResyncInterval: 300 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, client, registry)
	go func() { _ = c.Run(ctx) }()

	id := func(pod string) string { return instanceID("test", "shop", "payments-svc", pod) }
	get := func(id string) (atlas.Service, bool) {
		s, err := registry.Get(ctx, id)
		return s, err == nil
	}
	eventually(t, "ready pods registered healthy", func() bool {
		a, okA := get(id("p1"))
		b, okB := get(id("p2"))
		return okA && okB && a.Health == atlas.HealthHealthy && b.Health == atlas.HealthHealthy
	})
	p1, _ := get(id("p1"))
	if p1.Name != "payments" || p1.Address != "10.1.0.1:8080" || p1.Version != "2.1" || p1.TTLSeconds != 3 {
		t.Fatalf("p1 = %+v", p1)
	}
	for _, tag := range []string{"atlas-k8s:test", "k8s-namespace:shop", "k8s-service:payments-svc", "k8s-pod:p1", "team-a"} {
		if !strings.Contains(strings.Join(p1.Tags, ","), tag) {
			t.Errorf("p1 missing tag %q: %v", tag, p1.Tags)
		}
	}
	// Pods are registered one after another, so p3 may follow p1 and p2.
	eventually(t, "not-ready pod registered", func() bool { _, ok := get(id("p3")); return ok })
	if p3, _ := get(id("p3")); p3.Health != atlas.HealthUnknown {
		t.Fatalf("not-ready pod health = %s, want unknown (registered without heartbeats)", p3.Health)
	}
	if _, ok := get(instanceID("test", "shop", "ignored", "x1")); ok {
		t.Fatal("service without the annotation was registered")
	}
	eventually(t, "stale registration removed", func() bool { _, ok := get("k8s-test-shop-gone-p0"); return !ok })
	if _, ok := get("manual"); !ok {
		t.Fatal("controller removed a service it doesn't own")
	}

	// Rollout: p2 terminates (still serving), p1 is gone.
	kube.push("endpointslices", "MODIFIED", slice("shop", "payments-svc", 8080, ep{"p2", "10.1.0.2", false, true, true}, ep{"p3", "10.1.0.3", true, true, false}))
	eventually(t, "terminating pod drained", func() bool { s, ok := get(id("p2")); return ok && s.Draining })
	eventually(t, "removed pod deregistered", func() bool { _, ok := get(id("p1")); return !ok })
	eventually(t, "pod that became ready is healthy", func() bool { s, ok := get(id("p3")); return ok && s.Health == atlas.HealthHealthy })

	// Turning the annotation off removes everything.
	kube.push("services", "MODIFIED", service("shop", "payments-svc", map[string]string{AnnotationRegister: "false"}))
	eventually(t, "opted-out service removed", func() bool {
		_, a := get(id("p2"))
		_, b := get(id("p3"))
		return !a && !b
	})
}

func TestInstanceRules(t *testing.T) {
	c := New(Config{Cluster: "prod", TTL: 15 * time.Second}, nil, nil)
	decode := func(v any, out any) {
		raw, _ := json.Marshal(v)
		_ = json.Unmarshal(raw, out)
	}
	var svc Service
	var sl EndpointSlice
	decode(service("ns", "web", map[string]string{AnnotationRegister: "true", AnnotationHealth: "/healthz"}), &svc)
	decode(slice("ns", "web", 8080, ep{"w1", "10.0.0.1", true, true, false}), &sl)
	got, err := c.instancesFor(svc, []EndpointSlice{sl})
	if err != nil || len(got) != 1 {
		t.Fatalf("instances = %v %v", got, err)
	}
	for _, inst := range got {
		if inst.spec.HealthCheck != "http://10.0.0.1:8080/healthz" || inst.spec.TTLSeconds != 0 {
			t.Fatalf("health = %+v", inst.spec)
		}
	}

	decode(service("ns", "web", map[string]string{AnnotationRegister: "true", AnnotationMode: "service", AnnotationHealth: "tcp"}), &svc)
	got, _ = c.instancesFor(svc, nil)
	for _, inst := range got {
		if inst.spec.Address != "web.ns.svc.cluster.local:80" || inst.spec.HealthCheck != "tcp://web.ns.svc.cluster.local:80" {
			t.Fatalf("service mode = %+v", inst.spec)
		}
	}

	two := service("ns", "web", map[string]string{AnnotationRegister: "true"}, map[string]any{"name": "http", "port": 80}, map[string]any{"name": "grpc", "port": 90})
	decode(two, &svc)
	if _, err := c.instancesFor(svc, nil); err == nil || !strings.Contains(err.Error(), AnnotationPort) {
		t.Fatalf("two ports without atlas.io/port: %v", err)
	}
	svc.Metadata.Annotations[AnnotationPort] = "90"
	if p, err := servicePort(svc); err != nil || p.Name != "grpc" {
		t.Fatalf("port by number = %+v %v", p, err)
	}
	svc.Metadata.Annotations[AnnotationHealth] = "ping"
	if _, err := c.instancesFor(svc, nil); err == nil {
		t.Fatal("bad health annotation accepted")
	}

	long := instanceID("cluster", strings.Repeat("n", 63), strings.Repeat("s", 63), "pod/with:odd chars")
	if len(long) > 128 || strings.ContainsAny(long, "/: ") {
		t.Fatalf("id %q (%d)", long, len(long))
	}
	if instanceID("c", "ns", "svc", "p") != "k8s-c-ns-svc-p" {
		t.Fatal("short ids should stay readable")
	}
}

func TestInClusterNeedsConfiguration(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := InCluster(KubeConfig{}); err == nil || !strings.Contains(err.Error(), "ATLAS_K8S_API_URL") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "fd00::1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	cfg, err := InCluster(KubeConfig{})
	if err != nil || cfg.URL != "https://[fd00::1]:443" || !strings.HasSuffix(cfg.TokenFile, "/token") {
		t.Fatalf("cfg = %+v %v", cfg, err)
	}
}
