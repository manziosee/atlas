---
page_title: "atlas_service Resource"
description: |-
  A service instance registered in Atlas.
---

# atlas_service

A service instance registered in Atlas. The node the provider talks to owns
it and runs its health check.

Use `health_check` for instances Terraform manages, such as databases,
appliances and external endpoints. A `ttl_seconds` instance turns unhealthy
after one missed TTL and is removed after three, unless something else sends
heartbeats.

## Example

```terraform
resource "atlas_service" "orders_db" {
  id           = "orders-db-primary"
  name         = "orders-db"
  address      = "10.0.4.20:5432"
  version      = "16.4"
  tags         = ["primary", "postgres"]
  health_check = "tcp://10.0.4.20:5432"
}
```

## Arguments

- `name` (Required) — Service name.
- `address` (Required) — `host:port`.
- `id` (Optional) — Stable ID; generated when omitted. Changing it replaces the instance.
- `version` (Optional) — Version label.
- `tags` (Optional, Set of String) — Up to 16 tags.
- `health_check` (Optional) — `http(s)://`, `tcp://` or `grpc(s)://` URL on the address.
- `check_interval_seconds`, `unhealthy_threshold`, `healthy_threshold` (Optional) — Check tuning; need `health_check`.
- `ttl_seconds` (Optional) — Heartbeat TTL. Not with `health_check`.
- `draining` (Optional, default `false`) — Keep the record but send it no traffic.

## Attributes

- `node_id`, `region` — Owning node and its region.
- `health` — `unknown`, `healthy` or `unhealthy` as last read.
- `revision`, `updated_at` — Revision and last change.

## Behaviour

- Updates use `PUT`, so settings removed from the configuration are cleared.
- If the instance is deleted or expires outside Terraform, the next plan
  creates it again.

## Import

```shell
terraform import atlas_service.orders_db orders-db-primary
```
