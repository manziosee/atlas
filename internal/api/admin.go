package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

// Admin endpoints require the admin scope (security.RequiredScope).
//
//	GET    /v1/admin/credentials  configured credentials, without secrets
//	POST   /v1/admin/credentials  create a runtime credential, token shown once
//	DELETE /v1/admin/credentials/{id} revoke a runtime credential
//	GET    /v1/admin/audit       this node's retained admin audit log
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

type credentialAdminStore interface {
	CreateManagedCredentialWithAudit(context.Context, storage.ManagedCredential, *storage.AuditEntry) error
	RevokeManagedCredentialWithAudit(context.Context, string, time.Time, *storage.AuditEntry) (storage.ManagedCredential, error)
	ManagedCredentials(context.Context) ([]storage.ManagedCredential, error)
	Audit(context.Context, storage.AuditFilter) ([]storage.AuditEntry, int64, error)
	AddAudit(context.Context, storage.AuditEntry) error
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

func (d Deps) createCredential(w http.ResponseWriter, r *http.Request) {
	store, ok := d.Registry.(credentialAdminStore)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "runtime credential management is not supported")
		return
	}
	var request struct {
		Name      string           `json:"name"`
		Scopes    []security.Scope `json:"scopes"`
		ExpiresAt time.Time        `json:"expires_at"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 128 || len(request.Scopes) == 0 || request.ExpiresAt.IsZero() {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "name, at least one scope, and expires_at are required")
		return
	}
	now := time.Now().UTC()
	if !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.AddDate(1, 0, 0)) {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "expires_at must be in the future and no more than one year away")
		return
	}
	scopes := make([]string, len(request.Scopes))
	seen := make(map[security.Scope]bool, len(request.Scopes))
	for i, scope := range request.Scopes {
		if scope != security.ScopeRead && scope != security.ScopeWrite && scope != security.ScopeAdmin || seen[scope] {
			httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "scopes must be unique values from read, write, and admin")
			return
		}
		seen[scope] = true
		scopes[i] = string(scope)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		d.Log.Error("could not generate managed credential", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not generate credential")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	credential := storage.ManagedCredential{
		ID: hex.EncodeToString(digest[:12]), Name: request.Name, Digest: append([]byte(nil), digest[:]...),
		Scopes: scopes, ExpiresAt: request.ExpiresAt.UTC(), CreatedAt: now, UpdatedAt: now,
	}
	details, err := json.Marshal(map[string]any{"name": credential.Name, "scopes": scopes, "expires_at": credential.ExpiresAt})
	if err != nil {
		d.Log.Error("could not encode credential audit details", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not create credential")
		return
	}
	audit := &storage.AuditEntry{
		At: now, ActorID: security.AuthenticatedTokenID(r.Context()), Action: "credential.created",
		ResourceID: credential.ID, Details: details,
	}
	if err := store.CreateManagedCredentialWithAudit(r.Context(), credential, audit); err != nil {
		d.Log.Error("managed credential creation failed", "credential_id", credential.ID, "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not create credential")
		return
	}
	if err := d.applyManagedCredential(credential); err != nil {
		d.Log.Error("managed credential auth update failed", "credential_id", credential.ID, "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "credential was stored but could not be activated")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"credential": security.CredentialInfo{
			ID: credential.ID, Name: credential.Name, Scopes: request.Scopes, CreatedAt: credential.CreatedAt,
			ExpiresAt: credential.ExpiresAt, Source: "managed",
		},
		"token": token,
	})
}

func (d Deps) revokeCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == security.AuthenticatedTokenID(r.Context()) {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "cannot revoke the credential used for this request; connect with another admin credential")
		return
	}
	store, ok := d.Registry.(credentialAdminStore)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "runtime credential management is not supported")
		return
	}
	var known bool
	for _, credential := range d.Auth.Credentials() {
		if credential.ID == id {
			if credential.Source != "managed" {
				httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "only runtime-managed credentials can be revoked through the API")
				return
			}
			known = true
			break
		}
	}
	if !known {
		httperr.Write(w, http.StatusNotFound, httperr.CodeNotFound, "managed credential not found")
		return
	}
	now := time.Now().UTC()
	details, err := json.Marshal(map[string]string{"reason": "revoked by administrator"})
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not revoke credential")
		return
	}
	audit := &storage.AuditEntry{
		At: now, ActorID: security.AuthenticatedTokenID(r.Context()), Action: "credential.revoked",
		ResourceID: id, Details: details,
	}
	credential, err := store.RevokeManagedCredentialWithAudit(r.Context(), id, now, audit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httperr.Write(w, http.StatusNotFound, httperr.CodeNotFound, "managed credential not found")
			return
		}
		d.Log.Error("managed credential revocation failed", "credential_id", id, "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not revoke credential")
		return
	}
	if err := d.applyManagedCredential(credential); err != nil {
		d.Log.Error("managed credential auth update failed", "credential_id", id, "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "credential was revoked in storage but could not be deactivated")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) applyManagedCredential(credential storage.ManagedCredential) error {
	if d.Auth == nil {
		return errors.New("API token authenticator is unavailable")
	}
	if err := credential.Validate(); err != nil {
		return err
	}
	var digest [sha256.Size]byte
	copy(digest[:], credential.Digest)
	scopes := make([]security.Scope, len(credential.Scopes))
	for i, scope := range credential.Scopes {
		scopes[i] = security.Scope(scope)
	}
	d.Auth.UpsertRuntimeCredential(security.RuntimeCredential{
		ID: credential.ID, Name: credential.Name, Digest: digest, Scopes: scopes,
		CreatedAt: credential.CreatedAt, ExpiresAt: credential.ExpiresAt, RevokedAt: credential.RevokedAt,
	})
	return nil
}

func (d Deps) listAudit(w http.ResponseWriter, r *http.Request) {
	store, ok := d.Registry.(credentialAdminStore)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "audit log is not supported by this registry")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > storage.MaxAuditPage {
			httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "before must be a positive audit sequence number")
			return
		}
		before = n
	}
	entries, next, err := store.Audit(r.Context(), storage.AuditFilter{Before: before, Limit: limit})
	if err != nil {
		d.Log.Error("admin audit query failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read audit log")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(entries), "entries": entries, "next_before": next})
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
	if auditStore, ok := d.Registry.(interface {
		AddAudit(context.Context, storage.AuditEntry) error
	}); ok {
		if err := auditStore.AddAudit(r.Context(), storage.AuditEntry{
			At: time.Now().UTC(), ActorID: security.AuthenticatedTokenID(r.Context()),
			Action: "backup.downloaded", ResourceID: d.Cluster.LocalNode().ID,
		}); err != nil {
			d.Log.Error("admin backup audit failed", "err", err)
			httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not record backup audit event")
			return
		}
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
	if auditStore, ok := d.Registry.(interface {
		AddAudit(context.Context, storage.AuditEntry) error
	}); ok {
		details, _ := json.Marshal(map[string]int{"services_removed": removed})
		if err := auditStore.AddAudit(r.Context(), storage.AuditEntry{
			At: time.Now().UTC(), ActorID: security.AuthenticatedTokenID(r.Context()),
			Action: "node.evicted", ResourceID: id, Details: details,
		}); err != nil {
			d.Log.Error("node eviction audit failed", "member", id, "err", err)
			httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "node was evicted but the audit event could not be recorded")
			return
		}
	}
	body := map[string]any{"node_id": id, "services_removed": removed}
	if known {
		body["member"] = member
	}
	writeJSON(w, http.StatusOK, body)
}
