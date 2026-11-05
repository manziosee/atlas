---
page_title: "Atlas data sources"
description: |-
  atlas_services, atlas_service, atlas_lookup, atlas_members and atlas_node.
---

# Data sources

## atlas_services

Every instance matching the filters.

```terraform
data "atlas_services" "payments" {
  name   = "payments"
  health = "healthy"
  tags   = ["primary"]
}
```

- Filters (all optional): `name`, `region`, `health`, `version`, `node_id`, `tags`.
- `instances` — List of instances (`id`, `name`, `address`, `node_id`, `region`,
  `version`, `tags`, `health`, `health_check`, `ttl_seconds`, `draining`, `revision`).
- `addresses` — Sorted list of their addresses.

## atlas_service

One instance by `id`, with the same attributes as above.

## atlas_lookup

The instance the node selects, using the same rules as clients and DNS.

```terraform
data "atlas_lookup" "payments" {
  name   = "payments"
  region = "eu"
  key    = "user-42"
}
```

- `name` (Required); `region`, `key`, `tags`, `stale` (`include`,
  `prefer-live`, `exclude`), `latency` (`nearest`, `off`), `tolerance` (Optional).
- `address`, `instance` — The selected instance.

## atlas_members

- `members` — Every member (`id`, `addr`, `api_addr`, `region`, `state`).
- `alive` — Sorted IDs of alive members.

## atlas_node

The connected node: `id`, `region`, `addr`, `version`, `commit`.
