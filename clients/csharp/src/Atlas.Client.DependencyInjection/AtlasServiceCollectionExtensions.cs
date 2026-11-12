using Microsoft.Extensions.DependencyInjection;

namespace Atlas.Client.DependencyInjection;

/// <summary>Settings for <see cref="AtlasServiceCollectionExtensions.AddAtlasClient(IServiceCollection, Action{AtlasClientOptions})"/>.</summary>
public sealed class AtlasClientOptions
{
    /// <summary>The node origin, such as <c>https://atlas.example:8080</c>.</summary>
    public string Endpoint { get; set; } = "";

    /// <summary>API token sent as a bearer token.</summary>
    public string Token { get; set; } = "";

    /// <summary>Bound on each request; watch streams are exempt.</summary>
    public TimeSpan Timeout { get; set; } = AtlasClient.DefaultTimeout;
}

/// <summary>Registers <see cref="AtlasClient"/> on top of <c>IHttpClientFactory</c>.</summary>
public static class AtlasServiceCollectionExtensions
{
    /// <summary>The named HttpClient the Atlas client uses.</summary>
    public const string HttpClientName = "Atlas";

    /// <summary>
    /// Registers a transient <see cref="AtlasClient"/> backed by a named
    /// <c>HttpClient</c> from <c>IHttpClientFactory</c>, so handler lifetimes and
    /// DNS refresh follow the factory. Configure TLS (a private CA or client
    /// certificate) with <c>ConfigurePrimaryHttpMessageHandler</c> on the returned
    /// builder.
    /// </summary>
    public static IHttpClientBuilder AddAtlasClient(this IServiceCollection services, Action<AtlasClientOptions> configure)
    {
        ArgumentNullException.ThrowIfNull(services);
        ArgumentNullException.ThrowIfNull(configure);
        var options = new AtlasClientOptions();
        configure(options);
        // Validate eagerly so a bad endpoint or missing token fails at startup.
        AtlasClient.ParseEndpoint(options.Endpoint);
        ArgumentException.ThrowIfNullOrEmpty(options.Token, nameof(AtlasClientOptions.Token));

        services.AddTransient(provider =>
        {
            var http = provider.GetRequiredService<IHttpClientFactory>().CreateClient(HttpClientName);
            return new AtlasClient(http, options.Endpoint, options.Token, options.Timeout);
        });
        return services.AddHttpClient(HttpClientName, http =>
        {
            // AtlasClient bounds each request itself; an HttpClient timeout
            // would also cut long-lived watch streams.
            http.Timeout = System.Threading.Timeout.InfiniteTimeSpan;
        });
    }
}
