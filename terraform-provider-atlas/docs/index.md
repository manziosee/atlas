---
page_title: "Atlas Provider"
description: |-
  Manage Atlas service-discovery registrations as code, and read cluster state.
---

# Atlas Provider

[Atlas](https://github.com/manziosee/atlas) is a distributed service-discovery
system. This provider registers service instances in Atlas and reads its
registry, lookups and membership.

Connect it to any node. Writes to services owned by other nodes are forwarded
to the owner, and the node you connect to owns the services you create.

## Example

```terraform
provider "atlas" {
  address = "https://atlas.example.com:8080" # or ATLAS_ADDR
  # token = var.atlas_token                   # or ATLAS_TOKEN
}

resource "atlas_service" "orders_db" {
  name         = "orders-db"
  address      = "10.0.4.20:5432"
  health_check = "tcp://10.0.4.20:5432"
}
```

## Schema

- `address` (String) — Base URL of an Atlas node's HTTP API. Default: `ATLAS_ADDR`, then `http://127.0.0.1:8080`.
- `token` (String, Sensitive) — API token. Resources need the `write` scope, data sources `read`. Default: `ATLAS_TOKEN`.
- `ca_file` (String) — CA bundle for HTTPS. Default: `ATLAS_CA_FILE`.
- `client_cert`, `client_key` (String) — mTLS client certificate and key. Default: `ATLAS_CLIENT_CERT`, `ATLAS_CLIENT_KEY`.
- `timeout` (String) — Per-call timeout as a Go duration. Default `30s`.

## What it does not manage

Atlas API credentials and node configuration are set on the nodes
(environment, Helm values or Kubernetes Secrets), not through the API, so this
provider does not manage them. Use the Helm chart or your configuration tool
for those.
