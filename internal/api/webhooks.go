package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/webhooks"
)

// WebhookDispatcher delivers the node's webhooks; see internal/webhooks.
type WebhookDispatcher interface {
	Reload(context.Context) error
	Status() map[string]webhooks.Status
	Test(context.Context, storage.Webhook) webhooks.Result
}

type webhookStore interface {
	Webhooks(context.Context) ([]storage.Webhook, error)
	CreateWebhookWithAudit(context.Context, storage.Webhook, *storage.AuditEntry) error
	DeleteWebhookWithAudit(context.Context, string, *storage.AuditEntry) error
}

type webhookView struct {
	storage.Webhook
	Status webhooks.Status `json:"status"`
}

func (d Deps) webhookStore(w http.ResponseWriter) (webhookStore, bool) {
	store, ok := d.Registry.(webhookStore)
	if !ok || d.Webhooks == nil {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "webhooks are not supported by this node")
		return nil, false
	}
	return store, true
}

func (d Deps) listWebhooks(w http.ResponseWriter, r *http.Request) {
	store, ok := d.webhookStore(w)
	if !ok {
		return
	}
	hooks, err := store.Webhooks(r.Context())
	if err != nil {
		d.Log.Error("list webhooks failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read webhooks")
		return
	}
	status := d.Webhooks.Status()
	out := make([]webhookView, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, webhookView{Webhook: hook, Status: status[hook.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "webhooks": out})
}

func (d Deps) createWebhook(w http.ResponseWriter, r *http.Request) {
	store, ok := d.webhookStore(w)
	if !ok {
		return
	}
	var request struct {
		URL         string   `json:"url"`
		Kinds       []string `json:"kinds"`
		Service     string   `json:"service"`
		Description string   `json:"description"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	request.URL = strings.TrimSpace(request.URL)
	if err := webhooks.ValidateURL(request.URL); err != nil {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, err.Error())
		return
	}
	seen := map[string]bool{}
	for _, kind := range request.Kinds {
		if kind != events.KindMember && kind != events.KindService || seen[kind] {
			httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "kinds must be unique values from member and service")
			return
		}
		seen[kind] = true
	}
	slices.Sort(request.Kinds)
	if request.Service != "" && !serviceNamePattern.MatchString(request.Service) {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "service must be a service name")
		return
	}
	if len(request.Description) > 256 {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "description must be at most 256 characters")
		return
	}
	secret, err := webhooks.NewSecret()
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not generate webhook secret")
		return
	}
	var idBytes [12]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not allocate webhook ID")
		return
	}
	now := time.Now().UTC()
	hook := storage.Webhook{
		ID: "wh_" + hex.EncodeToString(idBytes[:]), URL: request.URL, Secret: secret, Kinds: request.Kinds,
		Service: request.Service, Description: request.Description, CreatedAt: now,
	}
	details, _ := json.Marshal(map[string]any{"url": redactURL(hook.URL), "kinds": hook.Kinds, "service": hook.Service})
	audit := &storage.AuditEntry{At: now, ActorID: security.AuthenticatedTokenID(r.Context()), Action: "webhook.created", ResourceID: hook.ID, Details: details}
	if err := store.CreateWebhookWithAudit(r.Context(), hook, audit); err != nil {
		if errors.Is(err, storage.ErrWebhookLimit) {
			httperr.Write(w, http.StatusConflict, httperr.CodeValidation, "this node already has the maximum number of webhooks")
			return
		}
		d.Log.Error("create webhook failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not create webhook")
		return
	}
	if err := d.Webhooks.Reload(r.Context()); err != nil {
		d.Log.Error("webhook reload failed", "err", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": webhookView{Webhook: hook}, "secret": secret})
}

func (d Deps) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	store, ok := d.webhookStore(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	audit := &storage.AuditEntry{At: time.Now().UTC(), ActorID: security.AuthenticatedTokenID(r.Context()), Action: "webhook.deleted", ResourceID: id}
	if err := store.DeleteWebhookWithAudit(r.Context(), id, audit); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httperr.Write(w, http.StatusNotFound, httperr.CodeNotFound, "webhook not found")
			return
		}
		d.Log.Error("delete webhook failed", "webhook", id, "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not delete webhook")
		return
	}
	if err := d.Webhooks.Reload(r.Context()); err != nil {
		d.Log.Error("webhook reload failed", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) testWebhook(w http.ResponseWriter, r *http.Request) {
	store, ok := d.webhookStore(w)
	if !ok {
		return
	}
	hooks, err := store.Webhooks(r.Context())
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read webhooks")
		return
	}
	id := r.PathValue("id")
	index := slices.IndexFunc(hooks, func(hook storage.Webhook) bool { return hook.ID == id })
	if index < 0 {
		httperr.Write(w, http.StatusNotFound, httperr.CodeNotFound, "webhook not found")
		return
	}
	result := d.Webhooks.Test(r.Context(), hooks[index])
	writeJSON(w, http.StatusOK, map[string]any{"delivered": result.Error == "", "result": result})
}

// redactURL keeps the scheme, host and path of a URL for the audit log and
// drops the query, which often carries a token.
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i] + "?…"
	}
	return raw
}
