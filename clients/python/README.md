# Atlas client for Python

A client for the [Atlas](https://github.com/manziosee/atlas) service registry.
It needs only the standard library and covers the whole HTTP API:
registration with idempotency keys, heartbeats, revision-checked updates,
draining, routed lookups, metadata filters, history, resumable watch streams
and admin calls. An asyncio variant is included.

```bash
pip install atlas-service-discovery
```

```python
from atlas import AtlasClient, AtlasError

client = AtlasClient("https://atlas.example:8080", token="...", ca_file="ca.crt")

# Safe to retry: the same idempotency key always maps to the same record.
me = client.register("payments", "10.0.0.7:8443", ttl_seconds=15,
                     idempotency_key="payments-host-7", metadata={"team": "payments"})
client.heartbeat(me["id"])                 # call every ~5 s from your service

instance = client.lookup("payments", region="eu", metadata={"team": "payments"})
print(instance["address"])

try:
    client.update(me["id"], version="2.0.0", if_revision=me["revision"])
except AtlasError as err:
    if err.code == "precondition_failed":
        ...                                # someone changed it first; re-read and retry

for event in client.watch(kinds=["service"], service="payments"):   # reconnects and resumes
    print(event["type"], event["service"]["address"])
```

```python
from atlas import AsyncAtlasClient

client = AsyncAtlasClient("https://atlas.example:8080", token="...")
instance = await client.lookup("payments")
```

For a private CA or mutual TLS, pass `ca_file`, `client_cert` and `client_key`.
The import name is `atlas`. Licensed under Apache-2.0.
