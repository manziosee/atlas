package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/storage"
)

type memoryStore struct {
	mu    sync.Mutex
	hooks []storage.Webhook
}

func (s *memoryStore) Webhooks(context.Context) ([]storage.Webhook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storage.Webhook(nil), s.hooks...), nil
}

func (s *memoryStore) set(hooks ...storage.Webhook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = hooks
}

type received struct {
	header http.Header
	body   []byte
	event  events.Event
}

// receiver records deliveries; respond picks each response status in order.
type receiver struct {
	*httptest.Server
	mu      sync.Mutex
	got     []received
	calls   atomic.Int32
	respond func(call int32) int
}

func newReceiver(t *testing.T, respond func(call int32) int) *receiver {
	t.Helper()
	r := &receiver{respond: respond}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		call := r.calls.Add(1)
		body, _ := io.ReadAll(req.Body)
		var event events.Event
		_ = json.Unmarshal(body, &event)
		status := http.StatusNoContent
		if r.respond != nil {
			status = r.respond(call)
		}
		if status < 300 {
			r.mu.Lock()
			r.got = append(r.got, received{header: req.Header.Clone(), body: body, event: event})
			r.mu.Unlock()
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) events() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

func startDispatcher(t *testing.T, store Store, opts Options) (*Dispatcher, *events.Broker) {
	t.Helper()
	broker := events.NewBroker(64)
	if opts.BaseDelay == 0 {
		opts.BaseDelay = 5 * time.Millisecond
	}
	if opts.Client == nil {
		opts.Client = &http.Client{} // httptest listens on loopback, which NewClient allows anyway
	}
	d := New(store, broker, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return broker.Subscribers() == 1 })
	return d, broker
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func serviceEvent(name string, revision uint64) events.Event {
	return events.Event{Type: events.ServiceRegistered, Kind: events.KindService, Service: &storage.Service{ID: name + "-1", Name: name, Revision: revision}}
}

func TestDeliversSignedEventsInOrder(t *testing.T) {
	r := newReceiver(t, nil)
	store := &memoryStore{}
	store.set(storage.Webhook{ID: "wh1", URL: r.URL, Secret: "whsec_test"})
	_, broker := startDispatcher(t, store, Options{})

	for i := 1; i <= 3; i++ {
		broker.Publish(serviceEvent("api", uint64(i)))
	}
	waitFor(t, func() bool { return len(r.events()) == 3 })
	for i, got := range r.events() {
		if got.event.Service.Revision != uint64(i+1) {
			t.Fatalf("delivery %d has revision %d: out of order", i, got.event.Service.Revision)
		}
		want := Sign("whsec_test", got.header.Get(HeaderTimestamp), got.body)
		if got.header.Get(HeaderSignature) != want {
			t.Fatalf("signature %q, want %q", got.header.Get(HeaderSignature), want)
		}
		if got.header.Get(HeaderWebhookID) != "wh1" || got.header.Get(HeaderEvent) != events.ServiceRegistered || got.header.Get(HeaderDelivery) == "" {
			t.Fatalf("delivery headers = %v", got.header)
		}
	}
}

func TestRetriesServerErrorsButNotRejections(t *testing.T) {
	flaky := newReceiver(t, func(call int32) int {
		if call < 3 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	})
	rejecting := newReceiver(t, func(int32) int { return http.StatusBadRequest })
	store := &memoryStore{}
	store.set(
		storage.Webhook{ID: "flaky", URL: flaky.URL, Secret: "s"},
		storage.Webhook{ID: "rejecting", URL: rejecting.URL, Secret: "s"},
	)
	d, broker := startDispatcher(t, store, Options{Attempts: 5})
	broker.Publish(serviceEvent("api", 1))

	waitFor(t, func() bool { return d.Status()["flaky"].Delivered == 1 && d.Status()["rejecting"].Failed == 1 })
	if calls := flaky.calls.Load(); calls != 3 {
		t.Fatalf("flaky endpoint called %d times, want 3 (two 503s, then success)", calls)
	}
	if calls := rejecting.calls.Load(); calls != 1 {
		t.Fatalf("400 response retried: %d calls", calls)
	}
	if status := d.Status()["rejecting"]; !strings.Contains(status.LastError, "400") || status.LastFailureAt.IsZero() {
		t.Fatalf("rejecting status = %+v", status)
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	down := newReceiver(t, func(int32) int { return http.StatusBadGateway })
	store := &memoryStore{}
	store.set(storage.Webhook{ID: "down", URL: down.URL, Secret: "s"})
	d, broker := startDispatcher(t, store, Options{Attempts: 3})
	broker.Publish(serviceEvent("api", 1))
	waitFor(t, func() bool { return d.Status()["down"].Failed == 1 })
	if calls := down.calls.Load(); calls != 3 {
		t.Fatalf("calls = %d, want 3 attempts", calls)
	}
}

func TestFiltersByKindAndService(t *testing.T) {
	all := newReceiver(t, nil)
	payments := newReceiver(t, nil)
	members := newReceiver(t, nil)
	store := &memoryStore{}
	store.set(
		storage.Webhook{ID: "all", URL: all.URL, Secret: "s"},
		storage.Webhook{ID: "payments", URL: payments.URL, Secret: "s", Kinds: []string{events.KindService}, Service: "payments"},
		storage.Webhook{ID: "members", URL: members.URL, Secret: "s", Kinds: []string{events.KindMember}},
	)
	_, broker := startDispatcher(t, store, Options{})
	broker.Publish(serviceEvent("search", 1))
	broker.Publish(serviceEvent("payments", 1))
	broker.Publish(events.Event{Type: "join", Kind: events.KindMember})

	waitFor(t, func() bool { return len(all.events()) == 3 })
	waitFor(t, func() bool { return len(payments.events()) == 1 && len(members.events()) == 1 })
	if got := payments.events()[0].event.Service.Name; got != "payments" {
		t.Fatalf("service filter delivered %q", got)
	}
	if got := members.events()[0].event.Kind; got != events.KindMember {
		t.Fatalf("kind filter delivered %q", got)
	}
}

func TestReloadStartsAndStopsWorkers(t *testing.T) {
	r := newReceiver(t, nil)
	store := &memoryStore{}
	d, broker := startDispatcher(t, store, Options{})

	store.set(storage.Webhook{ID: "late", URL: r.URL, Secret: "s"})
	if err := d.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	broker.Publish(serviceEvent("api", 2))
	waitFor(t, func() bool { return len(r.events()) == 1 })
	if rev := r.events()[0].event.Service.Revision; rev != 2 {
		t.Fatalf("new webhook received revision %d, want 2", rev)
	}

	store.set()
	if err := d.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Status()["late"]; ok {
		t.Fatal("deleted webhook still has a worker")
	}
	broker.Publish(serviceEvent("api", 3))
	time.Sleep(100 * time.Millisecond)
	if calls := r.calls.Load(); calls != 1 {
		t.Fatalf("deleted webhook still received deliveries: %d calls", calls)
	}
}

func TestDropsWhenQueueIsFull(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { close(release); slow.Close() })
	store := &memoryStore{}
	store.set(storage.Webhook{ID: "slow", URL: slow.URL, Secret: "s"})
	d, broker := startDispatcher(t, store, Options{QueueSize: 2, Timeout: 10 * time.Second})
	for i := 1; i <= 10; i++ {
		broker.Publish(serviceEvent("api", uint64(i)))
	}
	waitFor(t, func() bool { return d.Status()["slow"].Dropped > 0 })
}

func TestTestDeliveryReportsResult(t *testing.T) {
	r := newReceiver(t, nil)
	d := New(&memoryStore{}, events.NewBroker(4), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Client: &http.Client{}})
	result := d.Test(context.Background(), storage.Webhook{ID: "t", URL: r.URL, Secret: "s"})
	if result.Error != "" || result.StatusCode != http.StatusNoContent {
		t.Fatalf("test delivery = %+v", result)
	}
	if got := r.events(); len(got) != 1 || got[0].event.Type != TestEvent {
		t.Fatalf("test event = %+v", got)
	}
	failed := d.Test(context.Background(), storage.Webhook{ID: "t", URL: "http://127.0.0.1:1/", Secret: "s"})
	if failed.Error == "" {
		t.Fatal("unreachable endpoint reported success")
	}
}

func TestValidateURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hooks.example.com/atlas":                  true,
		"http://10.0.0.5:8080/hook":                        true,
		"http://localhost:9000/hook":                       true,
		"ftp://example.com/hook":                           false,
		"https:///nohost":                                  false,
		"https://user:pass@example.com/hook":               false,
		"https://example.com/hook#frag":                    false,
		"http://169.254.169.254/latest/meta-data/":         false,
		"http://[fe80::1]/hook":                            false,
		"http://0.0.0.0/hook":                              false,
		"https://example.com/" + strings.Repeat("a", 2048): false,
	} {
		if err := ValidateURL(raw); (err == nil) != ok {
			t.Errorf("ValidateURL(%q) error = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestClientRefusesLinkLocalAndRedirects(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/", nil)
	if response, err := client.Do(request); err == nil {
		_ = response.Body.Close()
		t.Fatal("connection to the metadata address was allowed")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("unexpected error: %v", err)
	}

	redirect := httptest.NewServer(http.RedirectHandler("http://example.com/", http.StatusFound))
	t.Cleanup(redirect.Close)
	request, _ = http.NewRequestWithContext(ctx, http.MethodPost, redirect.URL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("redirect was followed: status %d", response.StatusCode)
	}
}
