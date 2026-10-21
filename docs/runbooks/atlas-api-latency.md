# Atlas API latency

Use this runbook when `AtlasAPILatencySLOBurn` fires.

1. Identify the affected `node` and `route` in the alert labels. Compare p50
   and p95 from `atlas_http_request_duration_seconds` across nodes.
2. Check CPU, memory, SQLite volume latency, request rates, and storage error
   logs on the affected node.
3. For registry routes, inspect record count and compare SQLite query latency;
   for `/v1/events`, check subscriber count and client disconnects.
4. Compare probe RTT and Lifeguard local health score. A busy or overloaded
   node can raise both API and gossip latency.
5. Restore capacity or address the slow dependency, then confirm route p95 is
   below 500 ms for at least 10 minutes.

Do not raise the alert threshold until a measured product SLO change has been
approved and recorded.
