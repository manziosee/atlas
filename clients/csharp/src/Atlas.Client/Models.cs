namespace Atlas.Client;

/// <summary>
/// A registered service instance. <see cref="Health"/> is <c>healthy</c>,
/// <c>unhealthy</c> or <c>unknown</c>; absent optional fields are null or zero.
/// </summary>
public sealed record Service
{
    /// <summary>Stable instance ID.</summary>
    public string Id { get; init; } = "";
    /// <summary>Service name shared by all of its instances.</summary>
    public string Name { get; init; } = "";
    /// <summary>The node that owns this instance.</summary>
    public string NodeId { get; init; } = "";
    /// <summary>Where clients reach the instance, such as <c>10.0.0.7:8443</c>.</summary>
    public string Address { get; init; } = "";
    /// <summary>The owner node's region.</summary>
    public string Region { get; init; } = "";
    /// <summary>Optional version label.</summary>
    public string? Version { get; init; }
    /// <summary>Labels used to filter listings and lookups.</summary>
    public IReadOnlyList<string> Tags { get; init; } = [];
    /// <summary>Active health check URL, if any.</summary>
    public string? HealthCheck { get; init; }
    /// <summary><c>healthy</c>, <c>unhealthy</c> or <c>unknown</c>.</summary>
    public string Health { get; init; } = "";
    /// <summary>Increases on every change; use it with If-Match updates.</summary>
    public long Revision { get; init; }
    /// <summary>Heartbeat TTL in seconds, or 0 when the instance has none.</summary>
    public long TtlSeconds { get; init; }
    /// <summary>Health check interval in seconds, or 0 for the node default.</summary>
    public long CheckIntervalSeconds { get; init; }
    /// <summary>Failed checks before the instance is unhealthy, or 0 for the default.</summary>
    public int UnhealthyThreshold { get; init; }
    /// <summary>Passing checks before the instance is healthy again, or 0 for the default.</summary>
    public int HealthyThreshold { get; init; }
    /// <summary>True while the instance receives no new traffic.</summary>
    public bool Draining { get; init; }
    /// <summary>Relative share of traffic among routable instances (1 by default).</summary>
    public int TrafficWeight { get; init; }
    /// <summary>When the record last changed.</summary>
    public DateTimeOffset UpdatedAt { get; init; }
    /// <summary>When a health check last ran, if ever.</summary>
    public DateTimeOffset? CheckedAt { get; init; }
    /// <summary>When the last heartbeat arrived, if ever.</summary>
    public DateTimeOffset? HeartbeatAt { get; init; }
}

/// <summary>One cluster member as seen by the answering node.</summary>
public sealed record Member
{
    /// <summary>Node ID.</summary>
    public string Id { get; init; } = "";
    /// <summary>Gossip address.</summary>
    public string Addr { get; init; } = "";
    /// <summary>The node's region.</summary>
    public string Region { get; init; } = "";
    /// <summary><c>alive</c>, <c>suspect</c>, <c>dead</c> or <c>left</c>.</summary>
    public string State { get; init; } = "";
    /// <summary>SWIM incarnation number.</summary>
    public long Incarnation { get; init; }
    /// <summary>When the member entered its current state.</summary>
    public DateTimeOffset? Since { get; init; }
    /// <summary>The member's HTTP API address, when advertised.</summary>
    public string? ApiAddr { get; init; }
}

/// <summary>The answering node with its build version and commit.</summary>
/// <param name="Node">The node's own membership record.</param>
/// <param name="Version">Build version.</param>
/// <param name="Commit">Build commit.</param>
public sealed record NodeInfo(Member Node, string Version, string Commit);

/// <summary>
/// A membership event (<see cref="Member"/> set: join, update, reap) or registry
/// event (<see cref="Service"/> set: service.registered, service.updated,
/// service.health, service.deregistered). A <c>reset</c> event means missed
/// events could not be replayed: re-read the current state.
/// </summary>
public sealed record AtlasEvent
{
    /// <summary>Event ID; pass it as <see cref="WatchOptions.LastEventId"/> to resume.</summary>
    public string Id { get; init; } = "";
    /// <summary>Event type, such as <c>service.registered</c> or <c>reset</c>.</summary>
    public string Type { get; init; } = "";
    /// <summary><c>member</c> or <c>service</c>.</summary>
    public string Kind { get; init; } = "";
    /// <summary>When the event happened.</summary>
    public DateTimeOffset? At { get; init; }
    /// <summary>The member, for membership events.</summary>
    public Member? Member { get; init; }
    /// <summary>The service record after the change, for registry events.</summary>
    public Service? Service { get; init; }

    /// <summary>True when missed events could not be replayed and state must be re-read.</summary>
    public bool IsReset => Type == "reset";
}

/// <summary>One recorded change; <see cref="Service"/> is the record after it.</summary>
public sealed record HistoryEntry
{
    /// <summary>Sequence number; pass it as <c>before</c> to page further back.</summary>
    public long Seq { get; init; }
    /// <summary>The changed instance.</summary>
    public string ServiceId { get; init; } = "";
    /// <summary>The service name.</summary>
    public string Name { get; init; } = "";
    /// <summary>The record's revision after the change.</summary>
    public long Revision { get; init; }
    /// <summary><c>registered</c>, <c>updated</c>, <c>health</c> or <c>deregistered</c>.</summary>
    public string Change { get; init; } = "";
    /// <summary>When the change happened.</summary>
    public DateTimeOffset At { get; init; }
    /// <summary>The record after the change.</summary>
    public Service? Service { get; init; }
}

/// <summary>An API credential, identified by fingerprint. Secrets are never returned.</summary>
public sealed record Credential
{
    /// <summary>Fingerprint; matches <c>token_id</c> in the node's access logs.</summary>
    public string Id { get; init; } = "";
    /// <summary>Name given when the credential was created, if any.</summary>
    public string? Name { get; init; }
    /// <summary>Granted scopes: <c>read</c>, <c>write</c>, <c>admin</c>.</summary>
    public IReadOnlyList<string> Scopes { get; init; } = [];
    /// <summary>When the credential was created, if known.</summary>
    public DateTimeOffset? CreatedAt { get; init; }
    /// <summary>When the credential expires, if it does.</summary>
    public DateTimeOffset? ExpiresAt { get; init; }
    /// <summary><c>static</c>, <c>scoped</c> or <c>managed</c> (created through the API).</summary>
    public string Source { get; init; } = "";
    /// <summary>True once past <see cref="ExpiresAt"/>.</summary>
    public bool Expired { get; init; }
    /// <summary>True for a revoked managed credential.</summary>
    public bool Revoked { get; init; }
    /// <summary>True for the credential that made this request.</summary>
    public bool Current { get; init; }
}

/// <summary>A newly created managed credential and its bearer token, shown only once.</summary>
/// <param name="Credential">The credential's fingerprint, scopes and expiry.</param>
/// <param name="Token">The bearer token; store it now, it cannot be read again.</param>
public sealed record CreatedCredential(Credential Credential, string Token);

/// <summary>One entry in the node's admin audit log.</summary>
public sealed record AuditEntry
{
    /// <summary>Sequence number; pass it as <c>before</c> to page further back.</summary>
    public long Seq { get; init; }
    /// <summary>When the action happened.</summary>
    public DateTimeOffset At { get; init; }
    /// <summary>Fingerprint of the credential that acted.</summary>
    public string ActorId { get; init; } = "";
    /// <summary>The action, such as <c>credential.created</c>.</summary>
    public string Action { get; init; } = "";
    /// <summary>What the action applied to, if anything.</summary>
    public string? ResourceId { get; init; }
    /// <summary>Action-specific details as raw JSON.</summary>
    public System.Text.Json.JsonElement Details { get; init; }
}

/// <summary>One page of services; <see cref="NextCursor"/> is null on the last page.</summary>
/// <param name="Items">The services on this page.</param>
/// <param name="NextCursor">Pass to <see cref="AtlasClient.ListPageAsync"/> for the next page.</param>
public sealed record ServicePage(IReadOnlyList<Service> Items, string? NextCursor);

/// <summary>One page of a newest-first log; pass <see cref="NextBefore"/> to continue, null on the last page.</summary>
/// <typeparam name="T">The entry type.</typeparam>
/// <param name="Items">The entries on this page, newest first.</param>
/// <param name="NextBefore">Pass as <c>before</c> for the next page.</param>
public sealed record LogPage<T>(IReadOnlyList<T> Items, long? NextBefore);
