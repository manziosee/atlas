package atlas

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/api"
	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

type singleNode struct{}

func (singleNode) LocalNode() membership.Member {
	return membership.Member{ID: "node-1", Addr: "10.0.0.1:7946", Region: "eu", State: membership.StateAlive}
}
func (n singleNode) Members() []membership.Member { return []membership.Member{n.LocalNode()} }
func (singleNode) Evict(string) (membership.Member, error) {
	return membership.Member{}, membership.ErrUnknownMember
}

// newLiveClient serves the real API handler over a SQLite store.
func newLiveClient(t *testing.T) *Client {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	broker := events.NewBroker(256)
	store.OnChange(func(change storage.Change) {
		if event, ok := events.FromServiceChange(change); ok {
			broker.Publish(event)
		}
	})
	token := "integration-token-integration-token"
	server := httptest.NewServer(api.NewHandler(api.Deps{
		Cluster: singleNode{}, Registry: store, Events: broker,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientAgainstLiveAPI(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := client.RegisterIdempotent(ctx, "deploy-1", RegisterRequest{Name: "orders", Address: "10.1.0.1:80", TTLSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := client.RegisterIdempotent(ctx, "deploy-1", RegisterRequest{Name: "orders", Address: "10.1.0.1:80", TTLSeconds: 30})
	if err != nil || retry.ID != first.ID || retry.Revision != 1 {
		t.Fatalf("idempotent retry = %+v %v", retry, err)
	}
	beat, err := client.Heartbeat(ctx, first.ID)
	if err != nil || beat.Health != HealthHealthy {
		t.Fatalf("Heartbeat = %+v %v", beat, err)
	}
	version := "v2"
	updated, err := client.Update(ctx, first.ID, ServicePatch{Version: &version}, beat.Revision)
	if err != nil || updated.Version != "v2" {
		t.Fatalf("Update = %+v %v", updated, err)
	}
	_, err = client.Update(ctx, first.ID, ServicePatch{Version: &version}, 1)
	if !IsCode(err, "precondition_failed") {
		t.Fatalf("stale Update err = %v", err)
	}
	if _, err := client.Replace(ctx, "orders-fixed", RegisterRequest{Name: "orders", Address: "10.1.0.2:80", Tags: []string{"blue"}}, 0); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := client.Register(ctx, RegisterRequest{Name: "orders", Address: "10.1.0." + string(rune('3'+i)) + ":80"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := client.ListPage(ctx, Filter{Name: "orders"}, "", 2)
	if err != nil || len(page.Services) != 2 || page.NextCursor == "" {
		t.Fatalf("ListPage = %d %q %v", len(page.Services), page.NextCursor, err)
	}
	all, err := client.List(ctx, Filter{Name: "orders"})
	if err != nil || len(all) != 5 {
		t.Fatalf("List followed %d services, err %v; want 5", len(all), err)
	}
	blue, err := client.List(ctx, Filter{Tags: []string{"blue"}})
	if err != nil || len(blue) != 1 || blue[0].ID != "orders-fixed" {
		t.Fatalf("tag filter = %+v %v", blue, err)
	}

	node, err := client.Node(ctx)
	if err != nil || node.Node.ID != "node-1" {
		t.Fatalf("Node = %+v %v", node, err)
	}
	if members, err := client.Members(ctx); err != nil || len(members) != 1 {
		t.Fatalf("Members = %+v %v", members, err)
	}
	creds, err := client.Credentials(ctx)
	if err != nil || len(creds) != 1 || !creds[0].Current || creds[0].Source != "static" {
		t.Fatalf("Credentials = %+v %v", creds, err)
	}
	var snapshot bytes.Buffer
	if n, err := client.Backup(ctx, &snapshot); err != nil || n == 0 || !bytes.HasPrefix(snapshot.Bytes(), []byte("SQLite format 3")) {
		t.Fatalf("Backup = %d bytes, %v", n, err)
	}
	if _, err := client.EvictNode(ctx, "ghost"); !IsCode(err, "not_found") {
		t.Fatalf("EvictNode(ghost) err = %v", err)
	}
	if err := client.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(ctx, first.ID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" || apiErr.RequestID == "" {
		t.Fatalf("Get after delete err = %#v", err)
	}
}

func TestWatchResumesAfterDisconnect(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := make(chan Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- client.WatchWithOptions(ctx, WatchOptions{Kinds: []string{"service"}, Reconnect: true}, func(event Event) error {
			got <- event
			return nil
		})
	}()
	register := func(address string) {
		// Retry until the watcher is attached and sees the registration.
		deadline := time.After(3 * time.Second)
		for {
			if _, err := client.Register(ctx, RegisterRequest{Name: "watched", Address: address}); err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-got:
				if event.Type != "service.registered" || event.Service == nil || event.ID == "" {
					t.Fatalf("event = %+v", event)
				}
				return
			case <-time.After(100 * time.Millisecond):
			case <-deadline:
				t.Fatal("watcher never received a registration")
			}
		}
	}
	register("10.2.0.1:80")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("WatchWithOptions returned %v, want context.Canceled", err)
	}

	stop := errors.New("stop")
	err := client.WatchWithOptions(context.Background(), WatchOptions{LastEventID: "bogus.1"}, func(event Event) error {
		if event.Type != "reset" {
			t.Errorf("first event after unknown ID = %s, want reset", event.Type)
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("callback error = %v, want it returned unchanged", err)
	}
}
