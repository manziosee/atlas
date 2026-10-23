package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

var (
	readToken  = strings.Repeat("r", 40)
	writeToken = strings.Repeat("w", 40)
	adminToken = strings.Repeat("a", 40)
)

// testCluster is a local node "n1" plus peers whose states tests control.
type testCluster struct {
	peers   []membership.Member
	evicted []string
}

func (c *testCluster) LocalNode() membership.Member {
	return membership.Member{ID: "n1", Addr: "10.0.0.1:7946", Region: "eu", State: membership.StateAlive}
}

func (c *testCluster) Members() []membership.Member {
	return append([]membership.Member{c.LocalNode()}, c.peers...)
}

func (c *testCluster) Evict(id string) (membership.Member, error) {
	for i, peer := range c.peers {
		if peer.ID != id {
			continue
		}
		if peer.State == membership.StateAlive {
			return peer, membership.ErrMemberAlive
		}
		c.peers[i].State = membership.StateLeft
		c.evicted = append(c.evicted, id)
		return c.peers[i], nil
	}
	if id == "n1" {
		return membership.Member{}, membership.ErrEvictSelf
	}
	return membership.Member{}, membership.ErrUnknownMember
}

type harness struct {
	t       *testing.T
	auth    *security.TokenAuthenticator
	store   *storage.Store
	cluster *testCluster
	broker  *events.Broker
	handler http.Handler
}

func newHarness(t *testing.T, configure func(*Deps)) *harness {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	expiry := time.Now().Add(time.Hour)
	auth, err := security.NewCredentialAuthenticator([]security.Credential{
		{Token: readToken, Scopes: []security.Scope{security.ScopeRead}, ExpiresAt: expiry},
		{Token: writeToken, Scopes: []security.Scope{security.ScopeRead, security.ScopeWrite}, ExpiresAt: expiry},
		{Token: adminToken, Scopes: []security.Scope{security.ScopeAdmin}, ExpiresAt: expiry},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, auth: auth, store: store, cluster: &testCluster{}, broker: events.NewBroker(64)}
	store.OnChange(func(change storage.Change) {
		if event, ok := events.FromServiceChange(change); ok {
			h.broker.Publish(event)
		}
	})
	deps := Deps{
		Cluster: h.cluster, Registry: store, Auth: auth, Events: h.broker,
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if configure != nil {
		configure(&deps)
	}
	h.handler = NewHandler(deps)
	return h
}

type response struct {
	*httptest.ResponseRecorder
}

func (r response) json(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", r.Body.String(), err)
	}
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var body httperr.Body
	r.json(t, &body)
	if body.Error == "" {
		t.Fatalf("error body without message: %s", r.Body.String())
	}
	return body.Code
}

func (h *harness) do(method, path, token, body string, headers ...string) response {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return response{rec}
}

func (h *harness) register(body string, headers ...string) (storage.Service, response) {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/v1/services", writeToken, body, headers...)
	var service storage.Service
	if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
		rec.json(h.t, &service)
	}
	return service, rec
}

func TestRegistrationIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	body := `{"name":"api","address":"10.0.0.5:8080"}`
	first, rec := h.register(body, "Idempotency-Key", "deploy-42")
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/v1/services/"+first.ID || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("first registration = %d %v", rec.Code, rec.Header())
	}
	retry, rec := h.register(body, "Idempotency-Key", "deploy-42")
	if rec.Code != http.StatusOK || retry.ID != first.ID || retry.Revision != 1 {
		t.Fatalf("retry = %d %+v, want same record unchanged", rec.Code, retry)
	}
	other, _ := h.register(body, "Idempotency-Key", "deploy-43")
	if other.ID == first.ID {
		t.Fatal("different idempotency keys produced the same ID")
	}
	if _, rec := h.register(`{"id":"explicit","name":"api","address":"10.0.0.5:8080"}`, "Idempotency-Key", "deploy-42"); rec.Code != http.StatusBadRequest {
		t.Fatalf("id plus mismatched key = %d", rec.Code)
	}

	chosen, rec := h.register(`{"id":"api-blue-1","name":"api","address":"10.0.0.6:8080"}`)
	if rec.Code != http.StatusCreated || chosen.ID != "api-blue-1" {
		t.Fatalf("client-chosen id = %d %+v", rec.Code, chosen)
	}
	if put := h.do(http.MethodPut, "/v1/services/api-blue-1", writeToken, `{"name":"api","address":"10.0.0.6:8080"}`); put.Code != http.StatusOK {
		t.Fatalf("identical PUT = %d", put.Code)
	}
	if put := h.do(http.MethodPut, "/v1/services/api-blue-2", writeToken, `{"name":"api","address":"10.0.0.7:8080"}`); put.Code != http.StatusCreated {
		t.Fatalf("creating PUT = %d", put.Code)
	}
	if put := h.do(http.MethodPut, "/v1/services/api-blue-2", writeToken, `{"id":"other","name":"api","address":"10.0.0.7:8080"}`); put.Code != http.StatusBadRequest {
		t.Fatalf("PUT with mismatched body id = %d", put.Code)
	}
}

func TestPatchAndConditionalUpdates(t *testing.T) {
	h := newHarness(t, nil)
	service, _ := h.register(`{"id":"cart-1","name":"cart","address":"10.0.0.8:80","tags":["blue"]}`)

	patched := h.do(http.MethodPatch, "/v1/services/cart-1", writeToken, `{"tags":["green"],"version":"v2"}`, "If-Match", `"1"`)
	var updated storage.Service
	patched.json(t, &updated)
	if patched.Code != http.StatusOK || updated.Revision != 2 || updated.Version != "v2" || updated.Tags[0] != "green" || updated.Address != service.Address {
		t.Fatalf("PATCH = %d %+v", patched.Code, updated)
	}
	if stale := h.do(http.MethodPatch, "/v1/services/cart-1", writeToken, `{"version":"v3"}`, "If-Match", `"1"`); stale.Code != http.StatusPreconditionFailed || stale.errorCode(t) != httperr.CodePreconditionFailed {
		t.Fatalf("stale PATCH = %d %s", stale.Code, stale.Body.String())
	}
	if bad := h.do(http.MethodPatch, "/v1/services/cart-1", writeToken, `{"address":"nope"}`); bad.Code != http.StatusBadRequest || bad.errorCode(t) != httperr.CodeValidation {
		t.Fatalf("invalid PATCH = %d %s", bad.Code, bad.Body.String())
	}
	if replaced := h.do(http.MethodPut, "/v1/services/cart-1", writeToken, `{"name":"cart","address":"10.0.0.9:80"}`, "If-Match", `"2"`); replaced.Code != http.StatusOK {
		t.Fatalf("conditional PUT = %d %s", replaced.Code, replaced.Body.String())
	}
	if missing := h.do(http.MethodPatch, "/v1/services/missing", writeToken, `{"version":"x"}`); missing.Code != http.StatusNotFound || missing.errorCode(t) != httperr.CodeNotFound {
		t.Fatalf("PATCH missing = %d", missing.Code)
	}
	if readOnly := h.do(http.MethodPatch, "/v1/services/cart-1", readToken, `{"version":"x"}`); readOnly.Code != http.StatusForbidden || readOnly.errorCode(t) != httperr.CodeInsufficientScope {
		t.Fatalf("read-scoped PATCH = %d", readOnly.Code)
	}
}

func TestTTLHeartbeats(t *testing.T) {
	h := newHarness(t, nil)
	service, rec := h.register(`{"name":"worker","address":"10.0.0.10:9000","ttl_seconds":15}`)
	if rec.Code != http.StatusCreated || service.TTLSeconds != 15 || service.Health != storage.HealthUnknown {
		t.Fatalf("TTL registration = %d %+v", rec.Code, service)
	}
	beat := h.do(http.MethodPost, "/v1/services/"+service.ID+"/heartbeat", writeToken, "")
	var after storage.Service
	beat.json(t, &after)
	if beat.Code != http.StatusOK || after.Health != storage.HealthHealthy || after.HeartbeatAt.IsZero() {
		t.Fatalf("heartbeat = %d %+v", beat.Code, after)
	}
	plain, _ := h.register(`{"name":"plain","address":"10.0.0.11:9000"}`)
	if noTTL := h.do(http.MethodPost, "/v1/services/"+plain.ID+"/heartbeat", writeToken, ""); noTTL.Code != http.StatusConflict || noTTL.errorCode(t) != httperr.CodeNoTTL {
		t.Fatalf("heartbeat without TTL = %d %s", noTTL.Code, noTTL.Body.String())
	}
	if gone := h.do(http.MethodPost, "/v1/services/missing/heartbeat", writeToken, ""); gone.Code != http.StatusNotFound {
		t.Fatalf("heartbeat for missing service = %d", gone.Code)
	}
	if _, both := h.register(`{"name":"x","address":"10.0.0.12:80","ttl_seconds":5,"health_check":"http://10.0.0.12:80/health"}`); both.Code != http.StatusBadRequest {
		t.Fatalf("TTL plus health check = %d", both.Code)
	}
}

func TestForeignServicesFollowOwnerLiveness(t *testing.T) {
	var replays []string
	h := newHarness(t, func(d *Deps) {
		d.Replay = func(owner string) string { replays = append(replays, owner); return "instance=" + owner }
	})
	ctx := context.Background()
	for _, id := range []string{"alive-1", "alive-2", "dead-1"} {
		owner := "n2"
		if strings.HasPrefix(id, "dead") {
			owner = "n3"
		}
		if _, err := h.store.Register(ctx, storage.Service{ID: id, Name: "remote", NodeID: owner, Address: "10.0.1.1:80"}); err != nil {
			t.Fatal(err)
		}
	}
	h.cluster.peers = []membership.Member{
		{ID: "n2", Addr: "10.0.0.2:7946", State: membership.StateAlive},
		{ID: "n3", Addr: "10.0.0.3:7946", State: membership.StateDead},
	}

	refused := h.do(http.MethodDelete, "/v1/services/alive-1", writeToken, "")
	var body httperr.Body
	refused.json(t, &body)
	if refused.Code != http.StatusConflict || body.Code != httperr.CodeOwnerAlive || body.OwnerNode != "n2" || refused.Header().Get("Fly-Replay") != "instance=n2" {
		t.Fatalf("delete of live owner's service = %d %+v %v", refused.Code, body, refused.Header())
	}
	replayed := h.do(http.MethodDelete, "/v1/services/alive-1", writeToken, "", "Fly-Replay-Src", "instance=n9")
	if replayed.Header().Get("Fly-Replay") != "" {
		t.Fatal("replayed request was replayed again")
	}
	if admin := h.do(http.MethodDelete, "/v1/services/alive-2", adminToken, ""); admin.Code != http.StatusNoContent {
		t.Fatalf("admin delete = %d %s", admin.Code, admin.Body.String())
	}
	if orphan := h.do(http.MethodDelete, "/v1/services/dead-1", writeToken, ""); orphan.Code != http.StatusNoContent {
		t.Fatalf("delete of dead owner's service = %d %s", orphan.Code, orphan.Body.String())
	}
	patch := h.do(http.MethodPatch, "/v1/services/alive-1", writeToken, `{"version":"v9"}`)
	patch.json(t, &body)
	if patch.Code != http.StatusConflict || body.Code != httperr.CodeNotOwner || body.OwnerNode != "n2" {
		t.Fatalf("PATCH on another node's service = %d %+v", patch.Code, body)
	}

	// A dead owner's ID can be adopted by re-registering it here.
	if _, err := h.store.Register(ctx, storage.Service{ID: "move-me", Name: "remote", NodeID: "n3", Address: "10.0.1.2:80"}); err != nil {
		t.Fatal(err)
	}
	adopted, rec := h.register(`{"id":"move-me","name":"remote","address":"10.0.1.2:80"}`)
	if rec.Code != http.StatusCreated || adopted.NodeID != "n1" {
		t.Fatalf("adopting a dead owner's ID = %d %+v", rec.Code, adopted)
	}
}

func TestListPaginationAndFilters(t *testing.T) {
	h := newHarness(t, nil)
	for i := range 5 {
		tag := "odd"
		if i%2 == 0 {
			tag = "even"
		}
		h.register(`{"name":"pool","address":"10.0.2.` + string(rune('1'+i)) + `:80","version":"v1","tags":["` + tag + `"]}`)
	}
	var seen int
	path := "/v1/services?name=pool&limit=2"
	for pages := 0; ; pages++ {
		rec := h.do(http.MethodGet, path, readToken, "")
		var page struct {
			Count      int               `json:"count"`
			Services   []storage.Service `json:"services"`
			NextCursor string            `json:"next_cursor"`
		}
		rec.json(t, &page)
		seen += page.Count
		if page.NextCursor == "" {
			if pages != 2 {
				t.Fatalf("pages = %d, want 3", pages+1)
			}
			break
		}
		path = "/v1/services?name=pool&limit=2&cursor=" + page.NextCursor
	}
	if seen != 5 {
		t.Fatalf("paged %d services, want 5", seen)
	}
	var filtered struct {
		Count int `json:"count"`
	}
	h.do(http.MethodGet, "/v1/services?tag=even&version=v1", readToken, "").json(t, &filtered)
	if filtered.Count != 3 {
		t.Fatalf("tag filter count = %d, want 3", filtered.Count)
	}
	for _, query := range []string{"limit=0", "limit=5000", "limit=x", "cursor=not-a-cursor!", "health=sick"} {
		if rec := h.do(http.MethodGet, "/v1/services?"+query, readToken, ""); rec.Code != http.StatusBadRequest || rec.errorCode(t) != httperr.CodeValidation {
			t.Errorf("GET ?%s = %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := h.do(http.MethodGet, "/v1/lookup/pool?tag=odd", readToken, ""); rec.Code != http.StatusOK {
		t.Fatalf("lookup by tag = %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/v1/lookup/nothing", readToken, ""); rec.Code != http.StatusServiceUnavailable || rec.errorCode(t) != httperr.CodeNoHealthyInstance {
		t.Fatalf("lookup with no instances = %d", rec.Code)
	}
}

func TestErrorsUseStableJSONCodes(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		method, path, token, body string
		status                    int
		code                      string
	}{
		{http.MethodGet, "/v1/members", "", "", http.StatusUnauthorized, httperr.CodeUnauthorized},
		{http.MethodPost, "/v1/services", readToken, `{}`, http.StatusForbidden, httperr.CodeInsufficientScope},
		{http.MethodGet, "/v1/nothing-here", readToken, "", http.StatusNotFound, httperr.CodeRouteNotFound},
		{http.MethodPut, "/v1/members", writeToken, "", http.StatusMethodNotAllowed, httperr.CodeMethodNotAllowed},
		{http.MethodDelete, "/healthz", "", "", http.StatusMethodNotAllowed, httperr.CodeMethodNotAllowed},
		{http.MethodGet, "/nowhere", "", "", http.StatusNotFound, httperr.CodeRouteNotFound},
		{http.MethodPost, "/v1/services", writeToken, `{"name":`, http.StatusBadRequest, httperr.CodeInvalidJSON},
		{http.MethodPost, "/v1/services", writeToken, `{"name":"a","address":"1.2.3.4:5","bogus":1}`, http.StatusBadRequest, httperr.CodeInvalidJSON},
		{http.MethodGet, "/v1/services/missing", readToken, "", http.StatusNotFound, httperr.CodeNotFound},
		{http.MethodGet, "/v1/admin/credentials", writeToken, "", http.StatusForbidden, httperr.CodeInsufficientScope},
	}
	for _, c := range cases {
		rec := h.do(c.method, c.path, c.token, c.body)
		var body httperr.Body
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s %s: non-JSON error %q", c.method, c.path, rec.Body.String())
			continue
		}
		if rec.Code != c.status || body.Code != c.code || body.RequestID == "" {
			t.Errorf("%s %s = %d %+v, want %d %s with request ID", c.method, c.path, rec.Code, body, c.status, c.code)
		}
	}
	if rec := h.do(http.MethodPut, "/v1/members", writeToken, ""); !strings.Contains(rec.Header().Get("Allow"), "GET") {
		t.Errorf("405 Allow header = %q", rec.Header().Get("Allow"))
	}
	if rec := h.do(http.MethodGet, "/dashboard", "", ""); rec.Code != http.StatusPermanentRedirect {
		t.Errorf("dashboard redirect = %d", rec.Code)
	}
}

func TestAdminEndpoints(t *testing.T) {
	h := newHarness(t, nil)
	creds := h.do(http.MethodGet, "/v1/admin/credentials", adminToken, "")
	var listed struct {
		Count       int `json:"count"`
		Credentials []struct {
			ID      string           `json:"id"`
			Scopes  []security.Scope `json:"scopes"`
			Current bool             `json:"current"`
		} `json:"credentials"`
	}
	creds.json(t, &listed)
	current := 0
	for _, c := range listed.Credentials {
		if c.Current {
			current++
		}
	}
	if creds.Code != http.StatusOK || listed.Count != 3 || current != 1 || strings.Contains(creds.Body.String(), adminToken) {
		t.Fatalf("credentials = %d %s", creds.Code, creds.Body.String())
	}

	h.register(`{"name":"backup-me","address":"10.0.3.1:80"}`)
	backup := h.do(http.MethodPost, "/v1/admin/backup", adminToken, "")
	if backup.Code != http.StatusOK || !strings.HasPrefix(backup.Body.String(), "SQLite format 3") || !strings.Contains(backup.Header().Get("Content-Disposition"), "atlas-n1-") {
		t.Fatalf("backup = %d %v (%d bytes)", backup.Code, backup.Header(), backup.Body.Len())
	}

	ctx := context.Background()
	for _, id := range []string{"gone-1", "gone-2"} {
		if _, err := h.store.Register(ctx, storage.Service{ID: id, Name: "gone", NodeID: "n4", Address: "10.0.3.2:80"}); err != nil {
			t.Fatal(err)
		}
	}
	h.cluster.peers = []membership.Member{
		{ID: "n2", Addr: "10.0.0.2:7946", State: membership.StateAlive},
		{ID: "n4", Addr: "10.0.0.4:7946", State: membership.StateDead},
	}
	if alive := h.do(http.MethodDelete, "/v1/admin/nodes/n2", adminToken, ""); alive.Code != http.StatusConflict || alive.errorCode(t) != httperr.CodeMemberAlive {
		t.Fatalf("evict alive = %d %s", alive.Code, alive.Body.String())
	}
	if self := h.do(http.MethodDelete, "/v1/admin/nodes/n1", adminToken, ""); self.Code != http.StatusBadRequest {
		t.Fatalf("evict self = %d", self.Code)
	}
	evicted := h.do(http.MethodDelete, "/v1/admin/nodes/n4", adminToken, "")
	var result struct {
		Removed int `json:"services_removed"`
	}
	evicted.json(t, &result)
	if evicted.Code != http.StatusOK || result.Removed != 2 || len(h.cluster.evicted) != 1 {
		t.Fatalf("evict dead = %d %s", evicted.Code, evicted.Body.String())
	}
	if unknown := h.do(http.MethodDelete, "/v1/admin/nodes/n9", adminToken, ""); unknown.Code != http.StatusNotFound {
		t.Fatalf("evict unknown = %d", unknown.Code)
	}
}

func TestEventStreamCarriesServiceEventsAndResumes(t *testing.T) {
	h := newHarness(t, nil)
	server := httptest.NewServer(h.handler)
	defer server.Close()

	open := func(query, lastID string) (*bufio.Scanner, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events"+query, nil)
		req.Header.Set("Authorization", "Bearer "+readToken)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the returned cleanup function
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("open stream: %v %v", resp, err)
		}
		return bufio.NewScanner(resp.Body), func() { cancel(); _ = resp.Body.Close() }
	}
	// next reads frames until an event of type want arrives and returns its ID.
	next := func(scanner *bufio.Scanner, want string) string {
		t.Helper()
		found := make(chan string, 1)
		go func() {
			id, event := "", ""
			for scanner.Scan() {
				line := scanner.Text()
				switch {
				case strings.HasPrefix(line, "id: "):
					id = strings.TrimPrefix(line, "id: ")
				case strings.HasPrefix(line, "event: "):
					event = strings.TrimPrefix(line, "event: ")
				case line == "" && event == want:
					found <- id
					return
				case line == "":
					id, event = "", ""
				}
			}
			close(found)
		}()
		select {
		case id, ok := <-found:
			if !ok {
				t.Fatalf("stream ended before %s", want)
			}
			return id
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", want)
			return ""
		}
	}

	stream, closeStream := open("?kinds=service&service=orders", "")
	h.register(`{"name":"ignored","address":"10.0.4.1:80"}`)
	created, _ := h.register(`{"name":"orders","address":"10.0.4.2:80"}`)
	registeredID := next(stream, events.ServiceRegistered)
	closeStream()

	h.do(http.MethodPatch, "/v1/services/"+created.ID, writeToken, `{"version":"v2"}`)
	resumed, closeResumed := open("?kinds=service", registeredID)
	defer closeResumed()
	if next(resumed, events.ServiceUpdated) == "" {
		t.Fatal("resumed stream returned an empty event ID")
	}

	reset, closeReset := open("", "unknown-epoch.7")
	defer closeReset()
	next(reset, "reset")

	if bad := h.do(http.MethodGet, "/v1/events?kinds=everything", readToken, ""); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad kinds = %d", bad.Code)
	}
}
