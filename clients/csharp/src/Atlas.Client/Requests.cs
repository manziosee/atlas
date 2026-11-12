namespace Atlas.Client;

/// <summary>
/// The client-owned part of a registration. Use either <see cref="HealthCheck"/>
/// (an http(s)://, tcp:// or grpc(s):// URL on the address) or
/// <see cref="TtlSeconds"/> with heartbeats, not both. Unset (null) fields take
/// the node's defaults.
/// </summary>
/// <param name="Name">Service name shared by all of its instances.</param>
/// <param name="Address">Where clients reach this instance, such as <c>10.0.0.7:8443</c>.</param>
public sealed record ServiceSpec(string Name, string Address)
{
    /// <summary>Optional version label.</summary>
    public string? Version { get; init; }
    /// <summary>Labels used to filter listings and lookups.</summary>
    public IReadOnlyList<string>? Tags { get; init; }
    /// <summary>Active health check URL.</summary>
    public string? HealthCheck { get; init; }
    /// <summary>Heartbeat TTL; the instance becomes unhealthy, then is removed, without heartbeats.</summary>
    public long? TtlSeconds { get; init; }
    /// <summary>Health check interval in seconds.</summary>
    public long? CheckIntervalSeconds { get; init; }
    /// <summary>Failed checks before the instance is marked unhealthy.</summary>
    public int? UnhealthyThreshold { get; init; }
    /// <summary>Passing checks before the instance is marked healthy again.</summary>
    public int? HealthyThreshold { get; init; }
    /// <summary>Register already drained.</summary>
    public bool? Draining { get; init; }
    /// <summary>Relative share of traffic, 0 to 1000 (default 1).</summary>
    public int? TrafficWeight { get; init; }
    /// <summary>Key/value labels: at most 16 entries totalling 512 bytes; keys are letters, digits, '.', '_', '-' and '/'.</summary>
    public IReadOnlyDictionary<string, string>? Metadata { get; init; }
}

/// <summary>Fields to change with <see cref="AtlasClient.UpdateAsync"/>; null fields keep their values.</summary>
public sealed record ServicePatch
{
    /// <summary>New service name.</summary>
    public string? Name { get; init; }
    /// <summary>New address.</summary>
    public string? Address { get; init; }
    /// <summary>New version label.</summary>
    public string? Version { get; init; }
    /// <summary>New tags; an empty list removes all tags.</summary>
    public IReadOnlyList<string>? Tags { get; init; }
    /// <summary>New health check URL.</summary>
    public string? HealthCheck { get; init; }
    /// <summary>New heartbeat TTL in seconds.</summary>
    public long? TtlSeconds { get; init; }
    /// <summary>New health check interval in seconds.</summary>
    public long? CheckIntervalSeconds { get; init; }
    /// <summary>New unhealthy threshold.</summary>
    public int? UnhealthyThreshold { get; init; }
    /// <summary>New healthy threshold.</summary>
    public int? HealthyThreshold { get; init; }
    /// <summary>Drain (true) or return to rotation (false).</summary>
    public bool? Draining { get; init; }
    /// <summary>New traffic weight, 0 to 1000.</summary>
    public int? TrafficWeight { get; init; }
    /// <summary>Replaces the whole metadata map; an empty dictionary removes every entry.</summary>
    public IReadOnlyDictionary<string, string>? Metadata { get; init; }
}

/// <summary>Narrows a service listing. A service must carry every tag.</summary>
public sealed record ServiceFilter
{
    /// <summary>Only services with this name.</summary>
    public string? Name { get; init; }
    /// <summary>Only services in this region.</summary>
    public string? Region { get; init; }
    /// <summary>Only services with this health.</summary>
    public string? Health { get; init; }
    /// <summary>Only services with this version label.</summary>
    public string? Version { get; init; }
    /// <summary>Only services owned by this node.</summary>
    public string? NodeId { get; init; }
    /// <summary>Only services carrying all of these tags.</summary>
    public IReadOnlyList<string>? Tags { get; init; }
    /// <summary>Only services carrying every entry with exactly that value.</summary>
    public IReadOnlyDictionary<string, string>? Metadata { get; init; }
}

/// <summary>
/// Refines a routed lookup. <see cref="Stale"/> (include, prefer-live, exclude),
/// <see cref="Latency"/> (nearest, off) and <see cref="Tolerance"/> override the
/// node's routing policy for this request.
/// </summary>
public sealed record LookupOptions
{
    /// <summary>Prefer instances in this region.</summary>
    public string? Region { get; init; }
    /// <summary>Consistent-hashing key: the same key keeps reaching the same instance.</summary>
    public string? Key { get; init; }
    /// <summary>Only instances carrying all of these tags.</summary>
    public IReadOnlyList<string>? Tags { get; init; }
    /// <summary>Only instances carrying every entry with exactly that value.</summary>
    public IReadOnlyDictionary<string, string>? Metadata { get; init; }
    /// <summary><c>include</c>, <c>prefer-live</c> or <c>exclude</c> instances of nodes that are not alive.</summary>
    public string? Stale { get; init; }
    /// <summary><c>nearest</c> or <c>off</c>.</summary>
    public string? Latency { get; init; }
    /// <summary>With nearest latency routing, nodes this close to the nearest count as equally near.</summary>
    public TimeSpan? Tolerance { get; init; }
}

/// <summary>Selects and resumes an event stream for <see cref="AtlasClient.WatchAsync"/>.</summary>
public sealed record WatchOptions
{
    /// <summary><c>member</c>, <c>service</c>, or null for both.</summary>
    public string? Kinds { get; init; }
    /// <summary>Only registry events for this service name.</summary>
    public string? Service { get; init; }
    /// <summary>Resume after this event ID.</summary>
    public string? LastEventId { get; init; }
    /// <summary>Reconnect after drops, resuming from the last event (default true).</summary>
    public bool Reconnect { get; init; } = true;
}
