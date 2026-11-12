// Registers an HTTP service with Atlas and keeps it alive with heartbeats, or
// (with the "client" argument) looks one up and calls it.
//
//   ATLAS_ADDR=http://127.0.0.1:8081 ATLAS_TOKEN=... dotnet run
//   ATLAS_ADDR=http://127.0.0.1:8081 ATLAS_TOKEN=... dotnet run -- client

using Atlas.Client;

const string ServiceName = "example-csharp";

try
{
    var (endpoint, token) = AtlasConfig();
    if (args.Length > 0 && args[0] == "client")
    {
        await CallServiceAsync(endpoint, token);
        return 0;
    }
    await ServeAsync(endpoint, token);
    return 0;
}
catch (Exception e)
{
    Console.Error.WriteLine($"example failed: {e.Message}");
    return 1;
}

static (string Endpoint, string Token) AtlasConfig()
{
    var endpoint = Environment.GetEnvironmentVariable("ATLAS_ADDR") ?? "http://127.0.0.1:8081";
    var token = Environment.GetEnvironmentVariable("ATLAS_TOKEN") ?? "";
    if (token.Length == 0)
    {
        throw new InvalidOperationException("ATLAS_TOKEN must contain an Atlas API token");
    }
    return (endpoint, token);
}

static async Task ServeAsync(string endpoint, string token)
{
    var address = Environment.GetEnvironmentVariable("SERVICE_ADDRESS") ?? "127.0.0.1:8087";
    var separator = address.LastIndexOf(':');
    if (separator <= 0 || !int.TryParse(address[(separator + 1)..], out var port) || port is < 1 or > 65535)
    {
        throw new InvalidOperationException($"SERVICE_ADDRESS must be host:port, got '{address}'");
    }
    var listen = Environment.GetEnvironmentVariable("SERVICE_LISTEN_ADDR") ?? $"0.0.0.0:{port}";

    var builder = WebApplication.CreateSlimBuilder();
    builder.WebHost.UseUrls($"http://{listen}");
    builder.Logging.ClearProviders();
    builder.Services.AddSingleton(new AtlasClient(endpoint, token));
    builder.Services.AddSingleton(new Registration(address, $"{ServiceName}-{Environment.MachineName}"));
    builder.Services.AddHostedService<AtlasRegistration>();

    var app = builder.Build();
    app.MapGet("/healthz", () => Results.NoContent());
    app.MapGet("/", () => Results.Text($$"""{"service":"{{ServiceName}}","language":"C#"}""", "application/json"));
    await app.RunAsync();
}

static async Task CallServiceAsync(string endpoint, string token)
{
    using var atlas = new AtlasClient(endpoint, token);
    var selected = await atlas.LookupAsync(ServiceName);
    using var http = new HttpClient { Timeout = TimeSpan.FromSeconds(5) };
    try
    {
        Console.Write(await http.GetStringAsync($"http://{selected.Address}/"));
    }
    catch (HttpRequestException e)
    {
        throw new InvalidOperationException($"call selected instance {selected.Address}: {e.Message}", e);
    }
}

/// <summary>Where this instance is reachable and the idempotency key that keeps re-registration stable.</summary>
internal sealed record Registration(string Address, string IdempotencyKey);

/// <summary>Registers on start, heartbeats at a third of the TTL, re-registers after expiry and deregisters on shutdown.</summary>
internal sealed class AtlasRegistration(AtlasClient atlas, Registration registration, IHostApplicationLifetime lifetime) : BackgroundService
{
    private const int TtlSeconds = 15;
    private Service? _registered;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        // Register only once the HTTP server is listening, so callers that find
        // this instance through Atlas can reach it immediately.
        var started = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        using (lifetime.ApplicationStarted.Register(() => started.TrySetResult()))
        {
            await started.Task.WaitAsync(stoppingToken);
        }
        _registered = await RegisterAsync(stoppingToken);
        Console.WriteLine($"registered example-csharp at {registration.Address} as {_registered.Id}");

        using var timer = new PeriodicTimer(TimeSpan.FromSeconds(TtlSeconds / 3.0));
        while (await timer.WaitForNextTickAsync(stoppingToken))
        {
            try
            {
                await atlas.HeartbeatAsync(_registered!.Id, stoppingToken);
            }
            catch (AtlasException e) when (e.Code == "not_found")
            {
                try
                {
                    _registered = await RegisterAsync(stoppingToken);
                    Console.Error.WriteLine($"service expired; registered again as {_registered.Id}");
                }
                catch (AtlasException again)
                {
                    Console.Error.WriteLine($"re-registration failed: {again.Message}");
                }
            }
            catch (AtlasException e)
            {
                Console.Error.WriteLine($"heartbeat failed: {e.Message}");
            }
        }
    }

    public override async Task StopAsync(CancellationToken cancellationToken)
    {
        await base.StopAsync(cancellationToken);
        if (_registered is null)
        {
            return;
        }
        try
        {
            await atlas.DeleteAsync(_registered.Id, cancellationToken);
        }
        catch (AtlasException e) when (e.Code == "not_found")
        {
            // Already gone, for example after expiring.
        }
        Console.WriteLine($"deregistered {_registered.Id}");
    }

    private Task<Service> RegisterAsync(CancellationToken cancellationToken) =>
        atlas.RegisterAsync(
            new ServiceSpec("example-csharp", registration.Address) { TtlSeconds = TtlSeconds, Version = "1.0.0", Tags = ["example", "csharp"] },
            idempotencyKey: registration.IdempotencyKey,
            cancellationToken: cancellationToken);
}
