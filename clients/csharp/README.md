# Atlas client for .NET

A client for the [Atlas](https://github.com/manziosee/atlas) service registry
HTTP API. It targets .NET 8 and uses only `HttpClient` and `System.Text.Json`.

It covers the whole v1 API:

- **Registration:** register with an idempotency key, heartbeats, partial
  updates and full replacement with revision checks (`If-Match`), draining and
  deregistration.
- **Reads:** routed lookups with every override (region, key, tags, stale
  policy, latency mode and tolerance), and listings that follow cursors.
- **History:** per-service and recent change history.
- **Cluster:** node and member views.
- **Events:** a resumable event stream as `IAsyncEnumerable<AtlasEvent>`.
- **Admin:** credentials, the audit log, backups and node eviction.

## Use

```csharp
using Atlas.Client;

using var atlas = new AtlasClient("https://atlas.example:8080", Environment.GetEnvironmentVariable("ATLAS_TOKEN")!);

var me = await atlas.RegisterAsync(
    new ServiceSpec("payments", "10.0.0.7:8443") { TtlSeconds = 15, Tags = ["blue"] },
    idempotencyKey: "payments-" + Environment.MachineName);
await atlas.HeartbeatAsync(me.Id);

var instance = await atlas.LookupAsync("payments", new LookupOptions { Region = "eu", Stale = "exclude" });

try
{
    await atlas.UpdateAsync(me.Id, new ServicePatch { Version = "2.0.0" }, ifRevision: me.Revision);
}
catch (AtlasException e) when (e.Code == "precondition_failed")
{
    // Someone else changed it: re-read and retry.
}

await foreach (var change in atlas.WatchAsync(new WatchOptions { Kinds = "service", Service = "payments" }, stoppingToken))
{
    if (change.IsReset)
    {
        // Events were missed: re-read current state with ListServicesAsync.
        continue;
    }
    Console.WriteLine($"{change.Type} {change.Service?.Address}");
}
```

`AtlasException` carries the server's error `Code`, HTTP `Status` (null when
no response arrived), `RequestId` and, for forwarded writes, `OwnerNode`.

Every call takes a `CancellationToken` and is bounded by the client timeout,
which defaults to 5 seconds. Event streams are not time-limited: they run until
you cancel them or stop enumerating. A dropped stream reconnects with backoff
and resumes after the last event it delivered. HTTP errors are thrown rather
than retried, so a revoked token fails fast.

## Private CA and mutual TLS

Pass a handler configured for your certificates:

```csharp
var handler = new SocketsHttpHandler
{
    SslOptions = new SslClientAuthenticationOptions
    {
        ClientCertificates = [X509Certificate2.CreateFromPemFile("client.crt", "client.key")],
        RemoteCertificateValidationCallback = (_, cert, chain, errors) => ValidateAgainstMyCa(cert, chain, errors),
    },
};
using var atlas = new AtlasClient("https://atlas.internal:8080", token, handler);
```

## Dependency injection

`Manziosee.Atlas.Client.DependencyInjection` registers the client on top of
`IHttpClientFactory`:

```csharp
builder.Services.AddAtlasClient(options =>
{
    options.Endpoint = builder.Configuration["Atlas:Endpoint"]!;
    options.Token = builder.Configuration["Atlas:Token"]!;
});
// Then inject AtlasClient anywhere.
```

To set up TLS, chain `.ConfigurePrimaryHttpMessageHandler(...)` on the
returned builder.

## Build and test

```bash
cd clients/csharp
dotnet test tests/Atlas.Client.Tests
```

The offline tests always run. The live tests need a running node: set
`ATLAS_TEST_ENDPOINT` and an admin `ATLAS_TEST_TOKEN`, for example from
`./scripts/start-test-node.sh`.

A runnable service built on this client is in
[`examples/csharp`](../../examples/csharp). It registers itself with a TTL,
sends heartbeats, registers again after it expires and deregisters on shutdown.
