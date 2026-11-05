# Kubernetes

Atlas runs on Kubernetes from a Helm chart. Kubernetes Services can join the
Atlas registry without code changes: annotate them, and `atlas-k8s` keeps an
Atlas instance for every ready pod.

## Install

```bash
helm install atlas oci://ghcr.io/manziosee/charts/atlas \
  -n atlas --create-namespace \
  --set k8sSync.enabled=true \
  --set config.corsOrigins=https://your-console.netlify.app
```

This creates a 3-node StatefulSet, a headless Service for gossip and an API
Service. Secrets are generated once and kept across upgrades. With
`k8sSync.enabled`, it also installs the `atlas-k8s` controller with read-only
access to Services and EndpointSlices.

```bash
kubectl -n atlas get secret atlas-secrets -o jsonpath='{.data.ATLAS_ADMIN_TOKEN}' | base64 -d
kubectl -n atlas port-forward svc/atlas 8080
helm test atlas -n atlas
```

Then open the Atlas Console and connect to `http://localhost:8080`. Every
value is listed in the [chart README](../deploy/helm/atlas/README.md).
Without Helm, the Kustomize manifests in `deploy/k8s/` still work.

## Register a Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: payments
  namespace: shop
  annotations:
    atlas.io/register: "true"
    atlas.io/health: /healthz
    atlas.io/tags: team-billing,primary
spec:
  selector: { app: payments }
  ports:
    - { name: http, port: 80, targetPort: 8080 }
```

| Annotation | Default | Meaning |
|------------|---------|---------|
| `atlas.io/register` | — | `"true"` opts the Service in. Removing it deregisters everything. |
| `atlas.io/name` | the Service name | Atlas service name. |
| `atlas.io/port` | the only TCP port | Service port name or number, when there are several. |
| `atlas.io/health` | TTL kept by readiness | `/path` (HTTP check), `tcp` or `grpc`, run by the Atlas node. |
| `atlas.io/tags` | — | Comma-separated tags. |
| `atlas.io/version` | — | Version label. |
| `atlas.io/mode` | `endpoints` | `endpoints`: one instance per pod at the pod IP and target port. `service`: one instance at `<svc>.<ns>.svc.<domain>:<port>`. |

Every instance also carries the tags `atlas-k8s:<cluster>`,
`k8s-namespace:<ns>`, `k8s-service:<name>` and `k8s-pod:<pod>`. The console
uses them to show where each instance came from.

## Lifecycle

| Kubernetes | Atlas |
|------------|-------|
| Pod becomes ready | Registered. Without `atlas.io/health`, the controller sends TTL heartbeats while the pod stays ready. |
| Pod not ready | Stays registered but gets no heartbeats, so it turns unhealthy after one TTL. With `atlas.io/health`, the node's own check decides. |
| Pod terminating, still serving | Drained: lookups, DNS and client load balancers skip it while it shuts down. |
| Pod or Service deleted, or annotation removed | Deregistered. |

The controller lists, then watches, Services and EndpointSlices, so changes
apply within a second. Every minute it compares Atlas with the cluster and
removes registrations carrying its tag that no longer exist, for example
after a restart. Give each Kubernetes cluster its own `k8sSync.cluster` name,
so controllers in different clusters never remove each other's records.

## Reaching services from pods

Pods can use Atlas DNS (`<service>.atlas`, SRV `_<service>._tcp.atlas`). To
do that, forward the `atlas` zone from CoreDNS to the Atlas Service:

```
atlas:53 {
    forward . <atlas-service-cluster-ip>:5353
}
```

Pods can also use a client library, or the HTTP lookup API at
`http://atlas.atlas.svc:8080`.

## Running the controller outside the cluster

```bash
kubectl proxy &
ATLAS_K8S_API_URL=http://127.0.0.1:8001 ATLAS_ADDR=http://127.0.0.1:8080 \
ATLAS_TOKEN=<write token> atlas-k8s
```

| Variable | Default | |
|----------|---------|---|
| `ATLAS_ADDR` | `http://atlas:8080` | Atlas API |
| `ATLAS_TOKEN` / `ATLAS_TOKEN_FILE` | — | Token with the write scope |
| `ATLAS_K8S_CLUSTER` | `kubernetes` | Owner tag name |
| `ATLAS_K8S_NAMESPACES` | all | Comma-separated namespaces |
| `ATLAS_K8S_TTL` | `15s` | TTL for pods without `atlas.io/health` |
| `ATLAS_K8S_CLUSTER_DOMAIN` | `cluster.local` | For `atlas.io/mode: service` |
| `ATLAS_K8S_API_URL` / `_TOKEN` / `_CA_FILE` | in-cluster | API server outside a pod |
| `ATLAS_K8S_HEALTH_ADDR` | `:8081` | Liveness endpoint |
