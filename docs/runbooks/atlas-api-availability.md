# Atlas API availability

Use this runbook when `AtlasAPIAvailabilitySLOBurn` or `AtlasAPIRateLimited`
fires.

1. Check whether the alert is isolated to one node or affects all nodes:
   compare `atlas:http_availability:ratio5m` and `up{job="atlas"}` by `node`.
2. Inspect `atlas_http_requests_total` grouped by route and status code. A rise
   in 401 points to credentials; 429 points to client rate limits; 5xx points
   to handler, storage, or dependency failures.
3. Check node readiness and recent structured API logs. Confirm the SQLite
   volume is mounted and the process can write to its data directory.
4. For 429s, identify the client and its trusted-proxy path before changing
   `ATLAS_RATE_LIMIT_RPS` or `ATLAS_RATE_LIMIT_BURST`. Do not disable auth or
   rate limiting to silence the alert.
5. After remediation, verify readiness and confirm the 5-minute availability
   ratio returns above 99%.

Useful checks: `GET /readyz`, `GET /metrics`, and authenticated `GET
/v1/members` from the affected node.
