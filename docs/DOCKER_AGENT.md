# Docker-label agent

`atlas-agent` registers Docker containers in Atlas from their labels. Add
`atlas.name=payments` to a container and it shows up in the registry, in
lookups and in DNS. When it stops, it's drained and then removed. The
application needs no Atlas client and no code changes.

Run one agent per Docker host, next to (or pointed at) an Atlas node.

## Try it

```bash
export ATLAS_TOKEN=$(openssl rand -hex 24)
docker compose -f deploy/docker-agent/compose.yaml up --build -d
curl -s -H "Authorization: Bearer $ATLAS_TOKEN" 'localhost:8080/v1/services?name=whoami'
docker compose -f deploy/docker-agent/compose.yaml up -d --scale whoami=3
```

The example starts a dev-mode node, the agent, and a `traefik/whoami`
container whose labels are its whole integration.

## Labels

| Label | Required | Meaning |
|-------|----------|---------|
| `atlas.name` | yes | Service name. Its presence opts the container in. |
| `atlas.port` | when the image exposes more or fewer than one TCP port | Container port to register. |
| `atlas.health` | no | `/path` (HTTP check), `tcp`, or a full `http(s)://`, `tcp://` or `grpc://` URL on the instance address. The Atlas node runs the check. |
| `atlas.tags` | no | Comma-separated tags. |
| `atlas.version` | no | Version. Defaults to the image tag, unless it is `latest` or a digest. |
| `atlas.network` | when attached to several networks | Network whose IP to register. |
| `atlas.address` | no | Explicit `host:port` (or `host`, with the port from `atlas.port`). |
| `atlas.enable` | no | `false` opts a labeled container out. |

```bash
docker run -d --label atlas.name=payments --label atlas.port=8080 \
  --label atlas.health=/healthz --label atlas.tags=team-billing payments:1.4.2
```

The service ID is `docker-<first 12 characters of the container ID>`, so a
recreated container gets a new record and the old one is removed.

## Lifecycle

| Docker | Atlas |
|--------|-------|
| Container starts or is unpaused | Registered (or refreshed) |
| `docker stop`, a terminating `kill`, or pause | Drained: lookups, DNS and client load balancers skip it while it shuts down |
| Container exits (`die`) or is removed | Deregistered |
| Reload signals such as `SIGHUP` | Ignored |

Health comes from one of two places:

- **With `atlas.health`**, the Atlas node checks the instance itself.
- **Without it**, the agent registers a TTL (default 15s) and sends
  heartbeats while the container is running. If the image has a Docker
  `HEALTHCHECK`, it sends them only while Docker reports the container
  healthy, so an unhealthy container goes unhealthy in Atlas within one TTL.

Every registration is tagged `atlas-agent:<ATLAS_AGENT_ID>`. When it starts,
and every minute afterwards, the agent compares Docker with Atlas. It
registers anything it missed and removes registrations carrying its tag
whose container is gone. Keep `ATLAS_AGENT_ID` unique per host so agents
don't remove each other's records.

Stopping the agent leaves registrations in place. TTL instances expire on
their own, and a restarted agent reconciles the rest.

## Addresses

By default the agent registers the container's IP and port on its Docker
network. Atlas nodes and clients must be able to reach that network, for
example on the same Compose project or overlay network.

For clients on other hosts, set `ATLAS_AGENT_HOST_ADDRESS` to this host's
reachable IP. The agent then registers `<host address>:<published port>`, so
the port must be published (`-p`). Containers on the host network use
`ATLAS_AGENT_HOST_ADDRESS` (or `127.0.0.1`) with `atlas.port`.

## Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `ATLAS_ADDR` | `http://127.0.0.1:8080` | Atlas node API. |
| `ATLAS_TOKEN` | — | Token with `write` scope. |
| `ATLAS_CA_FILE` / `ATLAS_CLIENT_CERT` / `ATLAS_CLIENT_KEY` | — | TLS and mTLS to the node. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker daemon, `unix://` or `tcp://`. |
| `ATLAS_AGENT_ID` | hostname | Owner tag for this host's registrations. |
| `ATLAS_AGENT_HOST_ADDRESS` | — | Register published ports on this address instead of container IPs. |
| `ATLAS_AGENT_TTL` | `15s` | TTL for containers without `atlas.health`; minimum `3s`. |
| `ATLAS_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

## Docker socket access

The image runs as UID 65532, and the Docker socket is usually owned by root
with mode `0660`. Pick one of these:

- `--user 0:0` (or `user: "0:0"` in Compose), as in the example. This is
  simplest.
- `--group-add $(stat -c %g /var/run/docker.sock)` keeps the agent
  non-root.
- **Recommended for production:** a read-only socket proxy that allows only
  `GET` on containers and events, for example
  `tecnativa/docker-socket-proxy` with `CONTAINERS=1 EVENTS=1`. Point the
  agent at it with `DOCKER_HOST=tcp://socket-proxy:2375`.

Access to the Docker socket is equivalent to root on the host. The agent
only reads from it (list, inspect, events), but anything that compromises
the agent could do more, which is why the proxy is preferred.

The image's built-in `HEALTHCHECK` probes a local Atlas node. Disable it for
the agent container (`healthcheck: {disable: true}` or `--no-healthcheck`).
