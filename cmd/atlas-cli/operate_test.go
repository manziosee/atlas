package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/api"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

type cliTestCluster struct{}

func (cliTestCluster) LocalNode() membership.Member {
	return membership.Member{ID: "cli-node", Addr: "10.0.0.1:7946", State: membership.StateAlive}
}
func (c cliTestCluster) Members() []membership.Member { return []membership.Member{c.LocalNode()} }

func TestListAllFollowsPagesAndSendReportsCodes(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i := range 1005 {
		if _, err := store.Register(context.Background(), storage.Service{
			ID: fmt.Sprintf("svc-%04d", i), Name: "bulk", NodeID: "cli-node", Address: "10.0.0.2:80", Tags: []string{"t"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	token := strings.Repeat("c", 40)
	server := httptest.NewServer(api.NewHandler(api.Deps{
		Cluster: cliTestCluster{}, Registry: store,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Second
	c := &client{addr: server.URL, token: token, http: httpClient}

	all, err := c.listAll(url.Values{"tag": {"t"}})
	if err != nil || len(all) != 1005 {
		t.Fatalf("listAll = %d services, %v; want 1005 across two pages", len(all), err)
	}
	if err := c.send(http.MethodDelete, "/v1/services/svc-0000", nil); err != nil {
		t.Fatal(err)
	}
	err = c.send(http.MethodPost, "/v1/services/svc-0001/heartbeat", nil)
	if err == nil || !strings.Contains(err.Error(), "(no_ttl)") {
		t.Fatalf("heartbeat without TTL error = %v, want the no_ttl code", err)
	}
	if _, _, err := positional("evict", []string{"-addr", "x"}, "node ID"); err == nil {
		t.Fatal("positional accepted a flag as the node ID")
	}
}
