package atlas

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type countingServer struct {
	*httptest.Server
	hits atomic.Int64
}

func newCountingServer(t *testing.T) *countingServer {
	t.Helper()
	s := &countingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		_, _ = fmt.Fprint(w, "ok")
	}))
	t.Cleanup(s.Close)
	return s
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func TestTransportBalancesDrainsAndRetries(t *testing.T) {
	client := newLiveClient(t)
	ctx := context.Background()
	blue, green := newCountingServer(t), newCountingServer(t)
	blueSvc, err := client.Register(ctx, RegisterRequest{ID: "web-blue", Name: "web", Address: blue.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Register(ctx, RegisterRequest{ID: "web-green", Name: "web", Address: green.Listener.Addr().String()}); err != nil {
		t.Fatal(err)
	}

	transport := NewTransport(client, nil, InstanceOptions{})
	defer transport.Close()
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	get := func() {
		t.Helper()
		resp, err := httpClient.Get("http://web.atlas/")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	for range 10 {
		get()
	}
	if blue.hits.Load() == 0 || green.hits.Load() == 0 {
		t.Fatalf("hits blue=%d green=%d, want both used", blue.hits.Load(), green.hits.Load())
	}

	if _, err := client.Drain(ctx, blueSvc.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		before := blue.hits.Load()
		for range 4 {
			get()
		}
		return blue.hits.Load() == before
	}, "the drained instance to stop receiving traffic")

	// An instance that refuses connections is skipped by retrying elsewhere.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := listener.Addr().String()
	_ = listener.Close()
	if _, err := client.Register(ctx, RegisterRequest{ID: "web-dead", Name: "web", Address: dead}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(transport.watchers["web"].Instances()) == 2 }, "the new instance to be watched")
	for range 6 {
		get() // never fails: the dead instance is retried on green, then ejected
	}

	// Hosts without the suffix pass through untouched.
	resp, err := httpClient.Get(green.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp, err := httpClient.Get("http://missing.atlas/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to a service with no instances succeeded")
	}
}

func TestSelectRoutableRules(t *testing.T) {
	all := []Service{
		{ID: "b", Health: HealthHealthy, Region: "eu"},
		{ID: "a", Health: HealthHealthy, Region: "us"},
		{ID: "c", Health: HealthHealthy, Region: "eu", Draining: true},
		{ID: "d", Health: HealthUnknown, Region: "eu"},
		{ID: "e", Health: HealthUnhealthy, Region: "eu"},
	}
	if got := selectRoutable(all, ""); len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("no region = %+v, want healthy a and b", got)
	}
	if got := selectRoutable(all, "eu"); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("eu = %+v, want b", got)
	}
	if got := selectRoutable(all, "ap"); len(got) != 2 {
		t.Fatalf("region with no instances falls back to all: %+v", got)
	}
	if got := selectRoutable(all[3:], ""); len(got) != 1 || got[0].ID != "d" {
		t.Fatalf("unknown fallback = %+v", got)
	}
}
