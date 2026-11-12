package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/webhooks"
)

func TestWebhookLifecycleAndDelivery(t *testing.T) {
	var mu sync.Mutex
	var deliveries []*http.Request
	var bodies [][]byte
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		deliveries = append(deliveries, r.Clone(context.Background()))
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	h := newHarness(t, func(d *Deps) {
		dispatcher := webhooks.New(d.Registry.(webhooks.Store), d.Events, slog.New(slog.NewTextHandler(io.Discard, nil)),
			webhooks.Options{Client: &http.Client{}, BaseDelay: time.Millisecond})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { dispatcher.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		for d.Events.Subscribers() == 0 {
			time.Sleep(time.Millisecond)
		}
		d.Webhooks = dispatcher
	})

	if forbidden := h.do(http.MethodPost, "/v1/admin/webhooks", writeToken, `{"url":"`+receiver.URL+`"}`); forbidden.Code != http.StatusForbidden {
		t.Fatalf("write token created a webhook: %d", forbidden.Code)
	}
	created := h.do(http.MethodPost, "/v1/admin/webhooks", adminToken, `{"url":"`+receiver.URL+`/hook?token=abc","kinds":["service"],"service":"payments","description":"deploy bot"}`)
	var body struct {
		Webhook struct {
			ID    string   `json:"id"`
			URL   string   `json:"url"`
			Kinds []string `json:"kinds"`
		} `json:"webhook"`
		Secret string `json:"secret"`
	}
	created.json(t, &body)
	if created.Code != http.StatusCreated || !strings.HasPrefix(body.Webhook.ID, "wh_") || !strings.HasPrefix(body.Secret, "whsec_") {
		t.Fatalf("create webhook = %d %s", created.Code, created.Body.String())
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response carrying the secret is cacheable")
	}

	listed := h.do(http.MethodGet, "/v1/admin/webhooks", adminToken, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), body.Secret) || !strings.Contains(listed.Body.String(), body.Webhook.ID) {
		t.Fatalf("list webhooks = %d %s", listed.Code, listed.Body.String())
	}

	// Only service events for "payments" match this webhook.
	h.register(`{"name":"search","address":"10.0.0.1:80"}`)
	h.register(`{"name":"payments","address":"10.0.0.2:80","metadata":{"team":"payments"}}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(deliveries)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook was not delivered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1 (search must be filtered out)", len(deliveries))
	}
	request, payload := deliveries[0], bodies[0]
	mu.Unlock()
	var event events.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != events.ServiceRegistered || event.Service == nil || event.Service.Name != "payments" || event.Service.Metadata["team"] != "payments" {
		t.Fatalf("delivered event = %+v", event)
	}
	if request.URL.Query().Get("token") != "abc" {
		t.Fatal("query string was not preserved")
	}
	if got, want := request.Header.Get(webhooks.HeaderSignature), webhooks.Sign(body.Secret, request.Header.Get(webhooks.HeaderTimestamp), payload); got != want {
		t.Fatalf("signature %q, want %q", got, want)
	}

	tested := h.do(http.MethodPost, "/v1/admin/webhooks/"+body.Webhook.ID+"/test", adminToken, "")
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"delivered":true`) {
		t.Fatalf("test webhook = %d %s", tested.Code, tested.Body.String())
	}

	audit, _, err := h.store.Audit(context.Background(), storage.AuditFilter{Limit: 10})
	if err != nil || len(audit) == 0 || audit[0].Action != "webhook.created" || strings.Contains(string(audit[0].Details), "token=abc") {
		t.Fatalf("audit = %+v %v; want webhook.created without the URL query", audit, err)
	}

	if deleted := h.do(http.MethodDelete, "/v1/admin/webhooks/"+body.Webhook.ID, adminToken, ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete webhook = %d", deleted.Code)
	}
	if again := h.do(http.MethodDelete, "/v1/admin/webhooks/"+body.Webhook.ID, adminToken, ""); again.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", again.Code)
	}
	if missing := h.do(http.MethodPost, "/v1/admin/webhooks/wh_missing/test", adminToken, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("test missing webhook = %d", missing.Code)
	}
}

func TestWebhookValidation(t *testing.T) {
	h := newHarness(t, func(d *Deps) {
		d.Webhooks = webhooks.New(d.Registry.(webhooks.Store), d.Events, slog.New(slog.NewTextHandler(io.Discard, nil)), webhooks.Options{})
	})
	for _, bad := range []string{
		`{"url":"ftp://example.com/hook"}`,
		`{"url":"http://169.254.169.254/latest/meta-data/"}`,
		`{"url":"https://user:pw@example.com/"}`,
		`{"url":"https://example.com/","kinds":["everything"]}`,
		`{"url":"https://example.com/","kinds":["service","service"]}`,
		`{"url":"https://example.com/","service":"bad name!"}`,
		`{"url":"https://example.com/","description":"` + strings.Repeat("d", 257) + `"}`,
	} {
		if got := h.do(http.MethodPost, "/v1/admin/webhooks", adminToken, bad); got.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400: %s", bad, got.Code, got.Body.String())
		}
	}
	for i := range storage.MaxWebhooks {
		if got := h.do(http.MethodPost, "/v1/admin/webhooks", adminToken, `{"url":"https://example.com/`+string(rune('a'+i%26))+`"}`); got.Code != http.StatusCreated {
			t.Fatalf("webhook %d = %d", i, got.Code)
		}
	}
	if over := h.do(http.MethodPost, "/v1/admin/webhooks", adminToken, `{"url":"https://example.com/over"}`); over.Code != http.StatusConflict {
		t.Fatalf("webhook over the limit = %d, want 409", over.Code)
	}
}

func TestWebhooksUnavailableWithoutDispatcher(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.do(http.MethodGet, "/v1/admin/webhooks", adminToken, ""); got.Code != http.StatusNotImplemented {
		t.Fatalf("webhooks without a dispatcher = %d, want 501", got.Code)
	}
}
