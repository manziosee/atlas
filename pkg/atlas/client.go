// Package atlas provides a typed Go client for an Atlas node's HTTP API.
// For gRPC, use the generated client in github.com/manziosee/atlas/api/v1.
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
	"strconv"
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
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NodeID      string   `json:"node_id"`
	Address     string   `json:"address"`
	Region      string   `json:"region"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
	Health      Health   `json:"health"`
	Revision    uint64   `json:"revision"`
	TTLSeconds  int64    `json:"ttl_seconds,omitempty"`
	// Health-check tuning; zero values mean the node defaults.
	CheckIntervalSeconds int64 `json:"check_interval_seconds,omitempty"`
	UnhealthyThreshold   int   `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold     int   `json:"healthy_threshold,omitempty"`
	// Draining instances stay registered but receive no traffic.
	Draining    bool      `json:"draining,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	HeartbeatAt time.Time `json:"heartbeat_at,omitzero"`
}

// RegisterRequest describes a service instance. ID is optional: supply a
// stable ID (or use RegisterIdempotent) so retries and redeploys update the
// same record. Use either HealthCheck or TTLSeconds with Heartbeat.
// HealthCheck is an http(s)://, tcp:// or grpc(s):// URL on Address.
type RegisterRequest struct {
	ID                   string   `json:"id,omitempty"`
	Name                 string   `json:"name"`
	Address              string   `json:"address"`
	Version              string   `json:"version,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	HealthCheck          string   `json:"health_check,omitempty"`
	TTLSeconds           int64    `json:"ttl_seconds,omitempty"`
	CheckIntervalSeconds int64    `json:"check_interval_seconds,omitempty"`
	UnhealthyThreshold   int      `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold     int      `json:"healthy_threshold,omitempty"`
	Draining             bool     `json:"draining,omitempty"`
}

// ServicePatch changes only the non-nil fields.
type ServicePatch struct {
	Name        *string   `json:"name,omitempty"`
	Address     *string   `json:"address,omitempty"`
	Version     *string   `json:"version,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	HealthCheck *string   `json:"health_check,omitempty"`
	TTLSeconds  *int64    `json:"ttl_seconds,omitempty"`

	CheckIntervalSeconds *int64 `json:"check_interval_seconds,omitempty"`
	UnhealthyThreshold   *int   `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold     *int   `json:"healthy_threshold,omitempty"`
	Draining             *bool  `json:"draining,omitempty"`
}

// Filter narrows List and ListPage. A service must carry every tag in Tags.
type Filter struct {
	Name    string
	Region  string
	Health  Health
	Version string
	NodeID  string
	Tags    []string
}

// Page is one page of a listing; NextCursor is empty on the last page.
type Page struct {
	Services   []Service `json:"services"`
	NextCursor string    `json:"next_cursor"`
}

// Event is a membership event (Member set) or a registry event (Service set).
// Types: join, update, reap, service.registered, service.updated,
// service.health, service.deregistered, and reset.
type Event struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	Member  Member    `json:"member"`
	Service *Service  `json:"service,omitempty"`
}

type Member struct {
	ID          string    `json:"id"`
	Addr        string    `json:"addr"`
	Region      string    `json:"region"`
	State       string    `json:"state"`
	Incarnation uint64    `json:"incarnation"`
	Since       time.Time `json:"since,omitzero"`
	APIAddr     string    `json:"api_addr,omitempty"`
}

type NodeInfo struct {
	Node    Member `json:"node"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Credential struct {
	ID        string    `json:"id"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	Source    string    `json:"source"`
	Expired   bool      `json:"expired"`
	Current   bool      `json:"current"`
}

type EvictResult struct {
	NodeID          string  `json:"node_id"`
	ServicesRemoved int     `json:"services_removed"`
	Member          *Member `json:"member,omitempty"`
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

// Register creates a service owned by the node that receives the call.
// Re-sending identical content for the same ID is a no-op.
func (c *Client) Register(ctx context.Context, input RegisterRequest) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodPost, "/v1/services", input, &service, nil)
	return service, err
}

// RegisterIdempotent registers with an Idempotency-Key, so retrying after a
// timeout returns the same record instead of creating a duplicate.
func (c *Client) RegisterIdempotent(ctx context.Context, key string, input RegisterRequest) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodPost, "/v1/services", input, &service, http.Header{"Idempotency-Key": {key}})
	return service, err
}

// Replace creates or fully replaces the service with id. A non-zero
// ifRevision makes the call fail with code precondition_failed when the
// service changed since it was read.
func (c *Client) Replace(ctx context.Context, id string, input RegisterRequest, ifRevision uint64) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodPut, "/v1/services/"+url.PathEscape(id), input, &service, ifMatch(ifRevision))
	return service, err
}

// Update changes the fields set in patch.
func (c *Client) Update(ctx context.Context, id string, patch ServicePatch, ifRevision uint64) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodPatch, "/v1/services/"+url.PathEscape(id), patch, &service, ifMatch(ifRevision))
	return service, err
}

// Drain stops new traffic to an instance (lookups, DNS and client load
// balancers skip it) while it stays registered, for example before a deploy.
func (c *Client) Drain(ctx context.Context, id string) (Service, error) {
	draining := true
	return c.Update(ctx, id, ServicePatch{Draining: &draining}, 0)
}

// Undrain returns a drained instance to rotation.
func (c *Client) Undrain(ctx context.Context, id string) (Service, error) {
	draining := false
	return c.Update(ctx, id, ServicePatch{Draining: &draining}, 0)
}

// Heartbeat refreshes a TTL service. A NotFound error means the service
// expired; register it again.
func (c *Client) Heartbeat(ctx context.Context, id string) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(id)+"/heartbeat", nil, &service, nil)
	return service, err
}

// ListPage returns one page; pass the previous page's NextCursor to continue.
// limit 0 uses the server default (100).
func (c *Client) ListPage(ctx context.Context, filter Filter, cursor string, limit int) (Page, error) {
	query := url.Values{}
	for key, value := range map[string]string{
		"name": filter.Name, "region": filter.Region, "health": string(filter.Health),
		"version": filter.Version, "node_id": filter.NodeID, "cursor": cursor,
	} {
		if value != "" {
			query.Set(key, value)
		}
	}
	for _, tag := range filter.Tags {
		query.Add("tag", tag)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/services"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page Page
	err := c.request(ctx, http.MethodGet, path, nil, &page, nil)
	return page, err
}

// List returns every matching service, following pages.
func (c *Client) List(ctx context.Context, filter Filter) ([]Service, error) {
	var services []Service
	cursor := ""
	for {
		page, err := c.ListPage(ctx, filter, cursor, 1000)
		if err != nil {
			return nil, err
		}
		services = append(services, page.Services...)
		if page.NextCursor == "" {
			return services, nil
		}
		cursor = page.NextCursor
	}
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
	err := c.request(ctx, http.MethodGet, path, nil, &service, nil)
	return service, err
}

func (c *Client) Get(ctx context.Context, id string) (Service, error) {
	var service Service
	err := c.request(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(id), nil, &service, nil)
	return service, err
}

// Delete deregisters a service. Any node accepts the call when the owner has
// left the cluster; otherwise it must reach the owner or use an admin token.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/v1/services/"+url.PathEscape(id), nil, nil, nil)
}

// Node returns the receiving node's record and build.
func (c *Client) Node(ctx context.Context) (NodeInfo, error) {
	var node NodeInfo
	err := c.request(ctx, http.MethodGet, "/v1/node", nil, &node, nil)
	return node, err
}

// Members returns the receiving node's view of the cluster.
func (c *Client) Members(ctx context.Context) ([]Member, error) {
	var result struct {
		Members []Member `json:"members"`
	}
	err := c.request(ctx, http.MethodGet, "/v1/members", nil, &result, nil)
	return result.Members, err
}

// Credentials lists configured API credentials without secrets (admin scope).
func (c *Client) Credentials(ctx context.Context) ([]Credential, error) {
	var result struct {
		Credentials []Credential `json:"credentials"`
	}
	err := c.request(ctx, http.MethodGet, "/v1/admin/credentials", nil, &result, nil)
	return result.Credentials, err
}

// Backup writes a consistent SQLite snapshot of the node's registry to dst
// (admin scope).
func (c *Client) Backup(ctx context.Context, dst io.Writer) (int64, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/backup", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, responseError(resp)
	}
	return io.Copy(dst, resp.Body)
}

// EvictNode removes a failed node from membership and deletes its services
// (admin scope).
func (c *Client) EvictNode(ctx context.Context, nodeID string) (EvictResult, error) {
	var result EvictResult
	err := c.request(ctx, http.MethodDelete, "/v1/admin/nodes/"+url.PathEscape(nodeID), nil, &result, nil)
	return result, err
}

// WatchOptions selects events. Kinds may contain "member" and "service";
// empty means both. Service limits service events to one name. LastEventID
// resumes after an event the caller already processed.
type WatchOptions struct {
	Kinds       []string
	Service     string
	LastEventID string
	// Reconnect keeps the stream open across disconnects, resuming from the
	// last delivered event, until the context ends or the server rejects the
	// request.
	Reconnect bool
}

// Watch streams membership and registry events until the context is canceled
// or the stream ends (io.EOF). The callback runs serially on the caller's
// goroutine.
func (c *Client) Watch(ctx context.Context, callback func(Event) error) error {
	return c.WatchWithOptions(ctx, WatchOptions{}, callback)
}

// WatchWithOptions is Watch with filtering, resumption and reconnection. A
// "reset" event means missed events could not be replayed: re-read state.
func (c *Client) WatchWithOptions(ctx context.Context, opts WatchOptions, callback func(Event) error) error {
	if callback == nil {
		return errors.New("atlas: Watch requires an event callback")
	}
	lastID := opts.LastEventID
	backoff := 500 * time.Millisecond
	for {
		delivered := false
		err := c.watchOnce(ctx, opts, lastID, func(event Event) error {
			delivered = true
			if event.ID != "" {
				lastID = event.ID
			}
			return callback(event)
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var apiErr *APIError
		var cbErr callbackError
		if errors.As(err, &cbErr) {
			return cbErr.err
		}
		if !opts.Reconnect || errors.As(err, &apiErr) {
			return err
		}
		if delivered {
			backoff = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// callbackError marks errors returned by the caller's callback, which always
// stop the watch.
type callbackError struct{ err error }

func (e callbackError) Error() string { return e.err.Error() }
func (e callbackError) Unwrap() error { return e.err }

func (c *Client) watchOnce(ctx context.Context, opts WatchOptions, lastID string, callback func(Event) error) error {
	query := url.Values{}
	if len(opts.Kinds) > 0 {
		query.Set("kinds", strings.Join(opts.Kinds, ","))
	}
	if opts.Service != "" {
		query.Set("service", opts.Service)
	}
	path := "/v1/events"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
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
	eventType := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				return fmt.Errorf("atlas: decode event: %w", err)
			}
			if event.Type == "" {
				event.Type = eventType
			}
			if err := callback(event); err != nil {
				return callbackError{err: err}
			}
		case line == "":
			eventType = ""
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

func ifMatch(revision uint64) http.Header {
	if revision == 0 {
		return nil
	}
	return http.Header{"If-Match": {strconv.Quote(strconv.FormatUint(revision, 10))}}
}

func (c *Client) request(ctx context.Context, method, path string, input, output any, headers http.Header) error {
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
	for key, values := range headers {
		req.Header[key] = values
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

// APIError is a non-2xx response. Code is the server's stable error code
// (for example not_found, not_owner or precondition_failed); branch on it
// rather than on Message.
type APIError struct {
	StatusCode int
	Status     string
	Code       string
	Message    string
	RequestID  string
	OwnerNode  string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("atlas: %s (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("atlas: %s: %s", e.Status, e.Message)
}

// IsCode reports whether err is an APIError with the given code.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	apiErr := &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Message: strings.TrimSpace(string(body))}
	var decoded struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		RequestID string `json:"request_id"`
		OwnerNode string `json:"owner_node"`
	}
	if json.Unmarshal(body, &decoded) == nil && decoded.Error != "" {
		apiErr.Message, apiErr.Code, apiErr.RequestID, apiErr.OwnerNode = decoded.Error, decoded.Code, decoded.RequestID, decoded.OwnerNode
	}
	return apiErr
}
