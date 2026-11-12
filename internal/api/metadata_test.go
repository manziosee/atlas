package api

import (
	"net/http"
	"testing"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/storage"
)

func TestServiceMetadataRegisterFilterAndReplace(t *testing.T) {
	h := newHarness(t, nil)
	service, rec := h.register(`{"id":"pay-1","name":"payments","address":"10.0.0.1:80","metadata":{"team":"payments","protocol":"grpc"}}`)
	if rec.Code != http.StatusCreated || service.Metadata["team"] != "payments" || service.Metadata["protocol"] != "grpc" {
		t.Fatalf("register with metadata = %d %+v", rec.Code, service)
	}
	if _, rec := h.register(`{"id":"pay-2","name":"payments","address":"10.0.0.2:80","metadata":{"team":"search"}}`); rec.Code != http.StatusCreated {
		t.Fatalf("second register = %d %s", rec.Code, rec.Body.String())
	}

	var page struct {
		Services []storage.Service `json:"services"`
	}
	listed := h.do(http.MethodGet, "/v1/services?name=payments&meta=team:payments&meta=protocol:grpc", readToken, "")
	listed.json(t, &page)
	if listed.Code != http.StatusOK || len(page.Services) != 1 || page.Services[0].ID != "pay-1" {
		t.Fatalf("metadata filter = %d %+v", listed.Code, page.Services)
	}
	lookup := h.do(http.MethodGet, "/v1/lookup/payments?meta=team:search", readToken, "")
	var picked storage.Service
	lookup.json(t, &picked)
	if lookup.Code != http.StatusOK || picked.ID != "pay-2" {
		t.Fatalf("lookup with metadata filter = %d %+v", lookup.Code, picked)
	}
	if none := h.do(http.MethodGet, "/v1/lookup/payments?meta=team:nobody", readToken, ""); none.Code != http.StatusServiceUnavailable || none.errorCode(t) != httperr.CodeNoHealthyInstance {
		t.Fatalf("lookup matching no metadata = %d, want 503 no_healthy_instance", none.Code)
	}

	// PATCH replaces the whole map; other fields keep their values.
	patched := h.do(http.MethodPatch, "/v1/services/pay-1", writeToken, `{"metadata":{"team":"billing"}}`)
	var updated storage.Service
	patched.json(t, &updated)
	if patched.Code != http.StatusOK || len(updated.Metadata) != 1 || updated.Metadata["team"] != "billing" || updated.Address != "10.0.0.1:80" {
		t.Fatalf("metadata PATCH = %d %+v", patched.Code, updated)
	}
	// A PATCH without metadata leaves it alone.
	other := h.do(http.MethodPatch, "/v1/services/pay-1", writeToken, `{"version":"2"}`)
	var afterOther storage.Service
	other.json(t, &afterOther)
	if afterOther.Metadata["team"] != "billing" {
		t.Fatalf("unrelated PATCH dropped metadata: %+v", afterOther)
	}
	cleared := h.do(http.MethodPatch, "/v1/services/pay-1", writeToken, `{"metadata":{}}`)
	var afterClear storage.Service
	cleared.json(t, &afterClear)
	if cleared.Code != http.StatusOK || afterClear.Metadata != nil {
		t.Fatalf("clearing metadata = %d %+v", cleared.Code, afterClear)
	}
	// PUT replaces every field, so metadata it omits is removed.
	h.do(http.MethodPatch, "/v1/services/pay-1", writeToken, `{"metadata":{"team":"billing"}}`)
	replaced := h.do(http.MethodPut, "/v1/services/pay-1", writeToken, `{"name":"payments","address":"10.0.0.1:80"}`)
	var afterPut storage.Service
	replaced.json(t, &afterPut)
	if replaced.Code != http.StatusOK || afterPut.Metadata != nil {
		t.Fatalf("PUT without metadata = %d %+v", replaced.Code, afterPut)
	}

	for _, bad := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/services", `{"name":"x","address":"10.0.0.9:80","metadata":{"bad key":"v"}}`},
		{http.MethodPatch, "/v1/services/pay-2", `{"metadata":{".hidden":"v"}}`},
		{http.MethodGet, "/v1/services?meta=team", ""},
		{http.MethodGet, "/v1/services?meta=team:a&meta=team:b", ""},
		{http.MethodGet, "/v1/lookup/payments?meta=bad%20key:v", ""},
	} {
		token := writeToken
		if bad.method == http.MethodGet {
			token = readToken
		}
		if got := h.do(bad.method, bad.path, token, bad.body); got.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400: %s", bad.method, bad.path, got.Code, got.Body.String())
		}
	}
}
