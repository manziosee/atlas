# Atlas Helm chart

Installs an Atlas cluster as a StatefulSet. Optionally, it also installs
`atlas-k8s`, a controller that registers annotated Kubernetes Services in
Atlas.

```bash
helm install atlas oci://ghcr.io/manziosee/charts/atlas -n atlas --create-namespace \
  --set k8sSync.enabled=true \
  --set config.corsOrigins=https://your-console.netlify.app
```

The admin token is stored in the `<release>-secrets` Secret, under the key
`ATLAS_ADMIN_TOKEN`:

```bash
kubectl -n atlas get secret atlas-secrets -o jsonpath='{.data.ATLAS_ADMIN_TOKEN}' | base64 -d
```

## Secrets

When `secrets.existingSecret` is empty, the chart creates one Secret with:

| Key | Use |
|-----|-----|
| `ATLAS_GOSSIP_KEY` | Encrypts gossip between nodes |
| `ATLAS_ADMIN_TOKEN` | Admin token for you |
| `ATLAS_SYNC_TOKEN` | Token the Kubernetes controller uses |
| `ATLAS_API_TOKENS` | Both tokens, as the nodes read them |

Empty values are generated on first install and reused on every upgrade.
The Secret is kept when you uninstall, so a reinstall finds the same
cluster key. To bring your own, set `secrets.existingSecret` to a Secret
with `ATLAS_GOSSIP_KEY`, `ATLAS_API_TOKENS` (or `ATLAS_API_CREDENTIALS`) and
`ATLAS_SYNC_TOKEN`.

## Register a Kubernetes Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: payments
  annotations:
    atlas.io/register: "true"
    atlas.io/health: /healthz     # optional: /path, tcp or grpc; default is a TTL kept by readiness
    atlas.io/tags: team-billing   # optional
```

Every ready pod becomes an Atlas instance. Terminating pods are drained, and
deleted pods are deregistered. See the
[Kubernetes guide](../../../docs/KUBERNETES.md) for every annotation.

## Values

| Value | Default | Description |
|-------|---------|-------------|
| `replicaCount` | `3` | Atlas nodes |
| `autoscaling.enabled` | `false` | Create a CPU-based HPA for the Atlas StatefulSet |
| `autoscaling.minReplicas` / `maxReplicas` | `3` / `10` | Autoscaling bounds; minimum is clamped to three |
| `backup.enabled` | `false` | Schedule S3-compatible snapshots and (by default) weekly restore drills |
| `backup.bucket` / `prefix` / `region` | empty / `atlas` / `us-east-1` | Object-store location; bucket is required when enabled |
| `backup.existingSecret` | empty | Secret containing `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`; omit when using workload identity |
| `image.repository` / `image.tag` | `ghcr.io/manziosee/atlas` / appVersion | Image |
| `config.env` | `development` | `production` requires `tls.enabled` |
| `config.region` | `k8s` | Region for routing |
| `config.location` | — | `lat,lon` for the console's cluster map |
| `config.corsOrigins` | — | Browser origins allowed to call the API |
| `config.consoleURL` | — | Redirect `/` and `/dashboard` to the console |
| `extraEnv` | `{}` | Any other `ATLAS_*` setting |
| `secrets.existingSecret` | — | Use your own Secret |
| `secrets.gossipKey` / `secrets.apiToken` | generated | Fixed secrets |
| `tls.enabled` / `tls.secretName` / `tls.clientAuth` | `false` | HTTPS from a Secret with `tls.crt`, `tls.key`, `ca.crt` |
| `dns.enabled` | `true` | DNS on port 5353 |
| `persistence.size` / `persistence.storageClass` | `1Gi` | Registry volume |
| `podDisruptionBudget.enabled` | `true` | At most one node down voluntarily |
| `networkPolicy.enabled` | `false` | Restrict ingress to what Atlas needs |
| `serviceMonitor.enabled` | `false` | Prometheus Operator scraping |
| `k8sSync.enabled` | `false` | Install the Kubernetes controller |
| `k8sSync.cluster` | `kubernetes` | Owner tag `atlas-k8s:<cluster>` |
| `k8sSync.namespaces` | all | Limit the watch, and its RBAC, to these namespaces |
| `k8sSync.ttl` | `15s` | TTL for pods without `atlas.io/health` |

## Test

```bash
helm test atlas -n atlas
```

## Autoscaling

The chart keeps a fixed three-node cluster by default. To enable CPU-based
scaling, install a metrics server and set:

```bash
helm upgrade --install atlas oci://ghcr.io/manziosee/charts/atlas -n atlas \
  --create-namespace --set autoscaling.enabled=true \
  --set autoscaling.minReplicas=3 --set autoscaling.maxReplicas=10 \
  --set autoscaling.targetCPUUtilizationPercentage=70
```

The HPA scales the StatefulSet, not individual processes. Atlas needs at least
three members; service records replicate and nodes leave membership on SIGTERM.
Review persistent volume costs and the cluster's storage limits when choosing
the maximum. Kubernetes retains claims for scaled-down ordinals by default.

## Off-cluster backups and restore drills

The optional CronJobs take a consistent Atlas API snapshot and upload it to an
S3-compatible bucket using server-side AES256 encryption. A weekly job fetches
the newest object into an ephemeral volume and runs `atlas-cli db-restore`,
which checks SQLite integrity and refuses to overwrite a database. This is an
isolated integrity/restore drill; it never touches the live PVC. Configure a
bucket lifecycle policy separately to set retention and deletion rules.

For AWS, prefer workload identity. Set `backup.serviceAccountAnnotations` to
the cloud role binding and grant that role `s3:PutObject`, `s3:GetObject`, and
`s3:ListBucket` only for the configured backup prefix. For an S3-compatible
store or static credentials, create a Secret with `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`, then set `backup.existingSecret`.

```bash
helm upgrade --install atlas oci://ghcr.io/manziosee/charts/atlas -n atlas \
  --set backup.enabled=true --set backup.bucket=atlas-backups \
  --set backup.prefix=production/atlas --set backup.region=eu-west-1 \
  --set backup.existingSecret=atlas-backup-s3
```

Set `backup.endpointURL` for S3-compatible APIs. Jobs use the Atlas admin
credential from the chart's existing secret and snapshot one cluster member;
registry data is replicated across members. Monitor CronJob failures and run a
manual restore drill before relying on a new bucket or storage provider.
