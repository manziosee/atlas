package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/manziosee/atlas/internal/storage"
)

// History endpoints (read scope). Entries are this node's record of the
// changes it committed, its own and those learned from peers, newest first.
// A replica that merged several revisions at once records only the latest.
//
//	GET /v1/services/{id}/history  one service, including after deletion
//	GET /v1/history?name=          recent changes, optionally for one name

const defaultHistoryPage = 50

type historyStore interface {
	History(context.Context, storage.HistoryFilter) ([]storage.HistoryEntry, int64, error)
}

func (d Deps) serviceHistory(w http.ResponseWriter, r *http.Request) {
	d.writeHistory(w, r, storage.HistoryFilter{ServiceID: r.PathValue("id")})
}

func (d Deps) recentHistory(w http.ResponseWriter, r *http.Request) {
	d.writeHistory(w, r, storage.HistoryFilter{Name: r.URL.Query().Get("name")})
}

func (d Deps) writeHistory(w http.ResponseWriter, r *http.Request, filter storage.HistoryFilter) {
	query := r.URL.Query()
	limit, before, err := historyPaging(query.Get("limit"), query.Get("before"))
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	filter.Limit, filter.Before = limit, before
	entries, next, err := d.history(r.Context(), filter)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	body := map[string]any{"count": len(entries), "entries": entries}
	if next != 0 {
		body["next_before"] = next
	}
	writeJSON(w, http.StatusOK, body)
}

func historyPaging(rawLimit, rawBefore string) (int, int64, error) {
	limit := defaultHistoryPage
	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 || n > storage.MaxHistoryPage {
			return 0, 0, invalid("limit must be between 1 and %d", storage.MaxHistoryPage)
		}
		limit = n
	}
	var before int64
	if rawBefore != "" {
		n, err := strconv.ParseInt(rawBefore, 10, 64)
		if err != nil || n < 1 {
			return 0, 0, invalid("before must be a positive sequence number from next_before")
		}
		before = n
	}
	return limit, before, nil
}

func (d Deps) history(ctx context.Context, filter storage.HistoryFilter) ([]storage.HistoryEntry, int64, error) {
	store, ok := d.Registry.(historyStore)
	if !ok {
		return nil, 0, errRegistryUnavailable
	}
	if len(filter.ServiceID) > 128 || len(filter.Name) > 128 {
		return nil, 0, invalid("service id and name must be at most 128 characters")
	}
	if filter.Limit == 0 {
		filter.Limit = defaultHistoryPage
	}
	if filter.Limit < 1 || filter.Limit > storage.MaxHistoryPage {
		return nil, 0, invalid("limit must be between 1 and %d", storage.MaxHistoryPage)
	}
	return store.History(ctx, filter)
}
