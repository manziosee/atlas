// Package dockeragent registers Docker containers in Atlas from their
// labels, so applications need no code changes. See Agent for the label
// contract and lifecycle rules.
package dockeragent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// apiVersion is the oldest Docker Engine API the agent needs (Docker 20.10).
const apiVersion = "/v1.41"

// Docker is a minimal Docker Engine API client: list, inspect and the event
// stream, over a Unix socket or TCP. It avoids the Docker SDK's large
// dependency tree.
type Docker struct {
	http *http.Client
	base string
}

// NewDocker connects to host: unix:///var/run/docker.sock (the default when
// empty) or tcp://host:port.
func NewDocker(host string) (*Docker, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("dockeragent: DOCKER_HOST %q: %w", host, err)
	}
	switch u.Scheme {
	case "unix":
		socket := u.Path
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}
		return &Docker{http: &http.Client{Transport: transport}, base: "http://docker" + apiVersion}, nil
	case "tcp", "http":
		return &Docker{http: &http.Client{}, base: "http://" + u.Host + apiVersion}, nil
	default:
		return nil, fmt.Errorf("dockeragent: DOCKER_HOST must be unix:// or tcp://, got %q", host)
	}
}

// Container is the part of a container inspect result the agent uses.
type Container struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image        string              `json:"Image"`
		Labels       map[string]string   `json:"Labels"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"` // created, running, paused, restarting, exited, ...
		Health *struct {
			Status string `json:"Status"` // starting, healthy, unhealthy
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress         string `json:"IPAddress"`
			GlobalIPv6Address string `json:"GlobalIPv6Address"`
		} `json:"Networks"`
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

// Event is one Docker container event.
type Event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"` // start, kill, die, destroy, pause, unpause, health_status: healthy, ...
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

// ErrNotFound means the container no longer exists.
var ErrNotFound = errors.New("dockeragent: container not found")

// Labeled returns the IDs of running containers carrying label.
func (d *Docker) Labeled(ctx context.Context, label string) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}, "status": {"running", "paused"}})
	var list []struct {
		ID string `json:"Id"`
	}
	if err := d.get(ctx, "/containers/json?filters="+url.QueryEscape(string(filters)), &list); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// Inspect returns one container.
func (d *Docker) Inspect(ctx context.Context, id string) (Container, error) {
	var c Container
	err := d.get(ctx, "/containers/"+url.PathEscape(id)+"/json", &c)
	return c, err
}

// Events streams container events until ctx ends or the stream breaks. It
// calls opened once the stream is established, before the first event, so the
// caller can take a snapshot without missing changes.
func (d *Docker) Events(ctx context.Context, opened func(), fn func(Event)) error {
	filters, _ := json.Marshal(map[string][]string{"type": {"container"}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/events?filters="+url.QueryEscape(string(filters)), nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req) // #nosec G704 -- the operator-configured Docker daemon.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return dockerError(resp)
	}
	opened()
	decoder := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		fn(event)
	}
}

func (d *Docker) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req) // #nosec G704 -- the operator-configured Docker daemon.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return dockerError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func dockerError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var message struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &message) == nil && message.Message != "" {
		return fmt.Errorf("dockeragent: docker returned %d: %s", resp.StatusCode, message.Message)
	}
	return fmt.Errorf("dockeragent: docker returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
