package atlas

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Webhook is an HTTP endpoint that receives this node's events. It belongs to
// the node it was created on, which delivers it.
type Webhook struct {
	ID          string        `json:"id"`
	URL         string        `json:"url"`
	Kinds       []string      `json:"kinds,omitempty"`
	Service     string        `json:"service,omitempty"`
	Description string        `json:"description,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	Status      WebhookStatus `json:"status"`
}

// WebhookStatus counts deliveries since the node started.
type WebhookStatus struct {
	Delivered     uint64    `json:"delivered"`
	Failed        uint64    `json:"failed"`
	Dropped       uint64    `json:"dropped"`
	Pending       int       `json:"pending"`
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	LastFailureAt time.Time `json:"last_failure_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

// WebhookRequest creates a webhook. Kinds may contain "member" and "service"
// (empty means both); Service limits service events to one name.
type WebhookRequest struct {
	URL         string   `json:"url"`
	Kinds       []string `json:"kinds,omitempty"`
	Service     string   `json:"service,omitempty"`
	Description string   `json:"description,omitempty"`
}

// WebhookTestResult is the outcome of a test delivery.
type WebhookTestResult struct {
	Delivered bool `json:"delivered"`
	Result    struct {
		StatusCode int    `json:"status_code,omitempty"`
		Error      string `json:"error,omitempty"`
		DurationMS int64  `json:"duration_ms"`
	} `json:"result"`
}

// Webhooks lists the node's webhooks and their delivery counters (admin scope).
func (c *Client) Webhooks(ctx context.Context) ([]Webhook, error) {
	var result struct {
		Webhooks []Webhook `json:"webhooks"`
	}
	err := c.request(ctx, http.MethodGet, "/v1/admin/webhooks", nil, &result, nil)
	return result.Webhooks, err
}

// CreateWebhook adds a webhook on the node and returns it with its signing
// secret, which is shown only once (admin scope).
func (c *Client) CreateWebhook(ctx context.Context, req WebhookRequest) (Webhook, string, error) {
	var result struct {
		Webhook Webhook `json:"webhook"`
		Secret  string  `json:"secret"`
	}
	err := c.request(ctx, http.MethodPost, "/v1/admin/webhooks", req, &result, nil)
	return result.Webhook, result.Secret, err
}

// DeleteWebhook removes a webhook (admin scope).
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/v1/admin/webhooks/"+url.PathEscape(id), nil, nil, nil)
}

// TestWebhook sends a webhook.test event to the endpoint now (admin scope).
func (c *Client) TestWebhook(ctx context.Context, id string) (WebhookTestResult, error) {
	var result WebhookTestResult
	err := c.request(ctx, http.MethodPost, "/v1/admin/webhooks/"+url.PathEscape(id)+"/test", nil, &result, nil)
	return result, err
}

// Errors from VerifyWebhook.
var (
	ErrWebhookSignature = errors.New("atlas: webhook signature does not match")
	ErrWebhookStale     = errors.New("atlas: webhook timestamp is outside the allowed window")
)

// VerifyWebhook checks a delivery received by an HTTP handler: the
// X-Atlas-Signature header must match the body under secret, and the
// X-Atlas-Timestamp header must be within maxAge of now (5 minutes when
// zero), which stops replays of captured deliveries. body is the raw request
// body exactly as read.
func VerifyWebhook(secret string, header http.Header, body []byte, maxAge time.Duration) error {
	if maxAge <= 0 {
		maxAge = 5 * time.Minute
	}
	timestamp := header.Get("X-Atlas-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrWebhookStale
	}
	if age := time.Since(time.Unix(seconds, 0)); age > maxAge || age < -maxAge {
		return ErrWebhookStale
	}
	got, ok := strings.CutPrefix(header.Get("X-Atlas-Signature"), "sha256=")
	if !ok {
		return ErrWebhookSignature
	}
	sent, err := hex.DecodeString(got)
	if err != nil {
		return ErrWebhookSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(body)
	if !hmac.Equal(sent, mac.Sum(nil)) {
		return ErrWebhookSignature
	}
	return nil
}
