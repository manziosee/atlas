using System.Net;
using System.Text;
using System.Text.Json;
using Atlas.Client.DependencyInjection;
using Microsoft.Extensions.DependencyInjection;

namespace Atlas.Client.Tests;

/// <summary>Tests that need no running node.</summary>
public sealed class OfflineTests
{
    [Theory]
    [InlineData("ftp://example")]
    [InlineData("https://example/api")]
    [InlineData("https://example?x=1")]
    [InlineData("example:8080")]
    public void RejectsNonOriginEndpoints(string endpoint)
    {
        Assert.Throws<ArgumentException>(() => new AtlasClient(endpoint, "t"));
    }

    [Fact]
    public void AcceptsOriginsWithTrailingSlash()
    {
        using var atlas = new AtlasClient("https://atlas.example:8443/", "t");
        Assert.Equal(new Uri("https://atlas.example:8443/"), atlas.Endpoint);
    }

    [Fact]
    public async Task UnreachableNodeHasNoStatus()
    {
        using var offline = new AtlasClient("http://127.0.0.1:9", "t", timeout: TimeSpan.FromSeconds(2));
        var error = await Assert.ThrowsAsync<AtlasException>(() => offline.GetNodeAsync());
        Assert.Null(error.Status);
    }

    [Fact]
    public async Task MapsErrorBodiesAndSendsHeaders()
    {
        var handler = new StubHandler(_ => new HttpResponseMessage(HttpStatusCode.PreconditionFailed)
        {
            Content = new StringContent("""{"error":"revision mismatch","code":"precondition_failed","request_id":"r-1","owner_node":"n-2"}""", Encoding.UTF8, "application/json"),
        });
        using var atlas = new AtlasClient("http://atlas.test", "secret", handler);
        var error = await Assert.ThrowsAsync<AtlasException>(() => atlas.UpdateAsync("svc/1", new ServicePatch { Version = "2" }, ifRevision: 4));

        Assert.Equal(412, error.Status);
        Assert.Equal("precondition_failed", error.Code);
        Assert.Equal("r-1", error.RequestId);
        Assert.Equal("n-2", error.OwnerNode);
        Assert.Equal("revision mismatch", error.Message);

        var request = Assert.Single(handler.Requests);
        Assert.Equal(HttpMethod.Patch, request.Method);
        Assert.Equal("http://atlas.test/v1/services/svc%2F1", request.Url);
        Assert.Equal("Bearer secret", request.Authorization);
        Assert.Equal("\"4\"", request.IfMatch);
        Assert.Equal("""{"version":"2"}""", request.Body);
    }

    [Fact]
    public async Task RegisterSendsIdAndIdempotencyKey()
    {
        var handler = new StubHandler(_ => Json(HttpStatusCode.Created, """{"id":"a","name":"api","address":"10.0.0.1:80","health":"unknown","revision":1,"traffic_weight":1,"updated_at":"2026-01-02T03:04:05Z"}"""));
        using var atlas = new AtlasClient("http://atlas.test", "t", handler);
        var service = await atlas.RegisterAsync(new ServiceSpec("api", "10.0.0.1:80") { TtlSeconds = 15, Tags = ["blue"] }, serviceId: "a", idempotencyKey: "deploy-1");

        Assert.Equal("a", service.Id);
        Assert.Equal(new DateTimeOffset(2026, 1, 2, 3, 4, 5, TimeSpan.Zero), service.UpdatedAt);
        Assert.False(service.CheckedAt.HasValue);
        var request = Assert.Single(handler.Requests);
        Assert.Equal("deploy-1", request.IdempotencyKey);
        Assert.Equal("""{"id":"a","name":"api","address":"10.0.0.1:80","tags":["blue"],"ttl_seconds":15}""", request.Body);
    }

    [Fact]
    public async Task LookupEncodesRoutingOverrides()
    {
        var handler = new StubHandler(_ => Json(HttpStatusCode.OK, """{"id":"a","name":"pay ments"}"""));
        using var atlas = new AtlasClient("http://atlas.test", "t", handler);
        await atlas.LookupAsync("pay ments", new LookupOptions
        {
            Region = "eu",
            Key = "user-7",
            Tags = ["blue", "v2"],
            Stale = "exclude",
            Latency = "nearest",
            Tolerance = TimeSpan.FromMilliseconds(2.5),
        });
        Assert.Equal(
            "http://atlas.test/v1/lookup/pay%20ments?region=eu&key=user-7&tag=blue&tag=v2&stale=exclude&latency=nearest&tolerance=2.5ms",
            Assert.Single(handler.Requests).Url);
    }

    [Fact]
    public async Task ListServicesFollowsCursors()
    {
        var handler = new StubHandler(request => request.RequestUri!.Query.Contains("cursor=c2", StringComparison.Ordinal)
            ? Json(HttpStatusCode.OK, """{"services":[{"id":"b"}]}""")
            : Json(HttpStatusCode.OK, """{"services":[{"id":"a"}],"next_cursor":"c2"}"""));
        using var atlas = new AtlasClient("http://atlas.test", "t", handler);
        var all = await atlas.ListServicesAsync(new ServiceFilter { Name = "api", Tags = ["blue"] });

        Assert.Equal(new[] { "a", "b" }, all.Select(s => s.Id));
        Assert.Equal(2, handler.Requests.Count);
        Assert.Equal("http://atlas.test/v1/services?name=api&tag=blue&limit=1000", handler.Requests[0].Url);
    }

    [Fact]
    public async Task WatchParsesFramesAndUsesEventNameFallback()
    {
        const string stream = ": connected\nretry: 3000\n\n"
            + "event: reset\ndata: {\"type\":\"reset\"}\n\n"
            + "id: n1.7\nevent: service.registered\ndata: {\"id\":\"n1.7\",\"kind\":\"service\",\"service\":{\"id\":\"a\",\"name\":\"api\"}}\n\n";
        var handler = new StubHandler(_ => new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent(stream, Encoding.UTF8, "text/event-stream") });
        using var atlas = new AtlasClient("http://atlas.test", "t", handler);

        var events = new List<AtlasEvent>();
        await foreach (var item in atlas.WatchAsync(new WatchOptions { Kinds = "service", LastEventId = "n1.3", Reconnect = false }))
        {
            events.Add(item);
        }

        Assert.Equal(2, events.Count);
        Assert.True(events[0].IsReset);
        Assert.Equal("service.registered", events[1].Type);
        Assert.Equal("n1.7", events[1].Id);
        Assert.Equal("api", events[1].Service?.Name);
        var request = Assert.Single(handler.Requests);
        Assert.Equal("n1.3", request.LastEventId);
        Assert.Equal("http://atlas.test/v1/events?kinds=service", request.Url);
    }

    [Fact]
    public async Task WatchThrowsHttpErrorsInsteadOfRetrying()
    {
        var handler = new StubHandler(_ => Json(HttpStatusCode.Unauthorized, """{"error":"missing or invalid token","code":"unauthorized"}"""));
        using var atlas = new AtlasClient("http://atlas.test", "t", handler);
        var error = await Assert.ThrowsAsync<AtlasException>(async () =>
        {
            await foreach (var _ in atlas.WatchAsync())
            {
            }
        });
        Assert.Equal(401, error.Status);
        Assert.Single(handler.Requests);
    }

    [Fact]
    public void FormatsTolerancesAsGoDurations()
    {
        Assert.Equal("0ms", AtlasClient.FormatDuration(TimeSpan.Zero));
        Assert.Equal("10ms", AtlasClient.FormatDuration(TimeSpan.FromMilliseconds(10)));
        Assert.Equal("1500ms", AtlasClient.FormatDuration(TimeSpan.FromSeconds(1.5)));
        Assert.Throws<ArgumentOutOfRangeException>(() => AtlasClient.FormatDuration(TimeSpan.FromMilliseconds(-1)));
    }

    [Fact]
    public void ParsesAuditDetailsAsRawJson()
    {
        var entry = JsonSerializer.Deserialize<AuditEntry>(
            """{"seq":3,"at":"2026-01-02T03:04:05Z","actor_id":"abc","action":"credential.created","resource_id":"r","details":{"name":"ci","scopes":["read"]}}""",
            AtlasClient.Json)!;
        Assert.Equal(3, entry.Seq);
        Assert.Equal("ci", entry.Details.GetProperty("name").GetString());
    }

    [Fact]
    public void DependencyInjectionResolvesAClientWithoutAnHttpTimeout()
    {
        var services = new ServiceCollection();
        services.AddAtlasClient(o =>
        {
            o.Endpoint = "https://atlas.example:8080";
            o.Token = "t";
        });
        using var provider = services.BuildServiceProvider();
        var atlas = provider.GetRequiredService<AtlasClient>();
        Assert.Equal(new Uri("https://atlas.example:8080/"), atlas.Endpoint);
        var http = provider.GetRequiredService<IHttpClientFactory>().CreateClient(AtlasServiceCollectionExtensions.HttpClientName);
        Assert.Equal(Timeout.InfiniteTimeSpan, http.Timeout);
    }

    [Fact]
    public void DependencyInjectionValidatesSettingsAtStartup()
    {
        var services = new ServiceCollection();
        Assert.Throws<ArgumentException>(() => services.AddAtlasClient(o => o.Endpoint = "https://atlas.example"));
        Assert.Throws<ArgumentException>(() => services.AddAtlasClient(o =>
        {
            o.Endpoint = "not a url";
            o.Token = "t";
        }));
    }

    private static HttpResponseMessage Json(HttpStatusCode status, string body) =>
        new(status) { Content = new StringContent(body, Encoding.UTF8, "application/json") };

    private sealed record RecordedRequest(HttpMethod Method, string Url, string? Authorization, string? IfMatch, string? IdempotencyKey, string? LastEventId, string? Body);

    private sealed class StubHandler(Func<HttpRequestMessage, HttpResponseMessage> respond) : HttpMessageHandler
    {
        public List<RecordedRequest> Requests { get; } = [];

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            Requests.Add(new RecordedRequest(
                request.Method,
                request.RequestUri!.AbsoluteUri,
                request.Headers.Authorization?.ToString(),
                Header(request, "If-Match"),
                Header(request, "Idempotency-Key"),
                Header(request, "Last-Event-ID"),
                request.Content is null ? null : await request.Content.ReadAsStringAsync(cancellationToken)));
            return respond(request);
        }

        private static string? Header(HttpRequestMessage request, string name) =>
            request.Headers.TryGetValues(name, out var values) ? string.Join(",", values) : null;
    }
}
