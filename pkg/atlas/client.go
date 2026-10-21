// Package atlas provides a typed Go client for an Atlas node's HTTP API.
package atlas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Health string

const (
	HealthUnknown   Health = "unknown"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

type Service struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	NodeID      string    `json:"node_id"`
	Address     string    `json:"address"`
	Region      string    `json:"region"`
	Version     string    `json:"version,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	HealthCheck string    `json:"health_check,omitempty"`
	Health      Health    `json:"health"`
	Revision    uint64    `json:"revision"`
	UpdatedAt   time.Time `json:"updated_at"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
}

type RegisterRequest struct {
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
}

type Filter struct {
	Name   string
	Region string
	Health Health
}

type Event struct {
	Type   string    `json:"type"`
	Member Member    `json:"member"`
	At     time.Time `json:"at"`
}

type Member struct {
	ID          string    `json:"id"`
	Addr        string    `json:"addr"`
	Region      string    `json:"region"`
	State       string    `json:"state"`
	Incarnation uint64    `json:"incarnation"`
	Since       time.Time `json:"since,omitzero"`
}

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient creates an HTTP API client. Supply an http.Client with TLS options
// when using a private CA or mutual TLS; nil uses the default transport. Use
// context deadlines for bounded RPCs; Watch remains open until canceled.
func NewClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.Path != "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("atlas: base URL must be an absolute HTTP or HTTPS origin")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{base: base, token: token, http: httpClient}, nil
}

func (c *Client) Register(ctx context.Context, input RegisterRequest) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodPost, "/v1/services", input, &service)
	return service, err
}

func (c *Client) List(ctx context.Context, filter Filter) ([]Service, error) {
	query := url.Values{}
	if filter.Name != "" {
		query.Set("name", filter.Name)
	}
	if filter.Region != "" {
		query.Set("region", filter.Region)
	}
	if filter.Health != "" {
		query.Set("health", string(filter.Health))
	}
	path := "/v1/services"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result struct {
		Services []Service `json:"services"`
	}
	err := c.request(ctx, http.MethodGet, path, nil, &result)
	return result.Services, err
}

func (c *Client) Lookup(ctx context.Context, name, region, key string) (Service, error) {
	query := url.Values{}
	if region != "" {
		query.Set("region", region)
	}
	if key != "" {
		query.Set("key", key)
	}
	path := "/v1/lookup/" + url.PathEscape(name)
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var service Service
	err := c.request(ctx, http.MethodGet, path, nil, &service)
	return service, err
}

func (c *Client) Get(ctx context.Context, id string) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(id), nil, &service)
	return service, err
}

func (c *Client) Delete(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/v1/services/"+url.PathEscape(id), nil, nil)
}

// Watch streams membership events until the context is canceled or the stream
// fails. The callback runs serially on the caller's goroutine.
func (c *Client) Watch(ctx context.Context, callback func(Event) error) error {
	if callback == nil {
		return errors.New("atlas: Watch requires an event callback")
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return fmt.Errorf("atlas: decode event: %w", err)
		}
		if err := callback(event); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return io.EOF
}

func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(output); err != nil {
		return fmt.Errorf("atlas: decode response: %w", err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || !strings.HasPrefix(relative.Path, "/") {
		return nil, errors.New("atlas: invalid API path")
	}
	endpoint := *c.base
	endpoint.Path = relative.Path
	endpoint.RawPath = relative.RawPath
	endpoint.RawQuery = relative.RawQuery
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

type APIError struct {
	StatusCode int
	Status     string
	Message    string
}

func (e *APIError) Error() string { return fmt.Sprintf("atlas: %s: %s", e.Status, e.Message) }

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Message: strings.TrimSpace(string(body))}
}
