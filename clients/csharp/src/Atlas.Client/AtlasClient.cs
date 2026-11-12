using System.Globalization;
using System.Net.Http.Headers;
using System.Runtime.CompilerServices;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace Atlas.Client;

/// <summary>
/// Client for the Atlas v1 HTTP API, built on <see cref="HttpClient"/> and
/// System.Text.Json with no other dependencies. Instances are thread-safe; keep
/// one per node endpoint.
/// </summary>
/// <example>
/// <code>
/// using var atlas = new AtlasClient("https://atlas.example:8080", Environment.GetEnvironmentVariable("ATLAS_TOKEN")!);
/// var me = await atlas.RegisterAsync(new ServiceSpec("payments", "10.0.0.7:8443") { TtlSeconds = 15 },
///     idempotencyKey: "payments-" + Environment.MachineName);
/// await atlas.HeartbeatAsync(me.Id);
/// var instance = await atlas.LookupAsync("payments", new LookupOptions { Region = "eu" });
/// </code>
/// </example>
public sealed class AtlasClient : IDisposable
{
    /// <summary>The default bound on each request; watch streams are exempt.</summary>
    public static readonly TimeSpan DefaultTimeout = TimeSpan.FromSeconds(5);

    internal static readonly JsonSerializerOptions Json = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    private static readonly TimeSpan BackupTimeout = TimeSpan.FromMinutes(5);

    private readonly HttpClient _http;
    private readonly bool _ownsHttp;
    private readonly Uri _endpoint;
    private readonly string _token;
    private readonly TimeSpan _timeout;

    /// <summary>
    /// Connects to <paramref name="endpoint"/>, an http:// or https:// origin.
    /// Pass a <paramref name="handler"/> (for example an <see cref="HttpClientHandler"/>
    /// or <see cref="SocketsHttpHandler"/> with a custom CA or client certificate)
    /// for a private CA or mutual TLS.
    /// </summary>
    public AtlasClient(string endpoint, string token, HttpMessageHandler? handler = null, TimeSpan? timeout = null)
        : this(new HttpClient(handler ?? new SocketsHttpHandler(), disposeHandler: true), endpoint, token, timeout)
    {
        _ownsHttp = true;
    }

    /// <summary>
    /// Uses an existing <see cref="HttpClient"/>, such as one from
    /// <c>IHttpClientFactory</c>. Its own <see cref="HttpClient.Timeout"/> also
    /// applies, so set it to <see cref="Timeout.InfiniteTimeSpan"/> to keep
    /// watch streams open; this client bounds each request with
    /// <paramref name="timeout"/> instead. The caller owns <paramref name="http"/>.
    /// </summary>
    public AtlasClient(HttpClient http, string endpoint, string token, TimeSpan? timeout = null)
    {
        ArgumentNullException.ThrowIfNull(http);
        ArgumentException.ThrowIfNullOrEmpty(token);
        _endpoint = ParseEndpoint(endpoint);
        _http = http;
        _token = token;
        _timeout = timeout ?? DefaultTimeout;
        if (_timeout <= TimeSpan.Zero && _timeout != Timeout.InfiniteTimeSpan)
        {
            throw new ArgumentOutOfRangeException(nameof(timeout), "timeout must be positive");
        }
    }

    /// <summary>The node origin this client talks to.</summary>
    public Uri Endpoint => _endpoint;

    // --- registry ---------------------------------------------------------

    /// <summary>
    /// Registers an instance owned by the receiving node. With a
    /// <paramref name="serviceId"/> or <paramref name="idempotencyKey"/>,
    /// repeating the call returns the same record instead of a duplicate.
    /// </summary>
    public Task<Service> RegisterAsync(ServiceSpec spec, string? serviceId = null, string? idempotencyKey = null, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(spec);
        var fields = JsonSerializer.SerializeToNode(spec, Json)!.AsObject();
        var body = new JsonObject();
        if (!string.IsNullOrEmpty(serviceId))
        {
            body["id"] = serviceId;
        }
        foreach (var name in fields.Select(field => field.Key).ToList())
        {
            var value = fields[name];
            fields.Remove(name);
            body[name] = value;
        }
        var headers = new List<KeyValuePair<string, string>>();
        if (!string.IsNullOrEmpty(idempotencyKey))
        {
            headers.Add(new("Idempotency-Key", idempotencyKey));
        }
        return SendAsync<Service>(HttpMethod.Post, "/v1/services", null, body, headers, cancellationToken);
    }

    /// <summary>Creates or fully replaces the service <paramref name="id"/>; a non-zero <paramref name="ifRevision"/> requires that revision.</summary>
    public Task<Service> ReplaceAsync(string id, ServiceSpec spec, long ifRevision = 0, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(spec);
        return SendAsync<Service>(HttpMethod.Put, ServicePath(id), null, spec, IfMatch(ifRevision), cancellationToken);
    }

    /// <summary>
    /// Changes only the fields set in <paramref name="patch"/>. Any node accepts
    /// this and forwards it to the owner. With a non-zero
    /// <paramref name="ifRevision"/>, it fails with code <c>precondition_failed</c>
    /// if the service changed since that revision.
    /// </summary>
    public Task<Service> UpdateAsync(string id, ServicePatch patch, long ifRevision = 0, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(patch);
        return SendAsync<Service>(HttpMethod.Patch, ServicePath(id), null, patch, IfMatch(ifRevision), cancellationToken);
    }

    /// <summary>Stops new traffic to an instance while it stays registered.</summary>
    public Task<Service> DrainAsync(string id, CancellationToken cancellationToken = default) =>
        UpdateAsync(id, new ServicePatch { Draining = true }, 0, cancellationToken);

    /// <summary>Returns a drained instance to rotation.</summary>
    public Task<Service> UndrainAsync(string id, CancellationToken cancellationToken = default) =>
        UpdateAsync(id, new ServicePatch { Draining = false }, 0, cancellationToken);

    /// <summary>Refreshes a TTL instance. Code <c>not_found</c> means it expired: register again.</summary>
    public Task<Service> HeartbeatAsync(string id, CancellationToken cancellationToken = default) =>
        SendAsync<Service>(HttpMethod.Post, ServicePath(id) + "/heartbeat", null, null, null, cancellationToken);

    /// <summary>Reads one service by ID.</summary>
    public Task<Service> GetAsync(string id, CancellationToken cancellationToken = default) =>
        SendAsync<Service>(HttpMethod.Get, ServicePath(id), null, null, null, cancellationToken);

    /// <summary>Deregisters a service.</summary>
    public async Task DeleteAsync(string id, CancellationToken cancellationToken = default)
    {
        using var response = await SendRawAsync(HttpMethod.Delete, ServicePath(id), null, null, null, _timeout, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>One page of services; pass the previous page's <see cref="ServicePage.NextCursor"/> as <paramref name="cursor"/>.</summary>
    public async Task<ServicePage> ListPageAsync(ServiceFilter? filter = null, string? cursor = null, int limit = 0, CancellationToken cancellationToken = default)
    {
        var query = new List<KeyValuePair<string, string>>();
        if (filter is not null)
        {
            Add(query, "name", filter.Name);
            Add(query, "region", filter.Region);
            Add(query, "health", filter.Health);
            Add(query, "version", filter.Version);
            Add(query, "node_id", filter.NodeId);
            AddTags(query, filter.Tags);
            AddMetadata(query, filter.Metadata);
        }
        Add(query, "cursor", cursor);
        if (limit > 0)
        {
            query.Add(new("limit", limit.ToString(CultureInfo.InvariantCulture)));
        }
        var page = await SendAsync<ServiceList>(HttpMethod.Get, "/v1/services", query, null, null, cancellationToken).ConfigureAwait(false);
        return new ServicePage(page.Services ?? [], string.IsNullOrEmpty(page.NextCursor) ? null : page.NextCursor);
    }

    /// <summary>Every matching service, following <c>next_cursor</c> across pages.</summary>
    public async Task<IReadOnlyList<Service>> ListServicesAsync(ServiceFilter? filter = null, CancellationToken cancellationToken = default)
    {
        var all = new List<Service>();
        string? cursor = null;
        do
        {
            var page = await ListPageAsync(filter, cursor, 1000, cancellationToken).ConfigureAwait(false);
            all.AddRange(page.Items);
            cursor = page.NextCursor;
        }
        while (cursor is not null);
        return all;
    }

    /// <summary>Selects one routable instance of <paramref name="name"/> using the node's routing rules.</summary>
    public Task<Service> LookupAsync(string name, LookupOptions? options = null, CancellationToken cancellationToken = default)
    {
        var query = new List<KeyValuePair<string, string>>();
        if (options is not null)
        {
            Add(query, "region", options.Region);
            Add(query, "key", options.Key);
            AddTags(query, options.Tags);
            AddMetadata(query, options.Metadata);
            Add(query, "stale", options.Stale);
            Add(query, "latency", options.Latency);
            if (options.Tolerance is { } tolerance)
            {
                query.Add(new("tolerance", FormatDuration(tolerance)));
            }
        }
        return SendAsync<Service>(HttpMethod.Get, "/v1/lookup/" + Escape(name), query, null, null, cancellationToken);
    }

    /// <summary>Recorded changes to one service, newest first; a non-zero <paramref name="before"/> continues from a sequence number.</summary>
    public Task<LogPage<HistoryEntry>> GetHistoryAsync(string id, long before = 0, int limit = 0, CancellationToken cancellationToken = default) =>
        HistoryAsync(ServicePath(id) + "/history", [], before, limit, cancellationToken);

    /// <summary>Recent changes across all services, or for every instance called <paramref name="name"/>.</summary>
    public Task<LogPage<HistoryEntry>> GetRecentHistoryAsync(string? name = null, long before = 0, int limit = 0, CancellationToken cancellationToken = default)
    {
        var query = new List<KeyValuePair<string, string>>();
        Add(query, "name", name);
        return HistoryAsync("/v1/history", query, before, limit, cancellationToken);
    }

    // --- cluster ------------------------------------------------------------

    /// <summary>The answering node, its version and commit.</summary>
    public Task<NodeInfo> GetNodeAsync(CancellationToken cancellationToken = default) =>
        SendAsync<NodeInfo>(HttpMethod.Get, "/v1/node", null, null, null, cancellationToken);

    /// <summary>All cluster members as seen by the answering node.</summary>
    public async Task<IReadOnlyList<Member>> GetMembersAsync(CancellationToken cancellationToken = default)
    {
        var members = await SendAsync<MemberList>(HttpMethod.Get, "/v1/members", null, null, null, cancellationToken).ConfigureAwait(false);
        return members.Members ?? [];
    }

    /// <summary>
    /// Streams membership and registry events until <paramref name="cancellationToken"/>
    /// is cancelled or the caller stops enumerating. With
    /// <see cref="WatchOptions.Reconnect"/> (the default), drops are retried with
    /// backoff and the stream resumes after the last delivered event. A
    /// <c>reset</c> event (<see cref="AtlasEvent.IsReset"/>) means events were
    /// missed and current state should be re-read. HTTP errors such as a bad
    /// token are thrown, never retried.
    /// </summary>
    public async IAsyncEnumerable<AtlasEvent> WatchAsync(WatchOptions? options = null, [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        options ??= new WatchOptions();
        var lastId = options.LastEventId ?? "";
        var backoff = TimeSpan.FromMilliseconds(500);
        while (true)
        {
            cancellationToken.ThrowIfCancellationRequested();
            var delivered = false;
            HttpResponseMessage? response = null;
            Exception? failure = null;
            try
            {
                StreamReader? reader = null;
                try
                {
                    var query = new List<KeyValuePair<string, string>>();
                    Add(query, "kinds", options.Kinds);
                    Add(query, "service", options.Service);
                    var headers = new List<KeyValuePair<string, string>> { new("Accept", "text/event-stream") };
                    if (lastId.Length > 0)
                    {
                        headers.Add(new("Last-Event-ID", lastId));
                    }
                    using var request = BuildRequest(HttpMethod.Get, "/v1/events", query, null, headers);
                    response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, cancellationToken).ConfigureAwait(false);
                    if (!response.IsSuccessStatusCode)
                    {
                        throw await ErrorFromAsync(response, cancellationToken).ConfigureAwait(false);
                    }
                    var body = await response.Content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
                    reader = new StreamReader(body, Encoding.UTF8);
                }
                catch (Exception e) when (e is HttpRequestException or IOException)
                {
                    failure = e;
                }

                var eventType = "";
                while (reader is not null)
                {
                    string? line;
                    try
                    {
                        line = await reader.ReadLineAsync(cancellationToken).ConfigureAwait(false);
                    }
                    catch (Exception e) when (e is IOException or HttpRequestException)
                    {
                        failure = e;
                        break;
                    }
                    if (line is null)
                    {
                        break;
                    }
                    if (line.StartsWith("event: ", StringComparison.Ordinal))
                    {
                        eventType = line[7..];
                    }
                    else if (line.StartsWith("data: ", StringComparison.Ordinal))
                    {
                        var parsed = ParseEvent(line[6..], eventType);
                        delivered = true;
                        if (parsed.Id.Length > 0)
                        {
                            lastId = parsed.Id;
                        }
                        yield return parsed;
                    }
                    else if (line.Length == 0)
                    {
                        eventType = "";
                    }
                }
                reader?.Dispose();
            }
            finally
            {
                // Also runs when the caller stops enumerating mid-stream.
                response?.Dispose();
            }

            if (!options.Reconnect)
            {
                if (failure is not null)
                {
                    throw new AtlasException("Atlas event stream failed: " + failure.Message, inner: failure);
                }
                yield break;
            }
            backoff = delivered ? TimeSpan.FromMilliseconds(500) : TimeSpan.FromTicks(Math.Min(backoff.Ticks * 2, TimeSpan.FromSeconds(30).Ticks));
            await Task.Delay(backoff, cancellationToken).ConfigureAwait(false);
        }
    }

    // --- admin (admin scope) ---------------------------------------------

    /// <summary>Configured credentials by fingerprint, scopes and expiry; never the secrets.</summary>
    public async Task<IReadOnlyList<Credential>> GetCredentialsAsync(CancellationToken cancellationToken = default)
    {
        var list = await SendAsync<CredentialList>(HttpMethod.Get, "/v1/admin/credentials", null, null, null, cancellationToken).ConfigureAwait(false);
        return list.Credentials ?? [];
    }

    /// <summary>
    /// Creates a managed credential that every node accepts. The returned token
    /// is shown only once. <paramref name="scopes"/> are unique values from
    /// <c>read</c>, <c>write</c> and <c>admin</c>; <paramref name="expiresAt"/>
    /// must be within one year.
    /// </summary>
    public Task<CreatedCredential> CreateCredentialAsync(string name, IReadOnlyList<string> scopes, DateTimeOffset expiresAt, CancellationToken cancellationToken = default)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(name);
        ArgumentNullException.ThrowIfNull(scopes);
        var body = new { name, scopes, expires_at = expiresAt.ToUniversalTime() };
        return SendAsync<CreatedCredential>(HttpMethod.Post, "/v1/admin/credentials", null, body, null, cancellationToken);
    }

    /// <summary>Revokes a managed credential on every node. The credential making the request cannot revoke itself.</summary>
    public async Task RevokeCredentialAsync(string id, CancellationToken cancellationToken = default)
    {
        using var response = await SendRawAsync(HttpMethod.Delete, "/v1/admin/credentials/" + Escape(id), null, null, null, _timeout, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>The node's admin audit log, newest first; a non-zero <paramref name="before"/> continues from a sequence number.</summary>
    public async Task<LogPage<AuditEntry>> GetAuditAsync(long before = 0, int limit = 0, CancellationToken cancellationToken = default)
    {
        var query = new List<KeyValuePair<string, string>>();
        AddPaging(query, before, limit);
        var page = await SendAsync<AuditList>(HttpMethod.Get, "/v1/admin/audit", query, null, null, cancellationToken).ConfigureAwait(false);
        return new LogPage<AuditEntry>(page.Entries ?? [], page.NextBefore > 0 ? page.NextBefore : null);
    }

    /// <summary>Writes a consistent SQLite snapshot of the node's registry to <paramref name="destination"/>.</summary>
    public async Task BackupAsync(Stream destination, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(destination);
        using var response = await SendRawAsync(HttpMethod.Post, "/v1/admin/backup", null, null, null, BackupTimeout, cancellationToken).ConfigureAwait(false);
        await response.Content.CopyToAsync(destination, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Evicts a failed node and deletes its services; returns how many services were removed.</summary>
    public async Task<long> EvictNodeAsync(string nodeId, CancellationToken cancellationToken = default)
    {
        var result = await SendAsync<EvictResult>(HttpMethod.Delete, "/v1/admin/nodes/" + Escape(nodeId), null, null, null, cancellationToken).ConfigureAwait(false);
        return result.ServicesRemoved;
    }

    /// <summary>Disposes the underlying <see cref="HttpClient"/> if this client created it.</summary>
    public void Dispose()
    {
        if (_ownsHttp)
        {
            _http.Dispose();
        }
    }

    // --- transport ----------------------------------------------------------

    private async Task<LogPage<HistoryEntry>> HistoryAsync(string path, List<KeyValuePair<string, string>> query, long before, int limit, CancellationToken cancellationToken)
    {
        AddPaging(query, before, limit);
        var page = await SendAsync<HistoryList>(HttpMethod.Get, path, query, null, null, cancellationToken).ConfigureAwait(false);
        return new LogPage<HistoryEntry>(page.Entries ?? [], page.NextBefore > 0 ? page.NextBefore : null);
    }

    private async Task<T> SendAsync<T>(HttpMethod method, string path, List<KeyValuePair<string, string>>? query, object? body, List<KeyValuePair<string, string>>? headers, CancellationToken cancellationToken)
    {
        using var response = await SendRawAsync(method, path, query, body, headers, _timeout, cancellationToken).ConfigureAwait(false);
        var text = await response.Content.ReadAsStringAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            return JsonSerializer.Deserialize<T>(text, Json)
                ?? throw new AtlasException("Atlas returned an empty response", (int)response.StatusCode);
        }
        catch (JsonException e)
        {
            throw new AtlasException("Atlas returned an unreadable response: " + e.Message, (int)response.StatusCode, inner: e);
        }
    }

    private async Task<HttpResponseMessage> SendRawAsync(HttpMethod method, string path, List<KeyValuePair<string, string>>? query, object? body, List<KeyValuePair<string, string>>? headers, TimeSpan limit, CancellationToken cancellationToken)
    {
        using var request = BuildRequest(method, path, query, body, headers);
        request.Headers.Accept.ParseAdd("application/json");
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        if (limit != Timeout.InfiniteTimeSpan)
        {
            timeout.CancelAfter(limit);
        }
        HttpResponseMessage response;
        try
        {
            response = await _http.SendAsync(request, HttpCompletionOption.ResponseContentRead, timeout.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException e) when (!cancellationToken.IsCancellationRequested)
        {
            throw new AtlasException($"Atlas request timed out after {limit.TotalSeconds:0.###}s", inner: e);
        }
        catch (HttpRequestException e)
        {
            throw new AtlasException("Atlas request failed: " + e.Message, inner: e);
        }
        if (!response.IsSuccessStatusCode)
        {
            using (response)
            {
                throw await ErrorFromAsync(response, cancellationToken).ConfigureAwait(false);
            }
        }
        return response;
    }

    private HttpRequestMessage BuildRequest(HttpMethod method, string path, List<KeyValuePair<string, string>>? query, object? body, List<KeyValuePair<string, string>>? headers)
    {
        var url = new StringBuilder(path);
        if (query is not null)
        {
            for (var i = 0; i < query.Count; i++)
            {
                url.Append(i == 0 ? '?' : '&').Append(Escape(query[i].Key)).Append('=').Append(Escape(query[i].Value));
            }
        }
        var request = new HttpRequestMessage(method, new Uri(_endpoint, url.ToString()));
        request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _token);
        if (headers is not null)
        {
            foreach (var header in headers)
            {
                request.Headers.TryAddWithoutValidation(header.Key, header.Value);
            }
        }
        if (body is not null)
        {
            var json = body as string ?? JsonSerializer.Serialize(body, body.GetType(), Json);
            request.Content = new StringContent(json, Encoding.UTF8, "application/json");
        }
        return request;
    }

    private static async Task<AtlasException> ErrorFromAsync(HttpResponseMessage response, CancellationToken cancellationToken)
    {
        var status = (int)response.StatusCode;
        var text = await response.Content.ReadAsStringAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            if (JsonSerializer.Deserialize<ErrorBody>(text, Json) is { Error: { Length: > 0 } message } error)
            {
                return new AtlasException(message, status, error.Code ?? "", error.RequestId ?? "", error.OwnerNode ?? "");
            }
        }
        catch (JsonException)
        {
            // Not a JSON error body; fall back to the raw text below.
        }
        return new AtlasException(string.IsNullOrWhiteSpace(text) ? $"HTTP {status}" : text.Trim(), status);
    }

    internal static AtlasEvent ParseEvent(string data, string fallbackType)
    {
        AtlasEvent parsed;
        try
        {
            parsed = JsonSerializer.Deserialize<AtlasEvent>(data, Json) ?? new AtlasEvent();
        }
        catch (JsonException e)
        {
            throw new AtlasException("Atlas sent an unreadable event: " + e.Message, inner: e);
        }
        return parsed.Type.Length > 0 ? parsed : parsed with { Type = fallbackType };
    }

    internal static Uri ParseEndpoint(string endpoint)
    {
        ArgumentNullException.ThrowIfNull(endpoint);
        var trimmed = endpoint.TrimEnd('/');
        if (!Uri.TryCreate(trimmed, UriKind.Absolute, out var uri)
            || (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps)
            || uri.AbsolutePath != "/" || uri.Query.Length > 0 || uri.Fragment.Length > 0 || uri.UserInfo.Length > 0)
        {
            throw new ArgumentException("endpoint must be an http:// or https:// origin, such as https://atlas.example:8080", nameof(endpoint));
        }
        return new Uri(uri.GetLeftPart(UriPartial.Authority));
    }

    internal static string FormatDuration(TimeSpan duration)
    {
        if (duration < TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(nameof(duration), "tolerance cannot be negative");
        }
        return duration.TotalMilliseconds.ToString("0.###", CultureInfo.InvariantCulture) + "ms";
    }

    private static List<KeyValuePair<string, string>>? IfMatch(long revision) =>
        revision > 0 ? [new("If-Match", "\"" + revision.ToString(CultureInfo.InvariantCulture) + "\"")] : null;

    private static void AddPaging(List<KeyValuePair<string, string>> query, long before, int limit)
    {
        if (before > 0)
        {
            query.Add(new("before", before.ToString(CultureInfo.InvariantCulture)));
        }
        if (limit > 0)
        {
            query.Add(new("limit", limit.ToString(CultureInfo.InvariantCulture)));
        }
    }

    private static void Add(List<KeyValuePair<string, string>> query, string key, string? value)
    {
        if (!string.IsNullOrEmpty(value))
        {
            query.Add(new(key, value));
        }
    }

    private static void AddTags(List<KeyValuePair<string, string>> query, IReadOnlyList<string>? tags)
    {
        foreach (var tag in tags ?? [])
        {
            query.Add(new("tag", tag));
        }
    }

    private static void AddMetadata(List<KeyValuePair<string, string>> query, IReadOnlyDictionary<string, string>? metadata)
    {
        if (metadata is null)
        {
            return;
        }
        foreach (var key in metadata.Keys.OrderBy(key => key, StringComparer.Ordinal))
        {
            query.Add(new("meta", key + ":" + metadata[key]));
        }
    }

    private static string ServicePath(string id)
    {
        ArgumentException.ThrowIfNullOrEmpty(id);
        return "/v1/services/" + Escape(id);
    }

    private static string Escape(string value) => Uri.EscapeDataString(value);

    private sealed record ServiceList(List<Service>? Services, string? NextCursor);
    private sealed record MemberList(List<Member>? Members);
    private sealed record HistoryList(List<HistoryEntry>? Entries, long NextBefore);
    private sealed record AuditList(List<AuditEntry>? Entries, long NextBefore);
    private sealed record CredentialList(List<Credential>? Credentials);
    private sealed record EvictResult(long ServicesRemoved);
    private sealed record ErrorBody(string? Error, string? Code, string? RequestId, string? OwnerNode);
}
