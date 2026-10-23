package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
)

// Admin endpoints require the admin scope (security.RequiredScope).
//
//	GET    /v1/admin/credentials  configured credentials, without secrets
//	POST   /v1/admin/backup       consistent SQLite snapshot of this node
//	DELETE /v1/admin/nodes/{id}   evict a failed node and delete its services

type evicter interface {
	Evict(string) (membership.Member, error)
}

type backupStore interface {
	Backup(context.Context, string) error
}

type ownerTombstoner interface {
	TombstoneOwnedBy(context.Context, string) (int, error)
}

func (d Deps) listCredentials(w http.ResponseWriter, r *http.Request) {
	current := security.AuthenticatedTokenID(r.Context())
	type credential struct {
		security.CredentialInfo
		Current bool `json:"current"`
	}
	infos := d.Auth.Credentials()
	out := make([]credential, 0, len(infos))
	for _, info := range infos {
		out = append(out, credential{CredentialInfo: info, Current: info.ID == current})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "credentials": out})
}

// backup streams a point-in-time copy of this node's registry database. The
// snapshot is written to a private temporary file first so the response never
// exposes a partially written database.
func (d Deps) backup(w http.ResponseWriter, r *http.Request) {
	store, ok := d.Registry.(backupStore)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "backups are not supported by this registry")
		return
	}
	dir, err := os.MkdirTemp("", "atlas-backup-")
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not prepare backup")
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "atlas.db")
	if err := store.Backup(r.Context(), path); err != nil {
		d.Log.Error("admin backup failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not create backup")
		return
	}
	file, err := os.Open(path) // #nosec G304 -- path is inside a directory this handler created.
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read backup")
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read backup")
		return
	}
	name := fmt.Sprintf("atlas-%s-%s.db", d.Cluster.LocalNode().ID, time.Now().UTC().Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Minute))
	_, _ = io.Copy(w, file)
}

// evictNode marks a failed member as left and deletes the services it owned.
// A member that is no longer in the member list can still have its orphaned
// services removed.
func (d Deps) evictNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cluster, ok := d.Cluster.(evicter)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "node eviction is not supported")
		return
	}
	member, evictErr := cluster.Evict(id)
	switch {
	case errors.Is(evictErr, membership.ErrEvictSelf):
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "a node cannot evict itself; stop it instead")
		return
	case errors.Is(evictErr, membership.ErrMemberAlive):
		httperr.Write(w, http.StatusConflict, httperr.CodeMemberAlive, "node "+id+" is alive; only failed or departed nodes can be evicted")
		return
	case evictErr != nil && !errors.Is(evictErr, membership.ErrUnknownMember):
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not evict node")
		return
	}
	removed := 0
	if store, ok := d.Registry.(ownerTombstoner); ok {
		var err error
		removed, err = store.TombstoneOwnedBy(r.Context(), id)
		if err != nil {
			httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not delete the node's services")
			return
		}
	}
	known := evictErr == nil
	if !known && removed == 0 {
		httperr.Write(w, http.StatusNotFound, httperr.CodeNotFound, "node not found and owns no services")
		return
	}
	d.Log.Warn("node evicted by operator", "member", id, "services_removed", removed, "token_id", security.AuthenticatedTokenID(r.Context()))
	body := map[string]any{"node_id": id, "services_removed": removed}
	if known {
		body["member"] = member
	}
	writeJSON(w, http.StatusOK, body)
}
