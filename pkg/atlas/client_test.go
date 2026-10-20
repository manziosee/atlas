package atlas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRegistryOperationsAndAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/services":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"instance-1","name":"api","address":"10.0.0.1:8080","health":"unknown"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/lookup/api":
			if r.URL.Query().Get("region") != "jnb" || r.URL.Query().Get("key") != "customer 1" {
				t.Errorf("lookup query = %v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `{"id":"instance-1","name":"api","address":"10.0.0.1:8080","health":"healthy"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/services/instance-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.Register(context.Background(), RegisterRequest{Name: "api", Address: "10.0.0.1:8080"})
	if err != nil || created.ID != "instance-1" {
		t.Fatalf("Register() = %+v, %v", created, err)
	}
	selected, err := client.Lookup(context.Background(), "api", "jnb", "customer 1")
	if err != nil || selected.Health != HealthHealthy {
		t.Fatalf("Lookup() = %+v, %v", selected, err)
	}
	if err := client.Delete(context.Background(), selected.ID); err != nil {
		t.Fatal(err)
	}
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), "missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("Get() error = %v, want typed 404 APIError", err)
	}
}

func TestClientWatchStreamsEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("watch headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": connected\n\ndata: {\"type\":\"join\",\"member\":{\"id\":\"node-a\",\"state\":\"alive\"}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	err = client.Watch(context.Background(), func(event Event) error { got = event; return nil })
	if !errors.Is(err, io.EOF) || got.Type != "join" || got.Member.ID != "node-a" {
		t.Fatalf("Watch() = %+v, %v", got, err)
	}
}

func TestNewClientRejectsNonOriginURL(t *testing.T) {
	if _, err := NewClient("https://user@example.test/api", "", nil); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("NewClient() err = %v, want invalid origin", err)
	}
}
