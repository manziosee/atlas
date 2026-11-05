// Package k8ssync registers Kubernetes Services in Atlas. It watches
// Services that opt in with annotations and their EndpointSlices, and keeps
// one Atlas instance per ready pod endpoint (or one per Service), draining
// terminating pods and removing deleted ones. See Controller.
package k8ssync

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// Kube is a minimal Kubernetes API client: list and watch over HTTPS with a
// bearer token. It avoids client-go's large dependency tree.
type Kube struct {
	base      string
	http      *http.Client
	token     string
	tokenFile string

	mu        sync.Mutex
	readToken time.Time
}

// KubeConfig selects how to reach the API server. With nothing set, the
// in-cluster service account is used.
type KubeConfig struct {
	// URL of the API server, for example https://10.0.0.1:443, or
	// http://127.0.0.1:8001 for `kubectl proxy`.
	URL string
	// Token is a bearer token; TokenFile is re-read every minute because
	// projected service-account tokens rotate.
	Token     string
	TokenFile string
	// CAFile verifies the API server's certificate.
	CAFile string
}

// InCluster fills in the service-account defaults for a pod.
func InCluster(cfg KubeConfig) (KubeConfig, error) {
	if cfg.URL == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return cfg, errors.New("k8ssync: not running in a pod; set ATLAS_K8S_API_URL (for example http://127.0.0.1:8001 with kubectl proxy)")
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		cfg.URL = "https://" + host + ":" + port
		if cfg.TokenFile == "" && cfg.Token == "" {
			cfg.TokenFile = serviceAccountDir + "/token"
		}
		if cfg.CAFile == "" {
			cfg.CAFile = serviceAccountDir + "/ca.crt"
		}
	}
	return cfg, nil
}

// NewKube builds a client.
func NewKube(cfg KubeConfig) (*Kube, error) {
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("k8ssync: API URL must be http(s)://host[:port], got %q", cfg.URL)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("k8ssync: read CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("k8ssync: no certificates in %s", cfg.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Kube{base: u.String(), http: &http.Client{Transport: transport}, token: cfg.Token, tokenFile: cfg.TokenFile}, nil
}

func (k *Kube) bearer() string {
	if k.tokenFile == "" {
		return k.token
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if time.Since(k.readToken) > time.Minute || k.token == "" {
		if raw, err := os.ReadFile(k.tokenFile); err == nil {
			k.token = strings.TrimSpace(string(raw))
			k.readToken = time.Now()
		}
	}
	return k.token
}

func (k *Kube) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	target := k.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token := k.bearer(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := k.http.Do(req) // #nosec G704 -- the operator-configured Kubernetes API server.
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, statusError(resp)
	}
	return resp, nil
}

// StatusError is a non-200 answer from the API server.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("k8ssync: API server answered %d: %s", e.Code, e.Message)
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var status struct {
		Message string `json:"message"`
	}
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &status) == nil && status.Message != "" {
		message = status.Message
	}
	return &StatusError{Code: resp.StatusCode, Message: message}
}

// list fetches a collection and returns its items and resourceVersion.
func (k *Kube) list(ctx context.Context, path string) ([]json.RawMessage, string, error) {
	resp, err := k.get(ctx, path, url.Values{"limit": {"5000"}})
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
			Continue        string `json:"continue"`
		} `json:"metadata"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, "", fmt.Errorf("k8ssync: decode %s: %w", path, err)
	}
	items := list.Items
	for next := list.Metadata.Continue; next != ""; {
		more, err := k.get(ctx, path, url.Values{"limit": {"5000"}, "continue": {next}})
		if err != nil {
			return nil, "", err
		}
		var page struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []json.RawMessage `json:"items"`
		}
		err = json.NewDecoder(more.Body).Decode(&page)
		_ = more.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("k8ssync: decode %s: %w", path, err)
		}
		items = append(items, page.Items...)
		next = page.Metadata.Continue
	}
	return items, list.Metadata.ResourceVersion, nil
}

// watchEvent is one line of a watch stream.
type watchEvent struct {
	Type   string          `json:"type"` // ADDED, MODIFIED, DELETED, BOOKMARK, ERROR
	Object json.RawMessage `json:"object"`
}

// errGone means the resourceVersion is too old; the caller must list again.
var errGone = errors.New("k8ssync: watch resource version expired")

// watch streams changes after resourceVersion until the stream ends. It
// returns the last resourceVersion seen.
func (k *Kube) watch(ctx context.Context, path, resourceVersion string, fn func(watchEvent)) (string, error) {
	resp, err := k.get(ctx, path, url.Values{
		"watch": {"1"}, "resourceVersion": {resourceVersion}, "allowWatchBookmarks": {"true"}, "timeoutSeconds": {"300"},
	})
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) && status.Code == http.StatusGone {
			return resourceVersion, errGone
		}
		return resourceVersion, err
	}
	defer func() { _ = resp.Body.Close() }()
	decoder := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var event watchEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return resourceVersion, nil
			}
			return resourceVersion, err
		}
		var meta struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
			Code int `json:"code"`
		}
		_ = json.Unmarshal(event.Object, &meta)
		if event.Type == "ERROR" {
			if meta.Code == http.StatusGone {
				return resourceVersion, errGone
			}
			return resourceVersion, fmt.Errorf("k8ssync: watch error %d", meta.Code)
		}
		if meta.Metadata.ResourceVersion != "" {
			resourceVersion = meta.Metadata.ResourceVersion
		}
		if event.Type != "BOOKMARK" {
			fn(event)
		}
	}
}
