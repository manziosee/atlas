package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/routing"
	"github.com/manziosee/atlas/internal/storage"
)

type ServiceRegistry interface {
	Register(context.Context, storage.Service) (storage.Service, error)
	Get(context.Context, string) (storage.Service, error)
	List(context.Context, storage.Filter) ([]storage.Service, error)
	Delete(context.Context, string, string) error
}

type registerServiceRequest struct {
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
}

var serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func (d Deps) registerService(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service registry unavailable"})
		return
	}
	var input registerServiceRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := validateServiceRequest(input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not allocate service ID"})
		return
	}
	node := d.Cluster.LocalNode()
	service, err := d.Registry.Register(r.Context(), storage.Service{
		ID:          hex.EncodeToString(idBytes[:]),
		Name:        input.Name,
		NodeID:      node.ID,
		Address:     input.Address,
		Region:      node.Region,
		Version:     input.Version,
		Tags:        input.Tags,
		HealthCheck: input.HealthCheck,
		Health:      storage.HealthUnknown,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not register service"})
		return
	}
	writeJSON(w, http.StatusCreated, service)
}

func (d Deps) listServices(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service registry unavailable"})
		return
	}
	query := r.URL.Query()
	health := storage.Health(query.Get("health"))
	if health != "" && health != storage.HealthHealthy && health != storage.HealthUnhealthy && health != storage.HealthUnknown {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "health must be healthy, unhealthy, or unknown"})
		return
	}
	services, err := d.Registry.List(r.Context(), storage.Filter{
		Name: query.Get("name"), Region: query.Get("region"), Health: health,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list services"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(services), "services": services})
}

func (d Deps) lookupService(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil || d.Selector == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service registry unavailable"})
		return
	}
	name := r.PathValue("name")
	region, key := r.URL.Query().Get("region"), r.URL.Query().Get("key")
	if len(name) > 128 || len(region) > 64 || len(key) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lookup parameters exceed limits"})
		return
	}
	services, err := d.Registry.List(r.Context(), storage.Filter{Name: name})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not look up service"})
		return
	}
	service, err := d.Selector.SelectWithLatency(services, region, key, d.nodeLatency)
	if errors.Is(err, routing.ErrNoInstance) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no healthy service instance available"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not select service instance"})
		return
	}
	writeJSON(w, http.StatusOK, service)
}

func (d Deps) nodeLatency(nodeID string) time.Duration {
	if reader, ok := d.Cluster.(interface{ Latency(string) time.Duration }); ok {
		return reader.Latency(nodeID)
	}
	return 0
}

func (d Deps) getService(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service registry unavailable"})
		return
	}
	service, err := d.Registry.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "service not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read service"})
		return
	}
	writeJSON(w, http.StatusOK, service)
}

func (d Deps) deleteService(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service registry unavailable"})
		return
	}
	if err := d.Registry.Delete(r.Context(), r.PathValue("id"), d.Cluster.LocalNode().ID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "service not found"})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "service cannot be removed by this node"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func validateServiceRequest(in registerServiceRequest) error {
	if !serviceNamePattern.MatchString(in.Name) {
		return errors.New("name must be 1-128 letters, digits, dots, underscores, or hyphens")
	}
	if host, port, err := net.SplitHostPort(in.Address); err != nil || host == "" {
		return errors.New("address must be host:port")
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("address port must be between 1 and 65535")
	}
	if len(in.Version) > 128 {
		return errors.New("version must be at most 128 characters")
	}
	if len(in.Tags) > 16 {
		return errors.New("at most 16 tags are allowed")
	}
	for _, tag := range in.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 64 {
			return errors.New("tags must contain 1-64 non-whitespace characters")
		}
	}
	if in.HealthCheck != "" {
		u, err := url.ParseRequestURI(in.HealthCheck)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Host != in.Address {
			return fmt.Errorf("health_check must be an http or https URL on the service address without user information")
		}
	}
	return nil
}
