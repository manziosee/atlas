namespace Atlas.Client;

/// <summary>
/// An Atlas API error. <see cref="Code"/> is the server's stable error code
/// (such as <c>not_found</c>, <c>precondition_failed</c> or <c>validation_failed</c>);
/// <see cref="Status"/> is null when no response arrived.
/// </summary>
public sealed class AtlasException : Exception
{
    /// <summary>Creates an error from a server response or transport failure.</summary>
    public AtlasException(string message, int? status = null, string code = "", string requestId = "", string ownerNode = "", Exception? inner = null)
        : base(message, inner)
    {
        Status = status;
        Code = code;
        RequestId = requestId;
        OwnerNode = ownerNode;
    }

    /// <summary>HTTP status, or null for connection and timeout failures.</summary>
    public int? Status { get; }

    /// <summary>Stable machine-readable error code, or empty.</summary>
    public string Code { get; }

    /// <summary>The node's request ID, for matching its logs.</summary>
    public string RequestId { get; }

    /// <summary>For writes forwarded to another node, the owner that answered.</summary>
    public string OwnerNode { get; }
}
