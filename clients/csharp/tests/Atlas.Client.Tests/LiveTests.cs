using System.Text;

namespace Atlas.Client.Tests;

/// <summary>Integration tests against a running Atlas node with an admin token.</summary>
public sealed class LiveTests : IDisposable
{
    private readonly AtlasClient? _atlas = LiveNode.Configured ? LiveNode.Connect() : null;

    private AtlasClient Atlas => _atlas ?? throw new InvalidOperationException("live node not configured");

    public void Dispose() => _atlas?.Dispose();

    [Fact]
    public void LiveNodeIsConfiguredWhenRequired()
    {
        // CI sets ATLAS_TEST_REQUIRE_LIVE so a missing node fails the run
        // instead of silently skipping every live test.
        if (Environment.GetEnvironmentVariable("ATLAS_TEST_REQUIRE_LIVE") == "1")
        {
            Assert.True(LiveNode.Configured, "ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN must be set");
        }
    }

    [LiveFact]
    public async Task NodeAndMembers()
    {
        var node = await Atlas.GetNodeAsync();
        Assert.NotEmpty(node.Node.Id);
        Assert.NotEmpty(node.Version);
        var members = await Atlas.GetMembersAsync();
        Assert.Contains(members, m => m.Id == node.Node.Id && m.State == "alive");
    }

    [LiveFact]
    public async Task IdempotentRegistrationUpdateAndDelete()
    {
        var name = LiveNode.Unique("csharp");
        var spec = new ServiceSpec(name, "10.8.0.1:80") { Tags = ["csharp"], TrafficWeight = 5 };
        var first = await Atlas.RegisterAsync(spec, idempotencyKey: "deploy-" + name);
        var again = await Atlas.RegisterAsync(spec, idempotencyKey: "deploy-" + name);
        Assert.Equal(first.Id, again.Id);
        Assert.Equal(1, again.Revision);
        Assert.Equal(5, first.TrafficWeight);
        Assert.Equal(new[] { "csharp" }, first.Tags);

        var updated = await Atlas.UpdateAsync(first.Id, new ServicePatch { Version = "2", TrafficWeight = 7 }, ifRevision: 1);
        Assert.Equal("2", updated.Version);
        Assert.Equal(7, updated.TrafficWeight);
        Assert.Equal("10.8.0.1:80", updated.Address);
        var stale = await Assert.ThrowsAsync<AtlasException>(() => Atlas.UpdateAsync(first.Id, new ServicePatch { Version = "3" }, ifRevision: 1));
        Assert.Equal("precondition_failed", stale.Code);
        Assert.Equal(412, stale.Status);

        var replaced = await Atlas.ReplaceAsync(first.Id, new ServiceSpec(name, "10.8.0.2:80"), updated.Revision);
        Assert.Equal("10.8.0.2:80", replaced.Address);
        Assert.Empty(replaced.Tags);
        Assert.Equal(first.Id, (await Atlas.LookupAsync(name)).Id);

        await Atlas.DeleteAsync(first.Id);
        var missing = await Assert.ThrowsAsync<AtlasException>(() => Atlas.GetAsync(first.Id));
        Assert.Equal("not_found", missing.Code);
        Assert.Equal(404, missing.Status);
        Assert.NotEmpty(missing.RequestId);
    }

    [LiveFact]
    public async Task TtlHeartbeats()
    {
        var service = await Atlas.RegisterAsync(new ServiceSpec(LiveNode.Unique("ttl"), "10.8.1.1:80") { TtlSeconds = 30 });
        Assert.Equal("unknown", service.Health);
        Assert.Equal(30, service.TtlSeconds);
        var beat = await Atlas.HeartbeatAsync(service.Id);
        Assert.Equal("healthy", beat.Health);
        Assert.True(beat.HeartbeatAt.HasValue);

        var plain = await Atlas.RegisterAsync(new ServiceSpec(LiveNode.Unique("plain"), "10.8.1.2:80"));
        var error = await Assert.ThrowsAsync<AtlasException>(() => Atlas.HeartbeatAsync(plain.Id));
        Assert.Equal("no_ttl", error.Code);
    }

    [LiveFact]
    public async Task PagingAndFilters()
    {
        var name = LiveNode.Unique("page");
        for (var i = 0; i < 5; i++)
        {
            await Atlas.RegisterAsync(new ServiceSpec(name, $"10.8.2.{i + 1}:80") { Tags = [i % 2 == 0 ? "even" : "odd"] });
        }
        var page = await Atlas.ListPageAsync(new ServiceFilter { Name = name }, limit: 2);
        Assert.Equal(2, page.Items.Count);
        Assert.NotNull(page.NextCursor);
        Assert.Equal(5, (await Atlas.ListServicesAsync(new ServiceFilter { Name = name })).Count);
        Assert.Equal(3, (await Atlas.ListServicesAsync(new ServiceFilter { Name = name, Tags = ["even"] })).Count);
    }

    [LiveFact]
    public async Task DrainingAndRoutingOverrides()
    {
        var name = LiveNode.Unique("drain");
        var blue = await Atlas.RegisterAsync(new ServiceSpec(name, "10.8.3.1:80"));
        var green = await Atlas.RegisterAsync(new ServiceSpec(name, "10.8.3.2:80") { HealthCheck = "tcp://10.8.3.2:80", UnhealthyThreshold = 4 });
        Assert.Equal(4, green.UnhealthyThreshold);
        Assert.True((await Atlas.DrainAsync(blue.Id)).Draining);
        for (var i = 0; i < 4; i++)
        {
            var picked = await Atlas.LookupAsync(name, new LookupOptions { Stale = "exclude", Latency = "nearest", Tolerance = TimeSpan.FromMilliseconds(10) });
            Assert.NotEqual(blue.Id, picked.Id);
        }
        Assert.False((await Atlas.UndrainAsync(blue.Id)).Draining);
        var bad = await Assert.ThrowsAsync<AtlasException>(() => Atlas.LookupAsync(name, new LookupOptions { Stale = "sometimes" }));
        Assert.Equal("validation_failed", bad.Code);
    }

    [LiveFact]
    public async Task ServiceHistory()
    {
        var name = LiveNode.Unique("hist");
        var service = await Atlas.RegisterAsync(new ServiceSpec(name, "10.8.4.1:80"));
        await Atlas.UpdateAsync(service.Id, new ServicePatch { Version = "2" });
        var first = await Atlas.GetHistoryAsync(service.Id, limit: 1);
        Assert.Equal("updated", Assert.Single(first.Items).Change);
        Assert.True(first.NextBefore.HasValue);
        var rest = await Atlas.GetHistoryAsync(service.Id, before: first.NextBefore!.Value);
        Assert.Equal(new[] { "registered" }, rest.Items.Select(e => e.Change));
        Assert.False(rest.NextBefore.HasValue);
        Assert.Equal(2, (await Atlas.GetRecentHistoryAsync(name)).Items.Count);
    }

    [LiveFact]
    public async Task WatchStreamsServiceEventsAndResets()
    {
        var name = LiveNode.Unique("watch");
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(15));
        var first = FirstEventAsync(Atlas.WatchAsync(new WatchOptions { Kinds = "service", Service = name }, timeout.Token));
        await Task.Delay(500);
        await Atlas.RegisterAsync(new ServiceSpec(name, "10.8.5.1:80"));
        var received = await first;
        Assert.Equal("service.registered", received.Type);
        Assert.Equal(name, received.Service?.Name);
        Assert.NotEmpty(received.Id);

        var reset = await FirstEventAsync(Atlas.WatchAsync(new WatchOptions { LastEventId = "elsewhere.1", Reconnect = false }, timeout.Token));
        Assert.True(reset.IsReset);
    }

    [LiveFact]
    public async Task AdminCredentialsAuditBackupAndEviction()
    {
        Assert.Contains(await Atlas.GetCredentialsAsync(), c => c.Current);

        var name = LiveNode.Unique("ci");
        var created = await Atlas.CreateCredentialAsync(name, ["read"], DateTimeOffset.UtcNow.AddDays(1));
        Assert.Equal("managed", created.Credential.Source);
        Assert.NotEmpty(created.Token);
        using (var reader = new AtlasClient(LiveNode.Endpoint, created.Token))
        {
            Assert.NotEmpty(await reader.GetMembersAsync());
            var denied = await Assert.ThrowsAsync<AtlasException>(() => reader.RegisterAsync(new ServiceSpec(name, "10.8.6.1:80")));
            Assert.Equal(403, denied.Status);
        }
        await Atlas.RevokeCredentialAsync(created.Credential.Id);
        Assert.Contains(await Atlas.GetCredentialsAsync(), c => c.Id == created.Credential.Id && c.Revoked);

        var audit = await Atlas.GetAuditAsync(limit: 50);
        Assert.Contains(audit.Items, e => e.Action == "credential.created" && e.ResourceId == created.Credential.Id);
        Assert.Contains(audit.Items, e => e.Action == "credential.revoked" && e.ResourceId == created.Credential.Id);

        using var snapshot = new MemoryStream();
        await Atlas.BackupAsync(snapshot);
        Assert.Equal("SQLite format 3", Encoding.ASCII.GetString(snapshot.GetBuffer(), 0, 15));

        var unknown = await Assert.ThrowsAsync<AtlasException>(() => Atlas.EvictNodeAsync("no-such-node"));
        Assert.Equal("not_found", unknown.Code);
    }

    private static async Task<AtlasEvent> FirstEventAsync(IAsyncEnumerable<AtlasEvent> stream)
    {
        await foreach (var item in stream)
        {
            return item;
        }
        throw new InvalidOperationException("event stream ended without an event");
    }
}
