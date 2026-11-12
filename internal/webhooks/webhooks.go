// Package webhooks delivers registry and membership events to HTTP endpoints.
//
// A webhook belongs to the node it was created on, and only that node
// delivers it, so every event observed by the node is sent once. Each webhook
// has its own worker and bounded queue: deliveries to one endpoint are
// ordered and retried with exponential backoff, and a slow endpoint never
// delays another or the node's event stream.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/storage"
)

// Request headers sent with every delivery.
const (
	HeaderWebhookID = "X-Atlas-Webhook-Id"
	HeaderEvent     = "X-Atlas-Event"
	HeaderDelivery  = "X-Atlas-Delivery"
	HeaderTimestamp = "X-Atlas-Timestamp"
	HeaderSignature = "X-Atlas-Signature"
)

// TestEvent is the type of the event sent by Dispatcher.Test.
const TestEvent = "webhook.test"

// Store reads the node's configured webhooks.
type Store interface {
	Webhooks(context.Context) ([]storage.Webhook, error)
}

// Options tunes delivery; zero values select the defaults.
type Options struct {
	// Client sends deliveries. The default refuses redirects and connections
	// to link-local addresses such as cloud metadata endpoints.
	Client *http.Client
	// Attempts per event, including the first (default 8).
	Attempts int
	// BaseDelay doubles after each failed attempt up to MaxDelay
	// (defaults 1s and 5m).
	BaseDelay, MaxDelay time.Duration
	// QueueSize bounds undelivered events per webhook (default 1024); when
	// full, new events are dropped and counted.
	QueueSize int
	// Timeout bounds each delivery request (default 10s).
	Timeout time.Duration
	// UserAgent identifies the node in deliveries.
	UserAgent string
}

// Status reports a webhook's delivery counters since the node started.
type Status struct {
	Delivered     uint64    `json:"delivered"`
	Failed        uint64    `json:"failed"`
	Dropped       uint64    `json:"dropped"`
	Pending       int       `json:"pending"`
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	LastFailureAt time.Time `json:"last_failure_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

// Result is the outcome of one delivery attempt.
type Result struct {
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Dispatcher fans events out to the node's webhooks.
type Dispatcher struct {
	store  Store
	broker *events.Broker
	log    *slog.Logger
	opts   Options

	mu      sync.Mutex
	running bool
	workers map[string]*worker
}

// New creates a dispatcher; call Run to start delivering.
func New(store Store, broker *events.Broker, log *slog.Logger, opts Options) *Dispatcher {
	if opts.Attempts < 1 {
		opts.Attempts = 8
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = time.Second
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 5 * time.Minute
	}
	if opts.QueueSize < 1 {
		opts.QueueSize = 1024
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "Atlas-Webhook"
	}
	if opts.Client == nil {
		opts.Client = NewClient()
	}
	return &Dispatcher{store: store, broker: broker, log: log, opts: opts, workers: map[string]*worker{}}
}

// Run delivers events until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()
	if err := d.Reload(ctx); err != nil {
		d.log.Warn("could not load webhooks", "err", err)
	}
	sub, _, _ := d.broker.Subscribe("", d.opts.QueueSize)
	lastID := ""
	defer func() { sub.Close() }()
	for {
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case event, ok := <-sub.C:
			if !ok {
				// This reader fell behind the broker: resume after the last
				// routed event, or note the gap when it cannot be filled.
				var backlog []events.Event
				var reset bool
				sub, backlog, reset = d.broker.Subscribe(lastID, d.opts.QueueSize)
				if reset {
					d.log.Warn("webhook dispatcher missed events; deliveries skipped some changes")
				}
				for _, missed := range backlog {
					lastID = missed.ID
					d.route(missed)
				}
				continue
			}
			lastID = event.ID
			d.route(event)
		}
	}
}

// Reload re-reads the configured webhooks, starting and stopping workers to
// match. It is called after a webhook is created or deleted.
func (d *Dispatcher) Reload(ctx context.Context) error {
	hooks, err := d.store.Webhooks(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return nil // Run loads them when it starts; after it ends, nothing delivers.
	}
	keep := make(map[string]bool, len(hooks))
	for _, hook := range hooks {
		keep[hook.ID] = true
		if _, ok := d.workers[hook.ID]; ok {
			continue
		}
		// Workers outlive the request that triggered a reload: they keep its
		// values but not its cancellation, and stop through cancel when the
		// webhook is deleted or Run ends.
		workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		w := &worker{hook: hook, queue: make(chan events.Event, d.opts.QueueSize), cancel: cancel, done: make(chan struct{})}
		d.workers[hook.ID] = w
		go d.work(workerCtx, w)
	}
	for id, w := range d.workers {
		if !keep[id] {
			w.cancel()
			delete(d.workers, id)
		}
	}
	return nil
}

// Status returns delivery counters by webhook ID.
func (d *Dispatcher) Status() map[string]Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]Status, len(d.workers))
	for id, w := range d.workers {
		w.mu.Lock()
		status := w.status
		w.mu.Unlock()
		status.Pending = len(w.queue)
		out[id] = status
	}
	return out
}

// Test sends one webhook.test event to hook immediately, without retries.
func (d *Dispatcher) Test(ctx context.Context, hook storage.Webhook) Result {
	event := events.Event{ID: "test." + randomHex(8), Type: TestEvent, Kind: "test", At: time.Now().UTC()}
	return d.deliver(ctx, hook, event)
}

func (d *Dispatcher) stopAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = false
	for id, w := range d.workers {
		w.cancel()
		delete(d.workers, id)
	}
}

func (d *Dispatcher) route(event events.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, w := range d.workers {
		if !Matches(w.hook, event) {
			continue
		}
		select {
		case w.queue <- event:
		default:
			w.mu.Lock()
			w.status.Dropped++
			w.mu.Unlock()
		}
	}
}

// Matches reports whether hook wants event. Empty Kinds means every kind;
// Service limits service events to one name and does not affect member events.
func Matches(hook storage.Webhook, event events.Event) bool {
	if len(hook.Kinds) > 0 && !slices.Contains(hook.Kinds, event.Kind) {
		return false
	}
	if hook.Service != "" && event.Kind == events.KindService {
		return event.Service != nil && event.Service.Name == hook.Service
	}
	return true
}

type worker struct {
	hook   storage.Webhook
	queue  chan events.Event
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	status Status
}

func (d *Dispatcher) work(ctx context.Context, w *worker) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-w.queue:
			d.deliverWithRetries(ctx, w, event)
		}
	}
}

func (d *Dispatcher) deliverWithRetries(ctx context.Context, w *worker, event events.Event) {
	delay := d.opts.BaseDelay
	for attempt := 1; ; attempt++ {
		result := d.deliver(ctx, w.hook, event)
		now := time.Now().UTC()
		if result.Error == "" {
			w.mu.Lock()
			w.status.Delivered++
			w.status.LastSuccessAt = now
			w.mu.Unlock()
			return
		}
		retryable := result.StatusCode == 0 || result.StatusCode >= 500 ||
			result.StatusCode == http.StatusRequestTimeout || result.StatusCode == http.StatusTooManyRequests
		if !retryable || attempt >= d.opts.Attempts || ctx.Err() != nil {
			w.mu.Lock()
			w.status.Failed++
			w.status.LastFailureAt = now
			w.status.LastError = result.Error
			w.mu.Unlock()
			d.log.Warn("webhook delivery failed", "webhook", w.hook.ID, "event", event.Type, "attempts", attempt, "err", result.Error)
			return
		}
		w.mu.Lock()
		w.status.LastError = result.Error
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, d.opts.MaxDelay)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, hook storage.Webhook, event events.Event) Result {
	start := time.Now()
	body, err := json.Marshal(event)
	if err != nil {
		return Result{Error: "encode event: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return Result{Error: err.Error()}
	}
	timestamp := strconv.FormatInt(start.Unix(), 10)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", d.opts.UserAgent)
	request.Header.Set(HeaderWebhookID, hook.ID)
	request.Header.Set(HeaderEvent, event.Type)
	request.Header.Set(HeaderDelivery, randomHex(16))
	request.Header.Set(HeaderTimestamp, timestamp)
	request.Header.Set(HeaderSignature, Sign(hook.Secret, timestamp, body))
	response, err := d.opts.Client.Do(request)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return Result{Error: err.Error(), DurationMS: elapsed}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return Result{StatusCode: response.StatusCode, Error: "endpoint returned " + response.Status, DurationMS: elapsed}
	}
	return Result{StatusCode: response.StatusCode, DurationMS: elapsed}
}

// Sign returns the X-Atlas-Signature value for a delivery: "sha256=" and the
// hex HMAC-SHA256, keyed with the webhook secret, of the timestamp header, a
// dot and the raw body. Receivers recompute it, compare in constant time and
// reject stale timestamps to stop replays.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// NewSecret returns a random signing secret.
func NewSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ValidateURL checks a webhook URL: http or https with a host, no
// credentials or fragment, at most 2048 bytes, and not a link-local or
// otherwise unroutable IP literal.
func ValidateURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("url must be at most 2048 characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return errors.New("url must be an http:// or https:// URL with a host")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("url must not contain credentials or a fragment")
	}
	if ip, err := netip.ParseAddr(parsed.Hostname()); err == nil && blocked(ip) {
		return fmt.Errorf("url must not target %s: link-local, multicast and unspecified addresses are refused", ip)
	}
	return nil
}

// NewClient returns the delivery client: no redirects, and no connections to
// link-local (including cloud metadata), multicast or unspecified addresses,
// checked on the resolved address so DNS cannot route around it.
func NewClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(host); err == nil && blocked(ip.Unmap()) {
				return fmt.Errorf("webhook connection to %s refused", ip)
			}
			return nil
		},
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func blocked(ip netip.Addr) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
