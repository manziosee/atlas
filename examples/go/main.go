package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	serviceName = "example-go"
	ttlSeconds  = 15
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

type service struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

type apiError struct {
	Message string `json:"error"`
	Code    string `json:"code"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "client" {
		if err := callService(context.Background()); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runService(); err != nil {
		log.Fatal(err)
	}
}

func runService() error {
	endpoint, token, err := atlasConfig()
	if err != nil {
		return err
	}
	address := envOr("SERVICE_ADDRESS", "127.0.0.1:8085")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid SERVICE_ADDRESS %q: %w", address, err)
	}
	listenAddress := envOr("SERVICE_LISTEN_ADDR", net.JoinHostPort("0.0.0.0", port))
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("get hostname: %w", err)
	}
	idempotencyKey := serviceName + "-" + hostname

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"service": serviceName, "language": "Go"})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: listenAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen for service requests: %w", err)
	}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	registered, err := register(ctx, endpoint, token, address, idempotencyKey)
	if err != nil {
		_ = server.Close()
		return fmt.Errorf("register with Atlas: %w", err)
	}
	log.Printf("registered %s at %s as %s", serviceName, address, registered.ID)

	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(ttlSeconds * time.Second / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				updated, err := heartbeat(ctx, endpoint, token, registered.ID)
				if err != nil {
					log.Printf("heartbeat failed: %v", err)
					continue
				}
				if updated != nil {
					registered = updated
					log.Printf("re-registered expired service as %s", registered.ID)
				}
			}
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serverErrors:
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := server.Shutdown(shutdownContext); err != nil {
		cancel()
		_ = server.Close()
		return fmt.Errorf("shut down service: %w", err)
	}
	stop()
	<-heartbeatDone
	cancel()
	deregisterContext, cancelDeregister := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDeregister()
	if err := deregister(deregisterContext, endpoint, token, registered.ID); err != nil {
		return fmt.Errorf("deregister from Atlas: %w", err)
	}
	if serveErr != nil {
		return fmt.Errorf("serve service: %w", serveErr)
	}
	log.Printf("deregistered %s", registered.ID)
	return nil
}

func callService(ctx context.Context) error {
	endpoint, token, err := atlasConfig()
	if err != nil {
		return err
	}
	var selected service
	if _, err := atlasRequest(ctx, http.MethodGet, endpoint+"/v1/lookup/"+serviceName, token, nil, "", &selected); err != nil {
		return fmt.Errorf("look up %s: %w", serviceName, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+selected.Address+"/", nil)
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call selected instance %s: %w", selected.Address, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("service returned %s", response.Status)
	}
	_, err = io.Copy(os.Stdout, response.Body)
	return err
}

func register(ctx context.Context, endpoint, token, address, idempotencyKey string) (*service, error) {
	body := map[string]any{
		"name": serviceName, "address": address, "ttl_seconds": ttlSeconds,
		"version": "1.0.0", "tags": []string{"example", "go"},
	}
	var registered service
	if _, err := atlasRequest(ctx, http.MethodPost, endpoint+"/v1/services", token, body, idempotencyKey, &registered); err != nil {
		return nil, err
	}
	return &registered, nil
}

func heartbeat(ctx context.Context, endpoint, token, id string) (*service, error) {
	var updated service
	_, err := atlasRequest(ctx, http.MethodPost, endpoint+"/v1/services/"+id+"/heartbeat", token, nil, "", &updated)
	var apiErr *requestError
	if errors.As(err, &apiErr) && apiErr.Code == "not_found" {
		address := envOr("SERVICE_ADDRESS", "127.0.0.1:8085")
		hostname, hostErr := os.Hostname()
		if hostErr != nil {
			return nil, hostErr
		}
		return register(ctx, endpoint, token, address, serviceName+"-"+hostname)
	}
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func deregister(ctx context.Context, endpoint, token, id string) error {
	_, err := atlasRequest(ctx, http.MethodDelete, endpoint+"/v1/services/"+id, token, nil, "", nil)
	var apiErr *requestError
	if errors.As(err, &apiErr) && apiErr.Code == "not_found" {
		return nil
	}
	return err
}

type requestError struct {
	Status int
	Code   string
	Msg    string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("Atlas returned HTTP %d (%s): %s", e.Status, e.Code, e.Msg)
}

func atlasRequest(ctx context.Context, method, url, token string, body any, idempotencyKey string, output any) (int, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure apiError
		_ = json.Unmarshal(responseData, &failure)
		return response.StatusCode, &requestError{Status: response.StatusCode, Code: failure.Code, Msg: failure.Message}
	}
	if output != nil && len(responseData) > 0 {
		if err := json.Unmarshal(responseData, output); err != nil {
			return response.StatusCode, fmt.Errorf("decode Atlas response: %w", err)
		}
	}
	return response.StatusCode, nil
}

func atlasConfig() (string, string, error) {
	endpoint := strings.TrimRight(envOr("ATLAS_ADDR", "http://127.0.0.1:8081"), "/")
	token := os.Getenv("ATLAS_TOKEN")
	if token == "" {
		return "", "", errors.New("ATLAS_TOKEN must contain an Atlas API token")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return "", "", errors.New("ATLAS_ADDR must start with http:// or https://")
	}
	return endpoint, token, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
