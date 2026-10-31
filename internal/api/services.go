package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/routing"
	"github.com/manziosee/atlas/internal/storage"
)

func (d Deps) registerService(w http.ResponseWriter, r *http.Request) {
	var spec serviceSpec
	if !decodeBody(w, r, &spec) {
		return
	}
	service, outcome, err := d.register(r.Context(), spec, r.Header.Get("Idempotency-Key"))
	d.writeServiceResult(w, r, service, outcome, err)
}

// replaceService implements PUT: create or fully replace the service with the
// path ID. With If-Match, the service must exist at that revision.
func (d Deps) replaceService(w http.ResponseWriter, r *http.Request) {
	var spec serviceSpec
	if !decodeBody(w, r, &spec) {
		return
	}
	id := r.PathValue("id")
	if spec.ID != "" && spec.ID != id {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "body id must match the path")
		return
	}
	spec.ID = id
	ifRevision, ok := ifMatch(w, r)
	if !ok {
		return
	}
	if ifRevision == 0 {
		service, outcome, err := d.register(r.Context(), spec, "")
		d.writeServiceResult(w, r, service, outcome, err)
		return
	}
	service, err := d.update(r.Context(), id, ifRevision, fullPatch(spec))
	d.writeServiceResult(w, r, service, storage.Updated, err)
}

func (d Deps) patchService(w http.ResponseWriter, r *http.Request) {
	var patch servicePatch
	if !decodeBody(w, r, &patch) {
		return
	}
	ifRevision, ok := ifMatch(w, r)
	if !ok {
		return
	}
	service, err := d.update(r.Context(), r.PathValue("id"), ifRevision, patch)
	d.writeServiceResult(w, r, service, storage.Updated, err)
}

func (d Deps) heartbeat(w http.ResponseWriter, r *http.Request) {
	service, err := d.heartbeatService(r.Context(), r.PathValue("id"))
	d.writeServiceResult(w, r, service, storage.Updated, err)
}

func (d Deps) getService(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		d.writeError(w, r, errRegistryUnavailable)
		return
	}
	service, err := d.Registry.Get(r.Context(), r.PathValue("id"))
	d.writeServiceResult(w, r, service, storage.Updated, err)
}

func (d Deps) deleteService(w http.ResponseWriter, r *http.Request) {
	if err := d.deregister(r.Context(), r.PathValue("id")); err != nil {
		d.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) listServices(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "limit must be a positive number")
			return
		}
		limit = n
	}
	filter := storage.Filter{
		Name: query.Get("name"), Region: query.Get("region"), Health: storage.Health(query.Get("health")),
		Version: query.Get("version"), NodeID: query.Get("node_id"), Tags: query["tag"],
	}
	page, err := d.listPage(r.Context(), filter, query.Get("cursor"), limit)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	body := map[string]any{"count": len(page.Services), "services": page.Services}
	if page.NextCursor != "" {
		body["next_cursor"] = page.NextCursor
	}
	writeJSON(w, http.StatusOK, body)
}

func (d Deps) lookupService(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	service, err := d.lookup(r.Context(), lookupRequest{
		Name: r.PathValue("name"), Region: query.Get("region"), Key: query.Get("key"), Tags: query["tag"],
		Stale: query.Get("stale"), Latency: query.Get("latency"), Tolerance: query.Get("tolerance"),
	})
	d.writeServiceResult(w, r, service, storage.Updated, err)
}

// writeServiceResult writes one service with its revision ETag, 201 and a
// Location header for a new record, or the mapped error.
func (d Deps) writeServiceResult(w http.ResponseWriter, r *http.Request, service storage.Service, outcome storage.Outcome, err error) {
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if outcome == storage.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/v1/services/"+url.PathEscape(service.ID))
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(service.Revision, 10)))
	writeJSON(w, status, service)
}

// writeError maps registry operation errors to HTTP statuses and stable codes.
func (d Deps) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var invalidInput validationError
	var alive ownerAliveError
	var remote *remoteError
	var unreachable *ownerUnreachableError
	owner, owned := ownerOf(err)
	switch {
	case errors.As(err, &remote):
		remote.body.RequestID = ""
		httperr.WriteBody(w, remote.status, remote.body)
	case errors.As(err, &unreachable):
		httperr.WriteBody(w, http.StatusBadGateway, httperr.Body{
			Error: "the owning node did not answer; retry shortly", Code: httperr.CodeOwnerUnreachable, OwnerNode: unreachable.owner,
		})
	case errors.As(err, &invalidInput):
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, invalidInput.msg)
	case errors.Is(err, storage.ErrNotFound):
		httperr.Write(w, http.StatusNotFound, httperr.CodeNotFound, "service not found")
	case errors.As(err, &alive):
		d.replayToOwner(w, r, owner)
		httperr.WriteBody(w, http.StatusConflict, httperr.Body{
			Error: "the owning node is alive; delete through it or use an admin credential",
			Code:  httperr.CodeOwnerAlive, OwnerNode: owner,
		})
	case owned:
		d.replayToOwner(w, r, owner)
		httperr.WriteBody(w, http.StatusConflict, httperr.Body{
			Error: "service is owned by node " + owner + "; send this request to that node",
			Code:  httperr.CodeNotOwner, OwnerNode: owner,
		})
	case errors.Is(err, storage.ErrRevisionMismatch):
		httperr.Write(w, http.StatusPreconditionFailed, httperr.CodePreconditionFailed, "service revision does not match If-Match")
	case errors.Is(err, storage.ErrNoTTL):
		httperr.Write(w, http.StatusConflict, httperr.CodeNoTTL, "service was registered without ttl_seconds")
	case errors.Is(err, routing.ErrNoInstance):
		httperr.Write(w, http.StatusServiceUnavailable, httperr.CodeNoHealthyInstance, "no healthy service instance available")
	case errors.Is(err, errRegistryUnavailable):
		httperr.Write(w, http.StatusServiceUnavailable, httperr.CodeUnavailable, "service registry unavailable")
	default:
		if d.Log != nil {
			// Log the route pattern, not the client-controlled path; the request ID
			// links this entry to the access log line that has the full path.
			d.Log.Error("registry operation failed", "err", err, "route", r.Pattern, "request_id", w.Header().Get("X-Request-Id"))
		}
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "internal error")
	}
}

// replayToOwner asks Fly.io's proxy to replay the request on the owning
// machine. Outside Fly the header is absent and the client sees the 409.
func (d Deps) replayToOwner(w http.ResponseWriter, r *http.Request, owner string) {
	if d.Replay == nil || r.Header.Get("Fly-Replay-Src") != "" || !d.ownerAlive(owner) {
		return
	}
	if value := d.Replay(owner); value != "" {
		w.Header().Set("Fly-Replay", value)
	}
}

// ifMatch parses an optional If-Match revision ETag.
func ifMatch(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, true
	}
	revision, err := strconv.ParseUint(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`), 10, 64)
	if err != nil || revision == 0 {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "If-Match must be a service revision ETag")
		return 0, false
	}
	return revision, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	err := decodeJSON(r, dst)
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httperr.Write(w, http.StatusRequestEntityTooLarge, httperr.CodePayloadTooLarge, "request body is too large")
		return false
	}
	httperr.Write(w, http.StatusBadRequest, httperr.CodeInvalidJSON, "invalid JSON body: "+jsonProblem(err))
	return false
}

// jsonProblem describes a decode failure without echoing request content.
func jsonProblem(err error) string {
	if text := err.Error(); strings.HasPrefix(text, "json: unknown field") {
		return strings.TrimPrefix(text, "json: ")
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return "field " + typeErr.Field + " has the wrong type"
	}
	return "malformed document"
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("api: multiple JSON values")
		}
		return err
	}
	return nil
}
